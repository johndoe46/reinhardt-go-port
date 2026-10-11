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

func TestNativeTemplatesMetadataAndURLContexts(test *testing.T) {
	app := testApp(test)
	artist := `O'Reilly & <b>Friends</b> / plus + question? #hash`
	title := `A "quoted" tune & <img src=x onerror=alert(1)>`
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET artist=?,title=? WHERE id=1", artist, title); err != nil {
		test.Fatal(err)
	}
	response := call(app, "GET", "/music_file_dropdown/?q=1", 1, nil, true)
	requireCode(test, response, 200)
	href := regexp.MustCompile(`href="(/artist/\?q=[^"]*)"`).FindStringSubmatch(response.Body.String())
	if len(href) != 2 {
		test.Fatal("missing artist link")
	}
	parsedURL, err := url.Parse(html.UnescapeString(href[1]))
	if err != nil || parsedURL.Query().Get("q") != artist {
		test.Fatalf("artist URL failed to round-trip: %v", err)
	}
	response = call(app, "GET", "/album/?q=Album", 1, nil, false)
	requireCode(test, response, 200)
	if strings.Contains(response.Body.String(), `<img src=x`) || strings.Contains(response.Body.String(), `<b>Friends</b>`) {
		test.Fatal("metadata escaped its text or attribute context")
	}
	if !strings.Contains(response.Body.String(), `&lt;img src=x onerror=alert(1)&gt;`) {
		test.Fatal("track title missing")
	}
}

func TestNativeTemplatesSharedJavaScriptEscaping(test *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		test.Skip("optional Node.js JavaScript verification unavailable")
	}
	for _, label := range []string{
		`${globalThis.injected = true}`,
		"` ; globalThis.injected = true; //",
		"</script><script>globalThis.injected = true</script>\n'\"\\&",
	} {
		test.Run(label, func(test *testing.T) {
			app := testApp(test)
			id := mustTag(test, app, 1, "safe label", []int64{1})
			if _, err := app.Store.DB.Exec("UPDATE reinhardt_tag SET label=?,is_shared=1 WHERE id=?", label, id); err != nil {
				test.Fatal(err)
			}
			tag, err := app.Store.Tag(id, 1)
			if err != nil {
				test.Fatal(err)
			}
			response := call(app, "GET", tag.S("share_url"), 0, nil, false)
			requireCode(test, response, 200)
			if strings.Count(response.Body.String(), "<script>") != 1 || strings.Contains(response.Body.String(), "<script>globalThis") {
				test.Fatal("label escaped the script element")
			}
			line := regexp.MustCompile(`document\.title = .*;`).FindString(response.Body.String())
			if line == "" {
				test.Fatal("shared player's title update is missing")
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
				test.Fatalf("rendered JavaScript failed: %v: %s", err, out)
			}
			var actual string
			if err = json.Unmarshal(out, &actual); err != nil || actual != "Song — "+label {
				test.Fatalf("player title did not preserve label: %q, %v", actual, err)
			}
		})
	}
}

func TestNativeTemplatesLayoutIsolation(test *testing.T) {
	app := testApp(test)
	// An empty footer override on login must not change other page templates.
	for index := 0; index < 2; index++ {
		login := call(app, "GET", "/accounts/login/", 0, nil, false)
		requireCode(test, login, 200)
		if strings.Contains(login.Body.String(), `id="player"`) {
			test.Fatal("login inherited the player footer")
		}
		page := call(app, "GET", "/", 1, nil, false)
		requireCode(test, page, 200)
		if !strings.Contains(page.Body.String(), `up-keep id="player"`) {
			test.Fatal("page lost its player footer")
		}
	}
}

func TestNativeTemplatesEmptyLibraryAndPermissions(test *testing.T) {
	app := testApp(test)
	id := mustTag(test, app, 1, "empty shared list", nil)
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_tag SET is_shared=1 WHERE id=?", id); err != nil {
		test.Fatal(err)
	}
	if _, err := app.Store.DB.Exec("DELETE FROM reinhardt_musicfile"); err != nil {
		test.Fatal(err)
	}
	paths := []string{"/", "/artists/", "/genres/", "/artist/?q=absent", "/album/?q=absent", "/genre/?q=absent", "/queue/", "/tags/", "/favorites/", "/search/?sq=absent", "/friends/"}
	for _, owner := range []int{1, 2} {
		for _, path := range paths {
			for _, fragment := range []bool{false, true} {
				test.Run(fmt.Sprintf("owner=%d/%s/fragment=%v", owner, path, fragment), func(test *testing.T) {
					response := call(app, "GET", path, owner, nil, fragment)
					requireCode(test, response, 200)
					if bytes.Contains(response.Body.Bytes(), []byte("#ZgotmplZ")) || bytes.Contains(response.Body.Bytes(), []byte("<no value>")) {
						test.Fatal("missing data was rendered as a template error")
					}
				})
			}
		}
	}
	tag, err := app.Store.Tag(id, 1)
	if err != nil {
		test.Fatal(err)
	}
	response := call(app, "GET", tag.S("share_url"), 0, nil, false)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), "This playlist has no tracks yet.") {
		test.Fatal("empty public playlist message missing")
	}
}
