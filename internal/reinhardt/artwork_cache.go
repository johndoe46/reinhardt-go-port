package reinhardt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Bump this version when thumbnail cropping or encoding changes. Image identity
// is independent of the track, requested size, and thumbnail rendering version.
const artworkRenderVersion = "v1"

var artworkHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Reuse references from ordinary track queries and refresh cached metadata rows
// per render. Discovery can establish a hash without changing library metadata.
// Loading is lazy so pages without artwork do not pay for another SQL query.
func (store *Store) templateArtworkURL(ctx context.Context, page TemplateContext) func(any, any, any) (string, error) {
	trackIDs := make(map[int64]bool)
	resolvedIDs := make(map[int64]bool)
	hashes := make(map[int64]string)
	var collect func(any)
	collect = func(value any) {
		switch value := value.(type) {
		case Row:
			if _, track := value["relative_path"]; track && value.N("id") > 0 {
				id := value.N("id")
				if value.B("artwork_hash_loaded") {
					resolvedIDs[id] = true
					hashes[id] = value.S("artwork_hash")
				} else {
					trackIDs[id] = true
				}
			}
			for _, item := range value {
				collect(item)
			}
		case TemplateContext:
			for _, item := range value {
				collect(item)
			}
		case []Row:
			for _, item := range value {
				collect(item)
			}
		case []any:
			for _, item := range value {
				collect(item)
			}
		}
	}
	collect(page)
	for id := range resolvedIDs {
		delete(trackIDs, id)
	}
	var loaded bool
	var loadErr error
	return func(trackID, size, version any) (string, error) {
		if !loaded {
			loaded = true
			ids := make([]int64, 0, len(trackIDs))
			for id := range trackIDs {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			for start := 0; start < len(ids); start += 500 {
				batch := ids[start:min(start+500, len(ids))]
				args := make([]any, len(batch))
				for index, id := range batch {
					args[index] = id
				}
				placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
				rows, err := store.Query(ctx, "SELECT track_id,artwork_hash FROM go_track_artwork WHERE track_id IN ("+placeholders+") AND artwork_hash IS NOT NULL", args...)
				if err != nil {
					loadErr = err
					break
				}
				for _, row := range rows {
					hashes[row.N("track_id")] = row.S("artwork_hash")
				}
			}
		}
		if loadErr != nil {
			return "", loadErr
		}
		id, err := number(trackID)
		if err != nil {
			return "", err
		}
		if hash := hashes[id]; hash != "" {
			return store.URL("artwork_hash", hash, size), nil
		}
		return store.URL("artwork", trackID, size, version), nil
	}
}

type trackArtwork struct {
	track    *Track
	revision int64
	hash     string
}

type artworkRenderFlight struct {
	done chan struct{}
	data []byte
	err  error
}

func (store *Store) trackArtwork(ctx context.Context, id string) (*trackArtwork, error) {
	row, err := store.One(ctx, `SELECT m.*,d.local_path,COALESCE(a.revision,0) artwork_revision,a.artwork_hash
FROM reinhardt_musicfile m JOIN reinhardt_musicdirectory d ON d.id=m.music_directory_id
LEFT JOIN go_track_artwork a ON a.track_id=m.id WHERE m.id=?`, id)
	if err != nil {
		return nil, err
	}
	track := trackFromRow(row)
	store.decorateTrack(track)
	return &trackArtwork{track: track, revision: row.N("artwork_revision"), hash: row.S("artwork_hash")}, nil
}

func (store *Store) rememberArtwork(ctx context.Context, artwork *trackArtwork, hash string) error {
	// The reference is registered before file publication. Cleanup holds a SQLite
	// write transaction while checking references, including across processes.
	_, err := store.DB.ExecContext(ctx, `INSERT INTO go_track_artwork(track_id,revision,artwork_hash)
SELECT id,?,? FROM reinhardt_musicfile WHERE id=?
 AND COALESCE((SELECT revision FROM go_track_artwork WHERE track_id=?),0)=?
ON CONFLICT(track_id) DO UPDATE SET artwork_hash=excluded.artwork_hash
 WHERE go_track_artwork.revision=excluded.revision`, artwork.revision, hash, artwork.track.ID, artwork.track.ID, artwork.revision)
	return err
}

func (app *App) invalidateCovers(ctx context.Context, id int64) error {
	_, err := app.Store.DB.ExecContext(ctx, `INSERT INTO go_track_artwork(track_id,revision)
VALUES(?,1) ON CONFLICT(track_id) DO UPDATE SET revision=revision+1,artwork_hash=NULL`, id)
	return err
}

// Hash canonical pixel rows so embedded, sidecar and differently encoded
// lossless copies of the same image can share thumbnails. Include dimensions
// and alpha; two different images must not share a key just because tags match.
func artworkImageHash(im image.Image) string {
	bounds := im.Bounds()
	hasher := sha256.New()
	fmt.Fprintf(hasher, "%d:%d:", bounds.Dx(), bounds.Dy())
	row := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), 1))
	for pixelY := bounds.Min.Y; pixelY < bounds.Max.Y; pixelY++ {
		draw.Draw(row, row.Bounds(), im, image.Pt(bounds.Min.X, pixelY), draw.Src)
		hasher.Write(row.Pix)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func (app *App) artworkThumbnailPath(hash string, size int) string {
	return filepath.Join(app.Store.Config.CoversDir, fmt.Sprintf("thumb-%s-%s-%d-square.jpg", artworkRenderVersion, hash, size))
}

func artworkFallbackKey(track *Track, size int) string {
	// Album contributes to the sleeve colors, while artist/title supply its label.
	metadata, _ := json.Marshal([]string{track.Artist, track.Album, track.Title})
	hash := sha256.Sum256(metadata)
	return fmt.Sprintf("fallback-%s-%x-%d", artworkRenderVersion, hash, size)
}

// Share JPEG generation for identical content/size. The leader is independent
// of a caller's cancellation, and unrelated keys never wait on image work.
// Fallbacks share in-flight bytes only; they are never persisted or referenced.
func (app *App) renderArtwork(ctx context.Context, key, path string, generate func() image.Image) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	app.artworkMu.Lock()
	flight := app.artworkFlights[key]
	if flight == nil {
		if app.artworkFlights == nil {
			app.artworkFlights = make(map[string]*artworkRenderFlight)
		}
		flight = &artworkRenderFlight{done: make(chan struct{})}
		app.artworkFlights[key] = flight
		go func() {
			if path != "" {
				_, flight.err = os.Stat(path)
			}
			if path == "" || errors.Is(flight.err, os.ErrNotExist) {
				var buffer bytes.Buffer
				flight.err = jpeg.Encode(&buffer, generate(), &jpeg.Options{Quality: 85})
				if flight.err == nil {
					flight.data = buffer.Bytes()
					if path != "" {
						flight.err = atomicFile(path, bytes.NewReader(flight.data), false)
						// Another server process may have published the same image.
						if errors.Is(flight.err, os.ErrExist) {
							flight.err = nil
						}
					}
				}
			}
			app.artworkMu.Lock()
			delete(app.artworkFlights, key)
			close(flight.done)
			app.artworkMu.Unlock()
		}()
	}
	app.artworkMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		return flight.data, flight.err
	}
}

