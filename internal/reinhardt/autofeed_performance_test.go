package reinhardt

import (
	"fmt"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestNextSongAutofeedDoesNotRescanLibrary(test *testing.T) {
	app := testApp(test)
	populateQueue(test, app, 1000)
	last, err := app.Store.queueItem(1, "a.music_file_id=?", 1000)
	if err != nil {
		test.Fatal(err)
	}
	if _, err = app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=?,autofeed_queue=1,autofeed_similar=0 WHERE owner_id=1", last["id"]); err != nil {
		test.Fatal(err)
	}
	stats := measureAppSQL(test, app)
	response := call(app, "POST", "/next_song/", 1, url.Values{}, true)
	requireCode(test, response, 200)
	if stats.autofeedLoads.Load() != 1 {
		test.Fatal("cold autofeed did not load the candidate pool once")
	}
	for _, request := range []struct {
		path     string
		fragment bool
	}{{"/next_song/", false}, {"/next_song/", true}, {"/api/dj/next_song/", false}} {
		stats.calls.Store(0)
		stats.writes.Store(0)
		stats.autofeedLoads.Store(0)
		stats.suggestions.Store(0)
		response = call(app, "POST", request.path, 1, url.Values{}, request.fragment)
		requireCode(test, response, 200)
		if stats.calls.Load() > 11 || stats.autofeedLoads.Load() != 0 || stats.suggestions.Load() != 0 {
			test.Fatalf("warm autofeed did unnecessary work: calls=%d candidates=%d suggestions=%d", stats.calls.Load(), stats.autofeedLoads.Load(), stats.suggestions.Load())
		}
		if writes := stats.writes.Load(); writes != 2 {
			test.Fatalf("warm autofeed made %d writes, want queue membership and playback position only", writes)
		}
	}
}

func BenchmarkNextSongDisabledSimilarity(benchmark *testing.B) {
	for _, tracks := range []int{3, 10000, 100000} {
		benchmark.Run(fmt.Sprintf("tracks=%d", tracks), func(benchmark *testing.B) {
			app := testApp(benchmark)
			if tracks > 3 {
				_, err := app.Store.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<?)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),'Artist',printf('Tune %d',id),'Album','jazz','','2001',10,id,'2026-01-01','2026-01-01',0 FROM n`, tracks)
				if err != nil {
					benchmark.Fatal(err)
				}
			}
			if _, err := app.Store.AddTracks(1, "queue", []int64{1}); err != nil {
				benchmark.Fatal(err)
			}
			if _, err := app.Store.DB.Exec("UPDATE reinhardt_preferences SET autofeed_queue=1,autofeed_similar=0 WHERE owner_id=1"); err != nil {
				benchmark.Fatal(err)
			}
			var externalCalls atomic.Int64
			app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
				externalCalls.Add(1)
				return nil, fmt.Errorf("unexpected external request: %s", request.URL)
			})
			stats := measureAppSQL(benchmark, app)
			for _, mode := range []string{"queued", "random-autofeed"} {
				benchmark.Run(mode, func(benchmark *testing.B) {
					form := url.Values{}
					if mode == "queued" {
						form.Set("list_item", "1")
					}
					if response := call(app, "POST", "/next_song/", 1, form, true); response.Code != 200 {
						benchmark.Fatal(response.Body.String())
					}
					stats.calls.Store(0)
					stats.writes.Store(0)
					stats.duration.Store(0)
					stats.autofeedLoads.Store(0)
					benchmark.ReportAllocs()
					benchmark.ResetTimer()
					for range benchmark.N {
						if response := call(app, "POST", "/next_song/", 1, form, true); response.Code != 200 {
							benchmark.Fatal(response.Body.String())
						}
					}
					benchmark.StopTimer()
					if externalCalls.Load() != 0 {
						benchmark.Fatal("disabled similarity made external HTTP requests")
					}
					benchmark.ReportMetric(float64(stats.calls.Load())/float64(benchmark.N), "sql/op")
					benchmark.ReportMetric(float64(stats.writes.Load())/float64(benchmark.N), "writes/op")
					benchmark.ReportMetric(float64(stats.duration.Load())/float64(benchmark.N)/1e6, "db-ms/op")
					benchmark.ReportMetric(0, "external/op")
					benchmark.ReportMetric(float64(stats.autofeedLoads.Load())/float64(benchmark.N), "candidate-loads/op")
				})
			}
		})
	}
}
