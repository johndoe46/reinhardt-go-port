package reinhardt

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestRadioFavoritesArePersonalAndSeparateFromMusicFavorites(test *testing.T) {
	app := testApp(test)
	mustTag(test, app, 1, "favorites", []int64{1})
	mustTag(test, app, 2, "favorites", []int64{2})
	station, err := app.Store.One(test.Context(), "SELECT id FROM go_radio_station WHERE name='Radio 666' AND owner_id IS NULL")
	if err != nil {
		test.Fatal(err)
	}
	id := station.N("id")
	requireCode(test, call(app, "POST", fmt.Sprintf("/radio/%d/play/", id), 2, url.Values{}, true), 200)
	queries := []string{
		"SELECT * FROM reinhardt_tag ORDER BY id",
		"SELECT * FROM reinhardt_musicfiletag ORDER BY id",
		"SELECT * FROM reinhardt_preferences ORDER BY id",
		"SELECT * FROM go_radio_playback ORDER BY owner_id",
	}
	before := make([][]Row, len(queries))
	for index, query := range queries {
		before[index], err = app.Store.Query(test.Context(), query)
		if err != nil {
			test.Fatal(err)
		}
	}
	form := url.Values{"favorite": {"1"}, "q": {"Radio 666"}, "country": {"France"}, "favorites": {"1"}, "page": {"2"}}
	for range 2 {
		response := call(app, "POST", fmt.Sprintf("/radio/%d/favorite/", id), 2, form, true)
		requireCode(test, response, 302)
		location, err := url.Parse(response.Header().Get("Location"))
		if err != nil || location.Path != "/radio/" || location.Query().Get("q") != "Radio 666" || location.Query().Get("country") != "France" || location.Query().Get("favorites") != "1" || location.Query().Get("page") != "2" {
			test.Fatal("favorite action lost search filters", location, err)
		}
		response = call(app, "GET", location.String(), 2, nil, true)
		requireCode(test, response, 200)
		for _, text := range []string{`<strong class="text-break">Radio 666</strong>`, `aria-pressed="true"`, `Remove Radio 666 from radio favorites`, `name="favorites" value="1" checked`, `1 station`} {
			if !strings.Contains(response.Body.String(), text) {
				test.Fatal("missing radio favorite state", text)
			}
		}
		if strings.Contains(response.Body.String(), "<m-audio") || strings.Contains(response.Body.String(), `up-target="main, #now_playing"`) {
			test.Fatal("favoriting as a regular account must update only main")
		}
	}
	rows, err := app.Store.Query(test.Context(), "SELECT * FROM go_radio_favorite")
	if err != nil || len(rows) != 1 || rows[0].N("owner_id") != 2 || rows[0].N("station_id") != id {
		test.Fatal("repeated favorite requests must save one association", rows, err)
	}
	response := call(app, "GET", "/radio/?favorites=1", 1, nil, true)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), "No stations found.") {
		test.Fatal("another account saw a personal radio favorite")
	}
	for range 2 {
		requireCode(test, call(app, "POST", fmt.Sprintf("/radio/%d/favorite/", id), 2, url.Values{"favorite": {"0"}}, true), 302)
	}
	response = call(app, "GET", "/radio/?favorites=1", 2, nil, true)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), "No stations found.") {
		test.Fatal("removing a radio favorite did not update the filter")
	}
	response = call(app, "GET", "/radio/?q=Radio+666", 2, nil, true)
	if !strings.Contains(response.Body.String(), `aria-pressed="false"`) || !strings.Contains(response.Body.String(), "Add Radio 666 to radio favorites") {
		test.Fatal("station did not return to its unfavorited state")
	}
	for index, query := range queries {
		after, err := app.Store.Query(test.Context(), query)
		if err != nil || !reflect.DeepEqual(before[index], after) {
			test.Fatal("radio favorite action changed music favorites, preferences, or playback", query, err)
		}
	}
}

