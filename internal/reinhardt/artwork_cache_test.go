package reinhardt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func artworkColorPNG(test *testing.T, ink color.Color) []byte {
	test.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for pixelY := range 20 {
		for pixelX := range 40 {
			im.Set(pixelX, pixelY, ink)
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, im); err != nil {
		test.Fatal(err)
	}
	return data.Bytes()
}

func artworkCacheFiles(test *testing.T, app *App) []string {
	test.Helper()
	files, err := filepath.Glob(filepath.Join(app.Store.Config.CoversDir, "thumb-*"))
	if err != nil {
		test.Fatal(err)
	}
	return files
}

func TestArtworkThumbnailsShareContentAcrossTracksAndRestarts(test *testing.T) {
	app := testApp(test)
	first, _ := app.Store.Track(test.Context(), 1)
	second, _ := app.Store.Track(test.Context(), 2)
	third, _ := app.Store.Track(test.Context(), 3)
	red := artworkColorPNG(test, color.RGBA{255, 0, 0, 255})
	blue := artworkColorPNG(test, color.RGBA{0, 0, 255, 255})
	if err := os.WriteFile(first.FullPath+".png", red, 0600); err != nil {
		test.Fatal(err)
	}
	// The same cover from embedded metadata must share the sidecar's thumbnails.
	if err := writeID3(second, red, "image/png"); err != nil {
		test.Fatal(err)
	}
	if err := os.WriteFile(third.FullPath+".png", blue, 0600); err != nil {
		test.Fatal(err)
	}
	// Album tags are not the cache identity: this track has a different cover.
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET artist='Artist',album='Album' WHERE id=3"); err != nil {
		test.Fatal(err)
	}
	responses := make(map[int][]byte)
	for _, size := range []int{96, 128, 192, 256, 384, 512} {
		for _, id := range []int{1, 2} {
			response := callArtwork(app, "GET", fmt.Sprintf("/artwork/%d/%d.jpg", id, size), 0, nil, false)
			requireCode(test, response, 200)
			if id == 1 {
				responses[size] = append([]byte(nil), response.Body.Bytes()...)
			} else if !bytes.Equal(responses[size], response.Body.Bytes()) {
				test.Fatal("identical embedded and sidecar images produced different thumbnails")
			}
		}
	}
	response := callArtwork(app, "GET", "/artwork/3/128.jpg", 0, nil, false)
	requireCode(test, response, 200)
	if bytes.Equal(responses[128], response.Body.Bytes()) || len(artworkCacheFiles(test, app)) != 7 {
		test.Fatal("expected six shared red thumbnails and one distinct blue thumbnail")
	}
	rows, err := app.Store.Query(test.Context(), "SELECT track_id,artwork_hash FROM go_track_artwork ORDER BY track_id")
	if err != nil || len(rows) != 3 || rows[0].S("artwork_hash") != rows[1].S("artwork_hash") || rows[0].S("artwork_hash") == rows[2].S("artwork_hash") {
		test.Fatal("wrong persistent content references", rows, err)
	}
	// Reopen SQLite as well as App: neither an in-memory map nor the audio/image
	// files may be necessary for a warm thumbnail hit after a server restart.
	for _, path := range []string{first.FullPath + ".png", second.FullPath} {
		if err := os.Remove(path); err != nil {
			test.Fatal(err)
		}
	}
	store, err := openStore(test.Context(), app.Store.Config)
	if err != nil {
		test.Fatal(err)
	}
	defer store.DB.Close()
	restarted, err := newApp(store)
	if err != nil {
		test.Fatal(err)
	}
	if _, err := store.DB.Exec("PRAGMA query_only=ON"); err != nil {
		test.Fatal(err)
	}
	for _, id := range []int{1, 2} {
		response := callArtwork(restarted, "GET", fmt.Sprintf("/artwork/%d/512.jpg", id), 0, nil, false)
		requireCode(test, response, 200)
		if !bytes.Equal(responses[512], response.Body.Bytes()) {
			test.Fatal("restart lost the shared thumbnail or reopened an unavailable source")
		}
	}
}

