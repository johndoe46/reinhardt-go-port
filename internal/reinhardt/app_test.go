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

func (fn transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func testApp(t testing.TB) *App {
	t.Helper()
	root := t.TempDir()
	c := Config{"DATABASE_PATH": filepath.Join(root, "db.sqlite3"), "BASE_DIR": root, "COVERS_DIR": filepath.Join(root, "covers"), "ALLOWED_HOSTS": "*", "LANGUAGE_CODE": "en-us", "SANITIZE_TAGS": "1", "MAX_UPLOAD_BYTES": "1048576", "SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS": "10800", "SECRET_KEY": "test-secret"}
	s, e := openStore(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.DB.Close() })
	for i, name := range []string{"alice", "bob"} {
		_, e = s.DB.Exec(`INSERT INTO auth_user(id,username,is_superuser,email,date_joined) VALUES(?,?,?,?,?)`, i+1, name, i == 0, name+"@example.com", now())
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Preferences(int64(i + 1)); e != nil {
			t.Fatal(e)
		}
		_, e = s.DB.Exec("INSERT INTO go_session(token,owner_id,expires) VALUES(?,?,?)", fmt.Sprintf("session-%d", i+1), i+1, time.Now().Unix()+3600)
		if e != nil {
			t.Fatal(e)
		}
	}
	music := filepath.Join(root, "music")
	if e = os.MkdirAll(music, 0755); e != nil {
		t.Fatal(e)
	}
	_, e = s.DB.Exec("INSERT INTO reinhardt_musicdirectory(id,local_path,created_at,modified_at) VALUES(1,?,?,?)", music, now(), now())
	if e != nil {
		t.Fatal(e)
	}
	for i, title := range []string{"First tune", "Second tune", "Third tune"} {
		path := fmt.Sprintf("%d.mp3", i+1)
		if e = os.WriteFile(filepath.Join(music, path), []byte("abcdefghijklmnopqrstuvwxyz"), 0644); e != nil {
			t.Fatal(e)
		}
		artist := "Artist"
		album := "Album"
		if i == 2 {
			artist = "Another"
			album = "Other"
		}
		_, e = s.DB.Exec(`INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing) VALUES(1,?,?,?,?,?,'Comment','2001',10,?,?,?,0)`, path, artist, title, album, "jazz", i+1, now(), now())
		if e != nil {
			t.Fatal(e)
		}
	}
	a, e := newApp(s)
	if e != nil {
		t.Fatal(e)
	}
	a.Client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) { return nil, fmt.Errorf("unexpected external request") })}
	a.AudioClient = &http.Client{Transport: a.Client.Transport}
	return a
}
func call(a *App, method, path string, owner int, form url.Values, up bool) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		form.Set("csrfmiddlewaretoken", strings.Repeat("a", 64))
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if owner > 0 {
		r.AddCookie(&http.Cookie{Name: "sessionid", Value: fmt.Sprintf("session-%d", owner)})
	}
	r.AddCookie(&http.Cookie{Name: "csrftoken", Value: strings.Repeat("a", 64)})
	if up {
		r.Header.Set("X-Up-Target", "main")
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func requireCode(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("HTTP %d, want %d: %s", w.Code, code, w.Body.String())
	}
}
func mustTag(t *testing.T, a *App, owner int64, name string, tracks []int64) int64 {
	t.Helper()
	if _, e := a.S.AddTracks(owner, name, tracks); e != nil {
		t.Fatal(e)
	}
	id, e := a.S.AddTag(owner, name)
	if e != nil {
		t.Fatal(e)
	}
	return id
}

