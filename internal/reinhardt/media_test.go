package reinhardt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndexFileMetadataPreservesTrackAndMembership(test *testing.T) {
	app := testApp(test)
	dir, err := app.Store.DefaultMusicDirectory()
	if err != nil {
		test.Fatal(err)
	}
	track, err := app.Store.Track(1)
	if err != nil {
		test.Fatal(err)
	}
	tag := mustTag(test, app, 1, "keep", []int64{1})
	if _, err = app.Store.DB.Exec("UPDATE reinhardt_musicfile SET missing=1 WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	// The metadata-only path must work without access to the audio file.
	if err = os.Remove(track.FullPath); err != nil {
		test.Fatal(err)
	}
	copied := *track
	metadata := &copied
	metadata.Title = "Updated title"
	metadata.Duration = 42
	metadata.ModifiedAt = "2002-03-04 05:06:07.123456"
	app.Store.Config["DBX_TOKEN"] = "test-refresh-token"
	stats := measureAppSQL(test, app)
	if err = app.Store.indexFileMetadata(track.FullPath, dir, metadata); err != nil {
		test.Fatal(err)
	}
	if queries := stats.calls.Load(); queries != 2 {
		test.Fatalf("indexing used %d SQL calls, want only upsert and sync enqueue", queries)
	}
	updated, err := app.Store.Track(1)
	if err != nil {
		test.Fatal(err)
	}
	if updated.Title != "Updated title" || updated.Duration != 42 || updated.Missing || updated.CreatedAt != track.CreatedAt || updated.ModifiedAt != "2002-03-04T05:06:07.123456+00:00" {
		test.Fatalf("indexed metadata or track identity changed: %#v", updated)
	}
	tracks, err := app.Store.TagTracks(tag)
	if err != nil || len(tracks) != 1 || tracks[0].ID != 1 {
		test.Fatal("indexing lost playlist membership", tracks, err)
	}
	jobs, err := app.Store.Query("SELECT kind,payload FROM go_job")
	if err != nil || len(jobs) != 1 || jobs[0].S("kind") != "dropbox" || jobs[0].S("payload") != "1" {
		test.Fatal("indexing failed to enqueue synchronization", jobs, err)
	}
	// A new path must enqueue the newly inserted ID, even if the supplied
	// metadata came from an existing track.
	stats.calls.Store(0)
	if err = app.Store.indexFileMetadata(filepath.Join(dir.LocalPath, "new.mp3"), dir, metadata); err != nil {
		test.Fatal(err)
	}
	if queries := stats.calls.Load(); queries != 2 {
		test.Fatalf("new-track indexing used %d SQL calls, want only upsert and sync enqueue", queries)
	}
	inserted, err := app.Store.One("SELECT id FROM reinhardt_musicfile WHERE relative_path='new.mp3'")
	if err != nil || inserted.N("id") <= 3 {
		test.Fatal("indexing did not insert a new track", inserted, err)
	}
	job, err := app.Store.One("SELECT kind,payload FROM go_job ORDER BY id DESC LIMIT 1")
	if err != nil || job.S("kind") != "dropbox" || job.S("payload") != inserted.S("id") {
		test.Fatal("indexing enqueued the wrong track ID", job, err)
	}
}

func TestImportFileRetainsSourceWhenIndexingFails(test *testing.T) {
	app := testApp(test)
	dir, err := app.Store.DefaultMusicDirectory()
	if err != nil {
		test.Fatal(err)
	}
	source := filepath.Join(test.TempDir(), "source.mp3")
	if err = os.WriteFile(source, make([]byte, 32), 0644); err != nil {
		test.Fatal(err)
	}
	metadata := &Track{FullPath: source, RelativePath: "source.mp3", Artist: "Artist", Title: "Imported title", Album: "Album"}
	if err = writeID3(metadata, nil, ""); err != nil {
		test.Fatal(err)
	}
	if _, err = app.Store.DB.Exec(`CREATE TRIGGER reject_import BEFORE INSERT ON reinhardt_musicfile BEGIN SELECT RAISE(ABORT, 'index failed'); END`); err != nil {
		test.Fatal(err)
	}
	if _, err = app.Store.importFile(source, dir); err == nil || !strings.Contains(err.Error(), "index failed") {
		test.Fatal("expected indexing failure", err)
	}
	if _, err = os.Stat(source); err != nil {
		test.Fatal("failed import removed its source", err)
	}
}
