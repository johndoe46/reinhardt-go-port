package reinhardt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestArtworkCacheControl(test *testing.T) {
	app := testApp(test)
	path := "/artwork/1/96.jpg"
	fallback := call(app, "GET", path, 0, nil, false)
	requireCode(test, fallback, http.StatusOK)
	if fallback.Header().Get("Cache-Control") != "public, max-age=3600" || fallback.Header().Get("Vary") != "" {
		test.Fatal("fallback should be cached briefly without varying by user", fallback.Header())
	}
	track, err := app.Store.Track(test.Context(), 1)
	if err != nil {
		test.Fatal(err)
	}
	if err := os.WriteFile(track.FullPath+".png", artworkTestPNG(test), 0600); err != nil {
		test.Fatal(err)
	}
	var first []byte
	for _, method := range []string{"GET", "GET", "HEAD"} {
		response := call(app, method, path, 1, nil, false)
		requireCode(test, response, http.StatusOK)
		if response.Header().Get("Cache-Control") != "public, max-age=31536000" || response.Header().Get("Vary") != "" {
			test.Fatal("generated and disk-cached artwork must have long-lived caching", response.Header())
		}
		if method == "GET" {
			if first != nil && !bytes.Equal(first, response.Body.Bytes()) {
				test.Fatal("disk cache changed artwork bytes")
			}
			first = append([]byte(nil), response.Body.Bytes()...)
		} else if response.Body.Len() != 0 {
			test.Fatal("HEAD returned artwork bytes")
		}
		if modified := response.Header().Get("Last-Modified"); modified != "" {
			request := httptest.NewRequest("GET", path, nil)
			request.Header.Set("If-Modified-Since", modified)
			conditional := httptest.NewRecorder()
			app.ServeHTTP(conditional, request)
			requireCode(test, conditional, http.StatusNotModified)
			if conditional.Header().Get("Cache-Control") != "public, max-age=31536000" {
				test.Fatal("conditional artwork response lost its cache policy")
			}
		}
	}
	for path, status := range map[string]int{"/artwork/1/100.jpg": http.StatusBadRequest, "/artwork/999/96.jpg": http.StatusNotFound} {
		response := call(app, "GET", path, 0, nil, false)
		requireCode(test, response, status)
		if response.Header().Get("Cache-Control") != "private, no-cache" {
			test.Fatal("artwork error response received long-lived caching")
		}
	}
}

func TestArtworkURLsChangeAfterTrackEdits(test *testing.T) {
	app := testApp(test)
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_preferences SET show_track_grid=1 WHERE owner_id=1"); err != nil {
		test.Fatal(err)
	}
	queue := mustTag(test, app, 1, "queue", []int64{1})
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=(SELECT id FROM reinhardt_musicfiletag WHERE tag_id=?) WHERE owner_id=1", queue); err != nil {
		test.Fatal(err)
	}
	tagID := mustTag(test, app, 1, "shared artwork", []int64{1})
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_tag SET is_shared=1 WHERE id=?", tagID); err != nil {
		test.Fatal(err)
	}
	tag, err := app.Store.Tag(test.Context(), tagID, 1)
	if err != nil {
		test.Fatal(err)
	}
	track, err := app.Store.Track(test.Context(), 1)
	if err != nil {
		test.Fatal(err)
	}
	oldVersion := track.ModifiedAt
	for _, version := range []string{oldVersion, "2026-10-25 22:00:00.123456"} {
		if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET modified_at=? WHERE id=1", version); err != nil {
			test.Fatal(err)
		}
		updated, err := app.Store.Track(test.Context(), 1)
		if err != nil {
			test.Fatal(err)
		}
		version = updated.ModifiedAt
		for _, page := range []struct {
			path string
			size int
		}{{"/album/?q=Album", 256}, {"/track_details_modal/1", 512}, {"/artwork_modal/1/512.jpg", 512}, {tag.ShareURL, 512}} {
			response := call(app, "GET", page.path, 1, nil, false)
			requireCode(test, response, http.StatusOK)
			want := app.Store.URL("artwork", 1, page.size, version)
			if !strings.Contains(response.Body.String(), want) {
				test.Fatalf("%s lacks versioned artwork URL %s", page.path, want)
			}
			if page.size == 256 && !strings.Contains(response.Body.String(), `data-artwork512="`+app.Store.URL("artwork", 1, 512, version)+`"`) {
				test.Fatal("footer and fullscreen player lost the artwork version")
			}
		}
	}
	app.Store.Config.URLPrefix = "reinhardt/"
	versioned := app.Store.URL("artwork", 1, 96, "a & b?#")
	parsed, err := url.Parse(versioned)
	if err != nil || parsed.Path != "/reinhardt/artwork/1/96.jpg" || parsed.Query().Get("v") != "a & b?#" {
		test.Fatal("versioned artwork URL lost prefix or escaping", versioned, err)
	}
}