func TestAllPagesAndFragments(t *testing.T) {
	a := testApp(t)
	tag := mustTag(t, a, 1, "my list", []int64{1, 2})
	mustTag(t, a, 1, "queue", []int64{1, 2})
	mustTag(t, a, 2, "friend list", []int64{3})
	paths := []string{"/", "/artists/", "/artists/?q=A", "/genres/", "/artist/?q=Artist", "/album/?q=Album", "/genre/?q=jazz", "/search/?sq=tune", "/search/", "/dj/", "/tags/", fmt.Sprintf("/tag/?q=%d", tag), "/favorites/", "/queue/", "/preferences/", "/friends/", "/friend_tag/?tag=3", "/music_file_tagger/?q=1", "/music_file_dropdown/?q=1", fmt.Sprintf("/rename_tag/%d", tag), "/artist/add_to_tag/?artist=Artist", "/search/add_to_tag/?sq=tune", "/queue/add_to_tag/", "/track_details_modal/1", "/artwork_modal/1/512.jpg", "/ytrip/", "/ytrip/failures/", "/upload/", "/accounts/login/"}
	for _, path := range paths {
		for _, up := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/up=%v", path, up), func(t *testing.T) {
				w := call(a, "GET", path, 1, nil, up)
				requireCode(t, w, 200)
				if w.Body.Len() == 0 {
					t.Fatal("empty page")
				}
				if strings.Contains(w.Body.String(), "{%") || strings.Contains(w.Body.String(), "{{") {
					t.Fatal("unrendered template")
				}
			})
		}
	}
}
func TestUnpolyPreservesPlayerAndPreference(t *testing.T) {
	a := testApp(t)
	mustTag(t, a, 1, "queue", []int64{1})
	w := call(a, "POST", "/next_song/", 1, url.Values{"list_item": {"1"}}, true)
	requireCode(t, w, 200)
	if !strings.Contains(w.Body.String(), `data-url="/stream/1/"`) {
		t.Fatal(w.Body.String())
	}
	w = call(a, "GET", "/", 1, nil, false)
	requireCode(t, w, 200)
	for _, s := range []string{`up-keep id="player"`, `data-url="/stream/1/"`, `data-oscilloscope="true"`} {
		if !strings.Contains(w.Body.String(), s) {
			t.Fatalf("missing %s", s)
		}
	}
	w = call(a, "GET", "/", 1, nil, true)
	requireCode(t, w, 200)
	if strings.Contains(w.Body.String(), `id="player"`) {
		t.Fatal("fragment replaced persistent player")
	}
	if !strings.Contains(w.Header().Get("Vary"), "X-Up-Target") {
		t.Fatal("missing Vary")
	}
	w = call(a, "POST", "/preferences/", 1, url.Values{}, true)
	requireCode(t, w, 200)
	if !strings.Contains(w.Body.String(), `data-player-oscilloscope="false"`) {
		t.Fatal(w.Body.String())
	}
	p, e := a.S.Preferences(2)
	if e != nil || !p.B("show_oscilloscope") {
		t.Fatal("changed another user's preferences")
	}
}

func TestStreamingAndDelegate(t *testing.T) {
	a := testApp(t)
	r := httptest.NewRequest("GET", "/stream/1/", nil)
	r.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	r.Header.Set("Range", "bytes=3-6")
	r.Header.Set("Accept-Encoding", "gzip")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	requireCode(t, w, 206)
	if w.Body.String() != "defg" || w.Header().Get("Content-Encoding") != "" || w.Header().Get("Content-Range") != "bytes 3-6/26" {
		t.Fatal(w.Result().Header, w.Body.String())
	}
	r = httptest.NewRequest("HEAD", "/stream/1/", nil)
	r.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	requireCode(t, w, 200)
	if w.Body.Len() != 0 || w.Header().Get("Content-Length") != "26" {
		t.Fatal(w.Result().Header)
	}
	r = httptest.NewRequest("GET", "/stream/1/", nil)
	r.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	r.Header.Set("Range", "bytes=99-")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	requireCode(t, w, 416)
	a.S.C["DELEGATE_DOWNLOAD_PREFIX"] = "https://audio.example/library"
	closed := false
	a.AudioClient.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://audio.example/library/1.mp3" || req.Header.Get("Range") != "bytes=3-6" || req.Header.Get("Accept-Encoding") != "identity" {
			t.Fatal(req.URL, req.Header)
		}
		return &http.Response{StatusCode: 206, Header: http.Header{"Content-Range": {"bytes 3-6/26"}, "Content-Length": {"4"}, "Content-Type": {"audio/mpeg"}}, Body: &closeBody{Reader: strings.NewReader("defg"), closed: &closed}}, nil
	})
	r = httptest.NewRequest("GET", "/stream/1/", nil)
	r.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	r.Header.Set("Range", "bytes=3-6")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	requireCode(t, w, 206)
	if !closed || w.Body.String() != "defg" {
		t.Fatal("upstream not relayed or closed")
	}
}

