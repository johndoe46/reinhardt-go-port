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

func (s *Store) Enqueue(kind string, payload any) error {
	b, e := json.Marshal(payload)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec("INSERT INTO go_job(kind,payload,run_at) VALUES(?,?,?)", kind, string(b), time.Now().Unix())
	return e
}
func (s *Store) enqueueSync(id int64) error {
	if s.C["JAMA_ENDPOINT"] != "" && s.C["JAMA_KEY"] != "" && s.C["JAMA_PROJECT"] != "" {
		if e := s.Enqueue("jama", id); e != nil {
			return e
		}
	}
	if s.C["DBX_TOKEN"] != "" {
		return s.Enqueue("dropbox", id)
	}
	return nil
}
func (a *App) Work(ctx context.Context, once bool) error {
	nextGather := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if a.S.C["MUSIC_IMPORT_DIR"] != "" && time.Now().After(nextGather) {
			if e := a.S.Gather(ctx); e != nil {
				log.Printf("music import: %v", e)
			}
			nextGather = time.Now().Add(5 * time.Minute)
		}
		// Claim in one SQLite statement so separate workers cannot select the same
		// ready job. Locks become reclaimable after an hour, longer than the
		// 30-minute job deadline. Handlers must tolerate reruns if a worker dies
		// after external side effects but before deleting the job.
		job, e := a.S.One(`UPDATE go_job SET locked_at=? WHERE id=(SELECT id FROM go_job WHERE run_at<=? AND attempts<3 AND (locked_at IS NULL OR locked_at<?) ORDER BY id LIMIT 1) RETURNING *`, time.Now().Unix(), time.Now().Unix(), time.Now().Unix()-3600)
		if e != nil && e != sql.ErrNoRows {
			return e
		}
		if e == nil {
			jobCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
			var err error
			switch job.S("kind") {
			case "youtube":
				var data Row
				err = json.Unmarshal([]byte(job.S("payload")), &data)
				if err == nil {
					err = a.youtubeRip(jobCtx, data)
				}
			case "jama", "dropbox":
				var id int64
				err = json.Unmarshal([]byte(job.S("payload")), &id)
				if err == nil {
					if job.S("kind") == "jama" {
						err = a.syncJama(jobCtx, id)
					} else {
						err = a.syncDropbox(jobCtx, id)
					}
				}
			default:
				err = fmt.Errorf("unknown job kind %q", job.S("kind"))
			}
			cancel()
			if err == nil {
				_, e = a.S.DB.Exec("DELETE FROM go_job WHERE id=?", job.N("id"))
			} else {
				log.Printf("job %d (%s): %v", job.N("id"), job.S("kind"), err)
				_, e = a.S.DB.Exec("UPDATE go_job SET attempts=attempts+1,run_at=?,locked_at=NULL,error=? WHERE id=?", time.Now().Add(10*time.Minute).Unix(), err.Error(), job.N("id"))
			}
			if e != nil {
				return e
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

func libraryPath(m Row, ext string) (string, error) {
	artist, title := slug(m.S("artist")), slug(m.S("title"))
	if artist == "" || title == "" {
		return "", fmt.Errorf("artist and title must contain letters or numbers")
	}
	album := slug(m.S("album"))
	if album == "" {
		album = "unknown"
	}
	return filepath.Join(artist[:1], artist, album, fmt.Sprintf("%02d-%s%s", m.N("track_number"), title, strings.ToLower(ext))), nil
}
func (s *Store) importFile(path string, dir Row) (string, error) {
	m, e := s.Metadata(path)
	if e != nil {
		return "", e
	}
	rel, e := libraryPath(m, filepath.Ext(path))
	if e != nil {
		return "", e
	}
	dest := filepath.Join(dir.S("local_path"), rel)
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	e = atomicFile(dest, f, false)
	f.Close()
	if e != nil {
		return "", e
	}
	if e = s.indexFile(dest, dir); e != nil {
		return "", e
	}
	// Keep the import source until both publication and indexing succeed. Those
	// steps span the filesystem and SQLite, so they cannot share a transaction.
	if e = os.Remove(path); e != nil {
		return "", e
	}
	return dest, nil
}
func (s *Store) Gather(ctx context.Context) error {
	root := s.C["MUSIC_IMPORT_DIR"]
	if root == "" {
		return fmt.Errorf("MUSIC_IMPORT_DIR is not configured")
	}
	dir, e := s.DefaultMusicDirectory()
	if e != nil {
		return e
	}
	var archives []string
	if e = filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() && strings.ToLower(filepath.Ext(path)) == ".zip" {
			archives = append(archives, path)
		}
		return nil
	}); e != nil {
		return e
	}
	for _, path := range archives {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e = extractArchive(path, root, int64(s.C.Int("MAX_UPLOAD_BYTES"))); e != nil {
			log.Printf("archive %s: %v", path, e)
			continue
		}
		if e = os.Remove(path); e != nil {
			return e
		}
	}
	return walkMusic(root, func(path string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, e := s.importFile(path, dir); e != nil {
			log.Printf("import %s: %v", path, e)
		}
		return nil
	})
}
func extractArchive(path, root string, max int64) error {
	z, e := zip.OpenReader(path)
	if e != nil {
		return e
	}
	defer z.Close()
	var total uint64
	// Validate every entry before creating any destination files.
	for _, entry := range z.File {
		clean := filepath.Clean(filepath.FromSlash(entry.Name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe archive path")
		}
		total += entry.UncompressedSize64
		if total > uint64(max) {
			return fmt.Errorf("archive too large")
		}
	}
	for _, entry := range z.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		dest := filepath.Join(root, filepath.FromSlash(entry.Name))
		// Safe archive names alone are insufficient: an existing symlink in the
		// destination tree could redirect a relative path outside the import root.
		if e = rejectSymlinkParents(root, dest); e != nil {
			return e
		}
		reader, e := entry.Open()
		if e != nil {
			return e
		}
		e = atomicFile(dest, io.LimitReader(reader, max+1), false)
		reader.Close()
		if e != nil {
			return e
		}
	}
	return nil
}
func rejectSymlinkParents(root, dest string) error {
	root, e := filepath.Abs(root)
	if e != nil {
		return e
	}
	dest, e = filepath.Abs(dest)
	if e != nil {
		return e
	}
	rel, e := filepath.Rel(root, dest)
	if e != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("path escapes import directory")
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if info, e := os.Lstat(current); e == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in import path")
		}
	}
	return nil
}
func (a *App) upload(w http.ResponseWriter, r *http.Request, u Row) error {
	if !u.B("is_superuser") {
		return forbidden()
	}
	if r.Method == "POST" {
		root := a.S.C["MUSIC_IMPORT_DIR"]
		if root == "" {
			return bad("MUSIC_IMPORT_DIR is not configured")
		}
		f, header, e := r.FormFile("file")
		if e != nil {
			return bad("A file is required")
		}
		defer f.Close()
		name := filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
		if name == "." || name == ".." || name == "" {
			return bad("Invalid filename")
		}
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".mp3" && ext != ".m4a" && ext != ".zip" {
			return bad("Upload an MP3, M4A or ZIP file")
		}
		if e = atomicFile(filepath.Join(root, name), f, false); e != nil {
			return bad(e.Error())
		}
	}
	return a.render(w, r, u, "reinhardt/upload.html", nil, 200)
}

