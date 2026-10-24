package reinhardt

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	id3 "github.com/bogem/id3v2/v2"
)

func TestImportFileTransliteratesKoreanPathsAndPreservesTags(test *testing.T) {
	app := testApp(test)
	dir, err := app.Store.DefaultMusicDirectory(test.Context())
	if err != nil {
		test.Fatal(err)
	}
	for index, scenario := range []struct {
		title, filename string
	}{
		{"구중승천", "01-gujungseungceon.mp3"},
		{"백두의 소환", "02-baegduyi-sohwan.mp3"},
		{"왕조의 용맹", "03-wangjoyi-yongmaeng.mp3"},
	} {
		test.Run(scenario.title, func(test *testing.T) {
			source := filepath.Join(test.TempDir(), scenario.title+".mp3")
			if err := os.WriteFile(source, make([]byte, 32), 0644); err != nil {
				test.Fatal(err)
			}
			metadata := &Track{FullPath: source, RelativePath: filepath.Base(source), Artist: "구룡", Title: scenario.title, Album: "백두의 소환", TrackNumber: int64(index + 1)}
			id3Tag, err := id3.Open(source, id3.Options{Parse: true})
			if err != nil {
				test.Fatal(err)
			}
			id3Tag.SetVersion(4)
			id3Tag.SetDefaultEncoding(id3.EncodingUTF8)
			id3Tag.SetArtist(metadata.Artist)
			id3Tag.SetTitle(metadata.Title)
			id3Tag.SetAlbum(metadata.Album)
			id3Tag.AddTextFrame("TRCK", id3.EncodingUTF8, strconv.Itoa(index+1)+"/3")
			saveErr := id3Tag.Save()
			closeErr := id3Tag.Close()
			if saveErr != nil || closeErr != nil {
				test.Fatal("write ID3 fixture", saveErr, closeErr)
			}
			sourceBytes, err := os.ReadFile(source)
			if err != nil {
				test.Fatal(err)
			}
			destination, err := app.Store.importFile(test.Context(), source, dir)
			if err != nil {
				test.Fatal(err)
			}
			relative := filepath.Join("g", "guryong", "baegduyi-sohwan", scenario.filename)
			if destination != filepath.Join(dir.LocalPath, relative) {
				test.Fatalf("destination=%q, want relative path %q", destination, relative)
			}
			importedBytes, err := os.ReadFile(destination)
			if err != nil || !bytes.Equal(importedBytes, sourceBytes) {
				test.Fatal("import changed audio file bytes", err)
			}
			if _, err := os.Stat(source); !os.IsNotExist(err) {
				test.Fatal("successful import retained its source", err)
			}
			imported, err := app.Store.One(test.Context(), "SELECT * FROM reinhardt_musicfile WHERE relative_path=?", relative)
			if err != nil {
				test.Fatal(err)
			}
			if imported.S("artist") != metadata.Artist || imported.S("title") != metadata.Title || imported.S("album") != metadata.Album || imported.N("track_number") != metadata.TrackNumber {
				test.Fatalf("import changed Korean metadata: %#v", imported)
			}
		})
	}
}

func TestIndexFileMetadataPreservesTrackAndMembership(test *testing.T) {
	app := testApp(test)
	dir, err := app.Store.DefaultMusicDirectory(test.Context())
	if err != nil {
		test.Fatal(err)
	}
	track, err := app.Store.Track(test.Context(), 1)
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
	app.Store.Config.DropboxToken = "test-refresh-token"
	stats := measureAppSQL(test, app)
	if err = app.Store.indexFileMetadata(test.Context(), track.FullPath, dir, metadata); err != nil {
		test.Fatal(err)
	}
	if queries := stats.calls.Load(); queries != 2 {
		test.Fatalf("indexing used %d SQL calls, want only upsert and sync enqueue", queries)
	}
	updated, err := app.Store.Track(test.Context(), 1)
	if err != nil {
		test.Fatal(err)
	}
	if updated.Title != "Updated title" || updated.Duration != 42 || updated.Missing || updated.CreatedAt != track.CreatedAt || updated.ModifiedAt != "2002-03-04T05:06:07.123456+00:00" {
		test.Fatalf("indexed metadata or track identity changed: %#v", updated)
	}
	tracks, err := app.Store.TagTracks(test.Context(), tag)
	if err != nil || len(tracks) != 1 || tracks[0].ID != 1 {
		test.Fatal("indexing lost playlist membership", tracks, err)
	}
	jobs, err := app.Store.Query(test.Context(), "SELECT kind,payload FROM go_job")
	if err != nil || len(jobs) != 1 || jobs[0].S("kind") != "dropbox" || jobs[0].S("payload") != "1" {
		test.Fatal("indexing failed to enqueue synchronization", jobs, err)
	}
	// A new path must enqueue the newly inserted ID, even if the supplied
	// metadata came from an existing track.
	stats.calls.Store(0)
	if err = app.Store.indexFileMetadata(test.Context(), filepath.Join(dir.LocalPath, "new.mp3"), dir, metadata); err != nil {
		test.Fatal(err)
	}
	if queries := stats.calls.Load(); queries != 2 {
		test.Fatalf("new-track indexing used %d SQL calls, want only upsert and sync enqueue", queries)
	}
	inserted, err := app.Store.One(test.Context(), "SELECT id FROM reinhardt_musicfile WHERE relative_path='new.mp3'")
	if err != nil || inserted.N("id") <= 3 {
		test.Fatal("indexing did not insert a new track", inserted, err)
	}
	job, err := app.Store.One(test.Context(), "SELECT kind,payload FROM go_job ORDER BY id DESC LIMIT 1")
	if err != nil || job.S("kind") != "dropbox" || job.S("payload") != inserted.S("id") {
		test.Fatal("indexing enqueued the wrong track ID", job, err)
	}
}

func TestImportFileRetainsSourceWhenIndexingFails(test *testing.T) {
	app := testApp(test)
	dir, err := app.Store.DefaultMusicDirectory(test.Context())
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
	if _, err = app.Store.importFile(test.Context(), source, dir); err == nil || !strings.Contains(err.Error(), "index failed") {
		test.Fatal("expected indexing failure", err)
	}
	if _, err = os.Stat(source); err != nil {
		test.Fatal("failed import removed its source", err)
	}
}