type closeBody struct {
	io.Reader
	closed *bool
}

func (c *closeBody) Close() error { *c.closed = true; return nil }

func TestSharedPlaylistBoundariesAndZIP(t *testing.T) {
	a := testApp(t)
	id := mustTag(t, a, 1, "My list", []int64{2, 1})
	w := call(a, "POST", "/tags/", 1, url.Values{"action": {"share_tag"}, "tag_pk": {fmt.Sprint(id)}}, false)
	requireCode(t, w, 200)
	tag, e := a.S.Tag(id, 1)
	if e != nil {
		t.Fatal(e)
	}
	path := tag.S("share_url")
	a.S.C["LANGUAGE_CODE"] = "fr-fr"
	w = call(a, "GET", path, 0, nil, false)
	requireCode(t, w, 200)
	if !strings.Contains(w.Body.String(), "Mélanger") {
		t.Fatal("missing French shuffle translation")
	}
	if !strings.Contains(w.Body.String(), "Télécharger Second tune") {
		t.Fatal("missing block translation")
	}
	w = call(a, "GET", path+"track/3/", 0, nil, false)
	requireCode(t, w, 404)
	w = call(a, "GET", path+"track/1/", 0, nil, false)
	requireCode(t, w, 200)
	w = call(a, "GET", strings.Replace(path, tag.S("share_hash"), "invalid", 1), 0, nil, false)
	requireCode(t, w, 404)
	w = call(a, "GET", path+"download/", 0, nil, false)
	requireCode(t, w, 200)
	z, e := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if e != nil {
		t.Fatal(e)
	}
	if len(z.File) != 2 || !strings.Contains(z.File[0].Name, "second-tune") {
		t.Fatal(z.File)
	}
	a.S.C["SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS"] = "20"
	w = call(a, "GET", path+"download/", 0, nil, false)
	requireCode(t, w, 404)
	w = call(a, "POST", "/tags/", 2, url.Values{"action": {"unshare_tag"}, "tag_pk": {fmt.Sprint(id)}}, false)
	requireCode(t, w, 404)
	w = call(a, "POST", "/tags/", 1, url.Values{"action": {"unshare_tag"}, "tag_pk": {fmt.Sprint(id)}}, false)
	requireCode(t, w, 200)
	w = call(a, "GET", path, 0, nil, false)
	requireCode(t, w, 404)
}

func TestBulkActionsAndOwnership(t *testing.T) {
	a := testApp(t)
	mustTag(t, a, 1, "queue", []int64{2, 1})
	foreign := mustTag(t, a, 2, "foreign", []int64{})
	for _, source := range []string{"artist", "search", "queue"} {
		values := url.Values{"artist": {"Artist"}, "sq": {"tune"}, "tag_choice": {"new"}, "new_tag": {source + " playlist"}}
		w := call(a, "POST", "/"+source+"/add_to_tag/", 1, values, true)
		requireCode(t, w, 200)
		tag, e := a.S.One("SELECT id FROM reinhardt_tag WHERE label=? AND owner_id=1", source+" playlist")
		if e != nil {
			t.Fatal(e)
		}
		tracks, e := a.S.TagTracks(tag.N("id"))
		if e != nil {
			t.Fatal(e)
		}
		expected := 2
		if source == "search" {
			expected = 3
		}
		if len(tracks) != expected {
			t.Fatal(source, tracks)
		}
		if source == "queue" && tracks[0].N("id") != 2 {
			t.Fatal("queue ordering lost")
		}
		values.Set("tag_choice", "existing")
		values.Set("tag_pk", fmt.Sprint(foreign))
		w = call(a, "POST", "/"+source+"/add_to_tag/", 1, values, true)
		requireCode(t, w, 404)
	}
	if _, e := a.S.AddTracks(1, "queue", []int64{1, 1, 2}); e != nil {
		t.Fatal(e)
	}
	queue, e := a.S.Queue(1)
	if e != nil || len(queue) != 2 || queue[0].N("music_file_id") != 2 {
		t.Fatal("duplicates or ordering changed", queue, e)
	}
	queueTag, e := a.S.One("SELECT id FROM reinhardt_tag WHERE label='queue' AND owner_id=1")
	if e != nil {
		t.Fatal(e)
	}
	w := call(a, "POST", "/reorder_queue/", 1, url.Values{"list_pk": {queueTag.S("id")}, "new_order": {`[{"item":1,"rank":1},{"item":2,"rank":2}]`}}, true)
	requireCode(t, w, 200)
	w = call(a, "POST", "/reorder_queue/", 1, url.Values{"list_pk": {fmt.Sprint(foreign)}, "new_order": {`[{"item":1,"rank":1}]`}}, true)
	requireCode(t, w, 404)
}

