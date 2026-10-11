package reinhardt

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func populateQueue(t testing.TB, a *App, count int) {
	t.Helper()
	if count > 3 {
		_, err := a.S.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<?)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),'Artist',printf('Tune %d',id),'Album','jazz','','2001',10,id,'2026-01-01','2026-01-01',0 FROM n`, count)
		if err != nil {
			t.Fatal(err)
		}
	}
	tag, err := a.S.AddTag(1, "queue")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.S.DB.Exec(`INSERT INTO reinhardt_musicfiletag(tag_id,music_file_id,rank,created_at,modified_at)
SELECT ?,id,id,'2026-01-01','2026-01-02' FROM reinhardt_musicfile WHERE id<=?`, tag, count); err != nil {
		t.Fatal(err)
	}
}

func TestQueueLoadsTracksInOneQuery(t *testing.T) {
	a := testApp(t)
	mustTag(t, a, 2, "queue", []int64{3})
	mustTag(t, a, 1, "favorites", []int64{1})
	populateQueue(t, a, 3)
	if _, err := a.S.DB.Exec("UPDATE reinhardt_musicfile SET missing=1 WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.S.DB.Exec("UPDATE reinhardt_musicfiletag SET rank=1 WHERE music_file_id=2 AND tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=1 AND label='queue')"); err != nil {
		t.Fatal(err)
	}
	stats := measureAppSQL(t, a)
	items, err := a.S.Queue(1)
	if err != nil || len(items) != 3 {
		t.Fatal("wrong queue contents", items, err)
	}
	if stats.calls.Load() != 1 || stats.writes.Load() != 0 {
		t.Fatalf("queue made %d SQL calls and %d writes", stats.calls.Load(), stats.writes.Load())
	}
	for i, item := range items {
		if item.N("music_file_id") != int64(i+1) || item.N("pk") != item.N("id") {
			t.Fatal("queue ordering or association ID changed", item)
		}
		association, err := a.S.One("SELECT * FROM reinhardt_musicfiletag WHERE id=?", item["id"])
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range association {
			if !reflect.DeepEqual(item[key], value) {
				t.Fatalf("association field %s changed: got %v, want %v", key, item[key], value)
			}
		}
		track, err := a.S.Track(item["music_file_id"])
		if err != nil || !reflect.DeepEqual(item["music_file"], track) {
			t.Fatal("joined track differs from individual lookup", item["music_file"], track, err)
		}
	}
	stats.calls.Store(0)
	empty, err := a.S.Queue(99)
	if err != nil || len(empty) != 0 || stats.calls.Load() != 1 {
		t.Fatal("empty queue lookup failed", empty, err)
	}
}

func TestFullPagesAvoidQueueQueryGrowth(t *testing.T) {
	for _, count := range []int{1, 1000} {
		t.Run(fmt.Sprintf("queue=%d", count), func(t *testing.T) {
			a := testApp(t)
			populateQueue(t, a, count)
			item, err := a.S.queueItem(1, "a.music_file_id=?", count)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = a.S.DB.Exec("UPDATE reinhardt_preferences SET current_position=? WHERE owner_id=1", item["id"]); err != nil {
				t.Fatal(err)
			}
			if _, err = a.suggestedData(); err != nil {
				t.Fatal(err)
			}
			stats := measureAppSQL(t, a)
			for _, page := range []struct {
				path     string
				fragment bool
				maxCalls int64
			}{{"/artists/", false, 6}, {"/queue/", false, 7}, {"/queue/", true, 6}} {
				stats.calls.Store(0)
				stats.writes.Store(0)
				w := call(a, "GET", page.path, 1, nil, page.fragment)
				requireCode(t, w, 200)
				if calls := stats.calls.Load(); calls > page.maxCalls {
					t.Fatalf("%s fragment=%v: %d SQL calls, want at most %d", page.path, page.fragment, calls, page.maxCalls)
				}
				if writes := stats.writes.Load(); writes != 0 {
					t.Fatalf("%s fragment=%v: %d unnecessary writes", page.path, page.fragment, writes)
				}
				if !page.fragment {
					for _, want := range []string{`id="now_playing"`, fmt.Sprintf(`data-position="%d"`, count), fmt.Sprintf(`data-url="/stream/%d/"`, count)} {
						if !strings.Contains(w.Body.String(), want) {
							t.Fatal("missing footer playback state", want)
						}
					}
				}
			}
		})
	}
}

func TestFullPageFooterQueueOwnershipAndStalePosition(t *testing.T) {
	for _, scenario := range []string{"unset", "deleted", "foreign", "other-tag", "missing-track"} {
		t.Run(scenario, func(t *testing.T) {
			a := testApp(t)
			mustTag(t, a, 1, "queue", []int64{1})
			mustTag(t, a, 2, "queue", []int64{2})
			mustTag(t, a, 1, "favorites", []int64{3})
			var position int64
			wantTrack := false
			switch scenario {
			case "deleted":
				position = 999
			case "foreign", "other-tag", "missing-track":
				trackID := map[string]int{"foreign": 2, "other-tag": 3, "missing-track": 1}[scenario]
				item, err := a.S.One("SELECT id FROM reinhardt_musicfiletag WHERE music_file_id=?", trackID)
				if err != nil {
					t.Fatal(err)
				}
				position = item.N("id")
				if scenario == "missing-track" {
					wantTrack = true
					if _, err = a.S.DB.Exec("UPDATE reinhardt_musicfile SET missing=1 WHERE id=1"); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := a.S.DB.Exec("UPDATE reinhardt_preferences SET current_position=? WHERE owner_id=1", position); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/artists/", "/queue/"} {
				w := call(a, "GET", path, 1, nil, false)
				requireCode(t, w, 200)
				if strings.Contains(w.Body.String(), `data-url="/stream/1/"`) != wantTrack {
					t.Fatal("wrong footer track", scenario, path)
				}
				if !wantTrack && !strings.Contains(w.Body.String(), `id="now_playing" data-position="0"`) {
					t.Fatal("stale or foreign position was not reset in footer", scenario, path)
				}
				if strings.Contains(w.Body.String(), `data-url="/stream/2/"`) || strings.Contains(w.Body.String(), `data-url="/stream/3/"`) {
					t.Fatal("footer exposed another user's queue or a non-queue tag")
				}
			}
			prefs, err := a.S.Preferences(1)
			if err != nil || prefs.N("current_position") != position {
				t.Fatal("rendering changed the saved playback position", prefs, err)
			}
		})
	}
}

func BenchmarkFullPageQueue(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("queue=%d", count), func(b *testing.B) {
			a := testApp(b)
			populateQueue(b, a, count)
			item, err := a.S.queueItem(1, "a.music_file_id=?", count)
			if err != nil {
				b.Fatal(err)
			}
			if _, err = a.S.DB.Exec("UPDATE reinhardt_preferences SET current_position=? WHERE owner_id=1", item["id"]); err != nil {
				b.Fatal(err)
			}
			stats := measureAppSQL(b, a)
			for _, path := range []string{"/artists/", "/queue/"} {
				b.Run(strings.Trim(path, "/"), func(b *testing.B) {
					if w := call(a, "GET", path, 1, nil, false); w.Code != 200 {
						b.Fatal(w.Body.String())
					}
					stats.calls.Store(0)
					stats.writes.Store(0)
					stats.duration.Store(0)
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if w := call(a, "GET", path, 1, nil, false); w.Code != 200 {
							b.Fatal(w.Body.String())
						}
					}
					b.StopTimer()
					b.ReportMetric(float64(stats.calls.Load())/float64(b.N), "sql/op")
					b.ReportMetric(float64(stats.writes.Load())/float64(b.N), "writes/op")
					b.ReportMetric(float64(stats.duration.Load())/float64(b.N)/1e6, "db-ms/op")
				})
			}
		})
	}
}
