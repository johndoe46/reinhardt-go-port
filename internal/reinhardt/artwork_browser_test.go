package reinhardt

import (
	"bytes"
	"fmt"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestArtworkBrowserURLsShareContentAcrossPagesAndUploads(test *testing.T) {
	for _, prefix := range []string{"", "music/"} {
		test.Run(prefix, func(test *testing.T) {
			app := testApp(test)
			app.Store.Config.URLPrefix = prefix
			app.router = app.routes()
			path := app.Store.Config.Path
			if _, err := app.Store.DB.Exec("UPDATE reinhardt_preferences SET show_track_grid=1 WHERE owner_id=1"); err != nil {
				test.Fatal(err)
			}
			queue := mustTag(test, app, 1, "queue", []int64{1, 2})
			if _, err := app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=(SELECT id FROM reinhardt_musicfiletag WHERE tag_id=? AND music_file_id=1) WHERE owner_id=1", queue); err != nil {
				test.Fatal(err)
			}
			sharedID := mustTag(test, app, 1, "shared", []int64{1, 2})
			if _, err := app.Store.DB.Exec("UPDATE reinhardt_tag SET is_shared=1 WHERE id=?", sharedID); err != nil {
				test.Fatal(err)
			}
			shared, err := app.Store.Tag(test.Context(), sharedID, 1)
			if err != nil {
				test.Fatal(err)
			}
			for _, id := range []int{1, 2} {
				track, err := app.Store.Track(test.Context(), id)
				if err != nil {
					test.Fatal(err)
				}
				if err := os.WriteFile(track.FullPath+".png", artworkTestPNG(test), 0600); err != nil {
					test.Fatal(err)
				}
			}
			// Populate the home metadata cache before discovery establishes hashes.
			initial := call(app, "GET", path(""), 1, nil, false)
			requireCode(test, initial, 200)
			if !strings.Contains(initial.Body.String(), path("artwork/1/")) && !strings.Contains(initial.Body.String(), path("artwork/2/")) {
				test.Fatal("unresolved artwork lost its track lookup URL")
			}
			var canonical string
			for _, id := range []int{1, 2} {
				lookup := call(app, "GET", path(fmt.Sprintf("artwork/%d/256.jpg", id)), 0, nil, false)
				requireCode(test, lookup, http.StatusFound)
				location := lookup.Header().Get("Location")
				if id == 1 {
					canonical = location
				} else if location != canonical {
					test.Fatal("tracks sharing an image redirected to different browser cache keys", location, canonical)
				}
				if !strings.HasPrefix(location, path("artwork/"+artworkRenderVersion+"/")) || strings.Contains(location, "?") || lookup.Header().Get("Cache-Control") != "public, max-age=3600" || lookup.Header().Get("Vary") != "" {
					test.Fatal("wrong shared redirect URL or mutable lookup cache policy", lookup.Header())
				}
			}
			artwork, err := app.Store.trackArtwork(test.Context(), "1")
			if err != nil || canonical != app.Store.URL("artwork_hash", artwork.hash, 256) {
				test.Fatal("wrong canonical artwork URL", artwork, err)
			}
			image := call(app, "GET", canonical, 0, nil, false)
			requireCode(test, image, 200)
			if image.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || image.Header().Get("Vary") != "" {
				test.Fatal("shared artwork cannot be reused immutably across users", image.Header())
			}
			for _, page := range []struct {
				path string
				size int
			}{{path(""), 256}, {path("album/?q=Album"), 256}, {path("queue/"), 256}, {path("track_details_modal/1"), 512}, {path("artwork_modal/1/512.jpg"), 512}, {shared.ShareURL, 512}} {
				response := call(app, "GET", page.path, 1, nil, false)
				requireCode(test, response, 200)
				want := app.Store.URL("artwork_hash", artwork.hash, page.size)
				if !strings.Contains(response.Body.String(), `src="`+want+`"`) {
					test.Fatalf("%s did not emit the shared artwork URL %s", page.path, want)
				}
				for _, id := range []int{1, 2} {
					if strings.Contains(response.Body.String(), `src="`+path(fmt.Sprintf("artwork/%d/", id))) {
						test.Fatal("resolved track still used a per-track image source", page.path, id)
					}
				}
			}
			playback := call(app, "POST", path("next_song/"), 1, url.Values{"next_track": {"1"}}, true)
			requireCode(test, playback, 200)
			for _, size := range []int{96, 128, 192, 256, 384, 512} {
				want := fmt.Sprintf(`data-artwork%d="%s"`, size, app.Store.URL("artwork_hash", artwork.hash, size))
				if !strings.Contains(playback.Body.String(), want) {
					test.Fatal("footer/fullscreen/Media Session metadata lost a canonical URL", want)
				}
			}
			uploadArtworkForTest(test, app, 1, artworkColorPNG(test, color.RGBA{0, 0, 255, 255}))
			lookup := call(app, "GET", path("artwork/1/256.jpg"), 0, nil, false)
			requireCode(test, lookup, http.StatusFound)
			if lookup.Header().Get("Location") == canonical {
				test.Fatal("upload reused an immutable URL for different pixels")
			}
			oldImage := call(app, "GET", canonical, 0, nil, false)
			requireCode(test, oldImage, 200)
			if !bytes.Equal(image.Body.Bytes(), oldImage.Body.Bytes()) {
				test.Fatal("an upload changed the old shared URL's image")
			}
			page := call(app, "GET", path("album/?q=Album"), 1, nil, true)
			requireCode(test, page, 200)
			for _, location := range []string{canonical, lookup.Header().Get("Location")} {
				if !strings.Contains(page.Body.String(), `src="`+location+`"`) {
					test.Fatal("edited and unedited tracks lost their respective image URLs", location)
				}
			}
		})
	}
}

