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

func fileExtension(track *Track) string { return filepath.Ext(track.RelativePath) }
func contentType(path string) string {
	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return contentType
}
func serveLocal(responseWriter http.ResponseWriter, request *http.Request, track *Track, attachment bool) error {
	file, err := os.Open(track.FullPath)
	if os.IsNotExist(err) {
		return sql.ErrNoRows
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	responseWriter.Header().Set("Content-Type", contentType(track.RelativePath))
	disposition := "inline"
	if attachment {
		disposition = "attachment"
	}
	responseWriter.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": track.CleanFileName}))
	// Keep the seekable file: ServeContent handles HEAD, conditional requests,
	// and byte ranges that the browser uses to seek within an audio track.
	http.ServeContent(responseWriter, request, track.CleanFileName, info.ModTime(), file)
	return nil
}
func (app *App) audio(responseWriter http.ResponseWriter, request *http.Request, parts []string) error {
	track, err := app.Store.Track(parts[1])
	if err != nil {
		return err
	}
	if parts[0] == "download" {
		if len(parts) < 3 {
			return sql.ErrNoRows
		}
		if parts[2] != track.CleanFileName {
			app.redirect(responseWriter, request, track.DownloadURL)
			return nil
		}
		return serveLocal(responseWriter, request, track, false)
	}
	if app.Store.Config["DELEGATE_DOWNLOAD_PREFIX"] == "" {
		return serveLocal(responseWriter, request, track, false)
	}
	req, err := http.NewRequestWithContext(request.Context(), request.Method, track.DownloadURL, nil)
	if err != nil {
		return err
	}
	// Range offsets and Content-Length must describe the original audio bytes;
	// transparent upstream compression would break those values during relaying.
	req.Header.Set("Accept-Encoding", "identity")
	for _, headerName := range []string{"Range", "If-Range"} {
		if headerValue := request.Header.Get(headerName); headerValue != "" {
			req.Header.Set(headerName, headerValue)
		}
	}
	// API timeouts include the response body. Delegated audio may stay open for
	// the duration of a song, so use a client bounded only during connection/headers.
	resp, err := app.AudioClient.Do(req)
	if err != nil {
		return httpError{502, "Audio server unavailable."}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 206 && resp.StatusCode != 416 {
		if resp.StatusCode == 404 {
			return sql.ErrNoRows
		}
		return httpError{502, "Audio stream unavailable."}
	}
	for _, headerName := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if headerValue := resp.Header.Get(headerName); headerValue != "" {
			responseWriter.Header().Set(headerName, headerValue)
		}
	}
	if responseWriter.Header().Get("Content-Type") == "" {
		responseWriter.Header().Set("Content-Type", contentType(track.RelativePath))
	}
	responseWriter.WriteHeader(resp.StatusCode)
	if request.Method != "HEAD" {
		// Browser seeks commonly cancel a previous stream. That cancellation also
		// stops the upstream request and should not be logged as a relay failure.
		if bytesCopied, err := io.Copy(responseWriter, resp.Body); err != nil && request.Context().Err() == nil {
			log.Printf("audio relay track %d: received/wrote %d bytes (upstream length %s): %v", track.ID, bytesCopied, resp.Header.Get("Content-Length"), err)
		}
	}
	return nil
}

func atomicFile(dest string, reader io.Reader, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	// Stage beside the destination so link/rename publication stays on the same
	// filesystem. Readers see a complete file, not an in-progress copy.
	temporaryFile, err := os.CreateTemp(filepath.Dir(dest), ".reinhardt-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporaryFile.Name())
	if _, err = io.Copy(temporaryFile, reader); err != nil {
		temporaryFile.Close()
		return err
	}
	if err = temporaryFile.Chmod(0644); err != nil {
		temporaryFile.Close()
		return err
	}
	if err = temporaryFile.Sync(); err != nil {
		temporaryFile.Close()
		return err
	}
	if err = temporaryFile.Close(); err != nil {
		return err
	}
	if !overwrite { // Link fails atomically if a library file already exists.
		return os.Link(temporaryFile.Name(), dest)
	}
	return os.Rename(temporaryFile.Name(), dest)
}
func atomicModify(path string, modify func(string) error) error {
	// Tag writers can rewrite the whole file. Work on a copy so a failed edit
	// leaves the original playable; existing Unix readers retain the old inode
	// after publication. This does not serialize simultaneous edits to one file.
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	dst, err := os.CreateTemp(filepath.Dir(path), ".reinhardt-*"+filepath.Ext(path))
	if err != nil {
		return err
	}
	temp := dst.Name()
	defer os.Remove(temp)
	if _, err = io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	if err = dst.Close(); err != nil {
		return err
	}
	if err = modify(temp); err != nil {
		return err
	}
	if err = os.Chmod(temp, info.Mode()); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
func writeID3(track *Track, picture []byte, pictureType string) error {
	if strings.ToLower(fileExtension(track)) != ".mp3" {
		return nil
	}
	return atomicModify(track.FullPath, func(path string) error {
		id3Tag, err := id3.Open(path, id3.Options{Parse: true})
		if err != nil {
			return err
		}
		defer id3Tag.Close()
		id3Tag.SetVersion(3)
		id3Tag.SetDefaultEncoding(id3.EncodingUTF8)
		for _, frameID := range []string{"TPE1", "TIT2", "TALB", "TCON", "TDRC", "TYER", "COMM"} {
			id3Tag.DeleteFrames(frameID)
		}
		id3Tag.SetArtist(track.Artist)
		id3Tag.SetTitle(track.Title)
		id3Tag.SetAlbum(track.Album)
		id3Tag.SetGenre(track.Genre)
		if track.Year != "" {
			id3Tag.AddTextFrame("TYER", id3.EncodingUTF8, track.Year)
		}
		if track.Comment != "" {
			id3Tag.AddCommentFrame(id3.CommentFrame{Encoding: id3.EncodingUTF8, Language: "eng", Text: track.Comment})
		}
		if picture != nil {
			id3Tag.DeleteFrames("APIC")
			id3Tag.AddAttachedPicture(id3.PictureFrame{Encoding: id3.EncodingUTF8, MimeType: pictureType, PictureType: 3, Description: "Cover", Picture: picture})
		}
		return id3Tag.Save()
	})
}

func decodedImage(reader io.ReadSeeker) (image.Image, string, error) {
	// An HTTP/upload byte limit does not bound decoded pixel memory. Inspect the
	// dimensions before allocating the image, then rewind for the actual decoder.
	conf, kind, err := image.DecodeConfig(reader)
	if err != nil {
		return nil, "", err
	}
	if conf.Width <= 0 || conf.Height <= 0 || int64(conf.Width)*int64(conf.Height) > 40000000 {
		return nil, "", fmt.Errorf("image dimensions too large")
	}
	if _, err = reader.Seek(0, io.SeekStart); err != nil {
		return nil, "", err
	}
	im, _, err := image.Decode(reader)
	return im, kind, err
}
func normalizedImage(reader io.ReadSeeker) ([]byte, string, error) {
	im, kind, err := decodedImage(reader)
	if err != nil {
		return nil, "", err
	}
	var buffer bytes.Buffer
	if kind == "jpeg" {
		err = jpeg.Encode(&buffer, im, &jpeg.Options{Quality: 95})
		return buffer.Bytes(), ".jpg", err
	}
	err = png.Encode(&buffer, im)
	return buffer.Bytes(), ".png", err
}
func (app *App) trackDetails(responseWriter http.ResponseWriter, request *http.Request, user *User, id string) error {
	track, operationErr := app.Store.Track(id)
	if operationErr != nil {
		return operationErr
	}
	message := ""
	status := 200
	if request.Method == "POST" {
		if !user.IsSuperuser {
			return forbidden()
		}
		track.Artist = strings.TrimSpace(request.FormValue("artist"))
		track.Title = strings.TrimSpace(request.FormValue("title"))
		track.Album = strings.TrimSpace(request.FormValue("album"))
		track.Genre = strings.TrimSpace(request.FormValue("genre"))
		track.Year = strings.TrimSpace(request.FormValue("year"))
		track.Comment = strings.TrimSpace(request.FormValue("comment"))
		if track.Artist == "" || track.Title == "" {
			message = "not enough data"
			status = 400
		} else {
			var data []byte
			var ext string
			upload, _, err := request.FormFile("track_image")
			if err != nil && err != http.ErrMissingFile {
				return bad("Invalid upload")
			}
			if err == nil {
				defer upload.Close()
				data, ext, operationErr = normalizedImage(upload)
				if operationErr != nil {
					message = "invalid image"
					status = 400
				} else if _, operationErr = os.Stat(track.FullPath); operationErr != nil {
					message = "audio file not found"
					status = 400
				}
			}
			if status == 200 {
				if operationErr = writeID3(track, data, contentType("cover"+ext)); operationErr != nil {
					log.Printf("metadata writing: %v", operationErr)
				}
				if data != nil {
					if operationErr = atomicFile(track.FullPath+ext, bytes.NewReader(data), true); operationErr != nil {
						return operationErr
					}
					app.invalidateCovers(track.ID)
					if fileExtension(track) != ".mp3" {
						if err := embedArtwork(request.Context(), track, track.FullPath+ext); err != nil {
							log.Printf("embed artwork: %v", err)
						}
					}
				}
				_, operationErr = app.Store.DB.Exec(`UPDATE reinhardt_musicfile SET artist=?,title=?,album=?,genre=?,year=?,comment=?,modified_at=? WHERE id=?`, track.Artist, track.Title, track.Album, track.Genre, track.Year, track.Comment, now(), track.ID)
				if operationErr != nil {
					return operationErr
				}
				if operationErr = app.Store.enqueueSync(track.ID); operationErr != nil {
					return operationErr
				}
				app.redirect(responseWriter, request, app.Store.URL("index"))
				return nil
			}
		}
	}
	return app.render(responseWriter, request, user, "reinhardt/track_details_modal.html", TemplateContext{"music_file": track, "message": message}, status)
}

func (app *App) invalidateCovers(id int64) {
	files, _ := filepath.Glob(filepath.Join(app.Store.Config["COVERS_DIR"], fmt.Sprintf("*-%d-*", id)))
	for _, cachePath := range files {
		os.Remove(cachePath)
	}
}
func nearbyPicture(track *Track) string {
	path := track.FullPath
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".gif"} {
		if info, err := os.Stat(path + ext); err == nil && info.Mode().IsRegular() {
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
	bounds := im.Bounds()
	side := bounds.Dx()
	if bounds.Dy() < side {
		side = bounds.Dy()
	}
	cropX, cropY := bounds.Min.X+(bounds.Dx()-side)/2, bounds.Min.Y+(bounds.Dy()-side)/2
	out := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(out, out.Bounds(), im, image.Rect(cropX, cropY, cropX+side, cropY+side), draw.Over, nil)
	return out
}
func fallbackArtwork(track *Track, size int) image.Image {
	colorSeed := sha256.Sum256([]byte(track.Artist + "/" + track.Album + "/" + track.Title))
	im := image.NewRGBA(image.Rect(0, 0, 512, 512))
	paper := color.RGBA{242, 232, 213, 255}
	ink := color.RGBA{36 + colorSeed[0]%35, 45 + colorSeed[1]%35, 55 + colorSeed[2]%35, 255}
	accent := color.RGBA{130 + colorSeed[3]%90, 90 + colorSeed[4]%80, 65 + colorSeed[5]%80, 255}
	draw.Draw(im, im.Bounds(), image.NewUniform(paper), image.Point{}, draw.Src)
	draw.Draw(im, image.Rect(0, 0, 12, 512), image.NewUniform(accent), image.Point{}, draw.Src)
	for pixelY := 0; pixelY < 400; pixelY++ {
		for pixelX := 80; pixelX < 512; pixelX++ {
			dx, dy := pixelX-345, pixelY-165
			squaredDistance := dx*dx + dy*dy
			if squaredDistance < 225*225 {
				pixelColor := ink
				if squaredDistance < 70*70 {
					pixelColor = accent
				} else {
					rr := intSqrt(squaredDistance)
					if rr%9 < 2 {
						pixelColor = color.RGBA{ink.R + 20, ink.G + 20, ink.B + 20, 255}
					}
				}
				if squaredDistance < 10*10 {
					pixelColor = paper
				}
				im.Set(pixelX, pixelY, pixelColor)
			}
		}
	}
	drawFallbackArtworkText(im, ink, track.Artist, track.Title)
	return cropSquare(im, size)
}
func intSqrt(value int) int {
	return int(math.Sqrt(float64(value)))
}
func (app *App) artwork(responseWriter http.ResponseWriter, request *http.Request, parts []string) error {
	if len(parts) != 3 {
		return sql.ErrNoRows
	}
	size, err := strconv.Atoi(strings.TrimSuffix(parts[2], ".jpg"))
	if err != nil {
		return bad("Invalid artwork size")
	}
	switch size {
	case 96, 128, 192, 256, 384, 512:
	default:
		return bad("Invalid artwork size")
	}
	track, err := app.Store.Track(parts[1])
	if err != nil {
		return err
	}
	cache := filepath.Join(app.Store.Config["COVERS_DIR"], fmt.Sprintf("thumb-%d-%d-square.jpg", track.ID, size))
	if cachedFile, err := os.Open(cache); err == nil {
		defer cachedFile.Close()
		info, err := cachedFile.Stat()
		if err != nil {
			return err
		}
		responseWriter.Header().Set("Content-Type", "image/jpeg")
		http.ServeContent(responseWriter, request, "artwork.jpg", info.ModTime(), cachedFile)
		return nil
	}
	var im image.Image
	// A track-specific sidecar records an explicit edit and must beat stale embedded artwork.
	if picture := nearbyPicture(track); strings.HasPrefix(picture, track.FullPath) && picture != "" {
		if pictureFile, err := os.Open(picture); err == nil {
			im, _, _ = decodedImage(pictureFile)
			pictureFile.Close()
		}
	}
	if im == nil {
		if audioFile, err := os.Open(track.FullPath); err == nil {
			if metadata, err := tag.ReadFrom(audioFile); err == nil && metadata.Picture() != nil {
				im, _, _ = decodedImage(bytes.NewReader(metadata.Picture().Data))
			}
			audioFile.Close()
		}
	}
	if im == nil {
		if path := nearbyPicture(track); path != "" {
			if discoveredFile, err := os.Open(path); err == nil {
				im, _, _ = decodedImage(discoveredFile)
				discoveredFile.Close()
			}
		}
	}
	if im == nil {
		im = app.discoverCover(request.Context(), track)
	}
	found := im != nil
	if found {
		im = cropSquare(im, size)
	} else {
		im = fallbackArtwork(track, size)
	}
	var buffer bytes.Buffer
	if err = jpeg.Encode(&buffer, im, &jpeg.Options{Quality: 85}); err != nil {
		return err
	}
	// Persist actual covers only. Saving a generated placeholder here would hide
	// artwork added or discovered later until someone explicitly removed the file.
	if found {
		if err = atomicFile(cache, bytes.NewReader(buffer.Bytes()), true); err != nil {
			return err
		}
	}
	responseWriter.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(responseWriter, request, "artwork.jpg", time.Time{}, bytes.NewReader(buffer.Bytes()))
	return nil
}

func (app *App) archive(responseWriter http.ResponseWriter, request *http.Request, tag *Tag) error {
	tracks, err := app.Store.TagTracks(tag.ID)
	if err != nil {
		return err
	}
	archiveFile, err := os.CreateTemp("", "reinhardt-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(archiveFile.Name())
	defer archiveFile.Close()
	archiveWriter := zip.NewWriter(archiveFile)
	for index, track := range tracks {
		src, err := os.Open(track.FullPath)
		if err != nil {
			archiveWriter.Close()
			return err
		}
		info, err := src.Stat()
		if err != nil {
			src.Close()
			archiveWriter.Close()
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			src.Close()
			archiveWriter.Close()
			return err
		}
		header.Name = fmt.Sprintf("%03d-%s", index+1, track.CleanFileName)
		// Audio is already compressed; deflating it adds CPU cost for little gain.
		header.Method = zip.Store
		dst, err := archiveWriter.CreateHeader(header)
		if err == nil {
			_, err = io.Copy(dst, src)
		}
		src.Close()
		if err != nil {
			archiveWriter.Close()
			return err
		}
	}
	if err = archiveWriter.Close(); err != nil {
		return err
	}
	if _, err = archiveFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	responseWriter.Header().Set("Content-Type", "application/zip")
	responseWriter.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": slug(tag.Label) + ".zip"}))
	http.ServeContent(responseWriter, request, "playlist.zip", time.Time{}, archiveFile)
	return nil
}
func (app *App) databaseBackup(responseWriter http.ResponseWriter, request *http.Request) error {
	temporaryFile, err := os.CreateTemp("", "reinhardt-*.sqlite3")
	if err != nil {
		return err
	}
	name := temporaryFile.Name()
	temporaryFile.Close()
	defer os.Remove(name)
	if err = os.Remove(name); err != nil {
		return err
	}
	// VACUUM INTO takes a consistent snapshot, including data in the WAL.
	// Copying DATABASE_PATH alone can miss committed rows still in its WAL file.
	if _, err = app.Store.DB.Exec("VACUUM INTO ?", name); err != nil {
		return err
	}
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer file.Close()
	responseWriter.Header().Set("Content-Type", "application/vnd.sqlite3")
	responseWriter.Header().Set("Content-Disposition", `attachment; filename="reinhardt.sqlite3"`)
	http.ServeContent(responseWriter, request, "reinhardt.sqlite3", time.Time{}, file)
	return nil
}

var genrePrefix = regexp.MustCompile(`^\([0-9]+\)(.+)`)

func (config Config) sanitize(text, field string) string {
	if !config.Bool("SANITIZE_TAGS") {
		return text
	}
	text = strings.ReplaceAll(text, "/", " / ")
	text = strings.ReplaceAll(text, "&", " & ")
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 1 && text[0] == '"' && text[len(text)-1] == '"' {
		text = text[1 : len(text)-1]
	}
	if field == "artist" || field == "genre" {
		text = strings.ReplaceAll(strings.ToLower(text), " & ", " and ")
	}
	if field == "artist" {
		for _, pair := range [][2]string{{" feat. ", " featuring "}, {" feat ", " featuring "}, {" vs. ", " versus "}, {" vs ", " versus "}} {
			text = strings.ReplaceAll(text, pair[0], pair[1])
		}
		words := strings.Fields(text)
		for wordIndex, word := range words {
			rs := []rune(word)
			if len(rs) > 0 {
				words[wordIndex] = strings.ToUpper(string(rs[:1])) + string(rs[1:])
			}
		}
		text = strings.Join(words, " ")
		if strings.HasPrefix(text, "The ") {
			text = strings.TrimPrefix(text, "The ") + " (the)"
		}
		if strings.HasPrefix(text, "Dj ") {
			text = strings.TrimPrefix(text, "Dj ") + " (DJ)"
		}
		switch strings.ToLower(text) {
		case "various", "various artist", "various artists":
			text = "various"
		}
	}
	if field == "genre" {
		switch text {
		case "bo", "bof", "b.o.f.", "soundtracks":
			text = "soundtrack"
		case "genre inconnu", "inconnu", "unknown", "(255)":
			text = ""
		}
		if matches := genrePrefix.FindStringSubmatch(text); matches != nil {
			text = matches[1]
		}
		text = strings.TrimSpace(strings.Split(text, "/")[0])
	}
	return text
}
func (store *Store) Metadata(path string) (*Track, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	metadata, err := tag.ReadFrom(file)
	if err != nil {
		return nil, err
	}
	track, _ := metadata.Track()
	genre := metadata.Genre()
	// Numeric ID3 genre prefixes are expanded by the reader, duplicating text
	// in values like "(18)Techno". Sanitize the original frame instead.
	for _, key := range []string{"TCON", "TCO"} {
		if raw, ok := metadata.Raw()[key].(string); ok {
			genre = raw
			break
		}
	}
	year := ""
	if metadataYear := metadata.Year(); metadataYear != 0 {
		year = strconv.Itoa(metadataYear)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	duration := 0
	if strings.ToLower(filepath.Ext(path)) == ".mp3" {
		file.Seek(0, io.SeekStart)
		dec := mp3.NewDecoder(file)
		var frame mp3.Frame
		var skipped int
		var total time.Duration
		for dec.Decode(&frame, &skipped) == nil {
			total += frame.Duration()
		}
		duration = int(total.Seconds())
	} else {
		duration = mp4Duration(file)
	}
	return &Track{
		Artist: store.Config.sanitize(metadata.Artist(), "artist"),
		Title:  store.Config.sanitize(metadata.Title(), ""),
		Album:  store.Config.sanitize(metadata.Album(), ""),
		Genre:  store.Config.sanitize(genre, "genre"),
		Year:   year, Comment: strings.TrimSpace(metadata.Comment()),
		Duration: int64(duration), TrackNumber: int64(track),
		CreatedAt:  info.ModTime().UTC().Format("2006-01-02 15:04:05.000000"),
		ModifiedAt: info.ModTime().UTC().Format("2006-01-02 15:04:05.000000"),
	}, nil
}
func mp4Duration(file *os.File) int {
	info, err := file.Stat()
	if err != nil {
		return 0
	}
	var walk func(int64, int64, int) int
	walk = func(start, end int64, depth int) int {
		if depth > 8 {
			return 0
		}
		for off := start; off+8 <= end; {
			var boxHeader [16]byte
			if _, err := file.ReadAt(boxHeader[:8], off); err != nil {
				return 0
			}
			size := int64(binary.BigEndian.Uint32(boxHeader[:4]))
			head := int64(8)
			kind := string(boxHeader[4:8])
			if size == 1 {
				if _, err := file.ReadAt(boxHeader[8:], off+8); err != nil {
					return 0
				}
				size = int64(binary.BigEndian.Uint64(boxHeader[8:]))
				head = 16
			}
			if size == 0 {
				size = end - off
			}
			if size < head || size > end-off {
				return 0
			}
			if kind == "mdhd" && size >= head+24 {
				var mediaHeader [32]byte
				bytesRead, _ := file.ReadAt(mediaHeader[:], off+head)
				if mediaHeader[0] == 0 && bytesRead >= 20 {
					scale := binary.BigEndian.Uint32(mediaHeader[12:16])
					if scale > 0 {
						return int(binary.BigEndian.Uint32(mediaHeader[16:20]) / scale)
					}
				}
				if mediaHeader[0] == 1 && bytesRead >= 32 {
					scale := binary.BigEndian.Uint32(mediaHeader[20:24])
					if scale > 0 {
						return int(binary.BigEndian.Uint64(mediaHeader[24:32]) / uint64(scale))
					}
				}
			}
			if kind == "moov" || kind == "trak" || kind == "mdia" {
				if duration := walk(off+head, off+size, depth+1); duration > 0 {
					return duration
				}
			}
			off += size
		}
		return 0
	}
	return walk(0, info.Size(), 0)
}
func (store *Store) indexFile(path string, dir *MusicDirectory) error {
	trackMetadata, err := store.Metadata(path)
	if err != nil {
		return err
	}
	return store.indexFileMetadata(path, dir, trackMetadata)
}

// indexFileMetadata stores already parsed metadata and queues synchronization
// without reopening the audio. Callers supply timestamps for the indexed file.
func (store *Store) indexFileMetadata(path string, dir *MusicDirectory, trackMetadata *Track) error {
	if trackMetadata.Artist == "" || trackMetadata.Title == "" {
		return fmt.Errorf("artist and title are required")
	}
	relative, err := filepath.Rel(dir.LocalPath, path)
	if err != nil {
		return err
	}
	// The legacy schema makes relative_path globally unique, even across music
	// directories. Update in place so rescans preserve IDs and playlist membership;
	// INSERT OR REPLACE would delete the old row and cascade its associations.
	var trackID int64
	err = store.DB.QueryRow(`INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,year,comment,duration,track_number,created_at,modified_at,missing,hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,0,NULL) ON CONFLICT(relative_path) DO UPDATE SET artist=excluded.artist,title=excluded.title,album=excluded.album,genre=excluded.genre,year=excluded.year,comment=excluded.comment,duration=excluded.duration,track_number=excluded.track_number,modified_at=excluded.modified_at,missing=0 RETURNING id`, dir.ID, relative, trackMetadata.Artist, trackMetadata.Title, trackMetadata.Album, trackMetadata.Genre, trackMetadata.Year, trackMetadata.Comment, trackMetadata.Duration, trackMetadata.TrackNumber, trackMetadata.CreatedAt, trackMetadata.ModifiedAt).Scan(&trackID)
	if err != nil {
		return err
	}
	return store.enqueueSync(trackID)
}
func walkMusic(root string, visit func(string) error) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !entry.IsDir() {
			switch strings.ToLower(filepath.Ext(path)) {
			case ".mp3", ".m4a":
				return visit(path)
			}
		}
		return nil
	})
}
func (store *Store) Index(out io.Writer) error {
	tracks, indexErr := store.Tracks("")
	if indexErr != nil {
		return indexErr
	}
	for _, track := range tracks {
		if _, err := os.Stat(track.FullPath); os.IsNotExist(err) {
			if _, indexErr = store.DB.Exec("UPDATE reinhardt_musicfile SET missing=1 WHERE id=?", track.ID); indexErr != nil {
				return indexErr
			}
		} else if err != nil {
			return err
		}
	}
	directoryRows, indexErr := store.Query("SELECT * FROM reinhardt_musicdirectory ORDER BY id")
	if indexErr != nil {
		return indexErr
	}
	for _, row := range directoryRows {
		musicDirectory := musicDirectoryFromRow(row)
		if indexErr = walkMusic(musicDirectory.LocalPath, func(path string) error {
			if err := store.indexFile(path, musicDirectory); err != nil {
				fmt.Fprintf(out, "Skipped %s: %v\n", path, err)
			} else {
				fmt.Fprintln(out, "Indexed", path)
			}
			return nil
		}); indexErr != nil {
			return indexErr
		}
	}
	return store.ReindexFTS()
}
func embedArtwork(ctx context.Context, track *Track, picture string) error {
	ext := strings.ToLower(fileExtension(track))
	if ext != ".flac" && ext != ".m4a" && ext != ".mp4" && ext != ".ogg" && ext != ".oga" && ext != ".opus" {
		return nil
	}
	// ffmpeg is only needed for artwork writing in formats without a Go writer.
	return atomicModify(track.FullPath, func(temp string) error {
		out := temp + filepath.Ext(temp)
		defer os.Remove(out)
		args := []string{"-v", "error", "-y", "-i", temp, "-i", picture, "-map", "0:a", "-map", "1:v", "-map_metadata", "0", "-c:a", "copy", "-c:v", "mjpeg", "-disposition:v", "attached_pic", out}
		if ext == ".ogg" || ext == ".oga" || ext == ".opus" {
			file, err := os.Open(picture)
			if err != nil {
				return err
			}
			im, kind, err := decodedImage(file)
			file.Close()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(picture)
			if err != nil {
				return err
			}
			mimeType := "image/" + kind
			if kind == "jpeg" {
				mimeType = "image/jpeg"
			}
			var block bytes.Buffer
			put := func(value int) { binary.Write(&block, binary.BigEndian, uint32(value)) }
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
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ffmpeg: %w: %s", err, output)
		}
		return os.Rename(out, temp)
	})
}
