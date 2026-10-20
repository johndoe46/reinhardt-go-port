package reinhardt

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestServeMuxExactPathsAndRedirects(test *testing.T) {
	for _, prefix := range []string{"", "music/"} {
		test.Run(prefix, func(test *testing.T) {
			app := testApp(test)
			app.Store.Config.URLPrefix = prefix
			var err error
			app, err = newApp(app.Store)
			if err != nil {
				test.Fatal(err)
			}
			base := "/" + prefix
			for _, scenario := range []struct{ path, location string }{
				{"search?sq=tune", "search/?sq=tune"},
				{"radio", "radio/"},
				{"search//", "search/"},
				{"search/../queue/", "queue/"},
			} {
				response := call(app, "GET", base+scenario.path, 1, nil, true)
				if response.Code < 300 || response.Code >= 400 || response.Header().Get("Location") != base+scenario.location {
					test.Fatalf("%s: expected redirect to %s, got %d %q", scenario.path, base+scenario.location, response.Code, response.Header().Get("Location"))
				}
			}
			for _, path := range []string{
				"search/child", "api/music_files/search/child", "radio/1/play/extra/",
				"stream/1/ignored", "stream%2F1/", "stream/1%2Fignored/",
				"track_details_modal/1/ignored", "artwork_modal/1/128.jpg/ignored",
				"rename_tag/1/ignored", "missing/",
			} {
				requireCode(test, call(app, "GET", base+path, 1, nil, false), http.StatusNotFound)
			}
			for _, path := range []string{"search/", "radio/", "track_details_modal/1", "artwork_modal/1/128.jpg"} {
				requireCode(test, call(app, "GET", base+path, 1, nil, false), http.StatusOK)
			}
			if prefix != "" {
				requireCode(test, call(app, "GET", "/search/", 1, nil, false), http.StatusNotFound)
			}
		})
	}
}

func TestServeMuxMethodResponses(test *testing.T) {
	app := testApp(test)
	for _, scenario := range []struct{ method, path, allow string }{
		{"GET", "/next_song/", "POST"},
		{"HEAD", "/clear_queue/", "POST"},
		{"GET", "/radio/999999999/play/", "POST"},
		{"GET", "/radio/1/delete/", "POST"},
		{"GET", "/ytrip/failures/1/retry/", "POST"},
		{"POST", "/search/", "GET, HEAD"},
		{"POST", "/stream/1/", "GET, HEAD"},
		{"POST", "/api/music_files/search/", "GET, HEAD"},
	} {
		test.Run(scenario.method+scenario.path, func(test *testing.T) {
			response := call(app, scenario.method, scenario.path, 1, url.Values{}, false)
			requireCode(test, response, http.StatusMethodNotAllowed)
			if response.Header().Get("Allow") != scenario.allow {
				test.Fatalf("Allow=%q, want %q", response.Header().Get("Allow"), scenario.allow)
			}
		})
	}
}

func TestServeMuxKeepsAuthenticationAndCSRFAheadOfRouting(test *testing.T) {
	app := testApp(test)
	for _, path := range []string{"/search", "/missing/", "/next_song/", "/radio/1/delete/"} {
		response := call(app, "POST", path, 0, nil, false)
		requireCode(test, response, http.StatusFound)
		if !strings.HasPrefix(response.Header().Get("Location"), "/accounts/login/?next=") {
			test.Fatal("anonymous request did not reach the authentication gate", response.Header())
		}
	}
	for _, path := range []string{"/search/", "/missing/", "/next_song/", "/radio/1/delete/"} {
		request := httptest.NewRequest("POST", path, nil)
		request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		requireCode(test, response, http.StatusForbidden)
		if response.Header().Get("Allow") != "" {
			test.Fatal("method routing ran before CSRF validation")
		}
	}
	requireCode(test, call(app, "POST", "/radio/1/delete/", 2, url.Values{}, false), http.StatusForbidden)
	requireCode(test, call(app, "GET", "/radio/new/", 2, nil, false), http.StatusForbidden)
	requireCode(test, call(app, "GET", "/radio/1/edit/", 2, nil, false), http.StatusForbidden)

	app.Store.Config.AllowedHosts = []string{"music.example"}
	request := httptest.NewRequest("GET", "/search", nil)
	request.Host = "untrusted.example"
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, http.StatusBadRequest)
}

func TestServeMuxNamedMediaParametersAndPrefix(test *testing.T) {
	app := testApp(test)
	tagID := mustTag(test, app, 1, "shared", []int64{1})
	if _, err := app.Store.DB.ExecContext(test.Context(), "UPDATE reinhardt_tag SET is_shared=1 WHERE id=?", tagID); err != nil {
		test.Fatal(err)
	}
	app.Store.Config.URLPrefix = "music/"
	var err error
	app, err = newApp(app.Store)
	if err != nil {
		test.Fatal(err)
	}
	track, err := app.Store.Track(test.Context(), 1)
	if err != nil {
		test.Fatal(err)
	}
	tag, err := app.Store.Tag(test.Context(), tagID, 1)
	if err != nil {
		test.Fatal(err)
	}
	for _, path := range []string{track.PlaybackURL, track.DownloadURL, tag.ShareURL + "track/1/"} {
		for _, method := range []string{"GET", "HEAD"} {
			test.Run(method+path, func(test *testing.T) {
				request := httptest.NewRequest(method, path, nil)
				if !strings.HasPrefix(path, "/music/shared/") {
					request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
				}
				request.Header.Set("Range", "bytes=1-3")
				response := httptest.NewRecorder()
				app.ServeHTTP(response, request)
				requireCode(test, response, http.StatusPartialContent)
				if response.Header().Get("Content-Range") != "bytes 1-3/26" {
					test.Fatal("byte range headers changed", response.Header())
				}
				if (method == "GET" && response.Body.String() != "bcd") || (method == "HEAD" && response.Body.Len() != 0) {
					test.Fatalf("%s returned unexpected bytes: %q", method, response.Body.String())
				}
			})
		}
	}
	response := call(app, "GET", "/music/download/1/wrong-name.mp3", 1, nil, false)
	requireCode(test, response, http.StatusFound)
	if response.Header().Get("Location") != track.DownloadURL || response.Header().Get("X-Up-Location") != track.DownloadURL {
		test.Fatal("canonical download redirect lost its prefix or Unpoly location", response.Header())
	}
	response = call(app, "GET", "/music/search/?sq=tune", 1, nil, false)
	if !strings.Contains(response.Body.String(), `src="/music/static/player.js"`) {
		test.Fatal("rendered page lost the URL prefix")
	}
	requireCode(test, call(app, "GET", "/music/static/app.js", 0, nil, false), http.StatusOK)
	requireCode(test, call(app, "GET", tag.ShareURL+"track/2/", 0, nil, false), http.StatusNotFound)
	wrongHash := fmt.Sprintf("/music/shared/%d/invalid/", tagID)
	requireCode(test, call(app, "GET", wrongHash, 0, nil, false), http.StatusNotFound)
}