func TestArtworkHashHTTPValidationAndConditionals(test *testing.T) {
	app := testApp(test)
	track, err := app.Store.Track(test.Context(), 1)
	if err != nil {
		test.Fatal(err)
	}
	if err := os.WriteFile(track.FullPath+".png", artworkTestPNG(test), 0600); err != nil {
		test.Fatal(err)
	}
	lookup := call(app, "GET", "/artwork/1/96.jpg", 0, nil, false)
	requireCode(test, lookup, http.StatusFound)
	location := lookup.Header().Get("Location")
	response := call(app, "GET", location, 0, nil, false)
	requireCode(test, response, 200)
	etag := response.Header().Get("ETag")
	if etag == "" {
		test.Fatal("hash artwork has no stable validator")
	}
	head := call(app, "HEAD", location, 0, nil, true)
	requireCode(test, head, 200)
	if head.Body.Len() != 0 || head.Header().Get("ETag") != etag || head.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		test.Fatal("HEAD lost the immutable image headers", head.Header())
	}
	request := httptest.NewRequest("GET", location, nil)
	request.Header.Set("If-None-Match", etag)
	conditional := httptest.NewRecorder()
	app.ServeHTTP(conditional, request)
	requireCode(test, conditional, http.StatusNotModified)
	if conditional.Body.Len() != 0 || conditional.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || conditional.Header().Get("Vary") != "" {
		test.Fatal("conditional artwork lost its shared immutable cache policy", conditional.Header())
	}
	for _, scenario := range []struct {
		path string
		code int
	}{
		{"/artwork/v1/" + strings.Repeat("a", 64) + "/96.jpg", 404},
		{"/artwork/v1/" + strings.Repeat("z", 64) + "/96.jpg", 404},
		{strings.Replace(location, "/v1/", "/v0/", 1), 404},
		{strings.Replace(location, "/96.jpg", "/100.jpg", 1), 400},
	} {
		response := call(app, "GET", scenario.path, 0, nil, false)
		requireCode(test, response, scenario.code)
		if response.Header().Get("Cache-Control") != "private, no-cache" || response.Header().Get("ETag") != "" {
			test.Fatal("an artwork error received immutable caching", scenario.path, response.Header())
		}
	}
	// Existing immutable images need no source, reference, or SQL read to serve.
	if _, err := app.Store.DB.Exec("DELETE FROM reinhardt_musicfile"); err != nil {
		test.Fatal(err)
	}
	stats := measureAppSQL(test, app)
	cached := call(app, "GET", location, 0, nil, false)
	requireCode(test, cached, 200)
	if stats.calls.Load() != 0 || !bytes.Equal(response.Body.Bytes(), cached.Body.Bytes()) {
		test.Fatal("warm immutable image reopened library state or changed bytes", stats.calls.Load())
	}
}

