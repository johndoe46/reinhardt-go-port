package reinhardt

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	id3 "github.com/bogem/id3v2/v2"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (fn transportFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func testApp(testingContext testing.TB) *App {
	testingContext.Helper()
	root := testingContext.TempDir()
	config := Config{"DATABASE_PATH": filepath.Join(root, "db.sqlite3"), "BASE_DIR": root, "COVERS_DIR": filepath.Join(root, "covers"), "ALLOWED_HOSTS": "*", "LANGUAGE_CODE": "en-us", "SANITIZE_TAGS": "1", "MAX_UPLOAD_BYTES": "1048576", "SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS": "10800", "SECRET_KEY": "test-secret"}
	store, err := openStore(config)
	if err != nil {
		testingContext.Fatal(err)
	}
	testingContext.Cleanup(func() { store.DB.Close() })
	for index, name := range []string{"alice", "bob"} {
		_, err = store.DB.Exec(`INSERT INTO auth_user(id,username,is_superuser,email,date_joined) VALUES(?,?,?,?,?)`, index+1, name, index == 0, name+"@example.com", now())
		if err != nil {
			testingContext.Fatal(err)
		}
		if _, err = store.Preferences(int64(index + 1)); err != nil {
			testingContext.Fatal(err)
		}
		_, err = store.DB.Exec("INSERT INTO go_session(token,owner_id,expires) VALUES(?,?,?)", fmt.Sprintf("session-%d", index+1), index+1, time.Now().Unix()+3600)
		if err != nil {
			testingContext.Fatal(err)
		}
	}
	music := filepath.Join(root, "music")
	if err = os.MkdirAll(music, 0755); err != nil {
		testingContext.Fatal(err)
	}
	_, err = store.DB.Exec("INSERT INTO reinhardt_musicdirectory(id,local_path,created_at,modified_at) VALUES(1,?,?,?)", music, now(), now())
	if err != nil {
		testingContext.Fatal(err)
	}
	for index, title := range []string{"First tune", "Second tune", "Third tune"} {
		path := fmt.Sprintf("%d.mp3", index+1)
		if err = os.WriteFile(filepath.Join(music, path), []byte("abcdefghijklmnopqrstuvwxyz"), 0644); err != nil {
			testingContext.Fatal(err)
		}
		artist := "Artist"
		album := "Album"
		if index == 2 {
			artist = "Another"
			album = "Other"
		}
		_, err = store.DB.Exec(`INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing) VALUES(1,?,?,?,?,?,'Comment','2001',10,?,?,?,0)`, path, artist, title, album, "jazz", index+1, now(), now())
		if err != nil {
			testingContext.Fatal(err)
		}
	}
	app, err := newApp(store)
	if err != nil {
		testingContext.Fatal(err)
	}
	app.Client = &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unexpected external request")
	})}
	app.AudioClient = &http.Client{Transport: app.Client.Transport}
	return app
}
func call(app *App, method, path string, owner int, form url.Values, up bool) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		form.Set("csrfmiddlewaretoken", strings.Repeat("a", 64))
		body = strings.NewReader(form.Encode())
	}
	request := httptest.NewRequest(method, path, body)
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if owner > 0 {
		request.AddCookie(&http.Cookie{Name: "sessionid", Value: fmt.Sprintf("session-%d", owner)})
	}
	request.AddCookie(&http.Cookie{Name: "csrftoken", Value: strings.Repeat("a", 64)})
	if up {
		request.Header.Set("X-Up-Target", "main")
	}
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	return response
}
func requireCode(test *testing.T, response *httptest.ResponseRecorder, code int) {
	test.Helper()
	if response.Code != code {
		test.Fatalf("HTTP %d, want %d: %s", response.Code, code, response.Body.String())
	}
}
func mustTag(test *testing.T, app *App, owner int64, name string, tracks []int64) int64 {
	test.Helper()
	if _, err := app.Store.AddTracks(owner, name, tracks); err != nil {
		test.Fatal(err)
	}
	id, err := app.Store.AddTag(owner, name)
	if err != nil {
		test.Fatal(err)
	}
	return id
}

