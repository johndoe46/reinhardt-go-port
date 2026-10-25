package reinhardt

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestLatestAdditionsSelectionAndInvalidation(test *testing.T) {
	app := testApp(test)
	populateHomeLibrary(test, app, 330)
	stats := measureAppSQL(test, app)
	check := func(reloads int64) {
		test.Helper()
		want, err := app.Store.Tracks(test.Context(), latestAdditionsClause)
		if err != nil {
			test.Fatal(err)
		}
		before, writes := stats.homeLoads.Load(), stats.writes.Load()
		for range 2 {
			got, err := app.latestAdditions(test.Context())
			if err != nil || !reflect.DeepEqual(got, want) {
				test.Fatalf("cached home tracks differ from the original query: got %v, want %v: %v", got, want, err)
			}
			if len(got) > 25 {
				test.Fatal("home cache exceeded the page limit")
			}
		}
		if loads := stats.homeLoads.Load() - before; loads != reloads {
			test.Fatalf("home query ran %d times, want %d", loads, reloads)
		}
		if stats.writes.Load() != writes {
			test.Fatal("home lookup wrote to SQLite")
		}
	}
	exec := func(query string) {
		test.Helper()
		if _, err := app.Store.DB.Exec(query); err != nil {
			test.Fatal(err)
		}
	}
	check(1)
	// Preferences and queue membership change per user, outside the shared cache.
	mustTag(test, app, 1, "queue", []int64{1})
	exec("UPDATE reinhardt_preferences SET current_position=123 WHERE owner_id=1")
	check(0)
	for _, query := range []string{
		`INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,created_at,modified_at,missing)
VALUES(1,'new.mp3','New artist','New tune','New album','','','2030-01-01','2030-01-01',0)`,
		"UPDATE reinhardt_musicfile SET title='Changed title',artist='Changed artist',genre='rock',comment='Edited' WHERE relative_path='new.mp3'",
		"UPDATE reinhardt_musicfile SET album='Album',created_at='2040-01-01' WHERE relative_path='new.mp3'",
		"UPDATE reinhardt_musicfile SET missing=1 WHERE relative_path='new.mp3'",
		"UPDATE reinhardt_musicfile SET missing=0,album='' WHERE relative_path='new.mp3'",
		"UPDATE reinhardt_musicfile SET id=1000,relative_path='renamed.mp3',duration=200,track_number=2,year='2020',hash='changed' WHERE relative_path='new.mp3'",
		"UPDATE reinhardt_musicdirectory SET local_path='/updated/music' WHERE id=1",
		"DELETE FROM reinhardt_musicfile WHERE id=1000",
	} {
		exec(query)
		check(1)
	}
	tx, err := app.Store.DB.Begin()
	if err != nil {
		test.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE reinhardt_musicfile SET album='Rolled back'"); err != nil {
		tx.Rollback()
		test.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		test.Fatal(err)
	}
	check(0)
	// Worker, CLI, and external writes must invalidate without touching the App object.
	other, err := sql.Open("sqlite", app.Store.Config.DatabasePath+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		test.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Exec("UPDATE reinhardt_musicfile SET album='External album',title='External title' WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	check(1)
	if _, err = other.Exec("DELETE FROM reinhardt_musicdirectory WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	check(1)
	// Empty results are cached too.
	check(0)
}

func TestLatestAdditionsRowsAreIsolatedAndURLsUseCurrentConfig(test *testing.T) {
	app := testApp(test)
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET hash='stored hash'"); err != nil {
		test.Fatal(err)
	}
	got, err := app.latestAdditions(test.Context())
	if err != nil || len(got) == 0 {
		test.Fatal("missing home tracks", err)
	}
	got[0].Title = "Request mutation"
	got[0].Artist = ""
	*got[0].Hash = "Request hash mutation"
	got[0] = &Track{ID: -1}
	app.Store.Config.DelegateDownloadPrefix = "https://audio.example/music/"
	app.Store.Config.URLPrefix = "reinhardt/"
	want, err := app.Store.Tracks(test.Context(), latestAdditionsClause)
	if err != nil {
		test.Fatal(err)
	}
	got, err = app.latestAdditions(test.Context())
	if err != nil || !reflect.DeepEqual(got, want) {
		test.Fatal("cached rows were modified or retained outdated URLs", err)
	}
}

func TestConcurrentHomeRequestsShareRefresh(test *testing.T) {
	app := testApp(test)
	stats := measureAppSQL(test, app)
	for round := range 2 {
		if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET title=?", fmt.Sprintf("Title %d", round)); err != nil {
			test.Fatal(err)
		}
		want, err := app.Store.Tracks(test.Context(), latestAdditionsClause)
		if err != nil {
			test.Fatal(err)
		}
		stats.homeLoads.Store(0)
		stats.writes.Store(0)
		start := make(chan struct{})
		results := make(chan error, 16)
		for range 16 {
			go func() {
				<-start
				got, err := app.latestAdditions(test.Context())
				if err == nil && !reflect.DeepEqual(got, want) {
					err = fmt.Errorf("concurrent home lookup returned wrong tracks")
				}
				results <- err
			}()
		}
		close(start)
		for range 16 {
			if err := <-results; err != nil {
				test.Error(err)
			}
		}
		if stats.homeLoads.Load() != 1 || stats.writes.Load() != 0 {
			test.Fatalf("concurrent refresh: loads=%d writes=%d", stats.homeLoads.Load(), stats.writes.Load())
		}
	}
}

func TestWarmHomeRequestsAvoidLibraryScan(test *testing.T) {
	app := testApp(test)
	populateHomeLibrary(test, app, 10000)
	stats := measureAppSQL(test, app)
	requireCode(test, call(app, "GET", "/", 1, nil, false), 200)
	if stats.homeLoads.Load() != 1 {
		test.Fatal("first home visit must load the original album selection")
	}
	for _, owner := range []int{1, 2} {
		for _, method := range []string{"GET", "HEAD"} {
			for _, fragment := range []bool{false, true} {
				stats.calls.Store(0)
				stats.writes.Store(0)
				stats.homeLoads.Store(0)
				stats.suggestions.Store(0)
				response := call(app, method, "/", owner, nil, fragment)
				requireCode(test, response, 200)
				// The default grid refreshes artwork references in one batch.
				wantCalls := int64(5)
				if fragment {
					wantCalls = 4
				}
				if stats.calls.Load() != wantCalls || stats.writes.Load() != 0 || stats.homeLoads.Load() != 0 || stats.suggestions.Load() != 0 {
					test.Fatalf("warm home: SQL=%d writes=%d home loads=%d suggestion loads=%d", stats.calls.Load(), stats.writes.Load(), stats.homeLoads.Load(), stats.suggestions.Load())
				}
				if method == "GET" && (strings.Count(response.Body.String(), `id="music_files_row_`) != 25 || !strings.Contains(response.Body.String(), `value="home"`)) {
					test.Fatal("cached home response lost its track list or Unpoly context")
				}
			}
		}
	}
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET title='Visible edit' WHERE album='Album'"); err != nil {
		test.Fatal(err)
	}
	stats.homeLoads.Store(0)
	response := call(app, "GET", "/", 1, nil, true)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), "Visible edit") || stats.homeLoads.Load() != 1 {
		test.Fatal("Unpoly home response did not reflect the committed edit")
	}
}

func TestHomeLookupDoesNotServeCacheAfterDatabaseError(test *testing.T) {
	app := testApp(test)
	if _, err := app.latestAdditions(test.Context()); err != nil {
		test.Fatal(err)
	}
	if _, err := app.Store.DB.Exec("DROP TABLE go_home_revision"); err != nil {
		test.Fatal(err)
	}
	if _, err := app.latestAdditions(test.Context()); err == nil {
		test.Fatal("revision failure must not return cached data")
	}
}
