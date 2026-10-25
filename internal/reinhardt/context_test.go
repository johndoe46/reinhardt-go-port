package reinhardt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDatabaseOperationsRespectCancellation(test *testing.T) {
	app := testApp(test)
	store := app.Store
	tagID := mustTag(test, app, 1, "queue", []int64{1, 2})
	dir, err := store.DefaultMusicDirectory(test.Context())
	if err != nil {
		test.Fatal(err)
	}
	track, err := store.Track(test.Context(), 1)
	if err != nil {
		test.Fatal(err)
	}
	ctx, cancel := context.WithCancel(test.Context())
	cancel()
	operations := []struct {
		name string
		run  func() error
	}{
		{"query", func() error { _, err := store.Query(ctx, "SELECT 1"); return err }},
		{"one", func() error { _, err := store.One(ctx, "SELECT 1"); return err }},
		{"track", func() error { _, err := store.Track(ctx, 1); return err }},
		{"search", func() error { _, err := store.Search(ctx, "First"); return err }},
		{"preferences", func() error { _, err := store.Preferences(ctx, 3); return err }},
		{"language", func() error { return store.SetUserLanguage(ctx, 1, "fr") }},
		{"tags", func() error { _, err := store.Tags(ctx, 1, true); return err }},
		{"tag", func() error { _, err := store.Tag(ctx, tagID, 1); return err }},
		{"tag-tracks", func() error { _, err := store.TagTracks(ctx, tagID); return err }},
		{"queue", func() error { _, err := store.Queue(ctx, 1); return err }},
		{"queue-item", func() error { _, err := store.queueItem(ctx, 1, "a.music_file_id=?", 1); return err }},
		{"shuffle", func() error { return store.ShuffleQueue(ctx, 1) }},
		{"add-tag", func() error { _, err := store.AddTag(ctx, 1, "cancelled"); return err }},
		{"add-track", func() error { _, err := store.AddTrack(ctx, 1, "queue", 3); return err }},
		{"add-tracks", func() error { _, err := store.AddTracks(ctx, 1, "queue", []int64{3}); return err }},
		{"directory", func() error { _, err := store.DefaultMusicDirectory(ctx); return err }},
		{"index", func() error { return store.indexFileMetadata(ctx, track.FullPath, dir, track) }},
		{"enqueue", func() error { return store.Enqueue(ctx, "jama", 1) }},
		{"fts", func() error { return store.ReindexFTS(ctx) }},
		{"home", func() error { _, err := app.latestAdditions(ctx); return err }},
		{"suggestions", func() error { _, err := app.suggestedData(ctx); return err }},
	}
	for _, operation := range operations {
		test.Run(operation.name, func(test *testing.T) {
			if err := operation.run(); !errors.Is(err, context.Canceled) {
				test.Fatalf("error=%v, want context.Canceled", err)
			}
		})
	}
	row, err := store.One(test.Context(), `SELECT
(SELECT count(*) FROM reinhardt_preferences WHERE owner_id=3) AS preferences,
(SELECT count(*) FROM reinhardt_tag WHERE label='cancelled') AS tags,
(SELECT count(*) FROM reinhardt_musicfiletag WHERE tag_id=? AND music_file_id=3) AS tracks,
(SELECT count(*) FROM go_job) AS jobs`, tagID)
	if err != nil {
		test.Fatal(err)
	}
	for _, key := range []string{"preferences", "tags", "tracks", "jobs"} {
		if row.N(key) != 0 {
			test.Fatalf("cancelled operation wrote %s", key)
		}
	}
}

func TestDatabaseDeadlineWhileWaitingForConnection(test *testing.T) {
	for _, scenario := range []string{"helper", "http-search", "worker"} {
		test.Run(scenario, func(test *testing.T) {
			app := testApp(test)
			conn, err := app.Store.DB.Conn(test.Context())
			if err != nil {
				test.Fatal(err)
			}
			defer conn.Close()
			// Hold the only connection: cancellation must stop the pool wait,
			// even though there is no SQLite statement to interrupt yet.
			ctx, cancel := context.WithTimeout(test.Context(), 50*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				switch scenario {
				case "helper":
					_, err := app.Store.Track(ctx, 1)
					done <- err
				case "http-search":
					request := httptest.NewRequestWithContext(ctx, "GET", "/api/music_files/", nil)
					done <- app.searchAPI(httptest.NewRecorder(), request)
				case "worker":
					done <- app.Work(ctx, true)
				}
			}()
			select {
			case err := <-done:
				if scenario == "worker" {
					if err != nil {
						test.Fatal("worker shutdown failed", err)
					}
				} else if !errors.Is(err, context.DeadlineExceeded) {
					test.Fatalf("error=%v, want context.DeadlineExceeded", err)
				}
			case <-time.After(2 * time.Second):
				test.Fatal("database operation ignored its deadline")
			}
			if app.Store.DB.Stats().WaitCount == 0 {
				test.Fatal("test did not exercise the connection pool wait")
			}
		})
	}
}