func TestAllPagesAndFragments(test *testing.T) {
	app := testApp(test)
	tag := mustTag(test, app, 1, "my list", []int64{1, 2})
	mustTag(test, app, 1, "queue", []int64{1, 2})
	mustTag(test, app, 2, "friend list", []int64{3})
	paths := []string{"/", "/artists/", "/artists/?q=A", "/genres/", "/artist/?q=Artist", "/album/?q=Album", "/genre/?q=jazz", "/search/?sq=tune", "/search/", "/dj/", "/tags/", fmt.Sprintf("/tag/?q=%d", tag), "/favorites/", "/queue/", "/preferences/", "/friends/", "/friend_tag/?tag=3", "/music_file_tagger/?q=1", "/music_file_dropdown/?q=1", fmt.Sprintf("/rename_tag/%d", tag), "/artist/add_to_tag/?artist=Artist", "/search/add_to_tag/?sq=tune", "/queue/add_to_tag/", "/track_details_modal/1", "/artwork_modal/1/512.jpg", "/ytrip/", "/ytrip/failures/", "/upload/", "/accounts/login/"}
	for _, path := range paths {
		for _, up := range []bool{false, true} {
			test.Run(fmt.Sprintf("%s/up=%v", path, up), func(test *testing.T) {
				response := call(app, "GET", path, 1, nil, up)
				requireCode(test, response, 200)
				if response.Body.Len() == 0 {
					test.Fatal("empty page")
				}
				if strings.Contains(response.Body.String(), "{%") || strings.Contains(response.Body.String(), "{{") {
					test.Fatal("unrendered template")
				}
			})
		}
	}
}
func TestUnpolyPreservesPlayerAndPreference(test *testing.T) {
	app := testApp(test)
	mustTag(test, app, 1, "queue", []int64{1})
	response := call(app, "POST", "/next_song/", 1, url.Values{"list_item": {"1"}}, true)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `data-url="/stream/1/"`) {
		test.Fatal(response.Body.String())
	}
	response = call(app, "GET", "/", 1, nil, false)
	requireCode(test, response, 200)
	for _, expectedMarkup := range []string{`up-keep id="player"`, `data-url="/stream/1/"`, `data-oscilloscope="true"`} {
		if !strings.Contains(response.Body.String(), expectedMarkup) {
			test.Fatalf("missing %s", expectedMarkup)
		}
	}
	response = call(app, "GET", "/", 1, nil, true)
	requireCode(test, response, 200)
	if strings.Contains(response.Body.String(), `id="player"`) {
		test.Fatal("fragment replaced persistent player")
	}
	if !strings.Contains(response.Header().Get("Vary"), "X-Up-Target") {
		test.Fatal("missing Vary")
	}
	response = call(app, "POST", "/preferences/", 1, url.Values{}, true)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `data-player-oscilloscope="false"`) {
		test.Fatal(response.Body.String())
	}
	preferences, err := app.Store.Preferences(2)
	if err != nil || !preferences.B("show_oscilloscope") {
		test.Fatal("changed another user's preferences")
	}
}

func TestStreamingAndDelegate(test *testing.T) {
	app := testApp(test)
	request := httptest.NewRequest("GET", "/stream/1/", nil)
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	request.Header.Set("Range", "bytes=3-6")
	request.Header.Set("Accept-Encoding", "gzip")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, 206)
	if response.Body.String() != "defg" || response.Header().Get("Content-Encoding") != "" || response.Header().Get("Content-Range") != "bytes 3-6/26" {
		test.Fatal(response.Result().Header, response.Body.String())
	}
	request = httptest.NewRequest("HEAD", "/stream/1/", nil)
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, 200)
	if response.Body.Len() != 0 || response.Header().Get("Content-Length") != "26" {
		test.Fatal(response.Result().Header)
	}
	request = httptest.NewRequest("GET", "/stream/1/", nil)
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	request.Header.Set("Range", "bytes=99-")
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, 416)
	app.Store.Config["DELEGATE_DOWNLOAD_PREFIX"] = "https://audio.example/library"
	closed := false
	app.AudioClient.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://audio.example/library/1.mp3" || req.Header.Get("Range") != "bytes=3-6" || req.Header.Get("Accept-Encoding") != "identity" {
			test.Fatal(req.URL, req.Header)
		}
		return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {"bytes 3-6/26"}, "Content-Length": {"4"}, "Content-Type": {"audio/mpeg"}}, Body: &closeBody{Reader: strings.NewReader("defg"), closed: &closed}}, nil
	})
	request = httptest.NewRequest("GET", "/stream/1/", nil)
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	request.Header.Set("Range", "bytes=3-6")
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, 206)
	if !closed || response.Body.String() != "defg" {
		test.Fatal("upstream not relayed or closed")
	}
}

