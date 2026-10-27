package reinhardt

import (
	"fmt"
	"testing"
)

func populateHomeLibrary(testingContext testing.TB, app *App, tracks int) {
	testingContext.Helper()
	if tracks <= 3 {
		return
	}
	_, err := app.Store.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<?)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),printf('Artist %d',id/100),printf('Tune %d',id),printf('Album %d',id/10),'jazz','','2001',10,id%10,
datetime('2025-01-01',printf('+%d seconds',id)), '2026-01-01',0 FROM n`, tracks)
	if err != nil {
		testingContext.Fatal(err)
	}
}

func BenchmarkHomePage(benchmark *testing.B) {
	for _, tracks := range []int{3, 10000, 100000} {
		benchmark.Run(fmt.Sprintf("tracks=%d", tracks), func(benchmark *testing.B) {
			app := testApp(benchmark)
			populateHomeLibrary(benchmark, app, tracks)
			stats := measureAppSQL(benchmark, app)
			for _, fragment := range []bool{false, true} {
				benchmark.Run(fmt.Sprintf("unpoly=%v", fragment), func(benchmark *testing.B) {
					if response := call(app, "GET", "/", 1, nil, fragment); response.Code != 200 {
						benchmark.Fatal(response.Body.String())
					}
					stats.calls.Store(0)
					stats.writes.Store(0)
					stats.duration.Store(0)
					stats.homeLoads.Store(0)
					benchmark.ReportAllocs()
					benchmark.ResetTimer()
					for range benchmark.N {
						if response := call(app, "GET", "/", 1, nil, fragment); response.Code != 200 {
							benchmark.Fatal(response.Body.String())
						}
					}
					benchmark.StopTimer()
					benchmark.ReportMetric(float64(stats.calls.Load())/float64(benchmark.N), "sql/op")
					benchmark.ReportMetric(float64(stats.writes.Load())/float64(benchmark.N), "writes/op")
					benchmark.ReportMetric(float64(stats.homeLoads.Load())/float64(benchmark.N), "library-loads/op")
					benchmark.ReportMetric(float64(stats.duration.Load())/float64(benchmark.N)/1e6, "db-ms/op")
				})
			}
		})
	}
}

func BenchmarkHomeLoadMore(benchmark *testing.B) {
	for _, tracks := range []int{10000, 100000} {
		benchmark.Run(fmt.Sprintf("tracks=%d", tracks), func(benchmark *testing.B) {
			app := testApp(benchmark)
			populateHomeLibrary(benchmark, app, tracks)
			stats := measureAppSQL(benchmark, app)
			for _, offset := range []int{0, 25, 250, 750} {
				benchmark.Run(fmt.Sprintf("offset=%d", offset), func(benchmark *testing.B) {
					path := fmt.Sprintf("/?offset=%d", offset)
					if response := call(app, "GET", path, 1, nil, true); response.Code != 200 {
						benchmark.Fatal(response.Body.String())
					}
					stats.calls.Store(0)
					stats.duration.Store(0)
					stats.homeLoads.Store(0)
					benchmark.ReportAllocs()
					benchmark.ResetTimer()
					for range benchmark.N {
						if response := call(app, "GET", path, 1, nil, true); response.Code != 200 {
							benchmark.Fatal(response.Body.String())
						}
					}
					benchmark.StopTimer()
					benchmark.ReportMetric(float64(stats.calls.Load())/float64(benchmark.N), "sql/op")
					benchmark.ReportMetric(float64(stats.duration.Load())/float64(benchmark.N)/1e6, "db-ms/op")
					benchmark.ReportMetric(float64(stats.homeLoads.Load())/float64(benchmark.N), "library-loads/op")
				})
			}
		})
	}
}
