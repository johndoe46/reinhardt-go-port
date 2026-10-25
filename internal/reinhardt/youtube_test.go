package reinhardt

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestYouTubeCookiesForMetadataAudioAndThumbnails(test *testing.T) {
	if runtime.GOOS == "windows" {
		test.Skip("fixture uses a POSIX shell")
	}
	for _, configured := range []bool{false, true} {
		name := "without cookies"
		if configured {
			name = "with cookies"
		}
		test.Run(name, func(test *testing.T) {
			app := testApp(test)
			tools := test.TempDir()
			logs := test.TempDir()
			test.Setenv("PATH", tools)
			test.Setenv("YTDLP_TEST_LOG", logs)
			if configured {
				app.Store.Config.YTDLPCookiesFile = filepath.Join(test.TempDir(), "cookies folder", "yt;session.txt")
			}
			// Exercise all three application calls without network or media tools.
			script := `#!/bin/sh
mode=audio
for arg in "$@"; do
    case "$arg" in
        --dump-single-json) mode=metadata ;;
        --write-thumbnail) mode=thumbnail ;;
    esac
done
printf '%s\n' "$@" > "$YTDLP_TEST_LOG/$mode"
case "$mode" in
    metadata) printf '%s\n' '{"artist":"Cookie Artist","track":"Cookie Track","release_year":2026}' ;;
    audio)
        while [ "$#" -gt 0 ]; do
            if [ "$1" = --output ]; then
                shift
                printf 'audio fixture' > "${1%.*}.mp3"
                break
            fi
            shift
        done ;;
esac
`
			if err := os.WriteFile(filepath.Join(tools, "yt-dlp"), []byte(script), 0700); err != nil {
				test.Fatal(err)
			}
			video := "https://www.youtube.com/watch?v=abcdefghijk"
			response := call(app, "GET", "/ytrip/metadata/?"+url.Values{"url": {video}}.Encode(), 1, nil, false)
			requireCode(test, response, 200)
			var metadata map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &metadata); err != nil || metadata["artist"] != "Cookie Artist" || metadata["title"] != "Cookie Track" || metadata["year"] != "2026" {
				test.Fatalf("unexpected metadata: %#v, %v", metadata, err)
			}
			if err := app.youtubeRip(test.Context(), Row{"url": video, "artist": metadata["artist"], "title": metadata["title"]}); err != nil {
				test.Fatal(err)
			}
			track, err := app.Store.One(test.Context(), "SELECT * FROM reinhardt_musicfile WHERE artist=? AND title=?", metadata["artist"], metadata["title"])
			if err != nil || track.S("relative_path") == "" {
				test.Fatalf("download was not imported: %#v, %v", track, err)
			}
			for _, mode := range []string{"metadata", "audio", "thumbnail"} {
				data, err := os.ReadFile(filepath.Join(logs, mode))
				if err != nil {
					test.Fatal(err)
				}
				args := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
				wantPrefix := []string{"--ignore-config"}
				if configured {
					wantPrefix = append(wantPrefix, "--cookies", app.Store.Config.YTDLPCookiesFile)
				}
				if len(args) < len(wantPrefix)+2 || !reflect.DeepEqual(args[:len(wantPrefix)], wantPrefix) || !reflect.DeepEqual(args[len(args)-2:], []string{"--", video}) {
					test.Fatalf("%s received incorrect arguments: %#v", mode, args)
				}
				if !configured && strings.Contains(string(data), "--cookies") {
					test.Fatalf("%s used cookies when unset", mode)
				}
			}
		})
	}
}

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