type closeBody struct {
	io.Reader
	closed *bool
}

func (body *closeBody) Close() error { *body.closed = true; return nil }

func TestSharedPlaylistBoundariesAndZIP(test *testing.T) {
	app := testApp(test)
	id := mustTag(test, app, 1, "My list", []int64{2, 1})
	response := call(app, "POST", "/tags/", 1, url.Values{"action": {"share_tag"}, "tag_pk": {fmt.Sprint(id)}}, false)
	requireCode(test, response, 200)
	tag, err := app.Store.Tag(id, 1)
	if err != nil {
		test.Fatal(err)
	}
	path := tag.S("share_url")
	app.Store.Config["LANGUAGE_CODE"] = "fr-fr"
	response = call(app, "GET", path, 0, nil, false)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), "Mélanger") {
		test.Fatal("missing French shuffle translation")
	}
	if !strings.Contains(response.Body.String(), "Télécharger Second tune") {
		test.Fatal("missing block translation")
	}
	response = call(app, "GET", path+"track/3/", 0, nil, false)
	requireCode(test, response, 404)
	response = call(app, "GET", path+"track/1/", 0, nil, false)
	requireCode(test, response, 200)
	response = call(app, "GET", strings.Replace(path, tag.S("share_hash"), "invalid", 1), 0, nil, false)
	requireCode(test, response, 404)
	response = call(app, "GET", path+"download/", 0, nil, false)
	requireCode(test, response, 200)
	archiveReader, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		test.Fatal(err)
	}
	if len(archiveReader.File) != 2 || !strings.Contains(archiveReader.File[0].Name, "second-tune") {
		test.Fatal(archiveReader.File)
	}
	app.Store.Config["SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS"] = "20"
	response = call(app, "GET", path+"download/", 0, nil, false)
	requireCode(test, response, 404)
	response = call(app, "POST", "/tags/", 2, url.Values{"action": {"unshare_tag"}, "tag_pk": {fmt.Sprint(id)}}, false)
	requireCode(test, response, 404)
	response = call(app, "POST", "/tags/", 1, url.Values{"action": {"unshare_tag"}, "tag_pk": {fmt.Sprint(id)}}, false)
	requireCode(test, response, 200)
	response = call(app, "GET", path, 0, nil, false)
	requireCode(test, response, 404)
}

func TestBulkActionsAndOwnership(test *testing.T) {
	app := testApp(test)
	mustTag(test, app, 1, "queue", []int64{2, 1})
	foreign := mustTag(test, app, 2, "foreign", []int64{})
	for _, source := range []string{"artist", "search", "queue"} {
		values := url.Values{"artist": {"Artist"}, "sq": {"tune"}, "tag_choice": {"new"}, "new_tag": {source + " playlist"}}
		response := call(app, "POST", "/"+source+"/add_to_tag/", 1, values, true)
		requireCode(test, response, 200)
		tag, err := app.Store.One("SELECT id FROM reinhardt_tag WHERE label=? AND owner_id=1", source+" playlist")
		if err != nil {
			test.Fatal(err)
		}
		tracks, err := app.Store.TagTracks(tag.N("id"))
		if err != nil {
			test.Fatal(err)
		}
		expected := 2
		if source == "search" {
			expected = 3
		}
		if len(tracks) != expected {
			test.Fatal(source, tracks)
		}
		if source == "queue" && tracks[0].N("id") != 2 {
			test.Fatal("queue ordering lost")
		}
		values.Set("tag_choice", "existing")
		values.Set("tag_pk", fmt.Sprint(foreign))
		response = call(app, "POST", "/"+source+"/add_to_tag/", 1, values, true)
		requireCode(test, response, 404)
	}
	if _, err := app.Store.AddTracks(1, "queue", []int64{1, 1, 2}); err != nil {
		test.Fatal(err)
	}
	queue, err := app.Store.Queue(1)
	if err != nil || len(queue) != 2 || queue[0].N("music_file_id") != 2 {
		test.Fatal("duplicates or ordering changed", queue, err)
	}
	queueTag, err := app.Store.One("SELECT id FROM reinhardt_tag WHERE label='queue' AND owner_id=1")
	if err != nil {
		test.Fatal(err)
	}
	response := call(app, "POST", "/reorder_queue/", 1, url.Values{"list_pk": {queueTag.S("id")}, "new_order": {`[{"item":1,"rank":1},{"item":2,"rank":2}]`}}, true)
	requireCode(test, response, 200)
	response = call(app, "POST", "/reorder_queue/", 1, url.Values{"list_pk": {fmt.Sprint(foreign)}, "new_order": {`[{"item":1,"rank":1}]`}}, true)
	requireCode(test, response, 404)
}

