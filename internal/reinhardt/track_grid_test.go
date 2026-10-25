package reinhardt

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestTrackGridPreference(test *testing.T) {
	app := testApp(test)
	before, err := app.Store.Preferences(test.Context(), 1)
	if err != nil || !before.ShowTrackGrid {
		test.Fatal("track grid should default to on", err)
	}
	response := call(app, "GET", "/album/?q=Album", 1, nil, true)
	requireCode(test, response, http.StatusOK)
	if !strings.Contains(response.Body.String(), `class="track-grid-artwork"`) {
		test.Fatal("new accounts should see the track grid without changing preferences")
	}
	response = call(app, "POST", "/preferences/", 1, url.Values{"show_track_grid": {"on"}, "autofeed_queue": {"on"}, "autofeed_similar": {"on"}, "show_oscilloscope": {"on"}}, true)
	requireCode(test, response, http.StatusOK)
	if !strings.Contains(response.Body.String(), `checked name="show_track_grid"`) || strings.Contains(response.Body.String(), `<m-audio`) {
		test.Fatal("display setting did not update in the main fragment")
	}
	switchForm := regexp.MustCompile(`(?s)<form id="switch_preferences".*?</form>`).FindString(response.Body.String())
	for _, field := range []string{"show_track_grid", "autofeed_queue", "autofeed_similar", "show_oscilloscope"} {
		if !strings.Contains(switchForm, `name="`+field+`"`) {
			test.Fatalf("shared switch form missing %s", field)
		}
	}
	before.ShowTrackGrid = true
	after, err := app.Store.Preferences(test.Context(), 1)
	if err != nil || !reflect.DeepEqual(before, after) {
		test.Fatal("display setting changed other preferences", err)
	}
	other, err := app.Store.Preferences(test.Context(), 2)
	if err != nil || !other.ShowTrackGrid {
		test.Fatal("display setting changed another account", err)
	}
	// The shared form saves every switch, including unchecked ones.
	requireCode(test, call(app, "POST", "/preferences/", 1, url.Values{"show_track_grid": {"on"}, "autofeed_queue": {"on"}}, true), http.StatusOK)
	before.AutofeedSimilar = false
	before.ShowOscilloscope = false
	after, err = app.Store.Preferences(test.Context(), 1)
	if err != nil || !reflect.DeepEqual(before, after) {
		test.Fatal("shared switch form did not save checked and unchecked settings together", err)
	}
	requireCode(test, call(app, "POST", "/preferences/", 1, url.Values{"language_code": {"fr"}}, true), http.StatusOK)
	store, err := openStore(test.Context(), app.Store.Config)
	if err != nil {
		test.Fatal(err)
	}
	defer store.DB.Close()
	saved, err := store.Preferences(test.Context(), 1)
	if err != nil || !saved.ShowTrackGrid {
		test.Fatal("saved display setting did not survive restart or other preference changes", err)
	}
	response = call(app, "GET", "/preferences/", 1, nil, false)
	if !strings.Contains(response.Body.String(), "Afficher les pistes en grille avec les pochettes") {
		test.Fatal("display setting lacks its French translation")
	}
	request := httptest.NewRequest("POST", "/preferences/", strings.NewReader("show_track_grid=on"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, http.StatusForbidden)
	requireCode(test, call(app, "POST", "/preferences/", 0, url.Values{"show_track_grid": {"on"}}, true), http.StatusFound)
	after, err = store.Preferences(test.Context(), 1)
	if err != nil || !reflect.DeepEqual(saved, after) {
		test.Fatal("rejected display setting changed preferences", err)
	}
	requireCode(test, call(app, "POST", "/preferences/", 1, url.Values{"autofeed_queue": {"on"}}, true), http.StatusOK)
	saved.ShowTrackGrid = false
	after, err = store.Preferences(test.Context(), 1)
	if err != nil || !reflect.DeepEqual(saved, after) {
		test.Fatal("disabling grid changed other preferences", err)
	}
	requireCode(test, call(app, "POST", "/preferences/", 1, url.Values{}, true), http.StatusOK)
	saved.AutofeedQueue = false
	after, err = store.Preferences(test.Context(), 1)
	if err != nil || !reflect.DeepEqual(saved, after) {
		test.Fatal("shared switch form did not save all switches off", err)
	}
}

func TestTrackGridSchemaUpgrade(test *testing.T) {
	app := testApp(test)
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=7,repeat_queue=0,language_code='fr',access_token='keep-token' WHERE owner_id=1"); err != nil {
		test.Fatal(err)
	}
	before, err := app.Store.Preferences(test.Context(), 1)
	if err != nil {
		test.Fatal(err)
	}
	if _, err := app.Store.DB.Exec("ALTER TABLE reinhardt_preferences DROP COLUMN show_track_grid"); err != nil {
		test.Fatal(err)
	}
	store, err := openStore(test.Context(), app.Store.Config)
	if err != nil {
		test.Fatal(err)
	}
	defer store.DB.Close()
	after, err := store.Preferences(test.Context(), 1)
	if err != nil || !reflect.DeepEqual(before, after) {
		test.Fatal("upgrade changed existing preferences", err)
	}
	if _, err := store.DB.Exec("UPDATE reinhardt_preferences SET show_track_grid=0 WHERE owner_id=1"); err != nil {
		test.Fatal(err)
	}
	if err := migratePreferences(test.Context(), store.DB); err != nil {
		test.Fatal(err)
	}
	before.ShowTrackGrid = false
	after, err = store.Preferences(test.Context(), 1)
	if err != nil || !reflect.DeepEqual(before, after) {
		test.Fatal("repeated upgrade reset saved preferences", err)
	}
}