func TestArtworkHashGeneratesOnlyMatchingContentAtMissingSizes(test *testing.T) {
	app := testApp(test)
	var firstPath, secondPath string
	for _, id := range []int{1, 2} {
		track, err := app.Store.Track(test.Context(), id)
		if err != nil {
			test.Fatal(err)
		}
		if err := os.WriteFile(track.FullPath+".png", artworkTestPNG(test), 0600); err != nil {
			test.Fatal(err)
		}
		if id == 1 {
			firstPath = track.FullPath + ".png"
		} else {
			secondPath = track.FullPath + ".png"
		}
		requireCode(test, callArtwork(app, "GET", fmt.Sprintf("/artwork/%d/96.jpg", id), 0, nil, false), 200)
	}
	artwork, err := app.Store.trackArtwork(test.Context(), "1")
	if err != nil {
		test.Fatal(err)
	}
	// An external source edit has not been reindexed yet. Skip its changed
	// pixels and use another track still carrying the requested original cover.
	if err := os.WriteFile(firstPath, artworkColorPNG(test, color.RGBA{0, 0, 255, 255}), 0600); err != nil {
		test.Fatal(err)
	}
	location := app.Store.URL("artwork_hash", artwork.hash, 512)
	response := call(app, "GET", location, 0, nil, false)
	requireCode(test, response, 200)
	im, err := jpeg.Decode(bytes.NewReader(response.Body.Bytes()))
	if err != nil || im.Bounds().Dx() != 512 || im.Bounds().Dy() != 512 {
		test.Fatal("new hash size was not generated correctly", err)
	}
	red, _, blue, _ := im.At(256, 256).RGBA()
	if red < 60000 || blue > 1000 {
		test.Fatal("changed pixels were published under an existing immutable hash")
	}
	if err := os.Remove(app.artworkThumbnailPath(artwork.hash, 512)); err != nil {
		test.Fatal(err)
	}
	if err := os.Remove(secondPath); err != nil {
		test.Fatal(err)
	}
	missing := call(app, "GET", location, 0, nil, false)
	requireCode(test, missing, 404)
	if missing.Header().Get("Cache-Control") != "private, no-cache" {
		test.Fatal("a missing source or placeholder poisoned an immutable URL")
	}
}

func TestArtworkTemplateURLLoadsReferencesOncePerPage(test *testing.T) {
	for _, cached := range []bool{false, true} {
		test.Run(fmt.Sprintf("cached=%v", cached), func(test *testing.T) {
			app := testApp(test)
			tracks, err := app.Store.Tracks(test.Context(), "ORDER BY m.id")
			if err != nil {
				test.Fatal(err)
			}
			if cached {
				for _, track := range tracks {
					track.artworkHashLoaded = false
				}
			}
			page := templateDomainValue(TemplateContext{"music_files": tracks}).(TemplateContext)
			stats := measureAppSQL(test, app)
			artworkURL := app.Store.templateArtworkURL(test.Context(), page)
			if stats.calls.Load() != 0 {
				test.Fatal("a page without artwork queried the artwork mapping")
			}
			for _, track := range tracks {
				for _, size := range []int{96, 128, 192, 256, 384, 512} {
					location, err := artworkURL(track.ID, size, track.ModifiedAt)
					if err != nil || location != app.Store.URL("artwork", track.ID, size, track.ModifiedAt) {
						test.Fatal("unresolved artwork lost its versioned lookup URL", location, err)
					}
				}
			}
			wantQueries := int64(0)
			if cached {
				wantQueries = 1
			}
			if stats.calls.Load() != wantQueries || stats.writes.Load() != 0 {
				test.Fatal("artwork URL rendering repeated database work per track or size", stats.calls.Load(), stats.writes.Load())
			}
		})
	}
}
