package reinhardt

import (
	"fmt"
	"testing"
)

func populateHomeLibrary(t testing.TB, a *App, tracks int) {
	t.Helper()
	if tracks <= 3 {
		return
	}
	_, err := a.S.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<?)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),printf('Artist %d',id/100),printf('Tune %d',id),printf('Album %d',id/10),'jazz','','2001',10,id%10,
datetime('2025-01-01',printf('+%d seconds',id)), '2026-01-01',0 FROM n`, tracks)
	if err != nil {
		t.Fatal(err)
	}
}

func BenchmarkHomePage(b *testing.B) {
	for _, tracks := range []int{3, 10000, 100000} {
		b.Run(fmt.Sprintf("tracks=%d", tracks), func(b *testing.B) {
			a := testApp(b)
			populateHomeLibrary(b, a, tracks)
			stats := measureAppSQL(b, a)
			for _, fragment := range []bool{false, true} {
				b.Run(fmt.Sprintf("unpoly=%v", fragment), func(b *testing.B) {
					if w := call(a, "GET", "/", 1, nil, fragment); w.Code != 200 {
						b.Fatal(w.Body.String())
					}
					stats.calls.Store(0)
					stats.writes.Store(0)
					stats.duration.Store(0)
					stats.homeLoads.Store(0)
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						if w := call(a, "GET", "/", 1, nil, fragment); w.Code != 200 {
							b.Fatal(w.Body.String())
						}
					}
					b.StopTimer()
					b.ReportMetric(float64(stats.calls.Load())/float64(b.N), "sql/op")
					b.ReportMetric(float64(stats.writes.Load())/float64(b.N), "writes/op")
					b.ReportMetric(float64(stats.homeLoads.Load())/float64(b.N), "library-loads/op")
					b.ReportMetric(float64(stats.duration.Load())/float64(b.N)/1e6, "db-ms/op")
				})
			}
		})
	}
}
