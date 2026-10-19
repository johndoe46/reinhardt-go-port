package reinhardt

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func BenchmarkSimilarArtists(benchmark *testing.B) {
	for _, tracks := range []int{3, 10000, 100000} {
		benchmark.Run(fmt.Sprintf("tracks=%d", tracks), func(benchmark *testing.B) {
			app := testApp(benchmark)
			if tracks > 3 {
				_, err := app.Store.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<?)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),printf('Artist %d',id/10),printf('Tune %d',id),'Album','jazz','','2001',10,id,'2026-01-01','2026-01-01',0 FROM n`, tracks)
				if err != nil {
					benchmark.Fatal(err)
				}
			}
			var calls atomic.Int64
			app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				// Controlled mock latency; this benchmark never contacts Last.fm.
				time.Sleep(10 * time.Millisecond)
				return remoteResponse(`{"similarartists":{"artist":[{"name":"Another"},{"name":"Artist 999"}]}}`), nil
			})
			stats := measureAppSQL(benchmark, app)
			if response := call(app, "GET", "/similar_artists/?q=Artist", 1, nil, true); response.Code != 200 {
				benchmark.Fatal(response.Body.String())
			}
			calls.Store(0)
			stats.calls.Store(0)
			stats.artistLoads.Store(0)
			stats.duration.Store(0)
			benchmark.ReportAllocs()
			benchmark.ResetTimer()
			for range benchmark.N {
				if response := call(app, "GET", "/similar_artists/?q=Artist", 1, nil, true); response.Code != 200 {
					benchmark.Fatal(response.Body.String())
				}
			}
			benchmark.StopTimer()
			benchmark.ReportMetric(float64(calls.Load())/float64(benchmark.N), "lastfm/op")
			benchmark.ReportMetric(float64(stats.artistLoads.Load())/float64(benchmark.N), "artist-loads/op")
			benchmark.ReportMetric(float64(stats.calls.Load())/float64(benchmark.N), "sql/op")
			benchmark.ReportMetric(float64(stats.duration.Load())/float64(benchmark.N)/1e6, "db-ms/op")
		})
	}
}
