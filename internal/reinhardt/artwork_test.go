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
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func artworkTestReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("artwork lookup did not complete independently")
		var zero T
		return zero
	}
}

func artworkTestPNG(t *testing.T) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 50, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 50; x++ {
			im.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func artworkSearchResponse(artist, album string) *http.Response {
	b, _ := json.Marshal(map[string]any{"results": []map[string]string{{
		"artistName": artist, "collectionName": album,
		"artworkUrl100": "https://is1.mzstatic.com/art/100x100bb.jpg",
	}}})
	return remoteResponse(string(b))
}

func TestIndependentArtworkLookupsAndCacheHitsDoNotBlock(t *testing.T) {
	a := testApp(t)
	first, err := a.S.Track(1)
	if err != nil {
		t.Fatal(err)
	}
	other, err := a.S.Track(3)
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var calls atomic.Int64
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Query().Get("term") == "artist album" {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return nil, fmt.Errorf("offline")
	})
	// Prime a cached miss, then block a different key during external I/O.
	if a.discoverCover(context.Background(), other) != nil {
		t.Fatal("unexpected cover from an offline transport")
	}
	firstDone := make(chan image.Image, 1)
	go func() { firstDone <- a.discoverCover(context.Background(), first) }()
	artworkTestReceive(t, started)
	for _, cached := range []bool{false, true} {
		track := other
		if !cached {
			track = Row{"artist": "Independent", "album": "Album", "full_path": filepath.Join(t.TempDir(), "new.mp3")}
		}
		done := make(chan image.Image, 1)
		go func() { done <- a.discoverCover(context.Background(), track) }()
		if artworkTestReceive(t, done) != nil {
			t.Fatal("unexpected cover from an offline transport")
		}
	}
	if calls.Load() != 3 {
		t.Fatal("cached miss fetched again, or independent discovery did not start", calls.Load())
	}
	unblock()
	artworkTestReceive(t, firstDone)
}

func TestConcurrentArtworkSizesAndAlbumTracksShareDiscovery(t *testing.T) {
	a := testApp(t)
	data := artworkTestPNG(t)
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var searches, downloads atomic.Int64
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Hostname() == "itunes.apple.com" {
			searches.Add(1)
			return artworkSearchResponse("Artist", "Album"), nil
		}
		if downloads.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	type result struct {
		size int
		w    *httptest.ResponseRecorder
	}
	const callers = 16
	results := make(chan result, callers)
	for i := range callers {
		go func() {
			size := []int{96, 128, 192, 256, 384, 512}[i%6]
			results <- result{size, call(a, "GET", fmt.Sprintf("/artwork/%d/%d.jpg", 1+i%2, size), 0, nil, false)}
		}()
	}
	artworkTestReceive(t, started)
	unblock()
	for range callers {
		r := artworkTestReceive(t, results)
		requireCode(t, r.w, 200)
		im, err := jpeg.Decode(bytes.NewReader(r.w.Body.Bytes()))
		if err != nil || im.Bounds().Dx() != r.size || im.Bounds().Dy() != r.size {
			t.Fatal("wrong concurrent artwork dimensions", r.size, err)
		}
		red, green, _, _ := im.At(r.size/2, r.size/2).RGBA()
		if red < 60000 || green > 1000 {
			t.Fatal("concurrent request received fallback artwork instead of discovered cover")
		}
	}
	if searches.Load() != 1 || downloads.Load() != 1 {
		t.Fatalf("same-album discovery duplicated: searches=%d downloads=%d", searches.Load(), downloads.Load())
	}
	a.coverMu.Lock()
	defer a.coverMu.Unlock()
	if len(a.coverFlights) != 0 || a.coverCache["artist/album"].image == nil {
		t.Fatal("completed discovery retained a flight or lost its cached image")
	}
}

func TestArtworkLookupCancellationDoesNotCancelSharedDiscovery(t *testing.T) {
	a := testApp(t)
	m, err := a.S.Track(1)
	if err != nil {
		t.Fatal(err)
	}
	data := artworkTestPNG(t)
	started, release := make(chan context.Context, 1), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var calls atomic.Int64
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Hostname() == "itunes.apple.com" {
			started <- r.Context()
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			return artworkSearchResponse("Artist", "Album"), nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader := make(chan image.Image, 1)
	go func() { leader <- a.discoverCover(ctx, m) }()
	lookupCtx := artworkTestReceive(t, started)
	deadline, ok := lookupCtx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 8*time.Second {
		t.Fatal("shared discovery lost its eight-second deadline")
	}
	follower := make(chan image.Image, 1)
	go func() { follower <- a.discoverCover(context.Background(), m) }()
	cancel()
	if artworkTestReceive(t, leader) != nil || lookupCtx.Err() != nil {
		t.Fatal("leader cancellation cancelled the shared lookup")
	}
	// An already-cancelled caller must not start a lookup or wait on this flight.
	if a.discoverCover(ctx, m) != nil || calls.Load() != 1 {
		t.Fatal("cancelled caller performed external work")
	}
	unblock()
	if artworkTestReceive(t, follower) == nil {
		t.Fatal("another caller lost its cover after leader cancellation")
	}
	if a.discoverCover(context.Background(), m) == nil || calls.Load() != 2 {
		t.Fatal("cancellation poisoned the success cache")
	}
}

func TestArtworkMissCacheExpiryAndFlightCleanup(t *testing.T) {
	a := testApp(t)
	m, err := a.S.Track(1)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("offline")
	})
	for round := range 2 {
		start := time.Now()
		for range 2 {
			if a.discoverCover(context.Background(), m) != nil {
				t.Fatal("unexpected artwork")
			}
		}
		if calls.Load() != int64(round+1) {
			t.Fatal("failure was not cached or expired failure was not retried", calls.Load())
		}
		a.coverMu.Lock()
		cached := a.coverCache["artist/album"]
		if cached.expires.Before(start.Add(time.Hour)) || cached.expires.After(time.Now().Add(time.Hour)) || len(a.coverFlights) != 0 {
			t.Error("wrong miss TTL or incomplete flight cleanup")
		}
		cached.expires = time.Now().Add(-time.Second)
		a.coverCache["artist/album"] = cached
		a.coverMu.Unlock()
	}
	if _, err := os.Stat(m.S("full_path") + ".jpg"); !os.IsNotExist(err) {
		t.Fatal("failed discovery created a sidecar", err)
	}
}
