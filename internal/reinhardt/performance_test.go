package reinhardt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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

func BenchmarkNextSong(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("queue=%d", count), func(b *testing.B) {
			a := testApp(b)
			if count > 3 {
				_, err := a.S.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<?)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),'Artist',printf('Tune %d',id),'Album','jazz','','2001',10,id,'2026-01-01','2026-01-01',0 FROM n`, count)
				if err != nil {
					b.Fatal(err)
				}
			}
			tag, err := a.S.AddTag(1, "queue")
			if err != nil {
				b.Fatal(err)
			}
			if _, err = a.S.DB.Exec(`INSERT INTO reinhardt_musicfiletag(tag_id,music_file_id,rank,created_at,modified_at)
SELECT ?,id,id,'2026-01-01','2026-01-01' FROM reinhardt_musicfile WHERE id<=?`, tag, count); err != nil {
				b.Fatal(err)
			}
			stats := measureAppSQL(b, a)
			for _, fragment := range []bool{false, true} {
				b.Run(fmt.Sprintf("unpoly=%v", fragment), func(b *testing.B) {
					if w := call(a, "POST", "/next_song/", 1, url.Values{"list_item": {"1"}}, fragment); w.Code != 200 {
						b.Fatal(w.Body.String())
					}
					stats.calls.Store(0)
					stats.writes.Store(0)
					stats.suggestions.Store(0)
					stats.duration.Store(0)
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if w := call(a, "POST", "/next_song/", 1, url.Values{"list_item": {"1"}}, fragment); w.Code != 200 {
							b.Fatal(w.Body.String())
						}
					}
					b.StopTimer()
					b.ReportMetric(float64(stats.calls.Load())/float64(b.N), "sql/op")
					b.ReportMetric(float64(stats.writes.Load())/float64(b.N), "writes/op")
					b.ReportMetric(float64(stats.suggestions.Load())/float64(b.N), "suggestions/op")
					b.ReportMetric(float64(stats.duration.Load())/float64(b.N)/1e6, "db-ms/op")
				})
			}
		})
	}
}

type measuredDriver struct {
	base  sqlite.Driver
	stats *measuredSQL
}

func (d *measuredDriver) Open(name string) (driver.Conn, error) {
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &measuredConn{Conn: c, stats: d.stats}, nil
}

type measuredConn struct {
	driver.Conn
	stats *measuredSQL
}

func (c *measuredConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.stats.calls.Add(1)
	c.stats.writes.Add(1)
	start := time.Now()
	defer func() { c.stats.duration.Add(int64(time.Since(start))) }()
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *measuredConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.stats.calls.Add(1)
	if strings.Contains(query, "SELECT word FROM") {
		c.stats.suggestions.Add(1)
	}
	if strings.HasPrefix(query, "SELECT m.id FROM reinhardt_musicfile m WHERE ") {
		c.stats.autofeedLoads.Add(1)
	}
	if query == "SELECT DISTINCT artist FROM reinhardt_musicfile" {
		c.stats.artistLoads.Add(1)
	}
	if query == tracksSQL+latestAdditionsClause {
		c.stats.homeLoads.Add(1)
	}
	start := time.Now()
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		c.stats.duration.Add(int64(time.Since(start)))
		return nil, err
	}
	return &measuredRows{Rows: rows, stats: c.stats, start: start}, nil
}

type measuredRows struct {
	driver.Rows
	stats  *measuredSQL
	start  time.Time
	closed bool
}

func (r *measuredRows) Close() error {
	err := r.Rows.Close()
	if !r.closed {
		r.stats.duration.Add(int64(time.Since(r.start)))
		r.closed = true
	}
	return err
}

var measuredDriverID atomic.Int64

func measureAppSQL(t testing.TB, a *App) *measuredSQL {
	t.Helper()
	if err := a.S.DB.Close(); err != nil {
		t.Fatal(err)
	}
	stats := &measuredSQL{}
	name := fmt.Sprintf("sqlite-measured-%d", measuredDriverID.Add(1))
	sql.Register(name, &measuredDriver{stats: stats})
	db, err := sql.Open(name, a.S.C["DATABASE_PATH"]+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	a.S.DB = db
	return stats
}

func dropdownRequest(a *App, fragment, gzip bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "http://localhost/music_file_dropdown/?q=1&tag=&playing=&context=home", nil)
	r.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	r.AddCookie(&http.Cookie{Name: "csrftoken", Value: strings.Repeat("a", 64)})
	if fragment {
		r.Header.Set("X-Up-Target", "main")
	}
	if gzip {
		r.Header.Set("Accept-Encoding", "gzip")
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}

func TestDropdownAvoidsLayoutQueriesAndWrites(t *testing.T) {
	a := testApp(t)
	before, err := a.S.One("SELECT seq FROM sqlite_sequence WHERE name='reinhardt_preferences'")
	if err != nil {
		t.Fatal(err)
	}
	stats := measureAppSQL(t, a)
	for _, fragment := range []bool{false, true} {
		stats.calls.Store(0)
		stats.writes.Store(0)
		stats.suggestions.Store(0)
		w := dropdownRequest(a, fragment, false)
		requireCode(t, w, 200)
		if stats.calls.Load() != 3 || stats.writes.Load() != 0 || stats.suggestions.Load() != 0 {
			t.Fatalf("dropdown fetched unused layout data: sql=%d writes=%d suggestions=%d", stats.calls.Load(), stats.writes.Load(), stats.suggestions.Load())
		}
		if !strings.Contains(w.Body.String(), `class="dropdown-item"`) || !strings.Contains(w.Body.String(), `id="favorite_action_1"`) {
			t.Fatal("dropdown controls missing")
		}
	}
	after, err := a.S.One("SELECT seq FROM sqlite_sequence WHERE name='reinhardt_preferences'")
	if err != nil || before.N("seq") != after.N("seq") {
		t.Fatal("dropdown advanced the preferences AUTOINCREMENT sequence", err)
	}
}

func TestNextSongAvoidsLayoutDataAndQueueGrowth(t *testing.T) {
	a := testApp(t)
	mustTag(t, a, 2, "queue", []int64{3})
	_, err := a.S.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<1000)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),'Artist',printf('Tune %d',id),'Album','jazz','','2001',10,id,'2026-01-01','2026-01-01',0 FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := a.S.AddTag(1, "queue")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.S.DB.Exec(`INSERT INTO reinhardt_musicfiletag(tag_id,music_file_id,rank,created_at,modified_at)
SELECT ?,id,id,'2026-01-01','2026-01-01' FROM reinhardt_musicfile`, tag); err != nil {
		t.Fatal(err)
	}
	stats := measureAppSQL(t, a)
	for _, fragment := range []bool{false, true} {
		stats.calls.Store(0)
		stats.writes.Store(0)
		stats.suggestions.Store(0)
		w := call(a, "POST", "/next_song/", 1, url.Values{"list_item": {"1"}}, fragment)
		requireCode(t, w, 200)
		if stats.calls.Load() > 6 || stats.writes.Load() != 1 || stats.suggestions.Load() != 0 {
			t.Fatalf("playback did unnecessary work: sql=%d writes=%d suggestions=%d", stats.calls.Load(), stats.writes.Load(), stats.suggestions.Load())
		}
		for _, want := range []string{`id="now_playing"`, `data-url="/stream/1/"`, `data-position="1"`, `data-autoplay="true"`} {
			if !strings.Contains(w.Body.String(), want) {
				t.Fatal("missing playback state", want)
			}
		}
		if strings.Contains(w.Body.String(), "<nav") || strings.Contains(w.Body.String(), `id="player"`) {
			t.Fatal("playback response included a page layout")
		}
	}
}

func TestPreferencesLookupAvoidsWriteForExistingUser(t *testing.T) {
	a := testApp(t)
	stats := measureAppSQL(t, a)
	prefs, err := a.S.Preferences(1)
	if err != nil || prefs.N("owner_id") != 1 {
		t.Fatal("preferences lookup failed", err)
	}
	if stats.calls.Load() != 1 || stats.writes.Load() != 0 {
		t.Fatal("existing preferences lookup issued a write")
	}
	if _, err = a.S.DB.Exec("DELETE FROM reinhardt_preferences WHERE owner_id=2"); err != nil {
		t.Fatal(err)
	}
	stats.writes.Store(0)
	prefs, err = a.S.Preferences(2)
	if err != nil || prefs.N("owner_id") != 2 || !prefs.B("autofeed_queue") || !prefs.B("show_oscilloscope") {
		t.Fatal("missing preferences were not initialized", err)
	}
	if stats.writes.Load() != 1 {
		t.Fatal("missing preferences were not inserted once")
	}
}

func BenchmarkMusicFileDropdown(b *testing.B) {
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
			// Optional consistent snapshots let the Django benchmark use the same
			// schema and data without ever opening the user's database.
			if export := os.Getenv("REINHARDT_HTTP_BENCH_EXPORT"); export != "" {
				if err := os.MkdirAll(export, 0700); err != nil {
					b.Fatal(err)
				}
				path := filepath.Join(export, fmt.Sprintf("tracks-%d.sqlite3", tracks))
				if _, err := a.S.DB.Exec("VACUUM INTO ?", path); err != nil {
					b.Fatal(err)
				}
			}
			stats := measureAppSQL(b, a)
			for _, mode := range []struct {
				name           string
				fragment, gzip bool
			}{{"fetch", false, false}, {"fetch-gzip", false, true}, {"unpoly-header", true, false}} {
				b.Run(mode.name, func(b *testing.B) {
					if w := dropdownRequest(a, mode.fragment, mode.gzip); w.Code != 200 {
						b.Fatal(w.Body.String())
					}
					stats.calls.Store(0)
					stats.writes.Store(0)
					stats.suggestions.Store(0)
					stats.duration.Store(0)
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if w := dropdownRequest(a, mode.fragment, mode.gzip); w.Code != 200 {
							b.Fatal(w.Body.String())
						}
					}
					b.StopTimer()
					b.ReportMetric(float64(stats.calls.Load())/float64(b.N), "sql/op")
					b.ReportMetric(float64(stats.writes.Load())/float64(b.N), "writes/op")
					b.ReportMetric(float64(stats.suggestions.Load())/float64(b.N), "suggestions/op")
					b.ReportMetric(float64(stats.duration.Load())/float64(b.N)/1e6, "db-ms/op")
				})
			}
		})
	}
}