func TestRadioFavoritesFilterCombinesSearchCountryAndPagination(test *testing.T) {
	app := testApp(test)
	if _, err := app.Store.DB.Exec(`WITH RECURSIVE numbers(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM numbers WHERE n<50)
INSERT INTO go_radio_station(owner_id,name,country,url)
SELECT 2,printf('Saved station %02d',n),'France',printf('https://radio.example/%d',n) FROM numbers;
INSERT INTO go_radio_favorite(owner_id,station_id) SELECT 2,id FROM go_radio_station WHERE owner_id=2`); err != nil {
		test.Fatal(err)
	}
	otherCountry := mustRadioStation(test, app, 2, "Saved station Germany", "Germany", "https://radio.example/germany")
	mustRadioStation(test, app, 2, "Saved station not a favorite", "France", "https://radio.example/not-favorite")
	requireCode(test, call(app, "POST", fmt.Sprintf("/radio/%d/favorite/", otherCountry), 2, url.Values{"favorite": {"1"}}, true), 302)
	response := call(app, "GET", "/radio/?q=Saved+station&country=France&favorites=1", 2, nil, true)
	requireCode(test, response, 200)
	if strings.Count(response.Body.String(), `<li class="list-group-item d-flex`) != 50 || !strings.Contains(response.Body.String(), "51 stations") || strings.Contains(response.Body.String(), `<strong class="text-break">Saved station Germany</strong>`) || strings.Contains(response.Body.String(), `<strong class="text-break">Saved station not a favorite</strong>`) {
		test.Fatal("favorite filter did not combine with search and country")
	}
	link := regexp.MustCompile(`<a class="btn btn-outline-secondary" href="([^"]+)">Next</a>`).FindStringSubmatch(response.Body.String())
	if len(link) != 2 {
		test.Fatal("missing next page link")
	}
	next, err := url.Parse(html.UnescapeString(link[1]))
	if err != nil || next.Query().Get("q") != "Saved station" || next.Query().Get("country") != "France" || next.Query().Get("favorites") != "1" || next.Query().Get("page") != "2" {
		test.Fatal("pagination lost favorite search filters", next, err)
	}
	response = call(app, "GET", next.String(), 2, nil, true)
	requireCode(test, response, 200)
	if strings.Count(response.Body.String(), `<li class="list-group-item d-flex`) != 1 || !strings.Contains(response.Body.String(), `<strong class="text-break">Saved station 50</strong>`) {
		test.Fatal("favorite pagination did not return the last station")
	}
	last, err := app.Store.One(test.Context(), "SELECT id FROM go_radio_station WHERE name='Saved station 50'")
	if err != nil {
		test.Fatal(err)
	}
	response = call(app, "POST", fmt.Sprintf("/radio/%d/favorite/", last.N("id")), 2, url.Values{"favorite": {"0"}, "q": {"Saved station"}, "country": {"France"}, "favorites": {"1"}, "page": {"2"}}, true)
	requireCode(test, response, 302)
	response = call(app, "GET", response.Header().Get("Location"), 2, nil, true)
	requireCode(test, response, 200)
	if strings.Count(response.Body.String(), `<li class="list-group-item d-flex`) != 50 || !strings.Contains(response.Body.String(), "50 stations") || strings.Contains(response.Body.String(), `<strong class="text-break">Saved station 50</strong>`) {
		test.Fatal("removing the last favorite on a page must return to a populated page")
	}
	if _, err = app.Store.DB.Exec("PRAGMA query_only=ON"); err != nil {
		test.Fatal(err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		requireCode(test, call(app, method, "/radio/?favorites=1&country=France&q=Saved", 2, nil, false), 200)
	}
}

func TestRadioFavoritesRequireAuthenticationCSRFAndVisibleStation(test *testing.T) {
	app := testApp(test)
	path := "/radio/1/favorite/"
	for _, method := range []string{"GET", "HEAD"} {
		requireCode(test, call(app, method, path, 2, nil, false), 405)
	}
	requireCode(test, call(app, "POST", path, 0, url.Values{"favorite": {"1"}}, true), 302)
	requireCode(test, call(app, "POST", path, 2, url.Values{"favorite": {"invalid"}}, true), 400)
	request := httptest.NewRequest("POST", path, strings.NewReader("favorite=1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-2"})
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, 403)
	private := mustRadioStation(test, app, 1, "Private", "France", "https://radio.example/private")
	requireCode(test, call(app, "POST", fmt.Sprintf("/radio/%d/favorite/", private), 2, url.Values{"favorite": {"1"}}, true), 404)
	if _, err := app.Store.DB.Exec("INSERT INTO go_radio_hidden(owner_id,station_id) VALUES(2,2)"); err != nil {
		test.Fatal(err)
	}
	requireCode(test, call(app, "POST", "/radio/2/favorite/", 2, url.Values{"favorite": {"1"}}, true), 404)
	// Header-based CSRF authentication must still parse the submitted state.
	request = httptest.NewRequest("POST", path, strings.NewReader("favorite=1"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-CSRFToken", strings.Repeat("a", 64))
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-2"})
	request.AddCookie(&http.Cookie{Name: "csrftoken", Value: strings.Repeat("a", 64)})
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, 302)
	requireCode(test, call(app, "POST", path, 2, url.Values{"favorite": {"1"}, "owner_id": {"1"}}, true), 302)
	requireCode(test, call(app, "POST", path, 1, url.Values{"favorite": {"0"}, "owner_id": {"2"}}, true), 302)
	rows, err := app.Store.Query(test.Context(), "SELECT * FROM go_radio_favorite")
	if err != nil || len(rows) != 1 || rows[0].N("owner_id") != 2 || rows[0].N("station_id") != 1 {
		test.Fatal("favorite requests bypassed visibility or account ownership", rows, err)
	}
}

func TestRadioFavoritesSurviveStationEditsAndAreCleanedUpOnRemoval(test *testing.T) {
	app := testApp(test)
	for _, owner := range []int{1, 2} {
		requireCode(test, call(app, "POST", "/radio/1/favorite/", owner, url.Values{"favorite": {"1"}}, true), 302)
	}
	clone, err := app.Store.saveRadioStation(test.Context(), 1, &RadioStation{ID: 1, Name: "Edited station", Country: "France", URL: "https://radio.example/edited"})
	if err != nil {
		test.Fatal(err)
	}
	rows, err := app.Store.Query(test.Context(), "SELECT * FROM go_radio_favorite ORDER BY owner_id")
	if err != nil || len(rows) != 2 || rows[0].N("station_id") != clone || rows[1].N("station_id") != 1 {
		test.Fatal("editing a shared station lost favorites or changed another account", rows, err)
	}
	if err = app.Store.deleteRadioStation(test.Context(), 1, clone); err != nil {
		test.Fatal(err)
	}
	if err = app.Store.deleteRadioStation(test.Context(), 2, 1); err != nil {
		test.Fatal(err)
	}
	rows, err = app.Store.Query(test.Context(), "SELECT * FROM go_radio_favorite")
	if err != nil || len(rows) != 0 {
		test.Fatal("deleted or hidden stations left favorite associations", rows, err)
	}
	requireCode(test, call(app, "POST", "/radio/2/favorite/", 2, url.Values{"favorite": {"1"}}, true), 302)
	if _, err = app.Store.DB.Exec("DELETE FROM auth_user WHERE id=2"); err != nil {
		test.Fatal(err)
	}
	rows, err = app.Store.Query(test.Context(), "SELECT * FROM go_radio_favorite")
	if err != nil || len(rows) != 0 {
		test.Fatal("deleted account left radio favorites", rows, err)
	}
}

func TestRadioFavoritesSchemaUpgradeAndRestartPreserveData(test *testing.T) {
	app := testApp(test)
	musicFavoritesTag := mustTag(test, app, 2, "favorites", []int64{2})
	if _, err := app.Store.DB.Exec("DROP TABLE go_radio_favorite"); err != nil {
		test.Fatal(err)
	}
	store, err := openStore(test.Context(), app.Store.Config)
	if err != nil {
		test.Fatal(err)
	}
	defer store.DB.Close()
	upgraded, err := newApp(store)
	if err != nil {
		test.Fatal(err)
	}
	requireCode(test, call(upgraded, "POST", "/radio/1/favorite/", 2, url.Values{"favorite": {"1"}}, true), 302)
	restartedStore, err := openStore(test.Context(), app.Store.Config)
	if err != nil {
		test.Fatal(err)
	}
	defer restartedStore.DB.Close()
	restarted, err := newApp(restartedStore)
	if err != nil {
		test.Fatal(err)
	}
	response := call(restarted, "GET", "/radio/?favorites=1", 2, nil, false)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), "1 station") || !strings.Contains(response.Body.String(), `aria-pressed="true"`) {
		test.Fatal("radio favorite did not survive a restart")
	}
	musicFavorites, err := restartedStore.TagTracks(test.Context(), musicFavoritesTag)
	if err != nil || len(musicFavorites) != 1 || musicFavorites[0].ID != 2 {
		test.Fatal("schema upgrade changed music favorites", err)
	}
}
