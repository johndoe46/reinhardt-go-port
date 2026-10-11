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

func TestJamaAndDropboxProtocols(test *testing.T) {
	app := testApp(test)
	app.Store.Config["JAMA_ENDPOINT"] = "https://jama.example/rpc/"
	app.Store.Config["JAMA_KEY"] = "test-key"
	app.Store.Config["JAMA_PROJECT"] = "17"
	calls := 0
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		data, _ := io.ReadAll(request.Body)
		if request.Header.Get("X-Api-Key") != "test-key" {
			test.Fatal("missing API key")
		}
		if strings.HasSuffix(request.URL.Path, "upload/partial/") {
			if string(data) != "abcdefghijklmnopqrstuvwxyz" || request.Header.Get("X-file-chunk") != "0/1" || request.Header.Get("X-Project") != "17" {
				test.Fatal(request.Header, string(data))
			}
			return remoteResponse("42"), nil
		}
		var payload Row
		if err := json.Unmarshal(data, &payload); err != nil {
			test.Fatal(err)
		}
		if payload.S("method") != "upload_infos" {
			test.Fatal(payload)
		}
		return remoteResponse(`{"error":null,"result":{"status":"not available"}}`), nil
	})
	if err := app.syncJama(context.Background(), 1); err != nil {
		test.Fatal(err)
	}
	if calls != 2 {
		test.Fatal(calls)
	}
	track, err := app.Store.Track(1)
	if err != nil || len(track.S("hash")) != 64 {
		test.Fatal("hash not saved", err)
	}
	app.Store.Config["DBX_KEY"] = "key"
	app.Store.Config["DBX_SECRET"] = "secret"
	app.Store.Config["DBX_TOKEN"] = "refresh"
	calls = 0
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if strings.HasSuffix(request.URL.Path, "oauth2/token") {
			data, _ := io.ReadAll(request.Body)
			form, _ := url.ParseQuery(string(data))
			if form.Get("refresh_token") != "refresh" || form.Get("grant_type") != "refresh_token" {
				test.Fatal(form)
			}
			return remoteResponse(`{"access_token":"access"}`), nil
		}
		if request.Header.Get("Authorization") != "Bearer access" {
			test.Fatal("missing bearer token")
		}
		if strings.HasSuffix(request.URL.Path, "upload_session/start") {
			return remoteResponse(`{"session_id":"session"}`), nil
		}
		if !strings.HasSuffix(request.URL.Path, "upload_session/finish") {
			test.Fatal(request.URL)
		}
		data, _ := io.ReadAll(request.Body)
		if string(data) != "abcdefghijklmnopqrstuvwxyz" {
			test.Fatal(string(data))
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
		if err := json.Unmarshal([]byte(request.Header.Get("Dropbox-API-Arg")), &arg); err != nil {
			test.Fatal(err)
		}
		if arg.Cursor.Session != "session" || arg.Cursor.Offset != 0 || arg.Commit.Path != "/music_collection/1.mp3" || arg.Commit.Mode != "overwrite" {
			test.Fatal(arg)
		}
		return remoteResponse(`{}`), nil
	})
	if err := app.syncDropbox(context.Background(), 1); err != nil {
		test.Fatal(err)
	}
	if calls != 3 {
		test.Fatal(calls)
	}
}

func TestArtworkDiscoveryAndMissCache(test *testing.T) {
	app := testApp(test)
	track, err := app.Store.Track(1)
	if err != nil {
		test.Fatal(err)
	}
	var imageBytes bytes.Buffer
	png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 50, 50)))
	calls := 0
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Hostname() == "itunes.apple.com" {
			if request.URL.Query().Get("entity") != "album" {
				test.Fatal(request.URL)
			}
			return remoteResponse(`{"results":[{"artistName":"Artist","collectionName":"Album","artworkUrl100":"https://is1.mzstatic.com/art/100x100bb.jpg"}]}`), nil
		}
		if request.URL.String() != "https://is1.mzstatic.com/art/500x500bb.jpg" {
			test.Fatal(request.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(imageBytes.Bytes()))}, nil
	})
	if im := app.discoverCover(context.Background(), track); im == nil {
		test.Fatal("cover missing")
	}
	if _, err = os.Stat(track.S("full_path") + ".jpg"); err != nil {
		test.Fatal(err)
	}
	if im := app.discoverCover(context.Background(), track); im == nil || calls != 2 {
		test.Fatal("cover not cached", calls)
	}
	app.coverCache = map[string]coverResult{}
	app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) { calls++; return nil, fmt.Errorf("offline") })
	track["artist"] = "Other artist"
	app.discoverCover(context.Background(), track)
	app.discoverCover(context.Background(), track)
	if calls != 3 {
		test.Fatal("miss not cached", calls)
	}
}

