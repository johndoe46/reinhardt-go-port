package reinhardt

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSuggestedDataOrderAndInvalidation(test *testing.T) {
	app := testApp(test)
	stats := measureAppSQL(test, app)
	check := func(want []string, reloads int64) {
		test.Helper()
		before, writes := stats.suggestions.Load(), stats.writes.Load()
		for range 2 {
			got, err := app.suggestedData()
			if err != nil || !reflect.DeepEqual(got, want) {
				test.Fatalf("suggestions: got %v, want %v: %v", got, want, err)
			}
		}
		if got := stats.suggestions.Load() - before; got != reloads {
			test.Fatalf("loaded suggestions %d times, want %d", got, reloads)
		}
		if stats.writes.Load() != writes {
			test.Fatal("suggestion lookup wrote to the database")
		}
	}
	exec := func(query string) {
		test.Helper()
		if _, err := app.Store.DB.Exec(query); err != nil {
			test.Fatal(err)
		}
	}
	initial := []string{"Another", "Artist", "Album", "Other"}
	check(initial, 1)
	exec("UPDATE reinhardt_preferences SET current_position=123 WHERE owner_id=1")
	exec("UPDATE reinhardt_musicfile SET title='Edited',missing=1 WHERE id=1")
	exec("UPDATE reinhardt_musicfile SET artist=artist,album=album")
	check(initial, 0)
	exec(`INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,created_at,modified_at,missing)
VALUES(1,'4.mp3','Zed','New tune','Album','','','2026-01-01','2026-01-01',1)`)
	check([]string{"Another", "Artist", "Zed", "Album", "Other"}, 1)
	exec("UPDATE reinhardt_musicfile SET artist='Album',album='' WHERE id=4")
	check([]string{"Album", "Another", "Artist", "", "Album", "Other"}, 1)
	exec("DELETE FROM reinhardt_musicfile WHERE id=4")
	check(initial, 1)
	tx, err := app.Store.DB.Begin()
	if err != nil {
		test.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE reinhardt_musicfile SET artist='Rolled back'"); err != nil {
		tx.Rollback()
		test.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		test.Fatal(err)
	}
	check(initial, 0)

	// A separate SQLite connection represents a worker, CLI, or Django writer.
	other, err := sql.Open("sqlite", app.Store.Config["DATABASE_PATH"]+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		test.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Exec("UPDATE reinhardt_musicfile SET artist='Changed'"); err != nil {
		test.Fatal(err)
	}
	check([]string{"Changed", "Album", "Other"}, 1)
	if _, err = other.Exec("DELETE FROM reinhardt_musicdirectory WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	check([]string{}, 1)
}

func TestSuggestedDataConcurrentRequests(test *testing.T) {
	app := testApp(test)
	stats := measureAppSQL(test, app)
	for round := range 2 {
		artist := fmt.Sprintf("Artist %d", round)
		if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET artist=? WHERE id=1", artist); err != nil {
			test.Fatal(err)
		}
		stats.suggestions.Store(0)
		start := make(chan struct{})
		errors := make(chan error, 20)
		var wg sync.WaitGroup
		for range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				words, err := app.suggestedData()
				if err != nil {
					errors <- err
				} else if !reflect.DeepEqual(words, []string{"Another", "Artist", artist, "Album", "Other"}) {
					errors <- fmt.Errorf("wrong concurrent suggestions: %v", words)
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errors)
		for err := range errors {
			test.Error(err)
		}
		if got := stats.suggestions.Load(); got != 1 {
			test.Fatalf("concurrent requests rebuilt suggestions %d times", got)
		}
	}
}

func TestFullPageSuggestionsSharedAndRefreshed(test *testing.T) {
	app := testApp(test)
	stats := measureAppSQL(test, app)
	response := call(app, "GET", "/preferences/", 1, nil, true)
	requireCode(test, response, 200)
	response = dropdownRequest(app, false, false)
	requireCode(test, response, 200)
	if stats.suggestions.Load() != 0 {
		test.Fatal("partials loaded search suggestions")
	}
	for _, owner := range []int{1, 2, 1} {
		response = call(app, "GET", "/preferences/", owner, nil, false)
		requireCode(test, response, 200)
		if !strings.Contains(response.Body.String(), `<option value="Another"></option>`) {
			test.Fatal("full page did not include suggestions")
		}
	}
	if stats.suggestions.Load() != 1 {
		test.Fatal("full pages did not share the suggestion cache")
	}
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET artist=? WHERE id=1", "New & <b>"); err != nil {
		test.Fatal(err)
	}
	response = call(app, "GET", "/preferences/", 2, nil, false)
	requireCode(test, response, 200)
	if stats.suggestions.Load() != 2 || !strings.Contains(response.Body.String(), `<option value="New &amp; &lt;b&gt;"></option>`) {
		test.Fatal("updated suggestions were not refreshed and escaped")
	}
}

func BenchmarkFullPageSuggestions(benchmark *testing.B) {
	for _, tracks := range []int{3, 10000, 100000} {
		benchmark.Run(fmt.Sprintf("tracks=%d", tracks), func(benchmark *testing.B) {
			app := testApp(benchmark)
			if tracks > 3 {
				_, err := app.Store.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<?)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),printf('Artist %d',id/10),printf('Tune %d',id),printf('Album %d',id/5),'jazz','','2001',10,id,'2026-01-01','2026-01-01',0 FROM n`, tracks)
				if err != nil {
					benchmark.Fatal(err)
				}
			}
			stats := measureAppSQL(benchmark, app)
			if response := call(app, "GET", "/preferences/", 1, nil, false); response.Code != 200 {
				benchmark.Fatal(response.Body.String())
			}
			stats.calls.Store(0)
			stats.suggestions.Store(0)
			stats.duration.Store(0)
			benchmark.ReportAllocs()
			benchmark.ResetTimer()
			for range benchmark.N {
				if response := call(app, "GET", "/preferences/", 1, nil, false); response.Code != 200 {
					benchmark.Fatal(response.Body.String())
				}
			}
			benchmark.StopTimer()
			benchmark.ReportMetric(float64(stats.calls.Load())/float64(benchmark.N), "sql/op")
			benchmark.ReportMetric(float64(stats.suggestions.Load())/float64(benchmark.N), "suggestions/op")
			benchmark.ReportMetric(float64(stats.duration.Load())/float64(benchmark.N)/1e6, "db-ms/op")
		})
	}
}
