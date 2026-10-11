package reinhardt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func remoteResponse(data string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(data))}
}

func TestJamaAndDropboxProtocols(t *testing.T) {
	a := testApp(t)
	a.S.C["JAMA_ENDPOINT"] = "https://jama.example/rpc/"
	a.S.C["JAMA_KEY"] = "test-key"
	a.S.C["JAMA_PROJECT"] = "17"
	calls := 0
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		data, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Fatal("missing API key")
		}
		if strings.HasSuffix(r.URL.Path, "upload/partial/") {
			if string(data) != "abcdefghijklmnopqrstuvwxyz" || r.Header.Get("X-file-chunk") != "0/1" || r.Header.Get("X-Project") != "17" {
				t.Fatal(r.Header, string(data))
			}
			return remoteResponse("42"), nil
		}
		var payload Row
		if e := json.Unmarshal(data, &payload); e != nil {
			t.Fatal(e)
		}
		if payload.S("method") != "upload_infos" {
			t.Fatal(payload)
		}
		return remoteResponse(`{"error":null,"result":{"status":"not available"}}`), nil
	})
	if e := a.syncJama(context.Background(), 1); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	track, e := a.S.Track(1)
	if e != nil || len(track.S("hash")) != 64 {
		t.Fatal("hash not saved", e)
	}
	a.S.C["DBX_KEY"] = "key"
	a.S.C["DBX_SECRET"] = "secret"
	a.S.C["DBX_TOKEN"] = "refresh"
	calls = 0
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if strings.HasSuffix(r.URL.Path, "oauth2/token") {
			data, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(data))
			if form.Get("refresh_token") != "refresh" || form.Get("grant_type") != "refresh_token" {
				t.Fatal(form)
			}
			return remoteResponse(`{"access_token":"access"}`), nil
		}
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Fatal("missing bearer token")
		}
		if strings.HasSuffix(r.URL.Path, "upload_session/start") {
			return remoteResponse(`{"session_id":"session"}`), nil
		}
		if !strings.HasSuffix(r.URL.Path, "upload_session/finish") {
			t.Fatal(r.URL)
		}
		data, _ := io.ReadAll(r.Body)
		if string(data) != "abcdefghijklmnopqrstuvwxyz" {
			t.Fatal(string(data))
		}
		var arg struct {
			Cursor struct {
				Session string `json:"session_id"`
				Offset  int
			}
			Commit struct {
				Path string
				Mode string
			}
		}
		if e := json.Unmarshal([]byte(r.Header.Get("Dropbox-API-Arg")), &arg); e != nil {
			t.Fatal(e)
		}
		if arg.Cursor.Session != "session" || arg.Cursor.Offset != 0 || arg.Commit.Path != "/music_collection/1.mp3" || arg.Commit.Mode != "overwrite" {
			t.Fatal(arg)
		}
		return remoteResponse(`{}`), nil
	})
	if e := a.syncDropbox(context.Background(), 1); e != nil {
		t.Fatal(e)
	}
	if calls != 3 {
		t.Fatal(calls)
	}
}

func TestArtworkDiscoveryAndMissCache(t *testing.T) {
	a := testApp(t)
	m, e := a.S.Track(1)
	if e != nil {
		t.Fatal(e)
	}
	var imageBytes bytes.Buffer
	png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 50, 50)))
	calls := 0
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Hostname() == "itunes.apple.com" {
			if r.URL.Query().Get("entity") != "album" {
				t.Fatal(r.URL)
			}
			return remoteResponse(`{"results":[{"artistName":"Artist","collectionName":"Album","artworkUrl100":"https://is1.mzstatic.com/art/100x100bb.jpg"}]}`), nil
		}
		if r.URL.String() != "https://is1.mzstatic.com/art/500x500bb.jpg" {
			t.Fatal(r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(imageBytes.Bytes()))}, nil
	})
	if im := a.discoverCover(context.Background(), m); im == nil {
		t.Fatal("cover missing")
	}
	if _, e = os.Stat(m.S("full_path") + ".jpg"); e != nil {
		t.Fatal(e)
	}
	if im := a.discoverCover(context.Background(), m); im == nil || calls != 2 {
		t.Fatal("cover not cached", calls)
	}
	a.coverCache = map[string]coverResult{}
	a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) { calls++; return nil, fmt.Errorf("offline") })
	m["artist"] = "Other artist"
	a.discoverCover(context.Background(), m)
	a.discoverCover(context.Background(), m)
	if calls != 3 {
		t.Fatal("miss not cached", calls)
	}
}

