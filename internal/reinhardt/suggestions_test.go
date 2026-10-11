package reinhardt

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestSuggestedDataOrderAndInvalidation(t *testing.T) {
	a := testApp(t)
	stats := measureAppSQL(t, a)
	check := func(want []string, reloads int64) {
		t.Helper()
		before, writes := stats.suggestions.Load(), stats.writes.Load()
		for range 2 {
			got, err := a.suggestedData()
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("suggestions: got %v, want %v: %v", got, want, err)
			}
		}
		if got := stats.suggestions.Load() - before; got != reloads {
			t.Fatalf("loaded suggestions %d times, want %d", got, reloads)
		}
		if stats.writes.Load() != writes {
			t.Fatal("suggestion lookup wrote to the database")
		}
	}
	exec := func(query string) {
		t.Helper()
		if _, err := a.S.DB.Exec(query); err != nil {
			t.Fatal(err)
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
	tx, err := a.S.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE reinhardt_musicfile SET artist='Rolled back'"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	check(initial, 0)

	// A separate SQLite connection represents a worker, CLI, or Django writer.
	other, err := sql.Open("sqlite", a.S.C["DATABASE_PATH"]+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Exec("UPDATE reinhardt_musicfile SET artist='Changed'"); err != nil {
		t.Fatal(err)
	}
	check([]string{"Changed", "Album", "Other"}, 1)
	if _, err = other.Exec("DELETE FROM reinhardt_musicdirectory WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	check([]string{}, 1)
}

func TestSuggestedDataConcurrentRequests(t *testing.T) {
	a := testApp(t)
	stats := measureAppSQL(t, a)
	for round := range 2 {
		artist := fmt.Sprintf("Artist %d", round)
		if _, err := a.S.DB.Exec("UPDATE reinhardt_musicfile SET artist=? WHERE id=1", artist); err != nil {
			t.Fatal(err)
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
				words, err := a.suggestedData()
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
			t.Error(err)
		}
		if got := stats.suggestions.Load(); got != 1 {
			t.Fatalf("concurrent requests rebuilt suggestions %d times", got)
		}
	}
}

func TestFullPageSuggestionsSharedAndRefreshed(t *testing.T) {
	a := testApp(t)
	stats := measureAppSQL(t, a)
	w := call(a, "GET", "/preferences/", 1, nil, true)
	requireCode(t, w, 200)
	w = dropdownRequest(a, false, false)
	requireCode(t, w, 200)
	if stats.suggestions.Load() != 0 {
		t.Fatal("partials loaded search suggestions")
	}
	for _, owner := range []int{1, 2, 1} {
		w = call(a, "GET", "/preferences/", owner, nil, false)
		requireCode(t, w, 200)
		if !strings.Contains(w.Body.String(), `<option value="Another"></option>`) {
			t.Fatal("full page did not include suggestions")
		}
	}
	if stats.suggestions.Load() != 1 {
		t.Fatal("full pages did not share the suggestion cache")
	}
	if _, err := a.S.DB.Exec("UPDATE reinhardt_musicfile SET artist=? WHERE id=1", "New & <b>"); err != nil {
		t.Fatal(err)
	}
	w = call(a, "GET", "/preferences/", 2, nil, false)
	requireCode(t, w, 200)
	if stats.suggestions.Load() != 2 || !strings.Contains(w.Body.String(), `<option value="New &amp; &lt;b&gt;"></option>`) {
		t.Fatal("updated suggestions were not refreshed and escaped")
	}
}

func BenchmarkFullPageSuggestions(b *testing.B) {
	for _, tracks := range []int{3, 10000, 100000} {
		b.Run(fmt.Sprintf("tracks=%d", tracks), func(b *testing.B) {
			a := testApp(b)
			if tracks > 3 {
				_, err := a.S.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<?)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),printf('Artist %d',id/10),printf('Tune %d',id),printf('Album %d',id/5),'jazz','','2001',10,id,'2026-01-01','2026-01-01',0 FROM n`, tracks)
				if err != nil {
					b.Fatal(err)
				}
			}
			stats := measureAppSQL(b, a)
			if w := call(a, "GET", "/preferences/", 1, nil, false); w.Code != 200 {
				b.Fatal(w.Body.String())
			}
			stats.calls.Store(0)
			stats.suggestions.Store(0)
			stats.duration.Store(0)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if w := call(a, "GET", "/preferences/", 1, nil, false); w.Code != 200 {
					b.Fatal(w.Body.String())
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(stats.calls.Load())/float64(b.N), "sql/op")
			b.ReportMetric(float64(stats.suggestions.Load())/float64(b.N), "suggestions/op")
			b.ReportMetric(float64(stats.duration.Load())/float64(b.N)/1e6, "db-ms/op")
		})
	}
}
