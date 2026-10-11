package reinhardt

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSimilarArtistsCacheAndLibraryChanges(t *testing.T) {
	a := testApp(t)
	stats := measureAppSQL(t, a)
	var calls atomic.Int64
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Hostname() != "ws.audioscrobbler.com" || r.URL.Query().Get("artist") != "Artist" {
			return nil, fmt.Errorf("unexpected Last.fm request: %s", r.URL)
		}
		return remoteResponse(`{"similarartists":{"artist":[{"name":"Another"},{"name":"Imported"},{"name":"Renamed"},{"name":"Artist"},{"name":"ANOTHER"}]}}`), nil
	})
	check := func(query string, want []string, loads int64) {
		t.Helper()
		before := stats.artistLoads.Load()
		got := a.similarArtists(context.Background(), query)
		if !reflect.DeepEqual(got, want) || calls.Load() != 1 || stats.artistLoads.Load()-before != loads {
			t.Fatalf("got %v, want %v; remote=%d, library loads=%d", got, want, calls.Load(), stats.artistLoads.Load()-before)
		}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := a.S.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	check("Artist", []string{"Another"}, 1)
	check("artist", []string{"Another"}, 0)
	exec("UPDATE reinhardt_preferences SET current_position=123 WHERE owner_id=1")
	mustTag(t, a, 1, "blacklist", []int64{1})
	exec("UPDATE reinhardt_musicfile SET album='Changed album',title='Changed title' WHERE id=1")
	check("Artist", []string{"Another"}, 0)
	exec(`INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,created_at,modified_at)
VALUES(1,'4.mp3','Imported','New tune','','','','2026-01-01','2026-01-01')`)
	check("Artist", []string{"Another", "Imported"}, 1)
	exec("UPDATE reinhardt_musicfile SET artist='Renamed' WHERE id=3")
	check("Artist", []string{"Imported", "Renamed"}, 1)
	exec("DELETE FROM reinhardt_musicfile WHERE id=4")
	check("Artist", []string{"Renamed"}, 1)
	tx, err := a.S.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE reinhardt_musicfile SET artist='Temporary' WHERE id=3"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check("Artist", []string{"Renamed"}, 0)
	other, err := sql.Open("sqlite", a.S.C["DATABASE_PATH"]+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Exec("UPDATE reinhardt_musicfile SET artist='Another',missing=1 WHERE id=3"); err != nil {
		t.Fatal(err)
	}
	// Artist-page matching retains its existing inclusion of missing tracks.
	check("Artist", []string{"Another"}, 1)
	if _, err = other.Exec("DELETE FROM reinhardt_musicdirectory WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	check("Artist", []string{}, 1)
	check("Artist", []string{}, 0)
}

func TestSimilarArtistCacheExpiryAndFailures(t *testing.T) {
	for _, scenario := range []string{"success", "empty", "network", "http", "json", "api-error"} {
		t.Run(scenario, func(t *testing.T) {
			a := testApp(t)
			var calls atomic.Int64
			a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				if calls.Add(1) > 1 || scenario == "success" {
					return remoteResponse(`{"similarartists":{"artist":[{"name":"Another"}]}}`), nil
				}
				switch scenario {
				case "empty":
					return remoteResponse(`{"similarartists":{"artist":[]}}`), nil
				case "network":
					return nil, fmt.Errorf("offline")
				case "http":
					resp := remoteResponse("unavailable")
					resp.StatusCode = 503
					return resp, nil
				case "json":
					return remoteResponse("invalid json"), nil
				default:
					return remoteResponse(`{"error":29,"message":"rate limited"}`), nil
				}
			})
			start := time.Now()
			first := a.similarArtists(context.Background(), "Artist")
			if scenario == "success" && !reflect.DeepEqual(first, []string{"Another"}) || scenario != "success" && len(first) != 0 {
				t.Fatal("wrong initial response", first)
			}
			if got := a.similarArtists(context.Background(), "Artist"); !reflect.DeepEqual(got, first) || calls.Load() != 1 {
				t.Fatal("response was not cached", got, calls.Load())
			}
			ttl := similarFailureTTL
			if scenario == "success" {
				ttl = similarSuccessTTL
			} else if scenario == "empty" {
				ttl = similarEmptyTTL
			}
			a.similar.mu.Lock()
			element := a.similar.entries["artist"]
			entry := element.Value.(similarLookupEntry)
			if entry.expires.Before(start.Add(ttl)) || entry.expires.After(time.Now().Add(ttl)) {
				t.Error("wrong cache lifetime", entry.expires)
			}
			entry.expires = time.Now().Add(-time.Second)
			element.Value = entry
			a.similar.mu.Unlock()
			if got := a.similarArtists(context.Background(), "Artist"); !reflect.DeepEqual(got, []string{"Another"}) || calls.Load() != 2 {
				t.Fatal("expired result was not refreshed", got, calls.Load())
			}
		})
	}
}