func TestTrackGridDefaultOnExistingSchema(test *testing.T) {
	app := testApp(test)
	// Existing SQLite columns retain their old default. New preferences must
	// explicitly enable the grid while preserving accounts already using lists.
	if _, err := app.Store.DB.Exec(`ALTER TABLE reinhardt_preferences DROP COLUMN show_track_grid;
ALTER TABLE reinhardt_preferences ADD COLUMN show_track_grid BOOL NOT NULL DEFAULT 0;
INSERT INTO auth_user(id,username) VALUES(3,'ordinary'),(4,'access-link'),(5,'language');
CREATE TABLE go_user_language(owner_id INTEGER PRIMARY KEY REFERENCES auth_user(id),language_code TEXT NOT NULL);
INSERT INTO go_user_language(owner_id,language_code) VALUES(5,'fr');`); err != nil {
		test.Fatal(err)
	}
	before, err := app.Store.Preferences(test.Context(), 1)
	if err != nil {
		test.Fatal(err)
	}
	if _, err := app.Store.Preferences(test.Context(), 3); err != nil {
		test.Fatal(err)
	}
	if _, err := app.Store.userAccessURL(test.Context(), 4, false); err != nil {
		test.Fatal(err)
	}
	managed, err := app.Store.saveManagedUser(test.Context(), User{Username: "managed", IsActive: true})
	if err != nil {
		test.Fatal(err)
	}
	if err := migratePreferences(test.Context(), app.Store.DB); err != nil {
		test.Fatal(err)
	}
	for _, owner := range []int64{3, 4, 5, managed} {
		prefs, err := app.Store.Preferences(test.Context(), owner)
		if err != nil || !prefs.ShowTrackGrid {
			test.Fatalf("new preferences for owner %d did not default to the grid: %v", owner, err)
		}
	}
	after, err := app.Store.Preferences(test.Context(), 1)
	if err != nil || !reflect.DeepEqual(before, after) || after.ShowTrackGrid {
		test.Fatal("new defaults changed an existing account's saved preferences", err)
	}
}