func TestSearchAPIValidationAndFTS(t *testing.T) {
	a := testApp(t)
	w := call(a, "GET", "/api/music_files/search/?sq=tune&limit=1&offset=1&order_by=title", 1, nil, false)
	requireCode(t, w, 200)
	var data struct {
		Count   int
		Results []Row
	}
	if e := json.Unmarshal(w.Body.Bytes(), &data); e != nil {
		t.Fatal(e)
	}
	if data.Count != 3 || len(data.Results) != 1 || data.Results[0].S("title") != "Second tune" {
		t.Fatal(w.Body.String())
	}
	for _, query := range []string{"limit=-1", "offset=bad", "artist__contains=A", "missing=maybe", "id=x", "created_at=invalid", "order_by=id"} {
		w = call(a, "GET", "/api/music_files/search/?"+query, 1, nil, false)
		requireCode(t, w, 400)
	}
	if _, e := a.S.DB.Exec("UPDATE reinhardt_musicfile SET title='Changed' WHERE id=1"); e != nil {
		t.Fatal(e)
	}
	w = call(a, "GET", "/api/music_files/search/?sq=Changed", 1, nil, false)
	requireCode(t, w, 200)
	if !strings.Contains(w.Body.String(), `"count":1`) {
		t.Fatal("FTS update failed", w.Body.String())
	}
}

