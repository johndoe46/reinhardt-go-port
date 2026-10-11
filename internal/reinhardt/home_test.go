package reinhardt

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestLatestAdditionsParityAndInvalidation(t *testing.T) {
	a := testApp(t)
	populateHomeLibrary(t, a, 330)
	stats := measureAppSQL(t, a)
	check := func(reloads int64) {
		t.Helper()
		want, err := a.S.Tracks(latestAdditionsClause)
		if err != nil {
			t.Fatal(err)
		}
		before, writes := stats.homeLoads.Load(), stats.writes.Load()
		for range 2 {
			got, err := a.latestAdditions()
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("cached home tracks differ from the original query: got %v, want %v: %v", got, want, err)
			}
			if len(got) > 25 {
				t.Fatal("home cache exceeded the page limit")
			}
		}
		if loads := stats.homeLoads.Load() - before; loads != reloads {
			t.Fatalf("home query ran %d times, want %d", loads, reloads)
		}
		if stats.writes.Load() != writes {
			t.Fatal("home lookup wrote to SQLite")
		}
	}
	exec := func(query string) {
		t.Helper()
		if _, err := a.S.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	check(1)
	// Preferences and queue membership change per user, outside the shared cache.
	mustTag(t, a, 1, "queue", []int64{1})
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
	tx, err := a.S.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE reinhardt_musicfile SET album='Rolled back'"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check(0)
	// Worker/CLI/Django writes must invalidate without touching the App object.
	other, err := sql.Open("sqlite", a.S.C["DATABASE_PATH"]+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Exec("UPDATE reinhardt_musicfile SET album='External album',title='External title' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	check(1)
	if _, err = other.Exec("DELETE FROM reinhardt_musicdirectory WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	check(1)
	// Empty results are cached too.
	check(0)
}

func TestLatestAdditionsRowsAreIsolatedAndURLsUseCurrentConfig(t *testing.T) {
	a := testApp(t)
	got, err := a.latestAdditions()
	if err != nil || len(got) == 0 {
		t.Fatal("missing home tracks", err)
	}
	got[0]["title"] = "Request mutation"
	delete(got[0], "artist")
	got[0] = Row{"id": -1}
	a.S.C["DELEGATE_DOWNLOAD_PREFIX"] = "https://audio.example/music/"
	a.S.C["URL_PREFIX"] = "reinhardt/"
	want, err := a.S.Tracks(latestAdditionsClause)
	if err != nil {
		t.Fatal(err)
	}
	got, err = a.latestAdditions()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("cached rows were modified or retained outdated URLs", err)
	}
}

func TestConcurrentHomeRequestsShareRefresh(t *testing.T) {
	a := testApp(t)
	stats := measureAppSQL(t, a)
	for round := range 2 {
		if _, err := a.S.DB.Exec("UPDATE reinhardt_musicfile SET title=?", fmt.Sprintf("Title %d", round)); err != nil {
			t.Fatal(err)
		}
		want, err := a.S.Tracks(latestAdditionsClause)
		if err != nil {
			t.Fatal(err)
		}
		stats.homeLoads.Store(0)
		stats.writes.Store(0)
		start := make(chan struct{})
		results := make(chan error, 16)
		for range 16 {
			go func() {
				<-start
				got, err := a.latestAdditions()
				if err == nil && !reflect.DeepEqual(got, want) {
					err = fmt.Errorf("concurrent home lookup returned wrong tracks")
				}
				results <- err
			}()
		}
		close(start)
		for range 16 {
			if err := <-results; err != nil {
				t.Error(err)
			}
		}
		if stats.homeLoads.Load() != 1 || stats.writes.Load() != 0 {
			t.Fatalf("concurrent refresh: loads=%d writes=%d", stats.homeLoads.Load(), stats.writes.Load())
		}
	}
}

func TestWarmHomeRequestsAvoidLibraryScan(t *testing.T) {
	a := testApp(t)
	populateHomeLibrary(t, a, 10000)
	stats := measureAppSQL(t, a)
	requireCode(t, call(a, "GET", "/", 1, nil, false), 200)
	if stats.homeLoads.Load() != 1 {
		t.Fatal("first home visit must load the original album selection")
	}
	for _, owner := range []int{1, 2} {
		for _, method := range []string{"GET", "HEAD"} {
			for _, fragment := range []bool{false, true} {
				stats.calls.Store(0)
				stats.writes.Store(0)
				stats.homeLoads.Store(0)
				stats.suggestions.Store(0)
				w := call(a, method, "/", owner, nil, fragment)
				requireCode(t, w, 200)
				wantCalls := int64(4)
				if fragment {
					wantCalls = 3
				}
				if stats.calls.Load() != wantCalls || stats.writes.Load() != 0 || stats.homeLoads.Load() != 0 || stats.suggestions.Load() != 0 {
					t.Fatalf("warm home: SQL=%d writes=%d home loads=%d suggestion loads=%d", stats.calls.Load(), stats.writes.Load(), stats.homeLoads.Load(), stats.suggestions.Load())
				}
				if method == "GET" && (strings.Count(w.Body.String(), `id="music_files_row_`) != 25 || !strings.Contains(w.Body.String(), `value="home"`)) {
					t.Fatal("cached home response lost its track list or Unpoly context")
				}
			}
		}
	}
	if _, err := a.S.DB.Exec("UPDATE reinhardt_musicfile SET title='Visible edit' WHERE album='Album'"); err != nil {
		t.Fatal(err)
	}
	stats.homeLoads.Store(0)
	w := call(a, "GET", "/", 1, nil, true)
	requireCode(t, w, 200)
	if !strings.Contains(w.Body.String(), "Visible edit") || stats.homeLoads.Load() != 1 {
		t.Fatal("Unpoly home response did not reflect the committed edit")
	}
}

func TestHomeLookupDoesNotServeCacheAfterDatabaseError(t *testing.T) {
	a := testApp(t)
	if _, err := a.latestAdditions(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.S.DB.Exec("DROP TABLE go_home_revision"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.latestAdditions(); err == nil {
		t.Fatal("revision failure must not return cached data")
	}
}
