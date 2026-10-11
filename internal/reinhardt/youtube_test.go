package reinhardt

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func removeTestMusicDirectories(t *testing.T, a *App) {
	t.Helper()
	if _, err := a.S.DB.Exec("DELETE FROM reinhardt_musicdirectory"); err != nil {
		t.Fatal(err)
	}
}

func TestYouTubeMissingMusicDirectoryReportsConfiguration(t *testing.T) {
	a := testApp(t)
	removeTestMusicDirectories(t, a)
	// A missing destination must fail before invoking external download tools.
	t.Setenv("PATH", t.TempDir())
	data := Row{"url": "https://youtu.be/abcdefghijk", "artist": "Artist", "title": "Title"}
	err := a.youtubeRip(context.Background(), data)
	if !errors.Is(err, errNoMusicDirectory) {
		t.Fatalf("expected a missing-directory error, got %v", err)
	}
	for _, detail := range []string{"addmusicdir", a.S.C["DATABASE_PATH"]} {
		if !strings.Contains(err.Error(), detail) {
			t.Fatalf("error omits %q: %v", detail, err)
		}
	}
	failure, err := a.S.One("SELECT * FROM reinhardt_youtuberipfailure")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failure.S("failure_message"), "no music directory is registered") || failure.S("url") != data.S("url") {
		t.Fatalf("failure does not preserve the useful error and submitted URL: %#v", failure)
	}
	if err := a.S.Enqueue("youtube", data); err != nil {
		t.Fatal(err)
	}
	if err := a.Work(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	job, err := a.S.One("SELECT * FROM go_job")
	if err != nil || job.N("attempts") != 1 || !strings.Contains(job.S("error"), "addmusicdir") {
		t.Fatalf("job retry lost the configuration error: %#v, %v", job, err)
	}
	w := call(a, "POST", "/ytrip/failures/"+failure.S("id")+"/retry/", 1, url.Values{}, true)
	requireCode(t, w, 400)
	count, err := a.S.One("SELECT count(*) n FROM go_job")
	if err != nil || count.N("n") != 1 {
		t.Fatalf("retry queued another job without a destination: %#v, %v", count, err)
	}
}

func TestYouTubeSubmissionNeedsDestinationBeforeEnqueuing(t *testing.T) {
	a := testApp(t)
	removeTestMusicDirectories(t, a)
	form := url.Values{"url": {"https://youtu.be/abcdefghijk"}, "artist": {"Artist"}, "title": {"Title"}}
	w := call(a, "POST", "/ytrip/", 1, form, true)
	requireCode(t, w, 400)
	if !strings.Contains(w.Body.String(), "No music directory is configured.") {
		t.Fatal("the download form does not explain the missing destination")
	}
	count, err := a.S.One("SELECT count(*) n FROM go_job")
	if err != nil || count.N("n") != 0 {
		t.Fatalf("queued a download without a destination: %#v, %v", count, err)
	}
	destination := filepath.Join(a.S.C["BASE_DIR"], "music")
	if _, err := a.S.DB.Exec("INSERT INTO reinhardt_musicdirectory(local_path,created_at,modified_at) VALUES(?,?,?)", destination, now(), now()); err != nil {
		t.Fatal(err)
	}
	w = call(a, "POST", "/ytrip/", 1, form, true)
	requireCode(t, w, 200)
	job, err := a.S.One("SELECT * FROM go_job")
	if err != nil || job.S("kind") != "youtube" {
		t.Fatalf("registering a destination did not unblock submission: %#v, %v", job, err)
	}
}

func TestGatherMissingMusicDirectoryReportsConfiguration(t *testing.T) {
	a := testApp(t)
	removeTestMusicDirectories(t, a)
	a.S.C["MUSIC_IMPORT_DIR"] = t.TempDir()
	if err := a.S.Gather(context.Background()); !errors.Is(err, errNoMusicDirectory) {
		t.Fatalf("expected a missing-directory error, got %v", err)
	}
}
