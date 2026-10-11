package reinhardt

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"log"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	id3 "github.com/bogem/id3v2/v2"
	"github.com/dhowden/tag"
	"github.com/tcolgate/mp3"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

func fileExtension(m Row) string { return filepath.Ext(m.S("relative_path")) }
func contentType(path string) string {
	t := mime.TypeByExtension(filepath.Ext(path))
	if t == "" {
		t = "application/octet-stream"
	}
	return t
}
func serveLocal(w http.ResponseWriter, r *http.Request, m Row, attachment bool) error {
	f, e := os.Open(m.S("full_path"))
	if os.IsNotExist(e) {
		return sql.ErrNoRows
	}
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	w.Header().Set("Content-Type", contentType(m.S("relative_path")))
	disposition := "inline"
	if attachment {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": m.S("clean_file_name")}))
	// Keep the seekable file: ServeContent handles HEAD, conditional requests,
	// and byte ranges that the browser uses to seek within an audio track.
	http.ServeContent(w, r, m.S("clean_file_name"), info.ModTime(), f)
	return nil
}
func (a *App) audio(w http.ResponseWriter, r *http.Request, parts []string) error {
	m, e := a.S.Track(parts[1])
	if e != nil {
		return e
	}
	if parts[0] == "download" {
		if len(parts) < 3 {
			return sql.ErrNoRows
		}
		if parts[2] != m.S("clean_file_name") {
			a.redirect(w, r, m.S("download_url"))
			return nil
		}
		return serveLocal(w, r, m, false)
	}
	if a.S.C["DELEGATE_DOWNLOAD_PREFIX"] == "" {
		return serveLocal(w, r, m, false)
	}
	req, e := http.NewRequestWithContext(r.Context(), r.Method, m.S("download_url"), nil)
	if e != nil {
		return e
	}
	// Range offsets and Content-Length must describe the original audio bytes;
	// transparent upstream compression would break those values during relaying.
	req.Header.Set("Accept-Encoding", "identity")
	for _, h := range []string{"Range", "If-Range"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	// API timeouts include the response body. Delegated audio may stay open for
	// the duration of a song, so use a client bounded only during connection/headers.
	resp, e := a.AudioClient.Do(req)
	if e != nil {
		return httpError{502, "Audio server unavailable."}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 && resp.StatusCode != 416 {
		if resp.StatusCode == 404 {
			return sql.ErrNoRows
		}
		return httpError{502, "Audio stream unavailable."}
	}
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", contentType(m.S("relative_path")))
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method != "HEAD" {
		// Browser seeks commonly cancel a previous stream. That cancellation also
		// stops the upstream request and should not be logged as a relay failure.
		if n, err := io.Copy(w, resp.Body); err != nil && r.Context().Err() == nil {
			log.Printf("audio relay track %s: received/wrote %d bytes (upstream length %s): %v", m.S("id"), n, resp.Header.Get("Content-Length"), err)
		}
	}
	return nil
}

func atomicFile(dest string, reader io.Reader, overwrite bool) error {
	if e := os.MkdirAll(filepath.Dir(dest), 0755); e != nil {
		return e
	}
	// Stage beside the destination so link/rename publication stays on the same
	// filesystem. Readers see a complete file, not an in-progress copy.
	f, e := os.CreateTemp(filepath.Dir(dest), ".reinhardt-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = io.Copy(f, reader); e != nil {
		f.Close()
		return e
	}
	if e = f.Chmod(0644); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if !overwrite { // Link fails atomically if a library file already exists.
		return os.Link(f.Name(), dest)
	}
	return os.Rename(f.Name(), dest)
}
func atomicModify(path string, modify func(string) error) error {
	// Tag writers can rewrite the whole file. Work on a copy so a failed edit
	// leaves the original playable; existing Unix readers retain the old inode
	// after publication. This does not serialize simultaneous edits to one file.
	src, e := os.Open(path)
	if e != nil {
		return e
	}
	defer src.Close()
	info, e := src.Stat()
	if e != nil {
		return e
	}
	dst, e := os.CreateTemp(filepath.Dir(path), ".reinhardt-*"+filepath.Ext(path))
	if e != nil {
		return e
	}
	temp := dst.Name()
	defer os.Remove(temp)
	if _, e = io.Copy(dst, src); e != nil {
		dst.Close()
		return e
	}
	if e = dst.Close(); e != nil {
		return e
	}
	if e = modify(temp); e != nil {
		return e
	}
	if e = os.Chmod(temp, info.Mode()); e != nil {
		return e
	}
	return os.Rename(temp, path)
}
func writeID3(m Row, picture []byte, pictureType string) error {
	if strings.ToLower(fileExtension(m)) != ".mp3" {
		return nil
	}
	return atomicModify(m.S("full_path"), func(path string) error {
		t, e := id3.Open(path, id3.Options{Parse: true})
		if e != nil {
			return e
		}
		defer t.Close()
		t.SetVersion(3)
		t.SetDefaultEncoding(id3.EncodingUTF8)
		for _, f := range []string{"TPE1", "TIT2", "TALB", "TCON", "TDRC", "TYER", "COMM"} {
			t.DeleteFrames(f)
		}
		t.SetArtist(m.S("artist"))
		t.SetTitle(m.S("title"))
		t.SetAlbum(m.S("album"))
		t.SetGenre(m.S("genre"))
		if m.S("year") != "" {
			t.AddTextFrame("TYER", id3.EncodingUTF8, m.S("year"))
		}
		if m.S("comment") != "" {
			t.AddCommentFrame(id3.CommentFrame{Encoding: id3.EncodingUTF8, Language: "eng", Text: m.S("comment")})
		}
		if picture != nil {
			t.DeleteFrames("APIC")
			t.AddAttachedPicture(id3.PictureFrame{Encoding: id3.EncodingUTF8, MimeType: pictureType, PictureType: 3, Description: "Cover", Picture: picture})
		}
		return t.Save()
	})
}

func decodedImage(reader io.ReadSeeker) (image.Image, string, error) {
	// An HTTP/upload byte limit does not bound decoded pixel memory. Inspect the
	// dimensions before allocating the image, then rewind for the actual decoder.
	conf, kind, e := image.DecodeConfig(reader)
	if e != nil {
		return nil, "", e
	}
	if conf.Width <= 0 || conf.Height <= 0 || int64(conf.Width)*int64(conf.Height) > 40000000 {
		return nil, "", fmt.Errorf("image dimensions too large")
	}
	if _, e = reader.Seek(0, io.SeekStart); e != nil {
		return nil, "", e
	}
	im, _, e := image.Decode(reader)
	return im, kind, e
}
func normalizedImage(reader io.ReadSeeker) ([]byte, string, error) {
	im, kind, e := decodedImage(reader)
	if e != nil {
		return nil, "", e
	}
	var b bytes.Buffer
	if kind == "jpeg" {
		e = jpeg.Encode(&b, im, &jpeg.Options{Quality: 95})
		return b.Bytes(), ".jpg", e
	}
	e = png.Encode(&b, im)
	return b.Bytes(), ".png", e
}
func (a *App) trackDetails(w http.ResponseWriter, r *http.Request, u Row, id string) error {
	m, e := a.S.Track(id)
	if e != nil {
		return e
	}
	message := ""
	status := 200
	if r.Method == "POST" {
		if !u.B("is_superuser") {
			return forbidden()
		}
		for _, field := range []string{"artist", "title", "album", "genre", "year", "comment"} {
			m[field] = strings.TrimSpace(r.FormValue(field))
		}
		if m.S("artist") == "" || m.S("title") == "" {
			message = "not enough data"
			status = 400
		} else {
			var data []byte
			var ext string
			upload, _, err := r.FormFile("track_image")
			if err != nil && err != http.ErrMissingFile {
				return bad("Invalid upload")
			}
			if err == nil {
				defer upload.Close()
				data, ext, e = normalizedImage(upload)
				if e != nil {
					message = "invalid image"
					status = 400
				} else if _, e = os.Stat(m.S("full_path")); e != nil {
					message = "audio file not found"
					status = 400
				}
			}
			if status == 200 {
				if e = writeID3(m, data, contentType("cover"+ext)); e != nil {
					log.Printf("metadata writing: %v", e)
				}
				if data != nil {
					if e = atomicFile(m.S("full_path")+ext, bytes.NewReader(data), true); e != nil {
						return e
					}
					a.invalidateCovers(m.N("id"))
					if fileExtension(m) != ".mp3" {
						if err := embedArtwork(r.Context(), m, m.S("full_path")+ext); err != nil {
							log.Printf("embed artwork: %v", err)
						}
					}
				}
				_, e = a.S.DB.Exec(`UPDATE reinhardt_musicfile SET artist=?,title=?,album=?,genre=?,year=?,comment=?,modified_at=? WHERE id=?`, m["artist"], m["title"], m["album"], m["genre"], m["year"], m["comment"], now(), m.N("id"))
				if e != nil {
					return e
				}
				if e = a.S.enqueueSync(m.N("id")); e != nil {
					return e
				}
				a.redirect(w, r, a.S.URL("index"))
				return nil
			}
		}
	}
	return a.render(w, r, u, "reinhardt/track_details_modal.html", TemplateContext{"music_file": m, "message": message}, status)
}

func (a *App) invalidateCovers(id int64) {
	files, _ := filepath.Glob(filepath.Join(a.S.C["COVERS_DIR"], fmt.Sprintf("*-%d-*", id)))
	for _, f := range files {
		os.Remove(f)
	}
}
func nearbyPicture(m Row) string {
	path := m.S("full_path")
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".gif"} {
		if info, e := os.Stat(path + ext); e == nil && info.Mode().IsRegular() {
			return path + ext
		}
	}
	for _, dir := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
		for _, ext := range []string{".jpg", ".png"} {
			files, _ := filepath.Glob(filepath.Join(dir, "*"+ext))
			if len(files) > 0 {
				return files[0]
			}
		}
	}
	return ""
}
func cropSquare(im image.Image, size int) image.Image {
	b := im.Bounds()
	side := b.Dx()
	if b.Dy() < side {
		side = b.Dy()
	}
	x, y := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(out, out.Bounds(), im, image.Rect(x, y, x+side, y+side), draw.Over, nil)
	return out
}
func fallbackArtwork(m Row, size int) image.Image {
	h := sha256.Sum256([]byte(m.S("artist") + "/" + m.S("album") + "/" + m.S("title")))
	im := image.NewRGBA(image.Rect(0, 0, 512, 512))
	paper := color.RGBA{242, 232, 213, 255}
	ink := color.RGBA{36 + h[0]%35, 45 + h[1]%35, 55 + h[2]%35, 255}
	accent := color.RGBA{130 + h[3]%90, 90 + h[4]%80, 65 + h[5]%80, 255}
	draw.Draw(im, im.Bounds(), image.NewUniform(paper), image.Point{}, draw.Src)
	draw.Draw(im, image.Rect(0, 0, 12, 512), image.NewUniform(accent), image.Point{}, draw.Src)
	for y := 0; y < 400; y++ {
		for x := 80; x < 512; x++ {
			dx, dy := x-345, y-165
			d := dx*dx + dy*dy
			if d < 225*225 {
				c := ink
				if d < 70*70 {
					c = accent
				} else {
					rr := intSqrt(d)
					if rr%9 < 2 {
						c = color.RGBA{ink.R + 20, ink.G + 20, ink.B + 20, 255}
					}
				}
				if d < 10*10 {
					c = paper
				}
				im.Set(x, y, c)
			}
		}
	}
	drawFallbackArtworkText(im, ink, m.S("artist"), m.S("title"))
	return cropSquare(im, size)
}
func intSqrt(n int) int {
	return int(math.Sqrt(float64(n)))
}
func (a *App) artwork(w http.ResponseWriter, r *http.Request, parts []string) error {
	if len(parts) != 3 {
		return sql.ErrNoRows
	}
	size, e := strconv.Atoi(strings.TrimSuffix(parts[2], ".jpg"))
	if e != nil {
		return bad("Invalid artwork size")
	}
	switch size {
	case 96, 128, 192, 256, 384, 512:
	default:
		return bad("Invalid artwork size")
	}
	m, e := a.S.Track(parts[1])
	if e != nil {
		return e
	}
	cache := filepath.Join(a.S.C["COVERS_DIR"], fmt.Sprintf("thumb-%s-%d-square.jpg", m.S("id"), size))
	if f, e := os.Open(cache); e == nil {
		defer f.Close()
		info, e := f.Stat()
		if e != nil {
			return e
		}
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeContent(w, r, "artwork.jpg", info.ModTime(), f)
		return nil
	}
	var im image.Image
	// A track-specific sidecar records an explicit edit and must beat stale embedded artwork.
	if picture := nearbyPicture(m); strings.HasPrefix(picture, m.S("full_path")) && picture != "" {
		if f, e := os.Open(picture); e == nil {
			im, _, _ = decodedImage(f)
			f.Close()
		}
	}
	if im == nil {
		if f, e := os.Open(m.S("full_path")); e == nil {
			if metadata, e := tag.ReadFrom(f); e == nil && metadata.Picture() != nil {
				im, _, _ = decodedImage(bytes.NewReader(metadata.Picture().Data))
			}
			f.Close()
		}
	}
	if im == nil {
		if path := nearbyPicture(m); path != "" {
			if f, e := os.Open(path); e == nil {
				im, _, _ = decodedImage(f)
				f.Close()
			}
		}
	}
	if im == nil {
		im = a.discoverCover(r.Context(), m)
	}
	found := im != nil
	if found {
		im = cropSquare(im, size)
	} else {
		im = fallbackArtwork(m, size)
	}
	var b bytes.Buffer
	if e = jpeg.Encode(&b, im, &jpeg.Options{Quality: 85}); e != nil {
		return e
	}
	// Persist actual covers only. Saving a generated placeholder here would hide
	// artwork added or discovered later until someone explicitly removed the file.
	if found {
		if e = atomicFile(cache, bytes.NewReader(b.Bytes()), true); e != nil {
			return e
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(w, r, "artwork.jpg", time.Time{}, bytes.NewReader(b.Bytes()))
	return nil
}

func (a *App) archive(w http.ResponseWriter, r *http.Request, t Row) error {
	tracks, e := a.S.TagTracks(t.N("id"))
	if e != nil {
		return e
	}
	f, e := os.CreateTemp("", "reinhardt-*.zip")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	z := zip.NewWriter(f)
	for i, m := range tracks {
		src, e := os.Open(m.S("full_path"))
		if e != nil {
			z.Close()
			return e
		}
		info, e := src.Stat()
		if e != nil {
			src.Close()
			z.Close()
			return e
		}
		header, e := zip.FileInfoHeader(info)
		if e != nil {
			src.Close()
			z.Close()
			return e
		}
		header.Name = fmt.Sprintf("%03d-%s", i+1, m.S("clean_file_name"))
		// Audio is already compressed; deflating it adds CPU cost for little gain.
		header.Method = zip.Store
		dst, e := z.CreateHeader(header)
		if e == nil {
			_, e = io.Copy(dst, src)
		}
		src.Close()
		if e != nil {
			z.Close()
			return e
		}
	}
	if e = z.Close(); e != nil {
		return e
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return e
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": slug(t.S("label")) + ".zip"}))
	http.ServeContent(w, r, "playlist.zip", time.Time{}, f)
	return nil
}
func (a *App) databaseBackup(w http.ResponseWriter, r *http.Request) error {
	f, e := os.CreateTemp("", "reinhardt-*.sqlite3")
	if e != nil {
		return e
	}
	name := f.Name()
	f.Close()
	defer os.Remove(name)
	if e = os.Remove(name); e != nil {
		return e
	}
	// VACUUM INTO takes a consistent snapshot, including data in the WAL.
	// Copying DATABASE_PATH alone can miss committed rows still in its WAL file.
	if _, e = a.S.DB.Exec("VACUUM INTO ?", name); e != nil {
		return e
	}
	file, e := os.Open(name)
	if e != nil {
		return e
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="reinhardt.sqlite3"`)
	http.ServeContent(w, r, "reinhardt.sqlite3", time.Time{}, file)
	return nil
}

var genrePrefix = regexp.MustCompile(`^\([0-9]+\)(.+)`)

func (c Config) sanitize(s, field string) string {
	if !c.Bool("SANITIZE_TAGS") {
		return s
	}
	s = strings.ReplaceAll(s, "/", " / ")
	s = strings.ReplaceAll(s, "&", " & ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 1 && s[0] == '"' && s[len(s)-1] == '"' {
		s = s[1 : len(s)-1]
	}
	if field == "artist" || field == "genre" {
		s = strings.ReplaceAll(strings.ToLower(s), " & ", " and ")
	}
	if field == "artist" {
		for _, pair := range [][2]string{{" feat. ", " featuring "}, {" feat ", " featuring "}, {" vs. ", " versus "}, {" vs ", " versus "}} {
			s = strings.ReplaceAll(s, pair[0], pair[1])
		}
		words := strings.Fields(s)
		for i, w := range words {
			rs := []rune(w)
			if len(rs) > 0 {
				words[i] = strings.ToUpper(string(rs[:1])) + string(rs[1:])
			}
		}
		s = strings.Join(words, " ")
		if strings.HasPrefix(s, "The ") {
			s = strings.TrimPrefix(s, "The ") + " (the)"
		}
		if strings.HasPrefix(s, "Dj ") {
			s = strings.TrimPrefix(s, "Dj ") + " (DJ)"
		}
		switch strings.ToLower(s) {
		case "various", "various artist", "various artists":
			s = "various"
		}
	}
	if field == "genre" {
		switch s {
		case "bo", "bof", "b.o.f.", "soundtracks":
			s = "soundtrack"
		case "genre inconnu", "inconnu", "unknown", "(255)":
			s = ""
		}
		if m := genrePrefix.FindStringSubmatch(s); m != nil {
			s = m[1]
		}
		s = strings.TrimSpace(strings.Split(s, "/")[0])
	}
	return s
}
func (s *Store) Metadata(path string) (Row, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	m, e := tag.ReadFrom(f)
	if e != nil {
		return nil, e
	}
	track, _ := m.Track()
	genre := m.Genre()
	// Numeric ID3 genre prefixes are expanded by the reader, duplicating text
	// in values like "(18)Techno". Sanitize the original frame instead.
	for _, key := range []string{"TCON", "TCO"} {
		if raw, ok := m.Raw()[key].(string); ok {
			genre = raw
			break
		}
	}
	year := ""
	if y := m.Year(); y != 0 {
		year = strconv.Itoa(y)
	}
	info, e := f.Stat()
	if e != nil {
		return nil, e
	}
	duration := 0
	if strings.ToLower(filepath.Ext(path)) == ".mp3" {
		f.Seek(0, io.SeekStart)
		dec := mp3.NewDecoder(f)
		var frame mp3.Frame
		var skipped int
		var total time.Duration
		for dec.Decode(&frame, &skipped) == nil {
			total += frame.Duration()
		}
		duration = int(total.Seconds())
	} else {
		duration = mp4Duration(f)
	}
	return Row{"artist": s.C.sanitize(m.Artist(), "artist"), "title": s.C.sanitize(m.Title(), ""), "album": s.C.sanitize(m.Album(), ""), "genre": s.C.sanitize(genre, "genre"), "year": year, "comment": strings.TrimSpace(m.Comment()), "duration": duration, "track_number": track, "created_at": info.ModTime().UTC().Format("2006-01-02 15:04:05.000000"), "modified_at": info.ModTime().UTC().Format("2006-01-02 15:04:05.000000")}, nil
}
func mp4Duration(f *os.File) int {
	info, e := f.Stat()
	if e != nil {
		return 0
	}
	var walk func(int64, int64, int) int
	walk = func(start, end int64, depth int) int {
		if depth > 8 {
			return 0
		}
		for off := start; off+8 <= end; {
			var h [16]byte
			if _, e := f.ReadAt(h[:8], off); e != nil {
				return 0
			}
			size := int64(binary.BigEndian.Uint32(h[:4]))
			head := int64(8)
			kind := string(h[4:8])
			if size == 1 {
				if _, e := f.ReadAt(h[8:], off+8); e != nil {
					return 0
				}
				size = int64(binary.BigEndian.Uint64(h[8:]))
				head = 16
			}
			if size == 0 {
				size = end - off
			}
			if size < head || size > end-off {
				return 0
			}
			if kind == "mdhd" && size >= head+24 {
				var b [32]byte
				n, _ := f.ReadAt(b[:], off+head)
				if b[0] == 0 && n >= 20 {
					scale := binary.BigEndian.Uint32(b[12:16])
					if scale > 0 {
						return int(binary.BigEndian.Uint32(b[16:20]) / scale)
					}
				}
				if b[0] == 1 && n >= 32 {
					scale := binary.BigEndian.Uint32(b[20:24])
					if scale > 0 {
						return int(binary.BigEndian.Uint64(b[24:32]) / uint64(scale))
					}
				}
			}
			if kind == "moov" || kind == "trak" || kind == "mdia" {
				if n := walk(off+head, off+size, depth+1); n > 0 {
					return n
				}
			}
			off += size
		}
		return 0
	}
	return walk(0, info.Size(), 0)
}
func (s *Store) indexFile(path string, dir Row) error {
	m, e := s.Metadata(path)
	if e != nil {
		return e
	}
	if m.S("artist") == "" || m.S("title") == "" {
		return fmt.Errorf("artist and title are required")
	}
	relative, e := filepath.Rel(dir.S("local_path"), path)
	if e != nil {
		return e
	}
	// The legacy schema makes relative_path globally unique, even across music
	// directories. Update in place so rescans preserve IDs and playlist membership;
	// INSERT OR REPLACE would delete the old row and cascade its associations.
	_, e = s.DB.Exec(`INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,year,comment,duration,track_number,created_at,modified_at,missing,hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,0,NULL) ON CONFLICT(relative_path) DO UPDATE SET artist=excluded.artist,title=excluded.title,album=excluded.album,genre=excluded.genre,year=excluded.year,comment=excluded.comment,duration=excluded.duration,track_number=excluded.track_number,modified_at=excluded.modified_at,missing=0`, dir.N("id"), relative, m["artist"], m["title"], m["album"], m["genre"], m["year"], m["comment"], m["duration"], m["track_number"], m["created_at"], m["modified_at"])
	if e != nil {
		return e
	}
	r, e := s.One("SELECT id FROM reinhardt_musicfile WHERE relative_path=?", relative)
	if e != nil {
		return fmt.Errorf("find indexed music file %q in database %q: %w", relative, s.C["DATABASE_PATH"], e)
	}
	return s.enqueueSync(r.N("id"))
}
func walkMusic(root string, visit func(string) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.IsDir() {
			switch strings.ToLower(filepath.Ext(path)) {
			case ".mp3", ".m4a":
				return visit(path)
			}
		}
		return nil
	})
}
func (s *Store) Index(out io.Writer) error {
	tracks, e := s.Tracks("")
	if e != nil {
		return e
	}
	for _, m := range tracks {
		if _, err := os.Stat(m.S("full_path")); os.IsNotExist(err) {
			if _, e = s.DB.Exec("UPDATE reinhardt_musicfile SET missing=1 WHERE id=?", m.N("id")); e != nil {
				return e
			}
		} else if err != nil {
			return err
		}
	}
	dirs, e := s.Query("SELECT * FROM reinhardt_musicdirectory ORDER BY id")
	if e != nil {
		return e
	}
	for _, d := range dirs {
		if e = walkMusic(d.S("local_path"), func(path string) error {
			if e := s.indexFile(path, d); e != nil {
				fmt.Fprintf(out, "Skipped %s: %v\n", path, e)
			} else {
				fmt.Fprintln(out, "Indexed", path)
			}
			return nil
		}); e != nil {
			return e
		}
	}
	return s.ReindexFTS()
}
func embedArtwork(ctx context.Context, m Row, picture string) error {
	ext := strings.ToLower(fileExtension(m))
	if ext != ".flac" && ext != ".m4a" && ext != ".mp4" && ext != ".ogg" && ext != ".oga" && ext != ".opus" {
		return nil
	}
	// ffmpeg is only needed for artwork writing in formats without a Go writer.
	return atomicModify(m.S("full_path"), func(temp string) error {
		out := temp + filepath.Ext(temp)
		defer os.Remove(out)
		args := []string{"-v", "error", "-y", "-i", temp, "-i", picture, "-map", "0:a", "-map", "1:v", "-map_metadata", "0", "-c:a", "copy", "-c:v", "mjpeg", "-disposition:v", "attached_pic", out}
		if ext == ".ogg" || ext == ".oga" || ext == ".opus" {
			f, e := os.Open(picture)
			if e != nil {
				return e
			}
			im, kind, e := decodedImage(f)
			f.Close()
			if e != nil {
				return e
			}
			data, e := os.ReadFile(picture)
			if e != nil {
				return e
			}
			mimeType := "image/" + kind
			if kind == "jpeg" {
				mimeType = "image/jpeg"
			}
			var block bytes.Buffer
			put := func(n int) { binary.Write(&block, binary.BigEndian, uint32(n)) }
			put(3)
			put(len(mimeType))
			block.WriteString(mimeType)
			put(5)
			block.WriteString("Cover")
			put(im.Bounds().Dx())
			put(im.Bounds().Dy())
			put(24)
			put(0)
			put(len(data))
			block.Write(data)
			args = []string{"-v", "error", "-y", "-i", temp, "-map", "0:a", "-map_metadata", "0", "-c:a", "copy", "-metadata", "METADATA_BLOCK_PICTURE=" + base64.StdEncoding.EncodeToString(block.Bytes()), out}
		}
		cmd := exec.CommandContext(ctx, "ffmpeg", args...)
		if output, e := cmd.CombinedOutput(); e != nil {
			return fmt.Errorf("ffmpeg: %w: %s", e, output)
		}
		return os.Rename(out, temp)
	})
}