var videoID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func youtubeURL(raw string) (string, error) {
	if len(raw) > 2048 {
		return "", bad("Invalid YouTube URL")
	}
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Port() != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", bad("Invalid YouTube URL")
	}
	id := ""
	switch strings.ToLower(u.Hostname()) {
	case "youtu.be":
		id = strings.TrimPrefix(u.Path, "/")
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com":
		if u.Path == "/watch" {
			id = u.Query().Get("v")
		} else if strings.HasPrefix(u.Path, "/shorts/") || strings.HasPrefix(u.Path, "/live/") {
			id = strings.Split(u.Path, "/")[2]
		}
	}
	if !videoID.MatchString(id) {
		return "", bad("Invalid YouTube URL")
	}
	// Pass only a validated single-video URL to yt-dlp; discard playlist, seek,
	// and other supplied query parameters before invoking the helper.
	return "https://www.youtube.com/watch?v=" + id, nil
}
func youtubeForm(r *http.Request) Row {
	m := Row{}
	for _, f := range []string{"url", "artist", "title", "album", "genre", "year", "comment"} {
		m[f] = strings.TrimSpace(r.FormValue(f))
	}
	return m
}
func fingerprint(m Row) string {
	// Deliberately select only form fields. Retried rows also contain database
	// IDs and failure details, which must not change the failure's identity.
	data := map[string]string{}
	for _, f := range []string{"url", "artist", "title", "album", "genre", "year", "comment"} {
		data[f] = strings.TrimSpace(m.S(f))
	}
	b, _ := json.Marshal(data)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (a *App) youtubeRip(ctx context.Context, m Row) (err error) {
	defer func() {
		if err != nil {
			_, e := a.S.DB.Exec(`INSERT INTO reinhardt_youtuberipfailure(url,artist,title,album,genre,year,comment,form_fingerprint,failure_message,retry_count,last_retried_at,created_at,modified_at) VALUES(?,?,?,?,?,?,?,?,?,0,NULL,?,?) ON CONFLICT(form_fingerprint) DO UPDATE SET failure_message=excluded.failure_message,modified_at=excluded.modified_at`, m.S("url"), m.S("artist"), m.S("title"), m.S("album"), m.S("genre"), m.S("year"), m.S("comment"), fingerprint(m), err.Error(), now(), now())
			if e != nil {
				log.Printf("record YouTube failure: %v", e)
			}
		} else {
			_, e := a.S.DB.Exec("DELETE FROM reinhardt_youtuberipfailure WHERE form_fingerprint=?", fingerprint(m))
			if e != nil {
				log.Printf("clear YouTube failure: %v", e)
			}
		}
	}()
	canonical, e := youtubeURL(m.S("url"))
	if e != nil {
		return e
	}
	if m.S("artist") == "" || m.S("title") == "" {
		return fmt.Errorf("artist and title are required")
	}
	dir, e := a.S.DefaultMusicDirectory()
	if e != nil {
		return e
	}
	tmp, e := os.MkdirTemp("", "reinhardt-ytrip-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "track.%(ext)s")
	// Ignore the service account's yt-dlp config: output names and MP3 extraction
	// must follow this pipeline's contract. Helpers are invoked without a shell.
	cmd := exec.CommandContext(ctx, "yt-dlp", "--ignore-config", "--no-playlist", "-x", "--audio-format", "mp3", "--audio-quality", "320k", "--output", out, "--", canonical)
	output, e := cmd.CombinedOutput()
	if e != nil {
		return fmt.Errorf("yt-dlp: %w: %s", e, output)
	}
	path := filepath.Join(tmp, "track.mp3")
	m["relative_path"] = "track.mp3"
	m["full_path"] = path
	// Artwork is optional. Give thumbnail extraction its own short deadline and
	// continue with another cover source or no image if it fails.
	thumbnailCtx, thumbnailCancel := context.WithTimeout(ctx, 30*time.Second)
	thumbnail := exec.CommandContext(thumbnailCtx, "yt-dlp", "--ignore-config", "--no-playlist", "--skip-download", "--write-thumbnail", "--convert-thumbnails", "jpg", "--output", out, "--", canonical)
	if output, e := thumbnail.CombinedOutput(); e != nil {
		log.Printf("YouTube thumbnail: %v: %.1000s", e, output)
	}
	thumbnailCancel()
	var pic []byte
	pictureType := "image/jpeg"
	if data, e := os.ReadFile(filepath.Join(tmp, "track.jpg")); e == nil {
		var ext string
		pic, ext, _ = normalizedImage(bytes.NewReader(data))
		pictureType = contentType("cover" + ext)
	}
	if pic == nil {
		if im := a.discoverCover(ctx, m); im != nil {
			pic, _ = os.ReadFile(path + ".jpg")
		}
	}
	if e = writeID3(m, pic, pictureType); e != nil {
		return e
	}
	dest, e := a.S.importFile(path, dir)
	if e != nil {
		return fmt.Errorf("import downloaded audio into %q: %w", dir.S("local_path"), e)
	}
	if pic != nil {
		if e = atomicFile(dest+".jpg", bytes.NewReader(pic), false); e != nil && !os.IsExist(e) {
			log.Printf("YouTube sidecar: %v", e)
		}
	}
	return nil
}
func (a *App) youtubeView(w http.ResponseWriter, r *http.Request, u Row, path string) error {
	if path == "ytrip/metadata/" {
		if e := safeOnly(r); e != nil {
			return e
		}
		canonical, e := youtubeURL(r.URL.Query().Get("url"))
		if e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "yt-dlp", "--ignore-config", "--dump-single-json", "--skip-download", "--no-playlist", "--no-warnings", "--", canonical)
		output, e := cmd.Output()
		if e != nil {
			return httpError{502, "Could not fetch video metadata"}
		}
		var info map[string]any
		if e = json.Unmarshal(output, &info); e != nil || info == nil {
			return httpError{502, "Could not fetch video metadata"}
		}
		str := func(keys ...string) string {
			for _, key := range keys {
				if v := info[key]; v != nil && fmt.Sprint(v) != "" {
					return fmt.Sprint(v)
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
		return jsonResponse(w, Row{"artist": artist, "title": str("track", "title"), "album": str("album"), "year": year})
	}
	if path == "ytrip/failures/" {
		if !u.B("is_superuser") {
			return forbidden()
		}
		failures, e := a.S.Query("SELECT * FROM reinhardt_youtuberipfailure ORDER BY modified_at DESC")
		if e != nil {
			return e
		}
		for _, f := range failures {
			f["modified_at"] = a.displayDate(f.S("modified_at"))
		}
		return a.render(w, r, u, "reinhardt/ytrip_failures.html", TemplateContext{"failures": failures}, 200)
	}
	ctx := TemplateContext{}
	if r.Method == "POST" {
		m := youtubeForm(r)
		if _, e := youtubeURL(m.S("url")); e != nil {
			return e
		}
		if m.S("artist") == "" || m.S("title") == "" {
			return bad("Artist and title are required")
		}
		if _, e := a.S.DefaultMusicDirectory(); e != nil {
			if !errors.Is(e, errNoMusicDirectory) {
				return e
			}
			log.Printf("YouTube download destination: %v", e)
			ctx["message"] = "No music directory is configured. Ask an administrator to register a music directory before downloading tracks."
			return a.render(w, r, u, "reinhardt/ytrip.html", ctx, http.StatusBadRequest)
		}
		if e := a.S.Enqueue("youtube", m); e != nil {
			return e
		}
		ctx["message"] = "added"
	}
	return a.render(w, r, u, "reinhardt/ytrip.html", ctx, 200)
}
func (a *App) retryYouTube(w http.ResponseWriter, r *http.Request, u Row, id string) error {
	if e := postOnly(r); e != nil {
		return e
	}
	if !u.B("is_superuser") {
		return forbidden()
	}
	m, e := a.S.One("SELECT * FROM reinhardt_youtuberipfailure WHERE id=?", id)
	if e != nil {
		return e
	}
	if _, e := a.S.DefaultMusicDirectory(); e != nil {
		if errors.Is(e, errNoMusicDirectory) {
			return bad(e.Error())
		}
		return e
	}
	if _, e = a.S.DB.Exec("UPDATE reinhardt_youtuberipfailure SET retry_count=retry_count+1,last_retried_at=?,modified_at=? WHERE id=?", now(), now(), id); e != nil {
		return e
	}
	if e = a.S.Enqueue("youtube", m); e != nil {
		return e
	}
	a.redirect(w, r, a.S.URL("ytrip_failures"))
	return nil
}
