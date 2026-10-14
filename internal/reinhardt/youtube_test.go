package reinhardt

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func removeTestMusicDirectories(test *testing.T, app *App) {
	test.Helper()
	if _, err := app.Store.DB.Exec("DELETE FROM reinhardt_musicdirectory"); err != nil {
		test.Fatal(err)
	}
}

func TestYouTubeMissingMusicDirectoryReportsConfiguration(test *testing.T) {
	app := testApp(test)
	removeTestMusicDirectories(test, app)
	// A missing destination must fail before invoking external download tools.
	test.Setenv("PATH", test.TempDir())
	data := Row{"url": "https://youtu.be/abcdefghijk", "artist": "Artist", "title": "Title"}
	err := app.youtubeRip(context.Background(), data)
	if !errors.Is(err, errNoMusicDirectory) {
		test.Fatalf("expected a missing-directory error, got %v", err)
	}
	for _, detail := range []string{"addmusicdir", app.Store.Config.DatabasePath} {
		if !strings.Contains(err.Error(), detail) {
			test.Fatalf("error omits %q: %v", detail, err)
		}
	}
	failure, err := app.Store.One(test.Context(), "SELECT * FROM reinhardt_youtuberipfailure")
	if err != nil {
		test.Fatal(err)
	}
	if !strings.Contains(failure.S("failure_message"), "no music directory is registered") || failure.S("url") != data.S("url") {
		test.Fatalf("failure does not preserve the useful error and submitted URL: %#v", failure)
	}
	if err := app.Store.Enqueue(test.Context(), "youtube", data); err != nil {
		test.Fatal(err)
	}
	if err := app.Work(context.Background(), true); err != nil {
		test.Fatal(err)
	}
	job, err := app.Store.One(test.Context(), "SELECT * FROM go_job")
	if err != nil || job.N("attempts") != 1 || !strings.Contains(job.S("error"), "addmusicdir") {
		test.Fatalf("job retry lost the configuration error: %#v, %v", job, err)
	}
	response := call(app, "POST", "/ytrip/failures/"+failure.S("id")+"/retry/", 1, url.Values{}, true)
	requireCode(test, response, 400)
	count, err := app.Store.One(test.Context(), "SELECT count(*) n FROM go_job")
	if err != nil || count.N("n") != 1 {
		test.Fatalf("retry queued another job without a destination: %#v, %v", count, err)
	}
}

func TestYouTubeSubmissionNeedsDestinationBeforeEnqueuing(test *testing.T) {
	app := testApp(test)
	removeTestMusicDirectories(test, app)
	form := url.Values{"url": {"https://youtu.be/abcdefghijk"}, "artist": {"Artist"}, "title": {"Title"}}
	response := call(app, "POST", "/ytrip/", 1, form, true)
	requireCode(test, response, 400)
	if !strings.Contains(response.Body.String(), "No music directory is configured.") {
		test.Fatal("the download form does not explain the missing destination")
	}
	count, err := app.Store.One(test.Context(), "SELECT count(*) n FROM go_job")
	if err != nil || count.N("n") != 0 {
		test.Fatalf("queued a download without a destination: %#v, %v", count, err)
	}
	destination := filepath.Join(app.Store.Config.BaseDir, "music")
	if _, err := app.Store.DB.Exec("INSERT INTO reinhardt_musicdirectory(local_path,created_at,modified_at) VALUES(?,?,?)", destination, now(), now()); err != nil {
		test.Fatal(err)
	}
	response = call(app, "POST", "/ytrip/", 1, form, true)
	requireCode(test, response, 200)
	job, err := app.Store.One(test.Context(), "SELECT * FROM go_job")
	if err != nil || job.S("kind") != "youtube" {
		test.Fatalf("registering a destination did not unblock submission: %#v, %v", job, err)
	}
}

func TestGatherMissingMusicDirectoryReportsConfiguration(test *testing.T) {
	app := testApp(test)
	removeTestMusicDirectories(test, app)
	app.Store.Config.MusicImportDir = test.TempDir()
	if err := app.Store.Gather(context.Background()); !errors.Is(err, errNoMusicDirectory) {
		test.Fatalf("expected a missing-directory error, got %v", err)
	}
}