func uploadArtworkForTest(test *testing.T, app *App, id int, data []byte) {
	test.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, field := range [][2]string{{"artist", "Artist"}, {"title", "Edited title"}, {"album", "Album"}, {"csrfmiddlewaretoken", strings.Repeat("a", 64)}} {
		if err := writer.WriteField(field[0], field[1]); err != nil {
			test.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("track_image", "new.png")
	if err != nil {
		test.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		test.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		test.Fatal(err)
	}
	request := httptest.NewRequest("POST", app.Store.Config.Path(fmt.Sprintf("track_details_modal/%d", id)), &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	request.AddCookie(&http.Cookie{Name: "csrftoken", Value: strings.Repeat("a", 64)})
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, http.StatusFound)
}

func TestArtworkUploadOnlyChangesTheEditedTrackReference(test *testing.T) {
	app := testApp(test)
	first, _ := app.Store.Track(test.Context(), 1)
	second, _ := app.Store.Track(test.Context(), 2)
	red, _, err := decodedImage(bytes.NewReader(artworkColorPNG(test, color.RGBA{255, 0, 0, 255})))
	if err != nil {
		test.Fatal(err)
	}
	var oldImage bytes.Buffer
	if err := jpeg.Encode(&oldImage, red, nil); err != nil {
		test.Fatal(err)
	}
	for _, track := range []*Track{first, second} {
		if err := os.WriteFile(track.FullPath+".jpg", oldImage.Bytes(), 0600); err != nil {
			test.Fatal(err)
		}
		requireCode(test, callArtwork(app, "GET", fmt.Sprintf("/artwork/%d/128.jpg", track.ID), 0, nil, false), 200)
	}
	old, err := app.Store.trackArtwork(test.Context(), "1")
	if err != nil {
		test.Fatal(err)
	}
	oldThumbnail := app.artworkThumbnailPath(old.hash, 128)
	before, err := os.ReadFile(oldThumbnail)
	if err != nil {
		test.Fatal(err)
	}
	uploadArtworkForTest(test, app, 1, artworkColorPNG(test, color.RGBA{0, 0, 255, 255}))
	if _, err := os.Stat(first.FullPath + ".jpg"); err != nil {
		test.Fatal("upload removed a pre-existing sidecar", err)
	}
	response := callArtwork(app, "GET", "/artwork/1/128.jpg", 0, nil, false)
	requireCode(test, response, 200)
	if bytes.Equal(before, response.Body.Bytes()) {
		test.Fatal("upload retained the old artwork")
	}
	updated, err := app.Store.trackArtwork(test.Context(), "1")
	if err != nil || updated.hash == old.hash || updated.hash == "" {
		test.Fatal("upload did not establish a new reference", updated, err)
	}
	// A request that began before the upload must not restore the old reference.
	if err := app.Store.rememberArtwork(test.Context(), old, old.hash); err != nil {
		test.Fatal(err)
	}
	current, err := app.Store.trackArtwork(test.Context(), "1")
	if err != nil || current.hash != updated.hash {
		test.Fatal("stale request overwrote uploaded artwork", current, err)
	}
	response = callArtwork(app, "GET", "/artwork/2/128.jpg", 0, nil, false)
	requireCode(test, response, 200)
	if !bytes.Equal(before, response.Body.Bytes()) || len(artworkCacheFiles(test, app)) != 2 {
		test.Fatal("editing one track changed or deleted the other track's shared cover")
	}
	// Aging and pruning must still keep the old cover while track 2 uses it.
	ancient := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldThumbnail, ancient, ancient); err != nil {
		test.Fatal(err)
	}
	if err := app.Store.pruneArtworkCache(test.Context(), time.Now().Add(-24*time.Hour)); err != nil {
		test.Fatal(err)
	}
	if _, err := os.Stat(oldThumbnail); err != nil {
		test.Fatal("cleanup removed artwork still used by another track", err)
	}
}

func TestArtworkReferencesFollowLibraryEditsAndRollbacks(test *testing.T) {
	app := testApp(test)
	track, _ := app.Store.Track(test.Context(), 1)
	if err := os.WriteFile(track.FullPath+".png", artworkTestPNG(test), 0600); err != nil {
		test.Fatal(err)
	}
	requireCode(test, callArtwork(app, "GET", "/artwork/1/96.jpg", 0, nil, false), 200)
	original, err := app.Store.trackArtwork(test.Context(), "1")
	if err != nil {
		test.Fatal(err)
	}
	tx, err := app.Store.DB.BeginTx(test.Context(), nil)
	if err != nil {
		test.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE reinhardt_musicfile SET title='Aborted edit' WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		test.Fatal(err)
	}
	unchanged, _ := app.Store.trackArtwork(test.Context(), "1")
	if unchanged.hash != original.hash || unchanged.revision != original.revision {
		test.Fatal("a rolled-back edit invalidated the reference")
	}
	for _, statement := range []string{
		"UPDATE reinhardt_musicfile SET title='New title' WHERE id=1",
		"UPDATE reinhardt_musicfile SET modified_at='2026-10-25 20:01:02.123456' WHERE id=1",
		"UPDATE reinhardt_musicfile SET relative_path='renamed.mp3' WHERE id=1",
		"UPDATE reinhardt_musicdirectory SET local_path=local_path||'/moved' WHERE id=1",
	} {
		if _, err := app.Store.DB.Exec(statement); err != nil {
			test.Fatal(err)
		}
		if err := app.Store.rememberArtwork(test.Context(), original, original.hash); err != nil {
			test.Fatal(err)
		}
		updated, err := app.Store.trackArtwork(test.Context(), "1")
		if err != nil || updated.hash != "" || updated.revision <= original.revision {
			test.Fatal("library edit did not reject the stale reference", statement, updated, err)
		}
		if err := app.Store.rememberArtwork(test.Context(), updated, original.hash); err != nil {
			test.Fatal(err)
		}
		original, err = app.Store.trackArtwork(test.Context(), "1")
		if err != nil || original.hash == "" {
			test.Fatal("current revision could not store artwork", original, err)
		}
	}
	if _, err := app.Store.DB.Exec("DELETE FROM reinhardt_musicfile WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	if err := app.Store.rememberArtwork(test.Context(), original, original.hash); err != nil {
		test.Fatal(err)
	}
	var count int
	if err := app.Store.DB.QueryRow("SELECT COUNT(*) FROM go_track_artwork WHERE track_id=1").Scan(&count); err != nil || count != 0 {
		test.Fatal("deletion retained or recreated a reference", count, err)
	}
}

func TestArtworkRenderSharesGenerationAndSurvivesCancellation(test *testing.T) {
	app := testApp(test)
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var generations atomic.Int64
	generate := func() image.Image {
		if generations.Add(1) == 1 {
			close(started)
		}
		<-release
		return image.NewRGBA(image.Rect(0, 0, 96, 96))
	}
	path := app.artworkThumbnailPath(strings.Repeat("a", 64), 96)
	leaderCtx, cancel := context.WithCancel(test.Context())
	defer cancel()
	leader := make(chan error, 1)
	go func() { _, err := app.renderArtwork(leaderCtx, path, path, generate); leader <- err }()
	artworkTestReceive(test, started)
	const callers = 16
	results := make(chan error, callers)
	for range callers {
		go func() { _, err := app.renderArtwork(test.Context(), path, path, generate); results <- err }()
	}
	cancel()
	if err := artworkTestReceive(test, leader); !errors.Is(err, context.Canceled) {
		test.Fatal("cancelled caller did not stop waiting", err)
	}
	// An unrelated image can finish while this thumbnail's generation is blocked.
	other := make(chan error, 1)
	go func() {
		_, err := app.renderArtwork(test.Context(), "other", "", func() image.Image { return image.NewRGBA(image.Rect(0, 0, 128, 128)) })
		other <- err
	}()
	if err := artworkTestReceive(test, other); err != nil {
		test.Fatal(err)
	}
	unblock()
	for range callers {
		if err := artworkTestReceive(test, results); err != nil {
			test.Fatal(err)
		}
	}
	if generations.Load() != 1 {
		test.Fatal("same-content thumbnail encoded multiple times", generations.Load())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		test.Fatal(err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
		test.Fatal("shared thumbnail was not published completely", err)
	}
	app.artworkMu.Lock()
	defer app.artworkMu.Unlock()
	if len(app.artworkFlights) != 0 {
		test.Fatal("completed renders retained their in-flight entries")
	}
}

func TestArtworkCacheUpgradePreservesLibraryAndReplacesLegacyThumbnails(test *testing.T) {
	app := testApp(test)
	for _, id := range []int{1, 2} {
		track, err := app.Store.Track(test.Context(), id)
		if err != nil {
			test.Fatal(err)
		}
		if err := os.WriteFile(track.FullPath+".png", artworkTestPNG(test), 0600); err != nil {
			test.Fatal(err)
		}
		legacy := filepath.Join(app.Store.Config.CoversDir, fmt.Sprintf("thumb-%d-96-square.jpg", id))
		if err := atomicFile(legacy, bytes.NewReader([]byte("old cache")), true); err != nil {
			test.Fatal(err)
		}
	}
	// Simulate a pre-upgrade database after preparing its per-track cache files.
	if _, err := app.Store.DB.Exec(`DROP TRIGGER go_track_artwork_update;
DROP TRIGGER go_track_artwork_directory_update;
DROP TABLE go_track_artwork;`); err != nil {
		test.Fatal(err)
	}
	library, err := app.Store.Query(test.Context(), "SELECT * FROM reinhardt_musicfile ORDER BY id")
	if err != nil {
		test.Fatal(err)
	}
	// Startup is the migration entry point. Run it twice to check repeatability.
	for range 2 {
		store, err := openStore(test.Context(), app.Store.Config)
		if err != nil {
			test.Fatal(err)
		}
		func() {
			defer store.DB.Close()
			upgraded, err := newApp(store)
			if err != nil {
				test.Fatal(err)
			}
			upgraded.Client = app.Client
			for _, id := range []int{1, 2} {
				response := callArtwork(upgraded, "GET", fmt.Sprintf("/artwork/%d/96.jpg", id), 0, nil, false)
				requireCode(test, response, 200)
				if _, err := jpeg.Decode(bytes.NewReader(response.Body.Bytes())); err != nil {
					test.Fatal("legacy file was served instead of regenerating from original artwork", err)
				}
			}
		}()
	}
	if files := artworkCacheFiles(test, app); len(files) != 1 || !sharedArtworkFilename.MatchString(filepath.Base(files[0])) {
		test.Fatal("upgrade did not replace duplicate per-track thumbnails", files)
	}
	after, err := app.Store.Query(test.Context(), "SELECT * FROM reinhardt_musicfile ORDER BY id")
	if err != nil || !reflect.DeepEqual(library, after) {
		test.Fatal("cache migration changed library metadata", err)
	}
}

func TestArtworkLegacyCacheSurvivesMissingOriginals(test *testing.T) {
	app := testApp(test)
	im, _, err := decodedImage(bytes.NewReader(artworkTestPNG(test)))
	if err != nil {
		test.Fatal(err)
	}
	var thumbnail bytes.Buffer
	if err := jpeg.Encode(&thumbnail, cropSquare(im, 96), nil); err != nil {
		test.Fatal(err)
	}
	legacy := filepath.Join(app.Store.Config.CoversDir, "thumb-1-96-square.jpg")
	if err := atomicFile(legacy, bytes.NewReader(thumbnail.Bytes()), true); err != nil {
		test.Fatal(err)
	}
	ancient := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(legacy, ancient, ancient); err != nil {
		test.Fatal(err)
	}
	response := callArtwork(app, "GET", "/artwork/1/96.jpg", 0, nil, false)
	requireCode(test, response, 200)
	if !bytes.Equal(response.Body.Bytes(), thumbnail.Bytes()) || response.Header().Get("Cache-Control") != "public, max-age=31536000" {
		test.Fatal("upgrade discarded cached artwork with no available original")
	}
	if err := app.Store.pruneArtworkCache(test.Context(), time.Now().Add(-24*time.Hour)); err != nil {
		test.Fatal(err)
	}
	if _, err := os.Stat(legacy); err != nil {
		test.Fatal("cleanup removed a legacy track's only cached artwork", err)
	}
}

func TestArtworkWorkerPrunesOnlyOldUnreferencedThumbnails(test *testing.T) {
	app := testApp(test)
	for _, id := range []int{1, 2, 3} {
		track, err := app.Store.Track(test.Context(), id)
		if err != nil {
			test.Fatal(err)
		}
		ink := color.RGBA{255, 0, 0, 255}
		if id == 3 {
			ink = color.RGBA{0, 0, 255, 255}
		}
		if err := os.WriteFile(track.FullPath+".png", artworkColorPNG(test, ink), 0600); err != nil {
			test.Fatal(err)
		}
		requireCode(test, callArtwork(app, "GET", fmt.Sprintf("/artwork/%d/96.jpg", id), 0, nil, false), 200)
	}
	red, _ := app.Store.trackArtwork(test.Context(), "1")
	blue, _ := app.Store.trackArtwork(test.Context(), "3")
	oldRed := app.artworkThumbnailPath(red.hash, 96)
	oldBlue := app.artworkThumbnailPath(blue.hash, 96)
	ancient := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{oldRed, oldBlue} {
		if err := os.Chtimes(path, ancient, ancient); err != nil {
			test.Fatal(err)
		}
	}
	recent := app.artworkThumbnailPath(strings.Repeat("b", 64), 128)
	unrelated := filepath.Join(app.Store.Config.CoversDir, "keep.jpg")
	legacy := filepath.Join(app.Store.Config.CoversDir, "thumb-999-96-square.jpg")
	for _, path := range []string{recent, unrelated, legacy} {
		if err := os.WriteFile(path, []byte("test file"), 0600); err != nil {
			test.Fatal(err)
		}
	}
	for _, path := range []string{unrelated, legacy} {
		if err := os.Chtimes(path, ancient, ancient); err != nil {
			test.Fatal(err)
		}
	}
	// A deleted track's cover is orphaned; the shared cover remains referenced
	// even though one of its two users has been invalidated by a metadata edit.
	if _, err := app.Store.DB.Exec("DELETE FROM reinhardt_musicfile WHERE id=3; UPDATE reinhardt_musicfile SET title='Edited' WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	if err := app.Work(test.Context(), true); err != nil {
		test.Fatal(err)
	}
	for _, path := range []string{oldBlue, legacy} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			test.Fatal("worker retained an old orphan thumbnail", path, err)
		}
	}
	for _, path := range []string{oldRed, recent, unrelated} {
		if _, err := os.Stat(path); err != nil {
			test.Fatal("worker removed a referenced, recent, or unrelated file", path, err)
		}
	}
	if _, err := app.Store.DB.Exec("DELETE FROM reinhardt_musicfile WHERE id=2"); err != nil {
		test.Fatal(err)
	}
	if err := app.Store.pruneArtworkCache(test.Context(), time.Now().Add(-24*time.Hour)); err != nil {
		test.Fatal(err)
	}
	if _, err := os.Stat(oldRed); !errors.Is(err, os.ErrNotExist) {
		test.Fatal("thumbnail survived removal of its final reference", err)
	}
}