func TestCSRFMethodsLoginAndPrefix(t *testing.T) {
	a := testApp(t)
	w := call(a, "GET", "/queue/", 0, nil, false)
	requireCode(t, w, 302)
	w = call(a, "GET", "/next_song/", 1, nil, false)
	requireCode(t, w, 405)
	r := httptest.NewRequest("POST", "/preferences/", strings.NewReader("autofeed_queue=on"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	requireCode(t, w, 403)
	_, e := a.S.DB.Exec("UPDATE reinhardt_preferences SET access_token='invite-secret' WHERE owner_id=1")
	if e != nil {
		t.Fatal(e)
	}
	w = call(a, "GET", "/invite/?q=invite-secret&next=https://evil.example/", 0, nil, false)
	requireCode(t, w, 302)
	if w.Header().Get("Location") != "/" {
		t.Fatal("unsafe redirect", w.Header().Get("Location"))
	}
	if len(w.Result().Cookies()) < 2 {
		t.Fatal("no session or rotated CSRF")
	}
	a.S.C["URL_PREFIX"] = "music/"
	w = call(a, "GET", "/music/", 1, nil, false)
	requireCode(t, w, 200)
	if !strings.Contains(w.Body.String(), `src="/music/static/player.js"`) {
		t.Fatal("prefix not applied")
	}
	w = call(a, "GET", "/music/static/unpoly.min.js", 0, nil, false)
	requireCode(t, w, 200)
	w = call(a, "GET", "/", 1, nil, false)
	requireCode(t, w, 404)
}

func TestArtworkUploadPermissionsValidationAndCache(t *testing.T) {
	a := testApp(t)
	m, e := a.S.Track(1)
	if e != nil {
		t.Fatal(e)
	}
	im := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			im.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	var imageBytes bytes.Buffer
	png.Encode(&imageBytes, im)
	w := call(a, "GET", "/artwork/1/96.jpg", 0, nil, false)
	requireCode(t, w, 200)
	first := append([]byte(nil), w.Body.Bytes()...)
	w = call(a, "GET", "/artwork/1/96.jpg", 0, nil, false)
	if !bytes.Equal(first, w.Body.Bytes()) {
		t.Fatal("unstable fallback")
	}
	files, _ := filepath.Glob(filepath.Join(a.S.C["COVERS_DIR"], "thumb-*"))
	if len(files) != 0 {
		t.Fatal("fallback cached")
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
		r := httptest.NewRequest("POST", "/track_details_modal/1", &body)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		r.AddCookie(&http.Cookie{Name: "sessionid", Value: fmt.Sprintf("session-%d", owner)})
		r.AddCookie(&http.Cookie{Name: "csrftoken", Value: strings.Repeat("a", 64)})
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	w = send(2, imageBytes.Bytes())
	requireCode(t, w, 403)
	w = send(1, []byte("invalid"))
	requireCode(t, w, 400)
	track, e := a.S.Track(1)
	if e != nil || track.S("artist") != "Artist" {
		t.Fatal("invalid upload mutated database")
	}
	w = send(1, imageBytes.Bytes())
	requireCode(t, w, 302)
	if _, e = os.Stat(m.S("full_path") + ".png"); e != nil {
		t.Fatal("format not derived from content", e)
	}
	w = call(a, "GET", "/artwork/1/96.jpg", 0, nil, false)
	requireCode(t, w, 200)
	decoded, e := jpeg.Decode(bytes.NewReader(w.Body.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	if decoded.Bounds().Dx() != 96 || decoded.Bounds().Dy() != 96 {
		t.Fatal(decoded.Bounds())
	}
	rr, gg, _, _ := decoded.At(48, 48).RGBA()
	if rr < 60000 || gg > 1000 {
		t.Fatal("wrong cover crop")
	}
	files, _ = filepath.Glob(filepath.Join(a.S.C["COVERS_DIR"], "thumb-*"))
	if len(files) != 1 {
		t.Fatal("cover not cached")
	}
}

func TestDatabaseSnapshotAndExistingSchema(t *testing.T) {
	a := testApp(t)
	w := call(a, "GET", "/preferences/database/", 2, nil, false)
	requireCode(t, w, 403)
	w = call(a, "GET", "/preferences/database/", 1, nil, false)
	requireCode(t, w, 200)
	if !bytes.HasPrefix(w.Body.Bytes(), []byte("SQLite format 3")) {
		t.Fatal("invalid snapshot")
	}
	path := filepath.Join(t.TempDir(), "copy.sqlite3")
	if e := os.WriteFile(path, w.Body.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
	c := Config{}
	for k, v := range a.S.C {
		c[k] = v
	}
	c["DATABASE_PATH"] = path
	s, e := openStore(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	tracks, e := s.Tracks("")
	if e != nil || len(tracks) != 3 {
		t.Fatal(tracks, e)
	}
}

func TestNativeIndexAndID3(t *testing.T) {
	a := testApp(t)
	dir, e := a.S.One("SELECT * FROM reinhardt_musicdirectory WHERE id=1")
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir.S("local_path"), "tagged.mp3")
	if e = os.WriteFile(path, make([]byte, 32), 0644); e != nil {
		t.Fatal(e)
	}
	m := Row{"full_path": path, "relative_path": "tagged.mp3", "artist": "the test & friends", "title": "Tagged title", "album": "Record", "genre": "(18)Techno", "year": "2020", "comment": "Hello"}
	if e = writeID3(m, nil, ""); e != nil {
		t.Fatal(e)
	}
	tags, e := id3.Open(path, id3.Options{Parse: true})
	if e != nil {
		t.Fatal(e)
	}
	if tags.Artist() != m.S("artist") {
		t.Fatal(tags.Artist())
	}
	tags.Close()
	if e = a.S.indexFile(path, dir); e != nil {
		t.Fatal(e)
	}
	tracks, e := a.S.Search("Tagged")
	if e != nil || len(tracks) != 1 {
		t.Fatal(tracks, e)
	}
	if tracks[0].S("artist") != "Test And Friends (the)" || tracks[0].S("year") != "2020" || tracks[0].S("genre") != "techno" {
		t.Fatalf("metadata: %#v", tracks[0])
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if e = a.S.Index(io.Discard); e != nil {
		t.Fatal(e)
	}
	m, e = a.S.Track(tracks[0].N("id"))
	if e != nil || !m.B("missing") {
		t.Fatal(m, e)
	}
}

func TestJobsFailureAndYouTubeValidation(t *testing.T) {
	a := testApp(t)
	for _, raw := range []string{"https://evil.example/watch?v=abcdefghijk", "file:///tmp/a", "https://www.youtube.com:443/watch?v=abcdefghijk", "https://youtu.be/notvalid", "https://user@youtube.com/watch?v=abcdefghijk"} {
		if _, e := youtubeURL(raw); e == nil {
			t.Fatal("accepted", raw)
		}
	}
	for _, raw := range []string{"https://youtu.be/abcdefghijk", "https://www.youtube.com/watch?v=abcdefghijk", "https://music.youtube.com/shorts/abcdefghijk"} {
		if v, e := youtubeURL(raw); e != nil || v != "https://www.youtube.com/watch?v=abcdefghijk" {
			t.Fatal(v, e)
		}
	}
	m := Row{"url": "invalid", "artist": "Artist", "title": "Title"}
	if e := a.S.Enqueue("youtube", m); e != nil {
		t.Fatal(e)
	}
	if e := a.Work(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	failure, e := a.S.One("SELECT * FROM reinhardt_youtuberipfailure")
	if e != nil {
		t.Fatal(e)
	}
	if failure.S("artist") != "Artist" {
		t.Fatal(failure)
	}
	job, e := a.S.One("SELECT * FROM go_job")
	if e != nil || job.N("attempts") != 1 || job.N("run_at") <= time.Now().Unix() {
		t.Fatal(job, e)
	}
}

func TestRejectArchiveTraversal(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "upload.zip")
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	file, _ := z.Create("../escape.mp3")
	file.Write([]byte("test"))
	z.Close()
	os.WriteFile(path, b.Bytes(), 0600)
	if e := extractArchive(path, root, 1000); e == nil {
		t.Fatal("accepted traversal")
	}
}

func TestAssetsAreByteIdenticalToDjango(t *testing.T) {
	// Go runs package tests from internal/reinhardt; Django is a sibling checkout.
	djangoRoot := filepath.Join("..", "..", "..", "reinhardt", "reinhardt")
	if _, e := os.Stat(filepath.Join(djangoRoot, "templates")); os.IsNotExist(e) {
		t.Skip("Django source checkout not present")
	}
	for _, root := range []string{"static", "locale"} {
		e := fs.WalkDir(assets, root, func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			original, e := os.ReadFile(filepath.Join(djangoRoot, path))
			if e != nil {
				return e
			}
			copy, e := assets.ReadFile(path)
			if e != nil {
				return e
			}
			if !bytes.Equal(original, copy) {
				return fmt.Errorf("asset differs: %s", path)
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
}

func TestConfigDotenvAndEnvironmentPrecedence(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, ".env")
	if e := os.WriteFile(file, []byte("BASE_DIR="+root+"\nURL_PREFIX=music\nEMAIL_PORT=2525\nMUSIC_IMPORT_DIR='/tmp/import with spaces'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("EMAIL_PORT", "1025")
	c, e := loadConfigFromHome(root, file, filepath.Join(root, "explicit.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	if c["EMAIL_PORT"] != "1025" || c["URL_PREFIX"] != "music/" || c["MUSIC_IMPORT_DIR"] != "/tmp/import with spaces" || c["DATABASE_PATH"] != filepath.Join(root, "explicit.sqlite3") {
		t.Fatal("configuration precedence failed")
	}
}