func TestSimilarArtistConcurrentLookupAndCancellation(t *testing.T) {
	a := testApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return remoteResponse(`{"similarartists":{"artist":[{"name":"Another"}]}}`), nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader := make(chan []string, 1)
	go func() { leader <- a.cachedSimilarNames(ctx, "Artist") }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("lookup did not start")
	}
	cancel()
	select {
	case got := <-leader:
		if len(got) != 0 {
			t.Fatal("cancelled caller received a result")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled caller did not stop waiting")
	}
	var wg sync.WaitGroup
	errors := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			got := a.similarArtists(ctx, "artist")
			if !reflect.DeepEqual(got, []string{"Another"}) {
				errors <- fmt.Errorf("shared lookup failed: %v", got)
			}
		}()
	}
	close(release)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent callers made duplicate Last.fm requests", calls.Load())
	}
}

func TestSimilarArtistLookupsDoNotSerializeArtists(t *testing.T) {
	a := testApp(t)
	started, release := make(chan struct{}), make(chan struct{})
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("artist") == "Slow artist" {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return remoteResponse(`{"similarartists":{"artist":[{"name":"Another"}]}}`), nil
	})
	slow := make(chan []string, 1)
	go func() { slow <- a.cachedSimilarNames(context.Background(), "Slow artist") }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	fast := a.cachedSimilarNames(ctx, "Fast artist")
	close(release)
	<-slow
	if !reflect.DeepEqual(fast, []string{"Another"}) {
		t.Fatal("a slow artist blocked an unrelated lookup", fast)
	}
}

func TestSimilarArtistsSharedByPageAndAutofeed(t *testing.T) {
	a := testApp(t)
	var calls atomic.Int64
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return remoteResponse(`{"similarartists":{"artist":[{"name":"Another"}]}}`), nil
	})
	w := call(a, "GET", "/similar_artists/?q=Artist", 1, nil, true)
	requireCode(t, w, 200)
	if !strings.Contains(w.Body.String(), "Another") {
		t.Fatal("similar artist response is missing")
	}
	mustTag(t, a, 1, "queue", []int64{1, 2})
	last, err := a.S.queueItem(1, "a.music_file_id=2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.S.DB.Exec("UPDATE reinhardt_preferences SET current_position=?,autofeed_queue=1,autofeed_similar=1 WHERE owner_id=1", last["id"]); err != nil {
		t.Fatal(err)
	}
	w = call(a, "POST", "/next_song/", 1, url.Values{}, true)
	requireCode(t, w, 200)
	if calls.Load() != 1 || !strings.Contains(w.Body.String(), `data-url="/stream/3/"`) {
		t.Fatal("autofeed did not reuse the artist-page lookup", calls.Load())
	}
}

func TestSimilarLookupCacheEviction(t *testing.T) {
	var cache similarLookupCache
	for i := range similarMaxEntries + 1 {
		cache.put(similarLookupEntry{artist: fmt.Sprint(i)})
	}
	if cache.recent.Len() != similarMaxEntries || cache.entries["0"] != nil {
		t.Fatal("cache did not enforce its entry limit")
	}
	large := strings.Repeat("x", similarMaxBytes/2)
	cache.put(similarLookupEntry{artist: "large-a", names: []string{large}})
	cache.put(similarLookupEntry{artist: "large-b", names: []string{large}})
	if cache.recent.Len() != 1 || cache.entries["large-b"] == nil {
		t.Fatal("cache did not enforce its storage limit")
	}
	cache.put(similarLookupEntry{artist: "large-b", names: []string{"small"}})
	if cache.bytes > 100 || cache.recent.Len() != 1 {
		t.Fatal("replacement retained obsolete names")
	}
}

func TestSimilarArtistsNormalizeNamesAndIsolateResults(t *testing.T) {
	a := testApp(t)
	if _, err := a.S.DB.Exec("UPDATE reinhardt_musicfile SET artist='Beyonce' WHERE id=3"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return remoteResponse(`{"similarartists":{"artist":[{"name":"BEYONCÉ"},{"name":"Beyonce"},{"name":"artist"},{"name":"Unavailable"}]}}`), nil
	})
	first := a.similarArtists(context.Background(), "Artist")
	if !reflect.DeepEqual(first, []string{"Beyonce"}) {
		t.Fatal("artist normalization, duplicate, or self filtering changed", first)
	}
	first[0] = "modified by caller"
	if got := a.similarArtists(context.Background(), "artist"); !reflect.DeepEqual(got, []string{"Beyonce"}) || calls.Load() != 1 {
		t.Fatal("caller modified the cached result", got, calls.Load())
	}
}