func (app *App) removeLegacyCovers(id int64) {
	files, _ := filepath.Glob(filepath.Join(app.Store.Config.CoversDir, fmt.Sprintf("thumb-%d-*-square.jpg", id)))
	for _, path := range files {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			// Legacy files are just a cache; a later worker cleanup can retry.
			return
		}
	}
}

var sharedArtworkFilename = regexp.MustCompile(`^thumb-v[0-9]+-([0-9a-f]{64})-(96|128|192|256|384|512)-square\.jpg$`)
var legacyArtworkFilename = regexp.MustCompile(`^thumb-([0-9]+)-(96|128|192|256|384|512)-square\.jpg$`)

// Give newly generated files a grace period before considering them orphaned.
// Keep legacy thumbnails until a shared reference replaces them (or the track
// is deleted), so installations with unavailable originals retain their covers.
func (store *Store) pruneArtworkCache(ctx context.Context, before time.Time) error {
	files, err := os.ReadDir(store.Config.CoversDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		query := ""
		var reference any
		if match := sharedArtworkFilename.FindStringSubmatch(file.Name()); match != nil {
			query = "SELECT EXISTS(SELECT 1 FROM go_track_artwork WHERE artwork_hash=?)"
			reference = match[1]
		} else if match := legacyArtworkFilename.FindStringSubmatch(file.Name()); match != nil {
			// Retain old files for tracks whose current artwork is unresolved.
			query = `SELECT EXISTS(SELECT 1 FROM reinhardt_musicfile m LEFT JOIN go_track_artwork a ON a.track_id=m.id WHERE m.id=? AND a.artwork_hash IS NULL)`
			reference, err = strconv.ParseInt(match[1], 10, 64)
			if err != nil {
				continue
			}
		} else {
			continue
		}
		info, err := file.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(before) {
			continue
		}
		if err := store.pruneArtworkFile(ctx, filepath.Join(store.Config.CoversDir, file.Name()), query, reference); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) pruneArtworkFile(ctx context.Context, path, query string, reference any) error {
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Acquire the SQLite writer lock before checking references; a concurrent
	// server cannot register a new user of an old file between check and unlink.
	if _, err = tx.ExecContext(ctx, "DELETE FROM go_track_artwork WHERE 0"); err != nil {
		return err
	}
	var referenced bool
	if err = tx.QueryRowContext(ctx, query, reference).Scan(&referenced); err != nil {
		return err
	}
	if !referenced {
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return tx.Commit()
}