func TestModelOutputEscaping(test *testing.T) {
	app := testApp(test)
	id := mustTag(test, app, 1, `<img src=x onerror=alert(1)>`, []int64{1})
	response := call(app, "GET", fmt.Sprintf("/tag/?q=%d", id), 1, nil, true)
	requireCode(test, response, 200)
	if strings.Contains(response.Body.String(), `<img src=x`) {
		test.Fatal("unescaped model string")
	}
	if !strings.Contains(response.Body.String(), `&lt;img src=x onerror=alert(1)&gt;`) {
		test.Fatal("missing escaped label")
	}
	_, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET artist=? WHERE id=1", `O'Reilly & <b>Friends</b>`)
	if err != nil {
		test.Fatal(err)
	}
	response = call(app, "GET", "/track_details_modal/1", 1, nil, true)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `O&#39;Reilly &amp; &lt;b&gt;Friends&lt;/b&gt;`) {
		test.Fatal("metadata was not HTML-escaped")
	}
}

func TestCobraCommandsAndConfiguration(test *testing.T) {
	cli := newCLI()
	for _, name := range []string{"addmusicdir", "giveaccess", "listaccess", "listtags", "indexmusic", "dedup", "fetchtagfiles", "jamasync", "populateyears", "tagsexport", "dbxrefreshtoken", "ytrip"} {
		if command, _, err := cli.Find([]string{name}); err != nil || command.Name() != name {
			test.Fatal("missing command", name, err)
		}
	}
	root := test.TempDir()
	env := filepath.Join(root, ".env")
	if err := os.WriteFile(env, []byte("BASE_DIR="+root+"\nLANGUAGE_CODE=en-us\nJAMA_ENDPOINT=\nJAMA_KEY=\nJAMA_PROJECT=\nDBX_TOKEN=\nACCESS_URL_PREFIX=http://localhost:8000\n"), 0600); err != nil {
		test.Fatal(err)
	}
	var output bytes.Buffer
	cli.SetOut(&output)
	cli.SetArgs([]string{"--env-file", env, "giveaccess", "ADMIN", "--superuser", "--email", "admin@example.com"})
	if err := cli.Execute(); err != nil {
		test.Fatal(err)
	}
	if !strings.Contains(output.String(), "http://localhost:8000/invite/?q=") {
		test.Fatal("missing invite link")
	}
	config, err := loadConfig(env, "")
	if err != nil {
		test.Fatal(err)
	}
	store, err := openStore(config)
	if err != nil {
		test.Fatal(err)
	}
	defer store.DB.Close()
	user, err := store.One("SELECT * FROM auth_user WHERE username='admin'")
	if err != nil || !user.B("is_superuser") || user.S("email") != "admin@example.com" {
		test.Fatal(user, err)
	}
}

func TestRealMP3AndM4AMetadata(test *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		test.Skip("optional ffmpeg test fixture generator unavailable")
	}
	app := testApp(test)
	dir, err := app.Store.One("SELECT * FROM reinhardt_musicdirectory WHERE id=1")
	if err != nil {
		test.Fatal(err)
	}
	for _, ext := range []string{".mp3", ".m4a"} {
		path := filepath.Join(dir.S("local_path"), "generated"+ext)
		cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-metadata", "artist=Artist", "-metadata", "title=Generated track", "-metadata", "album=Generated album", "-metadata", "date=2024", "-metadata", "track=3", path)
		if output, err := cmd.CombinedOutput(); err != nil {
			test.Fatalf("fixture: %v: %s", err, output)
		}
		metadata, err := app.Store.Metadata(path)
		if err != nil {
			test.Fatal(err)
		}
		if metadata.S("year") != "2024" || metadata.N("duration") < 1 || metadata.N("duration") > 3 || metadata.N("track_number") != 3 {
			test.Fatalf("metadata %#v", metadata)
		}
		if ext == ".m4a" {
			var imageBytes bytes.Buffer
			png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 50, 50)))
			picture := path + ".png"
			os.WriteFile(picture, imageBytes.Bytes(), 0644)
			track := Row{"full_path": path, "relative_path": "generated.m4a"}
			if err := embedArtwork(context.Background(), track, picture); err != nil {
				test.Fatal(err)
			}
			metadata, err = app.Store.Metadata(path)
			if err != nil || metadata.S("title") != "Generated track" {
				test.Fatal("artwork embedding lost metadata", err)
			}
		}
	}
}
