package reinhardt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestNativeTemplatesMetadataAndURLContexts(t *testing.T) {
	a := testApp(t)
	artist := `O'Reilly & <b>Friends</b> / plus + question? #hash`
	title := `A "quoted" tune & <img src=x onerror=alert(1)>`
	if _, err := a.S.DB.Exec("UPDATE reinhardt_musicfile SET artist=?,title=? WHERE id=1", artist, title); err != nil {
		t.Fatal(err)
	}
	w := call(a, "GET", "/music_file_dropdown/?q=1", 1, nil, true)
	requireCode(t, w, 200)
	href := regexp.MustCompile(`href="(/artist/\?q=[^"]*)"`).FindStringSubmatch(w.Body.String())
	if len(href) != 2 {
		t.Fatal("missing artist link")
	}
	u, err := url.Parse(html.UnescapeString(href[1]))
	if err != nil || u.Query().Get("q") != artist {
		t.Fatalf("artist URL failed to round-trip: %v", err)
	}
	w = call(a, "GET", "/album/?q=Album", 1, nil, false)
	requireCode(t, w, 200)
	if strings.Contains(w.Body.String(), `<img src=x`) || strings.Contains(w.Body.String(), `<b>Friends</b>`) {
		t.Fatal("metadata escaped its text or attribute context")
	}
	if !strings.Contains(w.Body.String(), `&lt;img src=x onerror=alert(1)&gt;`) {
		t.Fatal("track title missing")
	}
}

func TestNativeTemplatesSharedJavaScriptEscaping(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("optional Node.js JavaScript verification unavailable")
	}
	for _, label := range []string{
		`${globalThis.injected = true}`,
		"` ; globalThis.injected = true; //",
		"</script><script>globalThis.injected = true</script>\n'\"\\&",
	} {
		t.Run(label, func(t *testing.T) {
			a := testApp(t)
			id := mustTag(t, a, 1, "safe label", []int64{1})
			if _, err := a.S.DB.Exec("UPDATE reinhardt_tag SET label=?,is_shared=1 WHERE id=?", label, id); err != nil {
				t.Fatal(err)
			}
			tag, err := a.S.Tag(id, 1)
			if err != nil {
				t.Fatal(err)
			}
			w := call(a, "GET", tag.S("share_url"), 0, nil, false)
			requireCode(t, w, 200)
			if strings.Count(w.Body.String(), "<script>") != 1 || strings.Contains(w.Body.String(), "<script>globalThis") {
				t.Fatal("label escaped the script element")
			}
			line := regexp.MustCompile(`document\.title = .*;`).FindString(w.Body.String())
			if line == "" {
				t.Fatal("shared player's title update is missing")
			}
			// Execute the actual rendered assignment, checking both injection and
			// the value displayed by the player, without a browser or network.
			program := `const fs = require('fs'); const vm = require('vm');
const context = {document: {}, button: {dataset: {title: 'Song'}}};
vm.runInNewContext(fs.readFileSync(0, 'utf8'), context);
if (context.injected) process.exit(1);
process.stdout.write(JSON.stringify(context.document.title));`
			cmd := exec.Command(node, "-e", program)
			cmd.Stdin = strings.NewReader(line)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("rendered JavaScript failed: %v: %s", err, out)
			}
			var actual string
			if err = json.Unmarshal(out, &actual); err != nil || actual != "Song — "+label {
				t.Fatalf("player title did not preserve label: %q, %v", actual, err)
			}
		})
	}
}

func TestNativeTemplatesLayoutIsolation(t *testing.T) {
	a := testApp(t)
	// An empty footer override on login must not change other page templates.
	for i := 0; i < 2; i++ {
		login := call(a, "GET", "/accounts/login/", 0, nil, false)
		requireCode(t, login, 200)
		if strings.Contains(login.Body.String(), `id="player"`) {
			t.Fatal("login inherited the player footer")
		}
		page := call(a, "GET", "/", 1, nil, false)
		requireCode(t, page, 200)
		if !strings.Contains(page.Body.String(), `up-keep id="player"`) {
			t.Fatal("page lost its player footer")
		}
	}
}

func TestNativeTemplatesEmptyLibraryAndPermissions(t *testing.T) {
	a := testApp(t)
	id := mustTag(t, a, 1, "empty shared list", nil)
	if _, err := a.S.DB.Exec("UPDATE reinhardt_tag SET is_shared=1 WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.S.DB.Exec("DELETE FROM reinhardt_musicfile"); err != nil {
		t.Fatal(err)
	}
	paths := []string{"/", "/artists/", "/genres/", "/artist/?q=absent", "/album/?q=absent", "/genre/?q=absent", "/queue/", "/tags/", "/favorites/", "/search/?sq=absent", "/friends/"}
	for _, owner := range []int{1, 2} {
		for _, path := range paths {
			for _, fragment := range []bool{false, true} {
				t.Run(fmt.Sprintf("owner=%d/%s/fragment=%v", owner, path, fragment), func(t *testing.T) {
					w := call(a, "GET", path, owner, nil, fragment)
					requireCode(t, w, 200)
					if bytes.Contains(w.Body.Bytes(), []byte("#ZgotmplZ")) || bytes.Contains(w.Body.Bytes(), []byte("<no value>")) {
						t.Fatal("missing data was rendered as a template error")
					}
				})
			}
		}
	}
	tag, err := a.S.Tag(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	w := call(a, "GET", tag.S("share_url"), 0, nil, false)
	requireCode(t, w, 200)
	if !strings.Contains(w.Body.String(), "This playlist has no tracks yet.") {
		t.Fatal("empty public playlist message missing")
	}
}
