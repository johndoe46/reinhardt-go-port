package reinhardt

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func (store *Store) Enqueue(kind string, payload any) error {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = store.DB.Exec("INSERT INTO go_job(kind,payload,run_at) VALUES(?,?,?)", kind, string(payloadJSON), time.Now().Unix())
	return err
}
func (store *Store) enqueueSync(id int64) error {
	if store.Config["JAMA_ENDPOINT"] != "" && store.Config["JAMA_KEY"] != "" && store.Config["JAMA_PROJECT"] != "" {
		if err := store.Enqueue("jama", id); err != nil {
			return err
		}
	}
	if store.Config["DBX_TOKEN"] != "" {
		return store.Enqueue("dropbox", id)
	}
	return nil
}
func (app *App) Work(ctx context.Context, once bool) error {
	nextGather := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if app.Store.Config["MUSIC_IMPORT_DIR"] != "" && time.Now().After(nextGather) {
			if err := app.Store.Gather(ctx); err != nil {
				log.Printf("music import: %v", err)
			}
			nextGather = time.Now().Add(5 * time.Minute)
		}
		// Claim in one SQLite statement so separate workers cannot select the same
		// ready job. Locks become reclaimable after an hour, longer than the
		// 30-minute job deadline. Handlers must tolerate reruns if a worker dies
		// after external side effects but before deleting the job.
		job, storeErr := app.Store.One(`UPDATE go_job SET locked_at=? WHERE id=(SELECT id FROM go_job WHERE run_at<=? AND attempts<3 AND (locked_at IS NULL OR locked_at<?) ORDER BY id LIMIT 1) RETURNING *`, time.Now().Unix(), time.Now().Unix(), time.Now().Unix()-3600)
		if storeErr != nil && storeErr != sql.ErrNoRows {
			return storeErr
		}
		if storeErr == nil {
			jobCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			var err error
			switch job.S("kind") {
			case "youtube":
				var data Row
				err = json.Unmarshal([]byte(job.S("payload")), &data)
				if err == nil {
					err = app.youtubeRip(jobCtx, data)
				}
			case "jama", "dropbox":
				var id int64
				err = json.Unmarshal([]byte(job.S("payload")), &id)
				if err == nil {
					if job.S("kind") == "jama" {
						err = app.syncJama(jobCtx, id)
					} else {
						err = app.syncDropbox(jobCtx, id)
					}
				}
			default:
				err = fmt.Errorf("unknown job kind %q", job.S("kind"))
			}
			cancel()
			if err == nil {
				_, storeErr = app.Store.DB.Exec("DELETE FROM go_job WHERE id=?", job.N("id"))
			} else {
				log.Printf("job %d (%s): %v", job.N("id"), job.S("kind"), err)
				_, storeErr = app.Store.DB.Exec("UPDATE go_job SET attempts=attempts+1,run_at=?,locked_at=NULL,error=? WHERE id=?", time.Now().Add(10*time.Minute).Unix(), err.Error(), job.N("id"))
			}
			if storeErr != nil {
				return storeErr
			}
			continue
		}
		if once {
			// --once drains currently ready jobs; it does not wait for delayed retries.
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func libraryPath(track Row, ext string) (string, error) {
	artist, title := slug(track.S("artist")), slug(track.S("title"))
	if artist == "" || title == "" {
		return "", fmt.Errorf("artist and title must contain letters or numbers")
	}
	album := slug(track.S("album"))
	if album == "" {
		album = "unknown"
	}
	return filepath.Join(artist[:1], artist, album, fmt.Sprintf("%02d-%s%s", track.N("track_number"), title, strings.ToLower(ext))), nil
}
func (store *Store) importFile(path string, dir Row) (string, error) {
	trackMetadata, err := store.Metadata(path)
	if err != nil {
		return "", err
	}
	rel, err := libraryPath(trackMetadata, filepath.Ext(path))
	if err != nil {
		return "", err
	}
	dest := filepath.Join(dir.S("local_path"), rel)
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	err = atomicFile(dest, file, false)
	file.Close()
	if err != nil {
		return "", err
	}
	if err = store.indexFile(dest, dir); err != nil {
		return "", err
	}
	// Keep the import source until both publication and indexing succeed. Those
	// steps span the filesystem and SQLite, so they cannot share a transaction.
	if err = os.Remove(path); err != nil {
		return "", err
	}
	return dest, nil
}
func (store *Store) Gather(ctx context.Context) error {
	root := store.Config["MUSIC_IMPORT_DIR"]
	if root == "" {
		return fmt.Errorf("MUSIC_IMPORT_DIR is not configured")
	}
	dir, err := store.DefaultMusicDirectory()
	if err != nil {
		return err
	}
	var archives []string
	if err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.ToLower(filepath.Ext(path)) == ".zip" {
			archives = append(archives, path)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, path := range archives {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = extractArchive(path, root, int64(store.Config.Int("MAX_UPLOAD_BYTES"))); err != nil {
			log.Printf("archive %s: %v", path, err)
			continue
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	}
	return walkMusic(root, func(path string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := store.importFile(path, dir); err != nil {
			log.Printf("import %s: %v", path, err)
		}
		return nil
	})
}
func extractArchive(path, root string, max int64) error {
	archiveReader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer archiveReader.Close()
	var total uint64
	// Validate every entry before creating any destination files.
	for _, entry := range archiveReader.File {
		clean := filepath.Clean(filepath.FromSlash(entry.Name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe archive path")
		}
		total += entry.UncompressedSize64
		if total > uint64(max) {
			return fmt.Errorf("archive too large")
		}
	}
	for _, entry := range archiveReader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		dest := filepath.Join(root, filepath.FromSlash(entry.Name))
		// Safe archive names alone are insufficient: an existing symlink in the
		// destination tree could redirect a relative path outside the import root.
		if err = rejectSymlinkParents(root, dest); err != nil {
			return err
		}
		reader, err := entry.Open()
		if err != nil {
			return err
		}
		err = atomicFile(dest, io.LimitReader(reader, max+1), false)
		reader.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func rejectSymlinkParents(root, dest string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	dest, err = filepath.Abs(dest)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, dest)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("path escapes import directory")
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in import path")
		}
	}
	return nil
}
func (app *App) upload(responseWriter http.ResponseWriter, request *http.Request, user Row) error {
	if !user.B("is_superuser") {
		return forbidden()
	}
	if request.Method == "POST" {
		root := app.Store.Config["MUSIC_IMPORT_DIR"]
		if root == "" {
			return bad("MUSIC_IMPORT_DIR is not configured")
		}
		uploadedFile, header, err := request.FormFile("file")
		if err != nil {
			return bad("A file is required")
		}
		defer uploadedFile.Close()
		name := filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
		if name == "." || name == ".." || name == "" {
			return bad("Invalid filename")
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".mp3" && ext != ".m4a" && ext != ".zip" {
			return bad("Upload an MP3, M4A or ZIP file")
		}
		if err = atomicFile(filepath.Join(root, name), uploadedFile, false); err != nil {
			return bad(err.Error())
		}
	}
	return app.render(responseWriter, request, user, "reinhardt/upload.html", nil, 200)
}

var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func youtubeURL(raw string) (string, error) {
	if len(raw) > 2048 {
		return "", bad("Invalid YouTube URL")
	}
	parsedURL, err := url.Parse(raw)
	if err != nil || parsedURL.User != nil || parsedURL.Port() != "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return "", bad("Invalid YouTube URL")
	}
	id := ""
	switch strings.ToLower(parsedURL.Hostname()) {
	case "youtu.be":
		id = strings.TrimPrefix(parsedURL.Path, "/")
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com":
		if parsedURL.Path == "/watch" {
			id = parsedURL.Query().Get("v")
		} else if strings.HasPrefix(parsedURL.Path, "/shorts/") || strings.HasPrefix(parsedURL.Path, "/live/") {
			id = strings.Split(parsedURL.Path, "/")[2]
		}
	}
	if !videoID.MatchString(id) {
		return "", bad("Invalid YouTube URL")
	}
	// Pass only a validated single-video URL to yt-dlp; discard playlist, seek,
	// and other supplied query parameters before invoking the helper.
	return "https://www.youtube.com/watch?v=" + id, nil
}
func youtubeForm(request *http.Request) Row {
	ripMetadata := Row{}
	for _, field := range []string{"url", "artist", "title", "album", "genre", "year", "comment"} {
		ripMetadata[field] = strings.TrimSpace(request.FormValue(field))
	}
	return ripMetadata
}
func fingerprint(ripMetadata Row) string {
	// Deliberately select only form fields. Retried rows also contain database
	// IDs and failure details, which must not change the failure's identity.
	data := map[string]string{}
	for _, field := range []string{"url", "artist", "title", "album", "genre", "year", "comment"} {
		data[field] = strings.TrimSpace(ripMetadata.S(field))
	}
	metadataJSON, _ := json.Marshal(data)
	digest := sha256.Sum256(metadataJSON)
	return hex.EncodeToString(digest[:])
}
func (app *App) youtubeRip(ctx context.Context, ripMetadata Row) (err error) {
	defer func() {
		if err != nil {
			_, err := app.Store.DB.Exec(`INSERT INTO reinhardt_youtuberipfailure(url,artist,title,album,genre,year,comment,form_fingerprint,failure_message,retry_count,last_retried_at,created_at,modified_at) VALUES(?,?,?,?,?,?,?,?,?,0,NULL,?,?) ON CONFLICT(form_fingerprint) DO UPDATE SET failure_message=excluded.failure_message,modified_at=excluded.modified_at`, ripMetadata.S("url"), ripMetadata.S("artist"), ripMetadata.S("title"), ripMetadata.S("album"), ripMetadata.S("genre"), ripMetadata.S("year"), ripMetadata.S("comment"), fingerprint(ripMetadata), err.Error(), now(), now())
			if err != nil {
				log.Printf("record YouTube failure: %v", err)
			}
		} else {
			_, err := app.Store.DB.Exec("DELETE FROM reinhardt_youtuberipfailure WHERE form_fingerprint=?", fingerprint(ripMetadata))
			if err != nil {
				log.Printf("clear YouTube failure: %v", err)
			}
		}
	}()
	canonical, operationErr := youtubeURL(ripMetadata.S("url"))
	if operationErr != nil {
		return operationErr
	}
	if ripMetadata.S("artist") == "" || ripMetadata.S("title") == "" {
		return fmt.Errorf("artist and title are required")
	}
	dir, operationErr := app.Store.DefaultMusicDirectory()
	if operationErr != nil {
		return operationErr
	}
	tmp, operationErr := os.MkdirTemp("", "reinhardt-ytrip-*")
	if operationErr != nil {
		return operationErr
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "track.%(ext)s")
	// Ignore the service account's yt-dlp config: output names and MP3 extraction
	// must follow this pipeline's contract. Helpers are invoked without a shell.
	cmd := exec.CommandContext(ctx, "yt-dlp", "--ignore-config", "--no-playlist", "-x", "--audio-format", "mp3", "--audio-quality", "320k", "--output", out, "--", canonical)
	output, operationErr := cmd.CombinedOutput()
	if operationErr != nil {
		return fmt.Errorf("yt-dlp: %w: %s", operationErr, output)
	}
	path := filepath.Join(tmp, "track.mp3")
	ripMetadata["relative_path"] = "track.mp3"
	ripMetadata["full_path"] = path
	// Artwork is optional. Give thumbnail extraction its own short deadline and
	// continue with another cover source or no image if it fails.
	thumbnailCtx, thumbnailCancel := context.WithTimeout(ctx, 30*time.Second)
	thumbnail := exec.CommandContext(thumbnailCtx, "yt-dlp", "--ignore-config", "--no-playlist", "--skip-download", "--write-thumbnail", "--convert-thumbnails", "jpg", "--output", out, "--", canonical)
	if output, err := thumbnail.CombinedOutput(); err != nil {
		log.Printf("YouTube thumbnail: %v: %.1000s", err, output)
	}
	thumbnailCancel()
	var pic []byte
	pictureType := "image/jpeg"
	if data, err := os.ReadFile(filepath.Join(tmp, "track.jpg")); err == nil {
		var ext string
		pic, ext, _ = normalizedImage(bytes.NewReader(data))
		pictureType = contentType("cover" + ext)
	}
	if pic == nil {
		if im := app.discoverCover(ctx, ripMetadata); im != nil {
			pic, _ = os.ReadFile(path + ".jpg")
		}
	}
	if operationErr = writeID3(ripMetadata, pic, pictureType); operationErr != nil {
		return operationErr
	}
	dest, operationErr := app.Store.importFile(path, dir)
	if operationErr != nil {
		return fmt.Errorf("import downloaded audio into %q: %w", dir.S("local_path"), operationErr)
	}
	if pic != nil {
		if operationErr = atomicFile(dest+".jpg", bytes.NewReader(pic), false); operationErr != nil && !os.IsExist(operationErr) {
			log.Printf("YouTube sidecar: %v", operationErr)
		}
	}
	return nil
}
func (app *App) youtubeView(responseWriter http.ResponseWriter, request *http.Request, user Row, path string) error {
	if path == "ytrip/metadata/" {
		if err := safeOnly(request); err != nil {
			return err
		}
		canonical, err := youtubeURL(request.URL.Query().Get("url"))
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "yt-dlp", "--ignore-config", "--dump-single-json", "--skip-download", "--no-playlist", "--no-warnings", "--", canonical)
		output, err := cmd.Output()
		if err != nil {
			return httpError{502, "Could not fetch video metadata"}
		}
		var info map[string]any
		if err = json.Unmarshal(output, &info); err != nil || info == nil {
			return httpError{502, "Could not fetch video metadata"}
		}
		str := func(keys ...string) string {
			for _, key := range keys {
				if value := info[key]; value != nil && fmt.Sprint(value) != "" {
					return fmt.Sprint(value)
				}
			}
			return ""
		}
		artist := str("artist", "creator")
		if artist == "" {
			artist = strings.TrimSuffix(str("uploader"), " - Topic")
		}
		year := str("release_year")
		if year == "" {
			date := str("release_date", "upload_date")
			if len(date) >= 4 {
				year = date[:4]
			}
		}
		if !regexp.MustCompile(`^\d{4}$`).MatchString(year) {
			year = ""
		}
		return jsonResponse(responseWriter, Row{"artist": artist, "title": str("track", "title"), "album": str("album"), "year": year})
	}
	if path == "ytrip/failures/" {
		if !user.B("is_superuser") {
			return forbidden()
		}
		failures, err := app.Store.Query("SELECT * FROM reinhardt_youtuberipfailure ORDER BY modified_at DESC")
		if err != nil {
			return err
		}
		for _, failure := range failures {
			failure["modified_at"] = app.displayDate(failure.S("modified_at"))
		}
		return app.render(responseWriter, request, user, "reinhardt/ytrip_failures.html", TemplateContext{"failures": failures}, 200)
	}
	ctx := TemplateContext{}
	if request.Method == "POST" {
		ripMetadata := youtubeForm(request)
		if _, err := youtubeURL(ripMetadata.S("url")); err != nil {
			return err
		}
		if ripMetadata.S("artist") == "" || ripMetadata.S("title") == "" {
			return bad("Artist and title are required")
		}
		if _, err := app.Store.DefaultMusicDirectory(); err != nil {
			if !errors.Is(err, errNoMusicDirectory) {
				return err
			}
			log.Printf("YouTube download destination: %v", err)
			ctx["message"] = "No music directory is configured. Ask an administrator to register a music directory before downloading tracks."
			return app.render(responseWriter, request, user, "reinhardt/ytrip.html", ctx, http.StatusBadRequest)
		}
		if err := app.Store.Enqueue("youtube", ripMetadata); err != nil {
			return err
		}
		ctx["message"] = "added"
	}
	return app.render(responseWriter, request, user, "reinhardt/ytrip.html", ctx, 200)
}
func (app *App) retryYouTube(responseWriter http.ResponseWriter, request *http.Request, ripMetadata Row, id string) error {
	if err := postOnly(request); err != nil {
		return err
	}
	if !ripMetadata.B("is_superuser") {
		return forbidden()
	}
	failure, err := app.Store.One("SELECT * FROM reinhardt_youtuberipfailure WHERE id=?", id)
	if err != nil {
		return err
	}
	if _, err := app.Store.DefaultMusicDirectory(); err != nil {
		if errors.Is(err, errNoMusicDirectory) {
			return bad(err.Error())
		}
		return err
	}
	if _, err = app.Store.DB.Exec("UPDATE reinhardt_youtuberipfailure SET retry_count=retry_count+1,last_retried_at=?,modified_at=? WHERE id=?", now(), now(), id); err != nil {
		return err
	}
	if err = app.Store.Enqueue("youtube", failure); err != nil {
		return err
	}
	app.redirect(responseWriter, request, app.Store.URL("ytrip_failures"))
	return nil
}