func TestArtworkFallbacksStayTemporaryAndFollowMetadata(test *testing.T) {
	app := testApp(test)
	first := callArtwork(app, "GET", "/artwork/1/96.jpg", 0, nil, false)
	requireCode(test, first, 200)
	second := callArtwork(app, "GET", "/artwork/2/96.jpg", 0, nil, false)
	requireCode(test, second, 200)
	if bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		test.Fatal("different track titles shared a fallback sleeve")
	}
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET title='Changed sleeve label' WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	updated := callArtwork(app, "GET", "/artwork/1/96.jpg", 0, nil, false)
	requireCode(test, updated, 200)
	if bytes.Equal(first.Body.Bytes(), updated.Body.Bytes()) || updated.Header().Get("Cache-Control") != "public, max-age=3600" {
		test.Fatal("fallback was reused across metadata changes or cached too long")
	}
	var references int
	if err := app.Store.DB.QueryRow("SELECT COUNT(*) FROM go_track_artwork WHERE artwork_hash IS NOT NULL").Scan(&references); err != nil || references != 0 {
		test.Fatal("fallback created a persistent artwork reference", references, err)
	}
	if len(artworkCacheFiles(test, app)) != 0 {
		test.Fatal("fallback thumbnails were persisted")
	}
	track, _ := app.Store.Track(test.Context(), 1)
	if err := os.WriteFile(track.FullPath+".png", artworkTestPNG(test), 0600); err != nil {
		test.Fatal(err)
	}
	actual := callArtwork(app, "GET", "/artwork/1/96.jpg", 0, nil, false)
	requireCode(test, actual, 200)
	if actual.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || bytes.Equal(actual.Body.Bytes(), updated.Body.Bytes()) {
		test.Fatal("temporary fallback hid newly available artwork")
	}
}

func TestDiscoveredArtworkKeepsItsContentHashAfterSidecarReload(test *testing.T) {
	app := testApp(test)
	data := artworkTestPNG(test)
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname() == "itunes.apple.com" {
			return artworkSearchResponse("Artist", "Album"), nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	requireCode(test, callArtwork(app, "GET", "/artwork/1/96.jpg", 0, nil, false), 200)
	first, err := app.Store.trackArtwork(test.Context(), "1")
	if err != nil || first.hash == "" {
		test.Fatal("discovery did not establish an artwork reference", first, err)
	}
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET title='Edited metadata' WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	restarted, err := newApp(app.Store)
	if err != nil {
		test.Fatal(err)
	}
	restarted.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		test.Error("sidecar reload unexpectedly required an online lookup")
		return nil, fmt.Errorf("offline")
	})
	requireCode(test, callArtwork(restarted, "GET", "/artwork/1/128.jpg", 0, nil, false), 200)
	second, err := app.Store.trackArtwork(test.Context(), "1")
	if err != nil || second.hash != first.hash || len(artworkCacheFiles(test, app)) != 2 {
		test.Fatal("discovery and its saved JPEG produced different content identities", first, second, err)
	}
}
