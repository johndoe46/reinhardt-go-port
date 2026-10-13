package reinhardt

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSimilarArtistsAPIKeyConfiguration(test *testing.T) {
	for _, source := range []string{"default", "config", "dotenv", "environment"} {
		test.Run(source, func(test *testing.T) {
			unsetConfigEnvironment(test, configKeys...)
			home := test.TempDir()
			envFile := ""
			wantKey := defaultLastFMAPIKey
			if source != "default" {
				userDir := filepath.Join(home, ".reinhardt")
				if err := os.Mkdir(userDir, 0700); err != nil {
					test.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(userDir, "config"), []byte("LASTFM_API_KEY=config-key\n"), 0600); err != nil {
					test.Fatal(err)
				}
				wantKey = "config-key"
			}
			if source == "dotenv" || source == "environment" {
				envFile = filepath.Join(home, ".env")
				if err := os.WriteFile(envFile, []byte("LASTFM_API_KEY=dotenv-key\n"), 0600); err != nil {
					test.Fatal(err)
				}
				wantKey = "dotenv-key"
			}
			if source == "environment" {
				wantKey = "environment key&+?"
				test.Setenv("LASTFM_API_KEY", wantKey)
			}
			config, err := loadConfigFromHome(home, envFile, "")
			if err != nil {
				test.Fatal(err)
			}
			app := testApp(test)
			app.Store.Config["LASTFM_API_KEY"] = config["LASTFM_API_KEY"]
			app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
				query := request.URL.Query()
				if query.Get("api_key") != wantKey || query.Get("artist") != "Artist & Friends" || query.Get("method") != "artist.getsimilar" || query.Get("format") != "json" {
					return nil, fmt.Errorf("Last.fm request did not use the effective configuration")
				}
				return remoteResponse(`{"similarartists":{"artist":[{"name":"Another"}]}}`), nil
			})
			names, err := app.fetchSimilarNames(context.Background(), "Artist & Friends")
			if err != nil || !reflect.DeepEqual(names, []string{"Another"}) {
				test.Fatal("configured Last.fm lookup failed", names, err)
			}
		})
	}
}

func TestSimilarArtistsCacheAndLibraryChanges(test *testing.T) {
	app := testApp(test)
	stats := measureAppSQL(test, app)
	var calls atomic.Int64
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.URL.Hostname() != "ws.audioscrobbler.com" || request.URL.Query().Get("artist") != "Artist" {
			return nil, fmt.Errorf("unexpected Last.fm request: %s", request.URL)
		}
		return remoteResponse(`{"similarartists":{"artist":[{"name":"Another"},{"name":"Imported"},{"name":"Renamed"},{"name":"Artist"},{"name":"ANOTHER"}]}}`), nil
	})
	check := func(query string, want []string, loads int64) {
		test.Helper()
		before := stats.artistLoads.Load()
		got := app.similarArtists(context.Background(), query)
		if !reflect.DeepEqual(got, want) || calls.Load() != 1 || stats.artistLoads.Load()-before != loads {
			test.Fatalf("got %v, want %v; remote=%d, library loads=%d", got, want, calls.Load(), stats.artistLoads.Load()-before)
		}
	}
	exec := func(query string, args ...any) {
		test.Helper()
		if _, err := app.Store.DB.Exec(query, args...); err != nil {
			test.Fatal(err)
		}
	}
	check("Artist", []string{"Another"}, 1)
	check("artist", []string{"Another"}, 0)
	exec("UPDATE reinhardt_preferences SET current_position=123 WHERE owner_id=1")
	mustTag(test, app, 1, "blacklist", []int64{1})
	exec("UPDATE reinhardt_musicfile SET album='Changed album',title='Changed title' WHERE id=1")
	check("Artist", []string{"Another"}, 0)
	exec(`INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,created_at,modified_at)
VALUES(1,'4.mp3','Imported','New tune','','','','2026-01-01','2026-01-01')`)
	check("Artist", []string{"Another", "Imported"}, 1)
	exec("UPDATE reinhardt_musicfile SET artist='Renamed' WHERE id=3")
	check("Artist", []string{"Imported", "Renamed"}, 1)
	exec("DELETE FROM reinhardt_musicfile WHERE id=4")
	check("Artist", []string{"Renamed"}, 1)
	tx, err := app.Store.DB.Begin()
	if err != nil {
		test.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE reinhardt_musicfile SET artist='Temporary' WHERE id=3"); err != nil {
		tx.Rollback()
		test.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		test.Fatal(err)
	}
	check("Artist", []string{"Renamed"}, 0)
	other, err := sql.Open("sqlite", app.Store.Config["DATABASE_PATH"]+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		test.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Exec("UPDATE reinhardt_musicfile SET artist='Another',missing=1 WHERE id=3"); err != nil {
		test.Fatal(err)
	}
	// Artist-page matching retains its existing inclusion of missing tracks.
	check("Artist", []string{"Another"}, 1)
	if _, err = other.Exec("DELETE FROM reinhardt_musicdirectory WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	check("Artist", []string{}, 1)
	check("Artist", []string{}, 0)
}

func TestSimilarArtistCacheExpiryAndFailures(test *testing.T) {
	for _, scenario := range []string{"success", "empty", "network", "http", "json", "api-error"} {
		test.Run(scenario, func(test *testing.T) {
			app := testApp(test)
			var calls atomic.Int64
			app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
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
			first := app.similarArtists(context.Background(), "Artist")
			if scenario == "success" && !reflect.DeepEqual(first, []string{"Another"}) || scenario != "success" && len(first) != 0 {
				test.Fatal("wrong initial response", first)
			}
			if got := app.similarArtists(context.Background(), "Artist"); !reflect.DeepEqual(got, first) || calls.Load() != 1 {
				test.Fatal("response was not cached", got, calls.Load())
			}
			ttl := similarFailureTTL
			if scenario == "success" {
				ttl = similarSuccessTTL
			} else if scenario == "empty" {
				ttl = similarEmptyTTL
			}
			app.similar.mu.Lock()
			element := app.similar.entries["artist"]
			entry := element.Value.(similarLookupEntry)
			if entry.expires.Before(start.Add(ttl)) || entry.expires.After(time.Now().Add(ttl)) {
				test.Error("wrong cache lifetime", entry.expires)
			}
			entry.expires = time.Now().Add(-time.Second)
			element.Value = entry
			app.similar.mu.Unlock()
			if got := app.similarArtists(context.Background(), "Artist"); !reflect.DeepEqual(got, []string{"Another"}) || calls.Load() != 2 {
				test.Fatal("expired result was not refreshed", got, calls.Load())
			}
		})
	}
}

func TestSimilarArtistConcurrentLookupAndCancellation(test *testing.T) {
	app := testApp(test)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
			return remoteResponse(`{"similarartists":{"artist":[{"name":"Another"}]}}`), nil
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader := make(chan []string, 1)
	go func() { leader <- app.cachedSimilarNames(ctx, "Artist") }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		test.Fatal("lookup did not start")
	}
	cancel()
	select {
	case got := <-leader:
		if len(got) != 0 {
			test.Fatal("cancelled caller received a result")
		}
	case <-time.After(time.Second):
		test.Fatal("cancelled caller did not stop waiting")
	}
	var wg sync.WaitGroup
	errors := make(chan error, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			got := app.similarArtists(ctx, "artist")
			if !reflect.DeepEqual(got, []string{"Another"}) {
				errors <- fmt.Errorf("shared lookup failed: %v", got)
			}
		}()
	}
	close(release)
	wg.Wait()
	close(errors)
	for err := range errors {
		test.Error(err)
	}
	if calls.Load() != 1 {
		test.Fatal("concurrent callers made duplicate Last.fm requests", calls.Load())
	}
}