func TestTrackGridListings(test *testing.T) {
	app := testApp(test)
	tag := mustTag(test, app, 1, "playlist", []int64{2, 1})
	friendTag := mustTag(test, app, 2, "friend playlist", []int64{2, 1})
	mustTag(test, app, 1, "favorites", []int64{1, 2})
	queue := mustTag(test, app, 1, "queue", []int64{2, 1})
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=(SELECT id FROM reinhardt_musicfiletag WHERE tag_id=? AND music_file_id=1) WHERE owner_id=1", queue); err != nil {
		test.Fatal(err)
	}
	paths := []string{"/album/?q=Album", "/artist/?q=Artist", "/genre/?q=jazz", "/search/?sq=tune", "/favorites/", "/queue/", fmt.Sprintf("/tag/?q=%d", tag), fmt.Sprintf("/friend_tag/?tag=%d", friendTag)}
	for _, enabled := range []bool{false, true} {
		form := url.Values{}
		if enabled {
			form.Set("show_track_grid", "on")
		}
		requireCode(test, call(app, "POST", "/preferences/", 1, form, true), http.StatusOK)
		for _, path := range paths {
			for _, fragment := range []bool{false, true} {
				test.Run(fmt.Sprintf("grid=%v/fragment=%v/%s", enabled, fragment, path), func(test *testing.T) {
					response := call(app, "GET", path, 1, nil, fragment)
					requireCode(test, response, http.StatusOK)
					body := response.Body.String()
					if strings.Contains(body, " track-grid\"") != enabled || strings.Contains(body, `class="track-grid-artwork"`) != enabled {
						test.Fatal("listing did not respect grid preference")
					}
					for _, markup := range []string{`id="music_files_row_1"`, `data-trackid="1"`, `dropdown-menu-1`} {
						if !strings.Contains(body, markup) {
							test.Fatalf("listing lost track action markup %q", markup)
						}
					}
					if enabled && !regexp.MustCompile(`src="/artwork/1/256\.jpg\?v=[^"]+" alt="" width="256" height="256" loading="lazy"`).MatchString(body) {
						test.Fatal("grid artwork must use lazy-loaded thumbnails")
					}
					if enabled {
						for _, markup := range []string{`<track-card data-trackid="1"`, `class="track-card-line track-grid-artist"`, `data-track-action="listen"`, `aria-label="add to queue"`, `aria-label="More track actions"`} {
							if !strings.Contains(body, markup) {
								test.Fatalf("grid component missing %q", markup)
							}
						}
					}
					if path == "/queue/" || strings.HasPrefix(path, "/tag/") {
						if !strings.Contains(body, `class="sortable_handle"`) || !strings.Contains(body, `data-itempk="1"`) {
							test.Fatal("listing lost reorder controls")
						}
					}
					if path == "/queue/" && !strings.Contains(body, "now_playing list-group-item") {
						test.Fatal("queue lost its current-track highlight")
					}
				})
			}
		}
	}
	// A user's saved view cannot affect another account's library pages.
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_preferences SET show_track_grid=0 WHERE owner_id=2"); err != nil {
		test.Fatal(err)
	}
	requireCode(test, call(app, "POST", "/preferences/", 1, url.Values{"show_track_grid": {"on"}}, true), http.StatusOK)
	response := call(app, "GET", "/album/?q=Album", 2, nil, true)
	requireCode(test, response, http.StatusOK)
	if strings.Contains(response.Body.String(), `class="track-grid-artwork"`) {
		test.Fatal("grid preference leaked to another account")
	}
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET title='<img src=x onerror=alert(1)>' WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	response = call(app, "GET", "/album/?q=Album", 1, nil, true)
	requireCode(test, response, http.StatusOK)
	if strings.Contains(response.Body.String(), `<img src=x`) || !strings.Contains(response.Body.String(), `&lt;img src=x onerror=alert(1)&gt;`) {
		test.Fatal("grid title lost contextual escaping")
	}
}

func TestTrackCardMenuActions(test *testing.T) {
	app := testApp(test)
	tag := mustTag(test, app, 1, "playlist", []int64{1})
	for _, context := range []string{"album", "queue", "tag"} {
		for _, grid := range []bool{false, true} {
			test.Run(fmt.Sprintf("%s/grid=%v", context, grid), func(test *testing.T) {
				path := fmt.Sprintf("/music_file_dropdown/?q=1&context=%s&tag=%d", context, tag)
				if grid {
					path += "&grid=1"
				}
				response := call(app, "GET", path, 1, nil, false)
				requireCode(test, response, http.StatusOK)
				body := response.Body.String()
				if strings.Contains(body, `title="play now"`) == grid {
					test.Fatal("only compact menus should include the direct listen action")
				}
				if strings.Contains(body, `title="add to queue"`) != (!grid && context != "queue") {
					test.Fatal("grid menu duplicated the direct add action")
				}
				for _, action := range []string{"download", "manage tags", "add to favorites", "remove from tag", "details"} {
					if !strings.Contains(body, action) {
						test.Fatalf("remaining menu action missing: %s", action)
					}
				}
				if context == "queue" && !strings.Contains(body, "remove from queue") {
					test.Fatal("grid menu lost removal from queue")
				}
			})
		}
	}
}

func TestTrackCardComponent(test *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		test.Skip("optional Node.js JavaScript verification unavailable")
	}
	source, err := assets.ReadFile("static/track-card.js")
	if err != nil {
		test.Fatal(err)
	}
	command := exec.CommandContext(test.Context(), node, "testdata/track-card.cjs")
	command.Stdin = strings.NewReader(string(source))
	if output, err := command.CombinedOutput(); err != nil {
		test.Fatalf("track card component: %v\n%s", err, output)
	}
}
