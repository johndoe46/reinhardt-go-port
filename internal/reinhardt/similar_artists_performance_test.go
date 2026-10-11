package reinhardt

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func BenchmarkSimilarArtists(b *testing.B) {
	for _, tracks := range []int{3, 10000, 100000} {
		b.Run(fmt.Sprintf("tracks=%d", tracks), func(b *testing.B) {
			a := testApp(b)
			if tracks > 3 {
				_, err := a.S.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<?)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),printf('Artist %d',id/10),printf('Tune %d',id),'Album','jazz','','2001',10,id,'2026-01-01','2026-01-01',0 FROM n`, tracks)
				if err != nil {
					b.Fatal(err)
				}
			}
			var calls atomic.Int64
			a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				// Controlled mock latency; this benchmark never contacts Last.fm.
				time.Sleep(10 * time.Millisecond)
				return remoteResponse(`{"similarartists":{"artist":[{"name":"Another"},{"name":"Artist 999"}]}}`), nil
			})
			stats := measureAppSQL(b, a)
			if w := call(a, "GET", "/similar_artists/?q=Artist", 1, nil, true); w.Code != 200 {
				b.Fatal(w.Body.String())
			}
			calls.Store(0)
			stats.calls.Store(0)
			stats.artistLoads.Store(0)
			stats.duration.Store(0)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if w := call(a, "GET", "/similar_artists/?q=Artist", 1, nil, true); w.Code != 200 {
					b.Fatal(w.Body.String())
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(calls.Load())/float64(b.N), "lastfm/op")
			b.ReportMetric(float64(stats.artistLoads.Load())/float64(b.N), "artist-loads/op")
			b.ReportMetric(float64(stats.calls.Load())/float64(b.N), "sql/op")
			b.ReportMetric(float64(stats.duration.Load())/float64(b.N)/1e6, "db-ms/op")
		})
	}
}