func TestModelOutputEscaping(t *testing.T) {
	a := testApp(t)
	id := mustTag(t, a, 1, `<img src=x onerror=alert(1)>`, []int64{1})
	w := call(a, "GET", fmt.Sprintf("/tag/?q=%d", id), 1, nil, true)
	requireCode(t, w, 200)
	if strings.Contains(w.Body.String(), `<img src=x`) {
		t.Fatal("unescaped model string")
	}
	if !strings.Contains(w.Body.String(), `&lt;img src=x onerror=alert(1)&gt;`) {
		t.Fatal("missing escaped label")
	}
	_, e := a.S.DB.Exec("UPDATE reinhardt_musicfile SET artist=? WHERE id=1", `O'Reilly & <b>Friends</b>`)
	if e != nil {
		t.Fatal(e)
	}
	w = call(a, "GET", "/track_details_modal/1", 1, nil, true)
	requireCode(t, w, 200)
	if !strings.Contains(w.Body.String(), `O&#39;Reilly &amp; &lt;b&gt;Friends&lt;/b&gt;`) {
		t.Fatal("metadata was not HTML-escaped")
	}
}

func TestCobraCommandsAndConfiguration(t *testing.T) {
	cli := newCLI()
	for _, name := range []string{"addmusicdir", "giveaccess", "listaccess", "listtags", "indexmusic", "dedup", "fetchtagfiles", "jamasync", "populateyears", "tagsexport", "dbxrefreshtoken", "ytrip"} {
		if c, _, e := cli.Find([]string{name}); e != nil || c.Name() != name {
			t.Fatal("missing command", name, e)
		}
	}
	root := t.TempDir()
	env := filepath.Join(root, ".env")
	if e := os.WriteFile(env, []byte("BASE_DIR="+root+"\nLANGUAGE_CODE=en-us\nJAMA_ENDPOINT=\nJAMA_KEY=\nJAMA_PROJECT=\nDBX_TOKEN=\nACCESS_URL_PREFIX=http://localhost:8000\n"), 0600); e != nil {
		t.Fatal(e)
	}
	var output bytes.Buffer
	cli.SetOut(&output)
	cli.SetArgs([]string{"--env-file", env, "giveaccess", "ADMIN", "--superuser", "--email", "admin@example.com"})
	if e := cli.Execute(); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(output.String(), "http://localhost:8000/invite/?q=") {
		t.Fatal("missing invite link")
	}
	c, e := loadConfig(env, "")
	if e != nil {
		t.Fatal(e)
	}
	s, e := openStore(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	user, e := s.One("SELECT * FROM auth_user WHERE username='admin'")
	if e != nil || !user.B("is_superuser") || user.S("email") != "admin@example.com" {
		t.Fatal(user, e)
	}
}

func TestRealMP3AndM4AMetadata(t *testing.T) {
	ffmpeg, e := exec.LookPath("ffmpeg")
	if e != nil {
		t.Skip("optional ffmpeg test fixture generator unavailable")
	}
	a := testApp(t)
	dir, e := a.S.One("SELECT * FROM reinhardt_musicdirectory WHERE id=1")
	if e != nil {
		t.Fatal(e)
	}
	for _, ext := range []string{".mp3", ".m4a"} {
		path := filepath.Join(dir.S("local_path"), "generated"+ext)
		cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-metadata", "artist=Artist", "-metadata", "title=Generated track", "-metadata", "album=Generated album", "-metadata", "date=2024", "-metadata", "track=3", path)
		if output, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("fixture: %v: %s", e, output)
		}
		metadata, e := a.S.Metadata(path)
		if e != nil {
			t.Fatal(e)
		}
		if metadata.S("year") != "2024" || metadata.N("duration") < 1 || metadata.N("duration") > 3 || metadata.N("track_number") != 3 {
			t.Fatalf("metadata %#v", metadata)
		}
		if ext == ".m4a" {
			var imageBytes bytes.Buffer
			png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 50, 50)))
			picture := path + ".png"
			os.WriteFile(picture, imageBytes.Bytes(), 0644)
			m := Row{"full_path": path, "relative_path": "generated.m4a"}
			if e := embedArtwork(context.Background(), m, picture); e != nil {
				t.Fatal(e)
			}
			metadata, e = a.S.Metadata(path)
			if e != nil || metadata.S("title") != "Generated track" {
				t.Fatal("artwork embedding lost metadata", e)
			}
		}
	}
}
