package reinhardt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
)

// Instrument only test-owned connections; production requests never pay for SQL
// tracing. Include row scanning in the measured query duration.
type measuredSQL struct {
	calls, writes, suggestions, autofeedLoads, artistLoads, homeLoads, duration atomic.Int64
}

func BenchmarkNextSong(benchmark *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		benchmark.Run(fmt.Sprintf("queue=%d", count), func(benchmark *testing.B) {
			app := testApp(benchmark)
			if count > 3 {
				_, err := app.Store.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<?)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),'Artist',printf('Tune %d',id),'Album','jazz','','2001',10,id,'2026-01-01','2026-01-01',0 FROM n`, count)
				if err != nil {
					benchmark.Fatal(err)
				}
			}
			tag, err := app.Store.AddTag(1, "queue")
			if err != nil {
				benchmark.Fatal(err)
			}
			if _, err = app.Store.DB.Exec(`INSERT INTO reinhardt_musicfiletag(tag_id,music_file_id,rank,created_at,modified_at)
SELECT ?,id,id,'2026-01-01','2026-01-01' FROM reinhardt_musicfile WHERE id<=?`, tag, count); err != nil {
				benchmark.Fatal(err)
			}
			stats := measureAppSQL(benchmark, app)
			for _, fragment := range []bool{false, true} {
				benchmark.Run(fmt.Sprintf("unpoly=%v", fragment), func(benchmark *testing.B) {
					if response := call(app, "POST", "/next_song/", 1, url.Values{"list_item": {"1"}}, fragment); response.Code != 200 {
						benchmark.Fatal(response.Body.String())
					}
					stats.calls.Store(0)
					stats.writes.Store(0)
					stats.suggestions.Store(0)
					stats.duration.Store(0)
					benchmark.ReportAllocs()
					benchmark.ResetTimer()
					for index := 0; index < benchmark.N; index++ {
						if response := call(app, "POST", "/next_song/", 1, url.Values{"list_item": {"1"}}, fragment); response.Code != 200 {
							benchmark.Fatal(response.Body.String())
						}
					}
					benchmark.StopTimer()
					benchmark.ReportMetric(float64(stats.calls.Load())/float64(benchmark.N), "sql/op")
					benchmark.ReportMetric(float64(stats.writes.Load())/float64(benchmark.N), "writes/op")
					benchmark.ReportMetric(float64(stats.suggestions.Load())/float64(benchmark.N), "suggestions/op")
					benchmark.ReportMetric(float64(stats.duration.Load())/float64(benchmark.N)/1e6, "db-ms/op")
				})
			}
		})
	}
}

type measuredDriver struct {
	base  sqlite.Driver
	stats *measuredSQL
}

func (driver *measuredDriver) Open(name string) (driver.Conn, error) {
	connection, err := driver.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &measuredConn{Conn: connection, stats: driver.stats}, nil
}

type measuredConn struct {
	driver.Conn
	stats *measuredSQL
}

func (connection *measuredConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	connection.stats.calls.Add(1)
	connection.stats.writes.Add(1)
	start := time.Now()
	defer func() { connection.stats.duration.Add(int64(time.Since(start))) }()
	return connection.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (connection *measuredConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	connection.stats.calls.Add(1)
	if strings.Contains(query, "SELECT word FROM") {
		connection.stats.suggestions.Add(1)
	}
	if strings.HasPrefix(query, "SELECT m.id FROM reinhardt_musicfile m WHERE ") {
		connection.stats.autofeedLoads.Add(1)
	}
	if query == "SELECT DISTINCT artist FROM reinhardt_musicfile" {
		connection.stats.artistLoads.Add(1)
	}
	if query == tracksSQL+latestAdditionsClause {
		connection.stats.homeLoads.Add(1)
	}
	start := time.Now()
	rows, err := connection.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		connection.stats.duration.Add(int64(time.Since(start)))
		return nil, err
	}
	return &measuredRows{Rows: rows, stats: connection.stats, start: start}, nil
}

type measuredRows struct {
	driver.Rows
	stats  *measuredSQL
	start  time.Time
	closed bool
}

func (rows *measuredRows) Close() error {
	err := rows.Rows.Close()
	if !rows.closed {
		rows.stats.duration.Add(int64(time.Since(rows.start)))
		rows.closed = true
	}
	return err
}

var measuredDriverID atomic.Int64

func measureAppSQL(testingContext testing.TB, app *App) *measuredSQL {
	testingContext.Helper()
	if err := app.Store.DB.Close(); err != nil {
		testingContext.Fatal(err)
	}
	stats := &measuredSQL{}
	name := fmt.Sprintf("sqlite-measured-%d", measuredDriverID.Add(1))
	sql.Register(name, &measuredDriver{stats: stats})
	db, err := sql.Open(name, app.Store.Config["DATABASE_PATH"]+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		testingContext.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	app.Store.DB = db
	return stats
}

func dropdownRequest(app *App, fragment, gzip bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest("GET", "http://localhost/music_file_dropdown/?q=1&tag=&playing=&context=home", nil)
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	request.AddCookie(&http.Cookie{Name: "csrftoken", Value: strings.Repeat("a", 64)})
	if fragment {
		request.Header.Set("X-Up-Target", "main")
	}
	if gzip {
		request.Header.Set("Accept-Encoding", "gzip")
	}
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	return response
}

func TestDropdownAvoidsLayoutQueriesAndWrites(test *testing.T) {
	app := testApp(test)
	before, err := app.Store.One("SELECT seq FROM sqlite_sequence WHERE name='reinhardt_preferences'")
	if err != nil {
		test.Fatal(err)
	}
	stats := measureAppSQL(test, app)
	for _, fragment := range []bool{false, true} {
		stats.calls.Store(0)
		stats.writes.Store(0)
		stats.suggestions.Store(0)
		response := dropdownRequest(app, fragment, false)
		requireCode(test, response, 200)
		if stats.calls.Load() != 3 || stats.writes.Load() != 0 || stats.suggestions.Load() != 0 {
			test.Fatalf("dropdown fetched unused layout data: sql=%d writes=%d suggestions=%d", stats.calls.Load(), stats.writes.Load(), stats.suggestions.Load())
		}
		if !strings.Contains(response.Body.String(), `class="dropdown-item"`) || !strings.Contains(response.Body.String(), `id="favorite_action_1"`) {
			test.Fatal("dropdown controls missing")
		}
	}
	after, err := app.Store.One("SELECT seq FROM sqlite_sequence WHERE name='reinhardt_preferences'")
	if err != nil || before.N("seq") != after.N("seq") {
		test.Fatal("dropdown advanced the preferences AUTOINCREMENT sequence", err)
	}
}

func TestNextSongAvoidsLayoutDataAndQueueGrowth(test *testing.T) {
	app := testApp(test)
	mustTag(test, app, 2, "queue", []int64{3})
	_, err := app.Store.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<1000)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),'Artist',printf('Tune %d',id),'Album','jazz','','2001',10,id,'2026-01-01','2026-01-01',0 FROM n`)
	if err != nil {
		test.Fatal(err)
	}
	tag, err := app.Store.AddTag(1, "queue")
	if err != nil {
		test.Fatal(err)
	}
	if _, err = app.Store.DB.Exec(`INSERT INTO reinhardt_musicfiletag(tag_id,music_file_id,rank,created_at,modified_at)
SELECT ?,id,id,'2026-01-01','2026-01-01' FROM reinhardt_musicfile`, tag); err != nil {
		test.Fatal(err)
	}
	stats := measureAppSQL(test, app)
	for _, fragment := range []bool{false, true} {
		stats.calls.Store(0)
		stats.writes.Store(0)
		stats.suggestions.Store(0)
		response := call(app, "POST", "/next_song/", 1, url.Values{"list_item": {"1"}}, fragment)
		requireCode(test, response, 200)
		if stats.calls.Load() > 6 || stats.writes.Load() != 1 || stats.suggestions.Load() != 0 {
			test.Fatalf("playback did unnecessary work: sql=%d writes=%d suggestions=%d", stats.calls.Load(), stats.writes.Load(), stats.suggestions.Load())
		}
		for _, want := range []string{`id="now_playing"`, `data-url="/stream/1/"`, `data-position="1"`, `data-autoplay="true"`} {
			if !strings.Contains(response.Body.String(), want) {
				test.Fatal("missing playback state", want)
			}
		}
		if strings.Contains(response.Body.String(), "<nav") || strings.Contains(response.Body.String(), `id="player"`) {
			test.Fatal("playback response included a page layout")
		}
	}
}

