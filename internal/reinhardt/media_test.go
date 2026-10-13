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
	if err = os.Remove(track.S("full_path")); err != nil {
		test.Fatal(err)
	}
	metadata := Row{}
	for key, value := range track {
		metadata[key] = value
	}
	metadata["title"] = "Updated title"
	metadata["duration"] = 42
	metadata["modified_at"] = "2002-03-04 05:06:07.123456"
	app.Store.Config["DBX_TOKEN"] = "test-refresh-token"
	if err = app.Store.indexFileMetadata(track.S("full_path"), dir, metadata); err != nil {
		test.Fatal(err)
	}
	updated, err := app.Store.Track(1)
	if err != nil {
		test.Fatal(err)
	}
	if updated.S("title") != "Updated title" || updated.N("duration") != 42 || updated.B("missing") || updated.S("created_at") != track.S("created_at") || updated.S("modified_at") != "2002-03-04T05:06:07.123456+00:00" {
		test.Fatalf("indexed metadata or track identity changed: %#v", updated)
	}
	tracks, err := app.Store.TagTracks(tag)
	if err != nil || len(tracks) != 1 || tracks[0].N("id") != 1 {
		test.Fatal("indexing lost playlist membership", tracks, err)
	}
	jobs, err := app.Store.Query("SELECT kind,payload FROM go_job")
	if err != nil || len(jobs) != 1 || jobs[0].S("kind") != "dropbox" || jobs[0].S("payload") != "1" {
		test.Fatal("indexing failed to enqueue synchronization", jobs, err)
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
	metadata := Row{"full_path": source, "relative_path": "source.mp3", "artist": "Artist", "title": "Imported title", "album": "Album"}
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
