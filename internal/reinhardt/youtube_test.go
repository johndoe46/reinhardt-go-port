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

func installYouTubeTestTools(test *testing.T) string {
	test.Helper()
	if runtime.GOOS == "windows" {
		test.Skip("fixture uses a POSIX shell")
	}
	tools := test.TempDir()
	logs := test.TempDir()
	test.Setenv("PATH", tools)
	test.Setenv("YTDLP_TEST_LOG", logs)
	test.Setenv("YTDLP_TEST_FAIL", "")
	test.Setenv("YTDLP_TEST_PEAK", "-5.000000")
	// Record each helper invocation, supplying fixtures without network access.
	script := `#!/bin/sh
mode=audio
if [ "${0##*/}" = ffmpeg ]; then mode=encode; fi
for arg in "$@"; do
    case "$arg" in
        --dump-single-json) mode=metadata ;;
        --write-thumbnail) mode=thumbnail ;;
        astats=*) mode=scan ;;
    esac
done
printf '%s\n' "$@" > "$YTDLP_TEST_LOG/$mode"
printf '%s\n' "$mode" >> "$YTDLP_TEST_LOG/calls"
if [ "$mode" = "$YTDLP_TEST_FAIL" ]; then
    printf '%s failed\n' "$mode" >&2
    exit 1
fi
case "$mode" in
    metadata) printf '%s\n' '{"artist":"Cookie Artist","track":"Cookie Track","release_year":2026}' ;;
    audio)
        while [ "$#" -gt 0 ]; do
            if [ "$1" = --output ]; then
                shift
                source="${1%.*}.webm"
                printf 'source fixture' > "$source"
                printf '%s\n' "$source"
                break
            fi
            shift
        done ;;
    scan) printf 'Peak level dB: %s\n' "$YTDLP_TEST_PEAK" >&2 ;;
    encode)
        for arg in "$@"; do destination="$arg"; done
        printf 'audio fixture' > "$destination" ;;
esac
`
	for _, name := range []string{"yt-dlp", "ffmpeg"} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(script), 0700); err != nil {
			test.Fatal(err)
		}
	}
	return logs
}

func TestYouTubeCookiesForMetadataAudioAndThumbnails(test *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "without cookies"
		if configured {
			name = "with cookies"
		}
		test.Run(name, func(test *testing.T) {
			app := testApp(test)
			logs := installYouTubeTestTools(test)
			if configured {
				app.Store.Config.YTDLPCookiesFile = filepath.Join(test.TempDir(), "cookies folder", "yt;session.txt")
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
			calls, err := os.ReadFile(filepath.Join(logs, "calls"))
			if err != nil || string(calls) != "metadata\naudio\nscan\nencode\nthumbnail\n" {
				test.Fatalf("expected one scan followed by one encode: %s, %v", calls, err)
			}
			download, _ := os.ReadFile(filepath.Join(logs, "audio"))
			if strings.Contains(string(download), "\n-x\n") || strings.Contains(string(download), "--audio-format") || !strings.Contains(string(download), "\nbestaudio/best\n") {
				test.Fatalf("download must retain the original audio: %s", download)
			}
			encode, _ := os.ReadFile(filepath.Join(logs, "encode"))
			if !strings.Contains(string(encode), "\nvolume=4.000000dB:precision=double\n") || !strings.Contains(string(encode), "\nlibmp3lame\n-b:a\n320k\n") {
				test.Fatalf("gain was not applied during MP3 encoding: %s", encode)
			}
		})
	}
}

func TestYouTubeNormalizationFailureIsRecorded(test *testing.T) {
	for _, stage := range []string{"scan", "encode"} {
		test.Run(stage, func(test *testing.T) {
			app := testApp(test)
			logs := installYouTubeTestTools(test)
			test.Setenv("YTDLP_TEST_FAIL", stage)
			err := app.youtubeRip(test.Context(), Row{"url": "https://youtu.be/abcdefghijk", "artist": "Artist", "title": "Normalized"})
			if err == nil || !strings.Contains(err.Error(), stage+" failed") {
				test.Fatalf("lost normalization failure: %v", err)
			}
			failure, err := app.Store.One(test.Context(), "SELECT * FROM reinhardt_youtuberipfailure")
			if err != nil || !strings.Contains(failure.S("failure_message"), stage+" failed") {
				test.Fatalf("failure cannot be reviewed and retried: %#v, %v", failure, err)
			}
			count, err := app.Store.One(test.Context(), "SELECT count(*) n FROM reinhardt_musicfile")
			if err != nil || count.N("n") != 3 {
				test.Fatalf("imported failed normalization: %#v, %v", count, err)
			}
			if _, err := os.Stat(filepath.Join(logs, "thumbnail")); !os.IsNotExist(err) {
				test.Fatal("continued processing after normalization failed")
			}
			if stage == "scan" {
				if _, err := os.Stat(filepath.Join(logs, "encode")); !os.IsNotExist(err) {
					test.Fatal("encoded audio after the peak scan failed")
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