func TestSearchAPIValidationAndFTS(test *testing.T) {
	app := testApp(test)
	response := call(app, "GET", "/api/music_files/search/?sq=tune&limit=1&offset=1&order_by=title", 1, nil, false)
	requireCode(test, response, 200)
	var data struct {
		Count   int
		Results []Row
	}
	if err := json.Unmarshal(response.Body.Bytes(), &data); err != nil {
		test.Fatal(err)
	}
	if data.Count != 3 || len(data.Results) != 1 || data.Results[0].S("title") != "Second tune" {
		test.Fatal(response.Body.String())
	}
	for _, query := range []string{"limit=-1", "offset=bad", "artist__contains=A", "missing=maybe", "id=x", "created_at=invalid", "order_by=id"} {
		response = call(app, "GET", "/api/music_files/search/?"+query, 1, nil, false)
		requireCode(test, response, 400)
	}
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET title='Changed' WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	response = call(app, "GET", "/api/music_files/search/?sq=Changed", 1, nil, false)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `"count":1`) {
		test.Fatal("FTS update failed", response.Body.String())
	}
}

func TestCSRFMethodsLoginAndPrefix(test *testing.T) {
	app := testApp(test)
	response := call(app, "GET", "/queue/", 0, nil, false)
	requireCode(test, response, 302)
	response = call(app, "GET", "/next_song/", 1, nil, false)
	requireCode(test, response, 405)
	request := httptest.NewRequest("POST", "/preferences/", strings.NewReader("autofeed_queue=on"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, 403)
	_, err := app.Store.DB.Exec("UPDATE reinhardt_preferences SET access_token='invite-secret' WHERE owner_id=1")
	if err != nil {
		test.Fatal(err)
	}
	response = call(app, "GET", "/invite/?q=invite-secret&next=https://evil.example/", 0, nil, false)
	requireCode(test, response, 302)
	if response.Header().Get("Location") != "/" {
		test.Fatal("unsafe redirect", response.Header().Get("Location"))
	}
	if len(response.Result().Cookies()) < 2 {
		test.Fatal("no session or rotated CSRF")
	}
	app.Store.Config["URL_PREFIX"] = "music/"
	response = call(app, "GET", "/music/", 1, nil, false)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `src="/music/static/player.js"`) {
		test.Fatal("prefix not applied")
	}
	response = call(app, "GET", "/music/static/unpoly.min.js", 0, nil, false)
	requireCode(test, response, 200)
	response = call(app, "GET", "/", 1, nil, false)
	requireCode(test, response, 404)
}

