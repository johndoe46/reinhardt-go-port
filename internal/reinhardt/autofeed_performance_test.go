package reinhardt

import (
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestNextSongAutofeedDoesNotRescanLibrary(t *testing.T) {
	a := testApp(t)
	populateQueue(t, a, 1000)
	last, err := a.S.queueItem(1, "a.music_file_id=?", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.S.DB.Exec("UPDATE reinhardt_preferences SET current_position=?,autofeed_queue=1,autofeed_similar=0 WHERE owner_id=1", last["id"]); err != nil {
		t.Fatal(err)
	}
	stats := measureAppSQL(t, a)
	w := call(a, "POST", "/next_song/", 1, url.Values{}, true)
	requireCode(t, w, 200)
	if stats.autofeedLoads.Load() != 1 {
		t.Fatal("cold autofeed did not load the candidate pool once")
	}
	for _, request := range []struct {
		path     string
		fragment bool
	}{{"/next_song/", false}, {"/next_song/", true}, {"/api/dj/next_song/", false}} {
		stats.calls.Store(0)
		stats.writes.Store(0)
		stats.autofeedLoads.Store(0)
		stats.suggestions.Store(0)
		w = call(a, "POST", request.path, 1, url.Values{}, request.fragment)
		requireCode(t, w, 200)
		if stats.calls.Load() > 11 || stats.autofeedLoads.Load() != 0 || stats.suggestions.Load() != 0 {
			t.Fatalf("warm autofeed did unnecessary work: calls=%d candidates=%d suggestions=%d", stats.calls.Load(), stats.autofeedLoads.Load(), stats.suggestions.Load())
		}
		if writes := stats.writes.Load(); writes != 2 {
			t.Fatalf("warm autofeed made %d writes, want queue membership and playback position only", writes)
		}
	}
}

func BenchmarkNextSongDisabledSimilarity(b *testing.B) {
	for _, tracks := range []int{3, 10000, 100000} {
		b.Run(fmt.Sprintf("tracks=%d", tracks), func(b *testing.B) {
			a := testApp(b)
			if tracks > 3 {
				_, err := a.S.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<?)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),'Artist',printf('Tune %d',id),'Album','jazz','','2001',10,id,'2026-01-01','2026-01-01',0 FROM n`, tracks)
				if err != nil {
					b.Fatal(err)
				}
			}
			if _, err := a.S.AddTracks(1, "queue", []int64{1}); err != nil {
				b.Fatal(err)
			}
			if _, err := a.S.DB.Exec("UPDATE reinhardt_preferences SET autofeed_queue=1,autofeed_similar=0 WHERE owner_id=1"); err != nil {
				b.Fatal(err)
			}
			var externalCalls atomic.Int64
			a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				externalCalls.Add(1)
				return nil, fmt.Errorf("unexpected external request: %s", r.URL)
			})
			stats := measureAppSQL(b, a)
			for _, mode := range []string{"queued", "random-autofeed"} {
				b.Run(mode, func(b *testing.B) {
					form := url.Values{}
					if mode == "queued" {
						form.Set("list_item", "1")
					}
					if w := call(a, "POST", "/next_song/", 1, form, true); w.Code != 200 {
						b.Fatal(w.Body.String())
					}
					stats.calls.Store(0)
					stats.writes.Store(0)
					stats.duration.Store(0)
					stats.autofeedLoads.Store(0)
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						if w := call(a, "POST", "/next_song/", 1, form, true); w.Code != 200 {
							b.Fatal(w.Body.String())
						}
					}
					b.StopTimer()
					if externalCalls.Load() != 0 {
						b.Fatal("disabled similarity made external HTTP requests")
					}
					b.ReportMetric(float64(stats.calls.Load())/float64(b.N), "sql/op")
					b.ReportMetric(float64(stats.writes.Load())/float64(b.N), "writes/op")
					b.ReportMetric(float64(stats.duration.Load())/float64(b.N)/1e6, "db-ms/op")
					b.ReportMetric(0, "external/op")
					b.ReportMetric(float64(stats.autofeedLoads.Load())/float64(b.N), "candidate-loads/op")
				})
			}
		})
	}
}
