package reinhardt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func normalizeHTML(html string) string {
	comments := regexp.MustCompile(`(?s)<!--.*?-->|/\*.*?\*/`)
	csrf := regexp.MustCompile(`name="csrfmiddlewaretoken" value="[^"]*"`)
	space := regexp.MustCompile(`\s+`)
	// html/template strips comments, pads JS numbers, and escapes slashes in
	// JS strings. Canonicalize these equivalent spellings without dropping DOM
	// elements, attributes, or script instructions from the comparison.
	html = comments.ReplaceAllString(html, "")
	html = strings.NewReplacer(`\/`, `/`, "&#x27;", "'", "&#39;", "'", "&quot;", "&#34;", "&#43;", "+").Replace(html)
	jsEscape := regexp.MustCompile(`\\u[0-9a-fA-F]{4}`)
	html = jsEscape.ReplaceAllStringFunc(html, strings.ToLower)
	jsNumber := regexp.MustCompile(`playNext\(\s*([0-9]+)\s*\)`)
	html = jsNumber.ReplaceAllString(html, `playNext($1)`)
	html = csrf.ReplaceAllString(html, `name="csrfmiddlewaretoken" value="TOKEN"`)
	return strings.TrimSpace(space.ReplaceAllString(html, " "))
}

func TestDjangoDatabaseAndResponseParity(t *testing.T) {
	root := os.Getenv("DJANGO_PARITY_DIR")
	if root == "" {
		t.Skip("Set DJANGO_PARITY_DIR to a scripts/django_fixture.py output directory")
	}
	manifest, err := os.ReadFile(filepath.Join(root, "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []string
	if err = json.Unmarshal(manifest, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture, func(t *testing.T) {
			testDjangoDatabaseAndResponseParity(t, filepath.Join(root, fixture))
		})
	}
}

func testDjangoDatabaseAndResponseParity(t *testing.T, root string) {
	data, e := os.ReadFile(filepath.Join(root, "django.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	tmp := t.TempDir()
	userDir := filepath.Join(tmp, ".reinhardt")
	if e = os.Mkdir(userDir, 0700); e != nil {
		t.Fatal(e)
	}
	db := filepath.Join(userDir, "db.sqlite3")
	if e = os.WriteFile(db, data, 0600); e != nil {
		t.Fatal(e)
	}
	unsetConfigEnvironment(t, "BASE_DIR", "DATABASE_PATH", "COVERS_DIR", "SECRET_KEY", "ALLOWED_HOSTS", "LANGUAGE_CODE", "TIME_ZONE", "URL_PREFIX", "MAX_UPLOAD_BYTES", "SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS")
	if e = os.WriteFile(filepath.Join(userDir, "config"), []byte("SECRET_KEY=parity-secret\nALLOWED_HOSTS=*\n"), 0600); e != nil {
		t.Fatal(e)
	}
	c, e := loadConfigFromHome(tmp, "", "")
	if e != nil {
		t.Fatal(e)
	}
	s, e := openStore(c)
	if e != nil {
		t.Fatal(e)
	}
	defer s.DB.Close()
	a, e := newApp(s)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB.Exec("INSERT INTO go_session(token,owner_id,expires) VALUES('parity-session',1,?)", time.Now().Unix()+3600); e != nil {
		t.Fatal(e)
	}
	data, e = os.ReadFile(filepath.Join(root, "responses.json"))
	if e != nil {
		t.Fatal(e)
	}
	var snapshots []struct {
		Path     string
		Fragment bool
		Language string
		Body     string
	}
	if e = json.Unmarshal(data, &snapshots); e != nil {
		t.Fatal(e)
	}
	for _, snapshot := range snapshots {
		t.Run(fmt.Sprintf("%s/%s/fragment=%v", snapshot.Language, snapshot.Path, snapshot.Fragment), func(t *testing.T) {
			s.C["LANGUAGE_CODE"] = snapshot.Language
			r := httptest.NewRequest("GET", "http://testserver"+snapshot.Path, nil)
			r.AddCookie(&http.Cookie{Name: "sessionid", Value: "parity-session"})
			if snapshot.Fragment {
				r.Header.Set("X-Up-Target", "main")
			}
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			requireCode(t, w, 200)
			if strings.HasPrefix(snapshot.Path, "/api/") {
				var original, ported map[string]any
				json.Unmarshal([]byte(snapshot.Body), &original)
				json.Unmarshal(w.Body.Bytes(), &ported)
				b1, _ := json.Marshal(original)
				b2, _ := json.Marshal(ported)
				if !bytes.Equal(b1, b2) {
					t.Fatalf("API differs:\nDjango: %s\nGo: %s", b1, b2)
				}
				return
			}
			original, ported := normalizeHTML(snapshot.Body), normalizeHTML(w.Body.String())
			if original != ported {
				first := 0
				for first < len(original) && first < len(ported) && original[first] == ported[first] {
					first++
				}
				start := first - 80
				if start < 0 {
					start = 0
				}
				end1 := first + 160
				if end1 > len(original) {
					end1 = len(original)
				}
				end2 := first + 160
				if end2 > len(ported) {
					end2 = len(ported)
				}
				t.Fatalf("HTML differs at %d:\nDjango: %s\nGo:     %s", first, original[start:end1], ported[start:end2])
			}
		})
	}
	testDjangoPlaybackParity(t, root, c)
}

func testDjangoPlaybackParity(t *testing.T, root string, config Config) {
	data, err := os.ReadFile(filepath.Join(root, "playback.json"))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	type queueItem struct {
		ID    int64 `json:"id"`
		Track int64 `json:"music_file_id"`
		Rank  int64 `json:"rank"`
	}
	var snapshots []struct {
		Name, Language, Body string
		Fragment             bool
		Setup                []struct {
			SQL  string
			Args []any
		}
		Form     map[string]string
		Position int64
		Queue    []queueItem
	}
	if err = json.Unmarshal(data, &snapshots); err != nil {
		t.Fatal(err)
	}
	baseline, err := os.ReadFile(filepath.Join(root, "django.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range snapshots {
		t.Run(fmt.Sprintf("playback/%s/%s/fragment=%v", snapshot.Name, snapshot.Language, snapshot.Fragment), func(t *testing.T) {
			tmp := t.TempDir()
			c := Config{}
			for key, value := range config {
				c[key] = value
			}
			c["BASE_DIR"], c["DATABASE_PATH"], c["COVERS_DIR"] = tmp, filepath.Join(tmp, "db.sqlite3"), filepath.Join(tmp, "covers")
			c["LANGUAGE_CODE"] = snapshot.Language
			if err := os.WriteFile(c["DATABASE_PATH"], baseline, 0600); err != nil {
				t.Fatal(err)
			}
			s, err := openStore(c)
			if err != nil {
				t.Fatal(err)
			}
			defer s.DB.Close()
			if _, err = s.DB.Exec("INSERT INTO go_session(token,owner_id,expires) VALUES('session-1',1,?)", time.Now().Unix()+3600); err != nil {
				t.Fatal(err)
			}
			for _, setup := range snapshot.Setup {
				if _, err = s.DB.Exec(setup.SQL, setup.Args...); err != nil {
					t.Fatal(err)
				}
			}
			a, err := newApp(s)
			if err != nil {
				t.Fatal(err)
			}
			form := url.Values{}
			for key, value := range snapshot.Form {
				form.Set(key, value)
			}
			w := call(a, "POST", "/next_song/", 1, form, snapshot.Fragment)
			requireCode(t, w, 200)
			if normalizeHTML(w.Body.String()) != normalizeHTML(snapshot.Body) {
				t.Fatal("now-playing HTML differs from Django")
			}
			prefs, err := s.Preferences(1)
			if err != nil || prefs.N("current_position") != snapshot.Position {
				t.Fatal("playback position differs from Django", err)
			}
			rows, err := s.Query(`SELECT a.id,a.music_file_id,a.rank FROM reinhardt_musicfiletag a JOIN reinhardt_tag t ON t.id=a.tag_id WHERE t.owner_id=1 AND t.label='queue' ORDER BY a.rank,a.id`)
			if err != nil {
				t.Fatal(err)
			}
			actual := []queueItem{}
			for _, row := range rows {
				actual = append(actual, queueItem{row.N("id"), row.N("music_file_id"), row.N("rank")})
			}
			original, _ := json.Marshal(snapshot.Queue)
			ported, _ := json.Marshal(actual)
			if !bytes.Equal(original, ported) {
				t.Fatalf("queue differs: Django %s, Go %s", original, ported)
			}
		})
	}
}