func TestArtworkUploadPermissionsValidationAndCache(test *testing.T) {
	app := testApp(test)
	originalTrack, err := app.Store.Track(1)
	if err != nil {
		test.Fatal(err)
	}
	im := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for pixelY := 0; pixelY < 100; pixelY++ {
		for pixelX := 0; pixelX < 200; pixelX++ {
			im.Set(pixelX, pixelY, color.RGBA{255, 0, 0, 255})
		}
	}
	var imageBytes bytes.Buffer
	png.Encode(&imageBytes, im)
	response := call(app, "GET", "/artwork/1/96.jpg", 0, nil, false)
	requireCode(test, response, 200)
	first := append([]byte(nil), response.Body.Bytes()...)
	response = call(app, "GET", "/artwork/1/96.jpg", 0, nil, false)
	if !bytes.Equal(first, response.Body.Bytes()) {
		test.Fatal("unstable fallback")
	}
	files, _ := filepath.Glob(filepath.Join(app.Store.Config["COVERS_DIR"], "thumb-*"))
	if len(files) != 0 {
		test.Fatal("fallback cached")
	}
	send := func(owner int, data []byte) *httptest.ResponseRecorder {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		for _, pair := range [][2]string{{"artist", "New artist"}, {"title", "New title"}, {"year", "2024"}, {"csrfmiddlewaretoken", strings.Repeat("a", 64)}} {
			writer.WriteField(pair[0], pair[1])
		}
		part, _ := writer.CreateFormFile("track_image", "spoofed.jpg")
		part.Write(data)
		writer.Close()
		request := httptest.NewRequest("POST", "/track_details_modal/1", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request.AddCookie(&http.Cookie{Name: "sessionid", Value: fmt.Sprintf("session-%d", owner)})
		request.AddCookie(&http.Cookie{Name: "csrftoken", Value: strings.Repeat("a", 64)})
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		return response
	}
	response = send(2, imageBytes.Bytes())
	requireCode(test, response, 403)
	response = send(1, []byte("invalid"))
	requireCode(test, response, 400)
	track, err := app.Store.Track(1)
	if err != nil || track.S("artist") != "Artist" {
		test.Fatal("invalid upload mutated database")
	}
	response = send(1, imageBytes.Bytes())
	requireCode(test, response, 302)
	if _, err = os.Stat(originalTrack.S("full_path") + ".png"); err != nil {
		test.Fatal("format not derived from content", err)
	}
	response = call(app, "GET", "/artwork/1/96.jpg", 0, nil, false)
	requireCode(test, response, 200)
	decoded, err := jpeg.Decode(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		test.Fatal(err)
	}
	if decoded.Bounds().Dx() != 96 || decoded.Bounds().Dy() != 96 {
		test.Fatal(decoded.Bounds())
	}
	rr, gg, _, _ := decoded.At(48, 48).RGBA()
	if rr < 60000 || gg > 1000 {
		test.Fatal("wrong cover crop")
	}
	files, _ = filepath.Glob(filepath.Join(app.Store.Config["COVERS_DIR"], "thumb-*"))
	if len(files) != 1 {
		test.Fatal("cover not cached")
	}
}

func TestDatabaseSnapshotAndExistingSchema(test *testing.T) {
	app := testApp(test)
	response := call(app, "GET", "/preferences/database/", 2, nil, false)
	requireCode(test, response, 403)
	response = call(app, "GET", "/preferences/database/", 1, nil, false)
	requireCode(test, response, 200)
	if !bytes.HasPrefix(response.Body.Bytes(), []byte("SQLite format 3")) {
		test.Fatal("invalid snapshot")
	}
	path := filepath.Join(test.TempDir(), "copy.sqlite3")
	if err := os.WriteFile(path, response.Body.Bytes(), 0600); err != nil {
		test.Fatal(err)
	}
	config := Config{}
	for configKey, configValue := range app.Store.Config {
		config[configKey] = configValue
	}
	config["DATABASE_PATH"] = path
	store, err := openStore(config)
	if err != nil {
		test.Fatal(err)
	}
	defer store.DB.Close()
	tracks, err := store.Tracks("")
	if err != nil || len(tracks) != 3 {
		test.Fatal(tracks, err)
	}
}

func TestNativeIndexAndID3(test *testing.T) {
	app := testApp(test)
	dir, err := app.Store.One("SELECT * FROM reinhardt_musicdirectory WHERE id=1")
	if err != nil {
		test.Fatal(err)
	}
	path := filepath.Join(dir.S("local_path"), "tagged.mp3")
	if err = os.WriteFile(path, make([]byte, 32), 0644); err != nil {
		test.Fatal(err)
	}
	track := Row{"full_path": path, "relative_path": "tagged.mp3", "artist": "the test & friends", "title": "Tagged title", "album": "Record", "genre": "(18)Techno", "year": "2020", "comment": "Hello"}
	if err = writeID3(track, nil, ""); err != nil {
		test.Fatal(err)
	}
	tags, err := id3.Open(path, id3.Options{Parse: true})
	if err != nil {
		test.Fatal(err)
	}
	if tags.Artist() != track.S("artist") {
		test.Fatal(tags.Artist())
	}
	tags.Close()
	if err = app.Store.indexFile(path, dir); err != nil {
		test.Fatal(err)
	}
	tracks, err := app.Store.Search("Tagged")
	if err != nil || len(tracks) != 1 {
		test.Fatal(tracks, err)
	}
	if tracks[0].S("artist") != "Test And Friends (the)" || tracks[0].S("year") != "2020" || tracks[0].S("genre") != "techno" {
		test.Fatalf("metadata: %#v", tracks[0])
	}
	if err = os.Remove(path); err != nil {
		test.Fatal(err)
	}
	if err = app.Store.Index(io.Discard); err != nil {
		test.Fatal(err)
	}
	track, err = app.Store.Track(tracks[0].N("id"))
	if err != nil || !track.B("missing") {
		test.Fatal(track, err)
	}
}

func TestJobsFailureAndYouTubeValidation(test *testing.T) {
	app := testApp(test)
	for _, raw := range []string{"https://evil.example/watch?v=abcdefghijk", "file:///tmp/a", "https://www.youtube.com:443/watch?v=abcdefghijk", "https://youtu.be/notvalid", "https://user@youtube.com/watch?v=abcdefghijk"} {
		if _, err := youtubeURL(raw); err == nil {
			test.Fatal("accepted", raw)
		}
	}
	for _, raw := range []string{"https://youtu.be/abcdefghijk", "https://www.youtube.com/watch?v=abcdefghijk", "https://music.youtube.com/shorts/abcdefghijk"} {
		if canonicalURL, err := youtubeURL(raw); err != nil || canonicalURL != "https://www.youtube.com/watch?v=abcdefghijk" {
			test.Fatal(canonicalURL, err)
		}
	}
	ripMetadata := Row{"url": "invalid", "artist": "Artist", "title": "Title"}
	if err := app.Store.Enqueue("youtube", ripMetadata); err != nil {
		test.Fatal(err)
	}
	if err := app.Work(context.Background(), true); err != nil {
		test.Fatal(err)
	}
	failure, err := app.Store.One("SELECT * FROM reinhardt_youtuberipfailure")
	if err != nil {
		test.Fatal(err)
	}
	if failure.S("artist") != "Artist" {
		test.Fatal(failure)
	}
	job, err := app.Store.One("SELECT * FROM go_job")
	if err != nil || job.N("attempts") != 1 || job.N("run_at") <= time.Now().Unix() {
		test.Fatal(job, err)
	}
}

func TestRejectArchiveTraversal(test *testing.T) {
	root := test.TempDir()
	path := filepath.Join(root, "upload.zip")
	var buffer bytes.Buffer
	archiveWriter := zip.NewWriter(&buffer)
	file, _ := archiveWriter.Create("../escape.mp3")
	file.Write([]byte("test"))
	archiveWriter.Close()
	os.WriteFile(path, buffer.Bytes(), 0600)
	if err := extractArchive(path, root, 1000); err == nil {
		test.Fatal("accepted traversal")
	}
}

func TestAssetsAreByteIdenticalToDjango(test *testing.T) {
	// Go runs package tests from internal/reinhardt; Django is a sibling checkout.
	djangoRoot := filepath.Join("..", "..", "..", "reinhardt", "reinhardt")
	if _, err := os.Stat(filepath.Join(djangoRoot, "templates")); os.IsNotExist(err) {
		test.Skip("Django source checkout not present")
	}
	err := fs.WalkDir(assets, "static", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		original, err := os.ReadFile(filepath.Join(djangoRoot, path))
		if err != nil {
			return err
		}
		copy, err := assets.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(original, copy) {
			return fmt.Errorf("asset differs: %s", path)
		}
		return nil
	})
	if err != nil {
		test.Fatal(err)
	}
}

func TestConfigDotenvAndEnvironmentPrecedence(test *testing.T) {
	root := test.TempDir()
	file := filepath.Join(root, ".env")
	if err := os.WriteFile(file, []byte("BASE_DIR="+root+"\nURL_PREFIX=music\nEMAIL_PORT=2525\nMUSIC_IMPORT_DIR='/tmp/import with spaces'\n"), 0600); err != nil {
		test.Fatal(err)
	}
	test.Setenv("EMAIL_PORT", "1025")
	config, err := loadConfigFromHome(root, file, filepath.Join(root, "explicit.sqlite3"))
	if err != nil {
		test.Fatal(err)
	}
	if config["EMAIL_PORT"] != "1025" || config["URL_PREFIX"] != "music/" || config["MUSIC_IMPORT_DIR"] != "/tmp/import with spaces" || config["DATABASE_PATH"] != filepath.Join(root, "explicit.sqlite3") {
		test.Fatal("configuration precedence failed")
	}
}