func TestDatabaseCancellationDuringQuery(test *testing.T) {
	app := testApp(test)
	ctx, cancel := context.WithTimeout(test.Context(), 50*time.Millisecond)
	defer cancel()
	// This aggregate cannot return a row until its long scan finishes.
	_, err := app.Store.Query(ctx, `WITH RECURSIVE numbers(n) AS
(SELECT 1 UNION ALL SELECT n+1 FROM numbers WHERE n<100000000)
SELECT sum(n) FROM numbers`)
	if !errors.Is(err, context.DeadlineExceeded) {
		test.Fatalf("error=%v, want context.DeadlineExceeded", err)
	}
	if _, err := app.Store.One(test.Context(), "SELECT 1"); err != nil {
		test.Fatal("cancelled query left the database unusable", err)
	}
}

// Interrupt selected statements on test-owned SQLite connections, after their
// effects occur, to exercise rollback without scheduling races or sleeps.
type cancellationConnector struct {
	base      driver.Driver
	dsn       string
	afterExec func(string)
}

func (connector cancellationConnector) Driver() driver.Driver { return connector.base }
func (connector cancellationConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := connector.base.Open(connector.dsn)
	if err != nil {
		return nil, err
	}
	return cancellationConn{Conn: conn, afterExec: connector.afterExec}, nil
}

type cancellationConn struct {
	driver.Conn
	afterExec func(string)
}

func (conn cancellationConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	result, err := conn.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if err == nil {
		conn.afterExec(query)
	}
	return result, err
}
func (conn cancellationConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return conn.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}
func (conn cancellationConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	return conn.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func interruptSQL(test *testing.T, app *App, afterExec func(string)) {
	test.Helper()
	base := app.Store.DB.Driver()
	if err := app.Store.DB.Close(); err != nil {
		test.Fatal(err)
	}
	db := sql.OpenDB(cancellationConnector{base: base, dsn: app.Store.Config.DatabasePath + "?_pragma=foreign_keys(1)", afterExec: afterExec})
	db.SetMaxOpenConns(1)
	app.Store.DB = db
}

func TestCancelledPlaylistTransactionRollsBack(test *testing.T) {
	app := testApp(test)
	tagID := mustTag(test, app, 1, "cancel-test", nil)
	ctx, cancel := context.WithCancel(test.Context())
	defer cancel()
	interruptSQL(test, app, func(query string) {
		if strings.HasPrefix(query, "INSERT OR IGNORE INTO reinhardt_musicfiletag") {
			cancel()
		}
	})
	_, err := app.Store.AddTracks(ctx, 1, "cancel-test", []int64{1, 2})
	if !errors.Is(err, context.Canceled) && !errors.Is(err, sql.ErrTxDone) {
		test.Fatalf("error=%v, want cancellation", err)
	}
	tracks, err := app.Store.TagTracks(test.Context(), tagID)
	if err != nil || len(tracks) != 0 {
		test.Fatal("cancelled transaction retained partial playlist additions", tracks, err)
	}
}

func TestCancelledMigrationReleasesWriteLock(test *testing.T) {
	app := testApp(test)
	ctx, cancel := context.WithCancel(test.Context())
	defer cancel()
	interruptSQL(test, app, func(query string) {
		if query == "BEGIN IMMEDIATE" {
			cancel()
		}
	})
	if err := migratePreferences(ctx, app.Store.DB); !errors.Is(err, context.Canceled) {
		test.Fatalf("error=%v, want context.Canceled", err)
	}
	// Reuse the connection: another BEGIN would fail if the cancelled
	// migration's transaction had been returned to the pool without rollback.
	tx, err := app.Store.DB.BeginTx(test.Context(), nil)
	if err != nil {
		test.Fatal("migration left a transaction open", err)
	}
	if err := tx.Rollback(); err != nil {
		test.Fatal(err)
	}
}

func TestWorkerCancellationReleasesJob(test *testing.T) {
	app := testApp(test)
	app.Store.Config.JamaEndpoint = "http://jama.example/"
	app.Store.Config.JamaKey = "test-key"
	app.Store.Config.JamaProject = 1
	if err := app.Store.Enqueue(test.Context(), "jama", 1); err != nil {
		test.Fatal(err)
	}
	started := make(chan struct{})
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	ctx, cancel := context.WithCancel(test.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.Work(ctx, true) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		test.Fatal("worker did not start the job")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			test.Fatal("worker shutdown failed", err)
		}
	case <-time.After(2 * time.Second):
		test.Fatal("worker did not stop after cancellation")
	}
	job, err := app.Store.One(test.Context(), "SELECT * FROM go_job")
	if err != nil || job["locked_at"] != nil || job.N("attempts") != 0 {
		test.Fatal("interrupted job was not released for retry", job, err)
	}
}

func TestYouTubeCancellationPreservesFailureRecord(test *testing.T) {
	app := testApp(test)
	ctx, cancel := context.WithCancel(test.Context())
	cancel()
	metadata := Row{"url": "https://youtu.be/abcdefghijk", "artist": "Artist", "title": "Title"}
	if err := app.youtubeRip(ctx, metadata); !errors.Is(err, context.Canceled) {
		test.Fatalf("error=%v, want context.Canceled", err)
	}
	failure, err := app.Store.One(test.Context(), "SELECT * FROM reinhardt_youtuberipfailure")
	if err != nil || !strings.Contains(failure.S("failure_message"), context.Canceled.Error()) {
		test.Fatal("cancellation lost the download failure record", failure, err)
	}
}