func artworkTestReceive[Result any](test *testing.T, ch <-chan Result) Result {
	test.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		test.Fatal("artwork lookup did not complete independently")
		var zero Result
		return zero
	}
}

func artworkTestPNG(test *testing.T) []byte {
	test.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 50, 50))
	for pixelY := 0; pixelY < 50; pixelY++ {
		for pixelX := 0; pixelX < 50; pixelX++ {
			im.Set(pixelX, pixelY, color.RGBA{255, 0, 0, 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, im); err != nil {
		test.Fatal(err)
	}
	return buffer.Bytes()
}

func artworkSearchResponse(artist, album string) *http.Response {
	responseJSON, _ := json.Marshal(map[string]any{"results": []map[string]string{{
		"artistName": artist, "collectionName": album,
		"artworkUrl100": "https://is1.mzstatic.com/art/100x100bb.jpg",
	}}})
	return remoteResponse(string(responseJSON))
}

func TestIndependentArtworkLookupsAndCacheHitsDoNotBlock(test *testing.T) {
	app := testApp(test)
	first, err := app.Store.Track(test.Context(), 1)
	if err != nil {
		test.Fatal(err)
	}
	other, err := app.Store.Track(test.Context(), 3)
	if err != nil {
		test.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var calls atomic.Int64
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.URL.Query().Get("term") == "artist album" {
			close(started)
			select {
			case <-release:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		return nil, fmt.Errorf("offline")
	})
	// Prime a cached miss, then block a different key during external I/O.
	if app.discoverCover(context.Background(), other) != nil {
		test.Fatal("unexpected cover from an offline transport")
	}
	firstDone := make(chan image.Image, 1)
	go func() { firstDone <- app.discoverCover(context.Background(), first) }()
	artworkTestReceive(test, started)
	for _, cached := range []bool{false, true} {
		track := other
		if !cached {
			track = &Track{Artist: "Independent", Album: "Album", FullPath: filepath.Join(test.TempDir(), "new.mp3")}
		}
		done := make(chan image.Image, 1)
		go func() { done <- app.discoverCover(context.Background(), track) }()
		if artworkTestReceive(test, done) != nil {
			test.Fatal("unexpected cover from an offline transport")
		}
	}
	if calls.Load() != 3 {
		test.Fatal("cached miss fetched again, or independent discovery did not start", calls.Load())
	}
	unblock()
	artworkTestReceive(test, firstDone)
}

func TestConcurrentArtworkSizesAndAlbumTracksShareDiscovery(test *testing.T) {
	app := testApp(test)
	data := artworkTestPNG(test)
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var searches, downloads atomic.Int64
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname() == "itunes.apple.com" {
			searches.Add(1)
			return artworkSearchResponse("Artist", "Album"), nil
		}
		if downloads.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	type result struct {
		size     int
		response *httptest.ResponseRecorder
	}
	const callers = 16
	results := make(chan result, callers)
	for index := range callers {
		go func() {
			size := []int{96, 128, 192, 256, 384, 512}[index%6]
			results <- result{size, call(app, "GET", fmt.Sprintf("/artwork/%d/%d.jpg", 1+index%2, size), 0, nil, false)}
		}()
	}
	artworkTestReceive(test, started)
	unblock()
	for range callers {
		result := artworkTestReceive(test, results)
		requireCode(test, result.response, 200)
		im, err := jpeg.Decode(bytes.NewReader(result.response.Body.Bytes()))
		if err != nil || im.Bounds().Dx() != result.size || im.Bounds().Dy() != result.size {
			test.Fatal("wrong concurrent artwork dimensions", result.size, err)
		}
		red, green, _, _ := im.At(result.size/2, result.size/2).RGBA()
		if red < 60000 || green > 1000 {
			test.Fatal("concurrent request received fallback artwork instead of discovered cover")
		}
	}
	if searches.Load() != 1 || downloads.Load() != 1 {
		test.Fatalf("same-album discovery duplicated: searches=%d downloads=%d", searches.Load(), downloads.Load())
	}
	app.coverMu.Lock()
	defer app.coverMu.Unlock()
	if len(app.coverFlights) != 0 || app.coverCache["artist/album"].image == nil {
		test.Fatal("completed discovery retained a flight or lost its cached image")
	}
}

func TestArtworkLookupCancellationDoesNotCancelSharedDiscovery(test *testing.T) {
	app := testApp(test)
	track, err := app.Store.Track(test.Context(), 1)
	if err != nil {
		test.Fatal(err)
	}
	data := artworkTestPNG(test)
	started, release := make(chan context.Context, 1), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var calls atomic.Int64
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.URL.Hostname() == "itunes.apple.com" {
			started <- request.Context()
			select {
			case <-release:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
			return artworkSearchResponse("Artist", "Album"), nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader := make(chan image.Image, 1)
	go func() { leader <- app.discoverCover(ctx, track) }()
	lookupCtx := artworkTestReceive(test, started)
	deadline, ok := lookupCtx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 8*time.Second {
		test.Fatal("shared discovery lost its eight-second deadline")
	}
	follower := make(chan image.Image, 1)
	go func() { follower <- app.discoverCover(context.Background(), track) }()
	cancel()
	if artworkTestReceive(test, leader) != nil || lookupCtx.Err() != nil {
		test.Fatal("leader cancellation cancelled the shared lookup")
	}
	// An already-cancelled caller must not start a lookup or wait on this flight.
	if app.discoverCover(ctx, track) != nil || calls.Load() != 1 {
		test.Fatal("cancelled caller performed external work")
	}
	unblock()
	if artworkTestReceive(test, follower) == nil {
		test.Fatal("another caller lost its cover after leader cancellation")
	}
	if app.discoverCover(context.Background(), track) == nil || calls.Load() != 2 {
		test.Fatal("cancellation poisoned the success cache")
	}
}

func TestArtworkMissCacheExpiryAndFlightCleanup(test *testing.T) {
	app := testApp(test)
	track, err := app.Store.Track(test.Context(), 1)
	if err != nil {
		test.Fatal(err)
	}
	var calls atomic.Int64
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("offline")
	})
	for round := range 2 {
		start := time.Now()
		for range 2 {
			if app.discoverCover(context.Background(), track) != nil {
				test.Fatal("unexpected artwork")
			}
		}
		if calls.Load() != int64(round+1) {
			test.Fatal("failure was not cached or expired failure was not retried", calls.Load())
		}
		app.coverMu.Lock()
		cached := app.coverCache["artist/album"]
		if cached.expires.Before(start.Add(time.Hour)) || cached.expires.After(time.Now().Add(time.Hour)) || len(app.coverFlights) != 0 {
			test.Error("wrong miss TTL or incomplete flight cleanup")
		}
		cached.expires = time.Now().Add(-time.Second)
		app.coverCache["artist/album"] = cached
		app.coverMu.Unlock()
	}
	if _, err := os.Stat(track.FullPath + ".jpg"); !os.IsNotExist(err) {
		test.Fatal("failed discovery created a sidecar", err)
	}
}