func TestPreferencesLookupAvoidsWriteForExistingUser(test *testing.T) {
	app := testApp(test)
	stats := measureAppSQL(test, app)
	prefs, err := app.Store.Preferences(1)
	if err != nil || prefs.N("owner_id") != 1 {
		test.Fatal("preferences lookup failed", err)
	}
	if stats.calls.Load() != 1 || stats.writes.Load() != 0 {
		test.Fatal("existing preferences lookup issued a write")
	}
	if _, err = app.Store.DB.Exec("DELETE FROM reinhardt_preferences WHERE owner_id=2"); err != nil {
		test.Fatal(err)
	}
	stats.writes.Store(0)
	prefs, err = app.Store.Preferences(2)
	if err != nil || prefs.N("owner_id") != 2 || !prefs.B("autofeed_queue") || !prefs.B("show_oscilloscope") {
		test.Fatal("missing preferences were not initialized", err)
	}
	if stats.writes.Load() != 1 {
		test.Fatal("missing preferences were not inserted once")
	}
}

func BenchmarkMusicFileDropdown(benchmark *testing.B) {
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
			for _, mode := range []struct {
				name           string
				fragment, gzip bool
			}{{"fetch", false, false}, {"fetch-gzip", false, true}, {"unpoly-header", true, false}} {
				benchmark.Run(mode.name, func(benchmark *testing.B) {
					if response := dropdownRequest(app, mode.fragment, mode.gzip); response.Code != 200 {
						benchmark.Fatal(response.Body.String())
					}
					stats.calls.Store(0)
					stats.writes.Store(0)
					stats.suggestions.Store(0)
					stats.duration.Store(0)
					benchmark.ReportAllocs()
					benchmark.ResetTimer()
					for index := 0; index < benchmark.N; index++ {
						if response := dropdownRequest(app, mode.fragment, mode.gzip); response.Code != 200 {
							benchmark.Fatal(response.Body.String())
						}
					}
					benchmark.StopTimer()
					benchmark.ReportMetric(float64(stats.calls.Load())/float64(benchmark.N), "sql/op")
					benchmark.ReportMetric(float64(stats.writes.Load())/float64(benchmark.N), "writes/op")
					benchmark.ReportMetric(float64(stats.suggestions.Load())/float64(benchmark.N), "suggestions/op")
					benchmark.ReportMetric(float64(stats.duration.Load())/float64(benchmark.N)/1e6, "db-ms/op")
				})
			}
		})
	}
}