func TestSimilarArtistLookupsDoNotSerializeArtists(test *testing.T) {
	app := testApp(test)
	started, release := make(chan struct{}), make(chan struct{})
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("artist") == "Slow artist" {
			close(started)
			select {
			case <-release:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		return remoteResponse(`{"similarartists":{"artist":[{"name":"Another"}]}}`), nil
	})
	slow := make(chan []string, 1)
	go func() { slow <- app.cachedSimilarNames(context.Background(), "Slow artist") }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	fast := app.cachedSimilarNames(ctx, "Fast artist")
	close(release)
	<-slow
	if !reflect.DeepEqual(fast, []string{"Another"}) {
		test.Fatal("a slow artist blocked an unrelated lookup", fast)
	}
}

func TestSimilarArtistsSharedByPageAndAutofeed(test *testing.T) {
	app := testApp(test)
	var calls atomic.Int64
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return remoteResponse(`{"similarartists":{"artist":[{"name":"Another"}]}}`), nil
	})
	response := call(app, "GET", "/similar_artists/?q=Artist", 1, nil, true)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), "Another") {
		test.Fatal("similar artist response is missing")
	}
	mustTag(test, app, 1, "queue", []int64{1, 2})
	last, err := app.Store.queueItem(1, "a.music_file_id=2")
	if err != nil {
		test.Fatal(err)
	}
	if _, err = app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=?,autofeed_queue=1,autofeed_similar=1 WHERE owner_id=1", last["id"]); err != nil {
		test.Fatal(err)
	}
	response = call(app, "POST", "/next_song/", 1, url.Values{}, true)
	requireCode(test, response, 200)
	if calls.Load() != 1 || !strings.Contains(response.Body.String(), `data-url="/stream/3/"`) {
		test.Fatal("autofeed did not reuse the artist-page lookup", calls.Load())
	}
}

func TestSimilarLookupCacheEviction(test *testing.T) {
	var cache similarLookupCache
	for index := range similarMaxEntries + 1 {
		cache.put(similarLookupEntry{artist: fmt.Sprint(index)})
	}
	if cache.recent.Len() != similarMaxEntries || cache.entries["0"] != nil {
		test.Fatal("cache did not enforce its entry limit")
	}
	large := strings.Repeat("x", similarMaxBytes/2)
	cache.put(similarLookupEntry{artist: "large-a", names: []string{large}})
	cache.put(similarLookupEntry{artist: "large-b", names: []string{large}})
	if cache.recent.Len() != 1 || cache.entries["large-b"] == nil {
		test.Fatal("cache did not enforce its storage limit")
	}
	cache.put(similarLookupEntry{artist: "large-b", names: []string{"small"}})
	if cache.bytes > 100 || cache.recent.Len() != 1 {
		test.Fatal("replacement retained obsolete names")
	}
}

func TestSimilarArtistsNormalizeNamesAndIsolateResults(test *testing.T) {
	app := testApp(test)
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET artist='Beyonce' WHERE id=3"); err != nil {
		test.Fatal(err)
	}
	var calls atomic.Int64
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return remoteResponse(`{"similarartists":{"artist":[{"name":"BEYONCÉ"},{"name":"Beyonce"},{"name":"artist"},{"name":"Unavailable"}]}}`), nil
	})
	first := app.similarArtists(context.Background(), "Artist")
	if !reflect.DeepEqual(first, []string{"Beyonce"}) {
		test.Fatal("artist normalization, duplicate, or self filtering changed", first)
	}
	first[0] = "modified by caller"
	if got := app.similarArtists(context.Background(), "artist"); !reflect.DeepEqual(got, []string{"Beyonce"}) || calls.Load() != 1 {
		test.Fatal("caller modified the cached result", got, calls.Load())
	}
}
