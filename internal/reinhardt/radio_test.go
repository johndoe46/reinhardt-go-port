package reinhardt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mustRadioStation(test *testing.T, app *App, owner int64, name, country, address string) int64 {
	test.Helper()
	id, err := app.Store.saveRadioStation(test.Context(), owner, &RadioStation{Name: name, Country: country, URL: address})
	if err != nil {
		test.Fatal(err)
	}
	return id
}

func TestRadio666CatalogueAdditionUpgradesExistingDatabases(test *testing.T) {
	const streamURL = "https://stream2.radio666.net/remote192.mp3"
	for _, alreadyPresent := range []bool{false, true} {
		test.Run(fmt.Sprintf("alreadyPresent=%v", alreadyPresent), func(test *testing.T) {
			app := testApp(test)
			station, err := app.Store.One(test.Context(), "SELECT * FROM go_radio_station WHERE owner_id IS NULL AND url=?", streamURL)
			if err != nil || station.S("name") != "Radio 666" || station.S("country") != "France" {
				test.Fatal("new database did not receive Radio 666 from France", station, err)
			}
			// Simulate an installation initialized before this addition. Also cover
			// an existing shared URL and a separate personal station with that URL.
			if _, err = app.Store.DB.Exec("DELETE FROM go_settings WHERE key='radio_addition_radio_666_v1'"); err != nil {
				test.Fatal(err)
			}
			if !alreadyPresent {
				if _, err = app.Store.DB.Exec("DELETE FROM go_radio_station WHERE id=?", station.N("id")); err != nil {
					test.Fatal(err)
				}
			}
			personal := mustRadioStation(test, app, 1, "My Radio 666", "France", streamURL)
			before, err := app.Store.Preferences(test.Context(), 1)
			if err != nil {
				test.Fatal(err)
			}
			upgraded, err := openStore(test.Context(), app.Store.Config)
			if err != nil {
				test.Fatal(err)
			}
			defer upgraded.DB.Close()
			shared, err := upgraded.Query(test.Context(), "SELECT * FROM go_radio_station WHERE owner_id IS NULL AND url=?", streamURL)
			if err != nil || len(shared) != 1 || shared[0].S("name") != "Radio 666" || shared[0].S("country") != "France" {
				test.Fatal("upgrade must add exactly one shared Radio 666 station", shared, err)
			}
			after, err := upgraded.Preferences(test.Context(), 1)
			if err != nil || !reflect.DeepEqual(before, after) {
				test.Fatal("catalogue addition changed user preferences", err)
			}
			if _, err = upgraded.RadioStation(test.Context(), 1, personal); err != nil {
				test.Fatal("catalogue addition changed an existing personal station", err)
			}
			id := shared[0].N("id")
			if err = upgraded.deleteRadioStation(test.Context(), 1, id); err != nil {
				test.Fatal(err)
			}
			if err = upgraded.seedRadioStations(test.Context()); err != nil {
				test.Fatal(err)
			}
			if _, err = upgraded.RadioStation(test.Context(), 1, id); err != sql.ErrNoRows {
				test.Fatal("repeat initialization restored a hidden Radio 666", err)
			}
			if _, err = upgraded.RadioStation(test.Context(), 2, id); err != nil {
				test.Fatal("Radio 666 is unavailable to another account", err)
			}
			shared, err = upgraded.Query(test.Context(), "SELECT id FROM go_radio_station WHERE owner_id IS NULL AND url=?", streamURL)
			if err != nil || len(shared) != 1 || shared[0].N("id") != id {
				test.Fatal("repeat initialization duplicated Radio 666", shared, err)
			}
		})
	}
}

func TestRadioCatalogueInitializationPreservesExistingData(test *testing.T) {
	app := testApp(test)
	data, err := assets.ReadFile("radio/stations.json")
	if err != nil {
		test.Fatal(err)
	}
	var stations []RadioStation
	if err = json.Unmarshal(data, &stations); err != nil {
		test.Fatal(err)
	}
	countries := map[string]bool{}
	urls := map[string]bool{}
	for _, station := range stations {
		if station.Name == "" || station.Country == "" || urls[station.URL] {
			test.Fatal("catalogue entry is incomplete or duplicates a stream", station)
		}
		if _, err = parseRadioURL(station.URL); err != nil {
			test.Fatal("invalid seed URL", station.URL, err)
		}
		urls[station.URL], countries[station.Country] = true, true
	}
	if len(stations) != 16948 || len(countries) != 171 {
		test.Fatalf("unexpected pinned catalogue: %d stations, %d countries", len(stations), len(countries))
	}
	mustTag(test, app, 1, "queue", []int64{1, 2})
	if _, err = app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=7 WHERE owner_id=1"); err != nil {
		test.Fatal(err)
	}
	before, _ := app.Store.Preferences(test.Context(), 1)
	// Simulate an older installation without the new tables or seed marker.
	if _, err = app.Store.DB.Exec(`DROP TRIGGER go_radio_leave_on_queue_selection;
DROP TABLE go_radio_playback; DROP TABLE go_radio_hidden; DROP TABLE go_radio_station;
DELETE FROM go_settings WHERE key='radio_seed_v1' OR key LIKE 'radio_addition_%'`); err != nil {
		test.Fatal(err)
	}
	upgraded, err := openStore(test.Context(), app.Store.Config)
	if err != nil {
		test.Fatal(err)
	}
	defer upgraded.DB.Close()
	after, err := upgraded.Preferences(test.Context(), 1)
	if err != nil || !reflect.DeepEqual(before, after) {
		test.Fatal("upgrade changed existing preferences", err)
	}
	items, err := upgraded.Queue(test.Context(), 1)
	if err != nil || len(items) != 2 {
		test.Fatal("upgrade changed the queue", err)
	}
	personal := mustRadioStation(test, app, 1, "Personal", "France", "https://radio.example/live.mp3")
	if err = upgraded.deleteRadioStation(test.Context(), 1, 1); err != nil {
		test.Fatal(err)
	}
	if err = upgraded.seedRadioStations(test.Context()); err != nil {
		test.Fatal(err)
	}
	count, err := upgraded.One(test.Context(), "SELECT count(*) total FROM go_radio_station")
	if err != nil || count.N("total") != int64(len(stations)+2) {
		test.Fatal("repeat initialization duplicated the catalogue", count, err)
	}
	if _, err = upgraded.RadioStation(test.Context(), 1, 1); err != sql.ErrNoRows {
		test.Fatal("repeat initialization restored a hidden station", err)
	}
	if _, err = upgraded.RadioStation(test.Context(), 1, personal); err != nil {
		test.Fatal("repeat initialization removed a personal station", err)
	}
}

func TestRadioManagementAndAccountIsolation(test *testing.T) {
	app := testApp(test)
	requireCode(test, call(app, "GET", "/radio/", 0, nil, false), 302)
	response := call(app, "GET", "/radio/?country=France", 1, nil, false)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `value="France" selected`) || !strings.Contains(response.Body.String(), `up-keep id="player"`) || strings.Count(response.Body.String(), `class="list-group-item d-flex`) != 50 {
		test.Fatal("country page is missing stations, pagination, or persistent player")
	}
	response = call(app, "GET", "/radio/?q=unlikely-station-unique-name", 1, nil, true)
	requireCode(test, response, 200)
	if strings.Contains(response.Body.String(), "<m-audio") || !strings.Contains(response.Body.String(), "No stations found.") {
		test.Fatal("radio navigation replaced the player or search was ignored")
	}
	for _, values := range []url.Values{
		{"name": {""}, "country": {"France"}, "url": {"https://radio.example/live"}},
		{"name": {"Radio"}, "country": {""}, "url": {"https://radio.example/live"}},
		{"name": {"Radio"}, "country": {"France"}, "url": {"http://127.0.0.1/live"}},
	} {
		requireCode(test, call(app, "POST", "/radio/new/", 1, values, true), 422)
	}
	requireCode(test, call(app, "POST", "/radio/new/", 1, url.Values{"name": {`<script>alert(1)</script>`}, "country": {"France"}, "url": {"https://radio.example/live.mp3"}}, true), 302)
	row, err := app.Store.One(test.Context(), "SELECT id FROM go_radio_station WHERE owner_id=1")
	if err != nil {
		test.Fatal(err)
	}
	id := row.N("id")
	response = call(app, "GET", "/radio/?q=alert", 1, nil, true)
	if strings.Contains(response.Body.String(), "<script>alert(1)</script>") || !strings.Contains(response.Body.String(), "&lt;script&gt;") {
		test.Fatal("station names are not HTML escaped")
	}
	if !strings.Contains(response.Body.String(), `up-confirm="Remove station &lt;script&gt;alert(1)&lt;/script&gt;?"`) {
		test.Fatal("station deletion must ask for confirmation with the escaped station name")
	}
	for _, action := range []string{"play", "audio"} {
		method := "POST"
		if action == "audio" {
			method = "GET"
		}
		requireCode(test, call(app, method, fmt.Sprintf("/radio/%d/%s/", id, action), 2, url.Values{}, true), 404)
	}
	for _, action := range []string{"play", "delete"} {
		requireCode(test, call(app, "GET", fmt.Sprintf("/radio/%d/%s/", id, action), 1, nil, false), 405)
	}
	request := httptest.NewRequest("POST", fmt.Sprintf("/radio/%d/play/", id), nil)
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	response = httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, 403)
	// Shared station edits create a private copy and hide the original only for
	// the editing account, including its selected playback association.
	requireCode(test, call(app, "POST", "/radio/1/play/", 1, url.Values{}, true), 200)
	clone, err := app.Store.saveRadioStation(test.Context(), 1, &RadioStation{ID: 1, Name: "My edit", Country: "France", URL: "https://radio.example/new.mp3"})
	if err != nil || clone == 1 {
		test.Fatal("shared station was not copied", err)
	}
	if _, err = app.Store.RadioStation(test.Context(), 1, 1); err != sql.ErrNoRows {
		test.Fatal("original is still visible to editing account", err)
	}
	if station, err := app.Store.RadioStation(test.Context(), 2, 1); err != nil || station.Name == "My edit" {
		test.Fatal("shared edit changed another account", err)
	}
	if station, err := app.Store.currentRadioStation(test.Context(), 1); err != nil || station.ID != clone {
		test.Fatal("shared edit lost the active station", err)
	}
	requireCode(test, call(app, "POST", fmt.Sprintf("/radio/%d/delete/", clone), 1, url.Values{}, true), 302)
	if station, err := app.Store.currentRadioStation(test.Context(), 1); err != nil || station != nil {
		test.Fatal("deletion left stale playback", err)
	}
}

func TestRadioManagementRequiresSuperuser(test *testing.T) {
	app := testApp(test)
	id := mustRadioStation(test, app, 2, "Existing personal station", "France", "https://radio.example/live.mp3")
	queries := []string{
		fmt.Sprintf("SELECT * FROM go_radio_station WHERE id IN (1,%d) ORDER BY id", id),
		"SELECT * FROM go_radio_hidden ORDER BY owner_id,station_id",
		"SELECT * FROM go_radio_playback ORDER BY owner_id",
	}
	before := make([][]Row, len(queries))
	for index, query := range queries {
		var err error
		before[index], err = app.Store.Query(test.Context(), query)
		if err != nil {
			test.Fatal(err)
		}
	}
	// Staff accounts have no station management privileges unless they are
	// also superusers. Deny direct requests even for their own personal station.
	for _, staff := range []bool{false, true} {
		if _, err := app.Store.DB.Exec("UPDATE auth_user SET is_staff=? WHERE id=2", staff); err != nil {
			test.Fatal(err)
		}
		for _, method := range []string{"GET", "HEAD", "POST"} {
			for _, path := range []string{
				"/radio/new/", "/radio/1/edit/", "/radio/1/delete/",
				fmt.Sprintf("/radio/%d/edit/", id), fmt.Sprintf("/radio/%d/delete/", id),
			} {
				for _, fragment := range []bool{false, true} {
					test.Run(fmt.Sprintf("staff=%v/%s/%s/fragment=%v", staff, method, path, fragment), func(test *testing.T) {
						form := url.Values{"name": {"Unauthorized change"}, "country": {"Germany"}, "url": {"https://radio.example/changed.mp3"}}
						requireCode(test, call(app, method, path, 2, form, fragment), 403)
					})
				}
			}
		}
	}
	for index, query := range queries {
		after, err := app.Store.Query(test.Context(), query)
		if err != nil || !reflect.DeepEqual(before[index], after) {
			test.Fatal("denied management request changed station or playback data", query, err)
		}
	}
	count, err := app.Store.One(test.Context(), "SELECT count(*) total FROM go_radio_station")
	if err != nil || count.N("total") != 16950 {
		test.Fatal("denied creation added a station", err)
	}
	for _, fragment := range []bool{false, true} {
		response := call(app, "GET", "/radio/", 2, nil, fragment)
		requireCode(test, response, 200)
		for _, control := range []string{`href="/radio/new/"`, "/edit/", "/delete/"} {
			if strings.Contains(response.Body.String(), control) {
				test.Fatal("regular account was shown a station management control", control)
			}
		}
		response = call(app, "GET", "/radio/", 1, nil, fragment)
		requireCode(test, response, 200)
		for _, control := range []string{`href="/radio/new/"`, "/edit/", "/delete/"} {
			if !strings.Contains(response.Body.String(), control) {
				test.Fatal("superuser is missing a station management control", control)
			}
		}
	}
	// Playback and returning to the queue remain available to ordinary accounts;
	// their player menu must not offer the station edit form.
	response := call(app, "POST", "/radio/1/play/", 2, url.Values{}, true)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `data-kind="live"`) || strings.Contains(response.Body.String(), "/edit/") {
		test.Fatal("regular account playback was restricted or exposed the edit action")
	}
	app.RadioClient.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"audio/mpeg"}}, Body: io.NopCloser(strings.NewReader("audio"))}, nil
	})
	requireCode(test, call(app, "GET", "/radio/1/audio/", 2, nil, false), 200)
	requireCode(test, call(app, "POST", "/radio/queue/", 2, url.Values{}, true), 200)
	// Promoting the account must immediately enable all three operations.
	if _, err = app.Store.DB.Exec("UPDATE auth_user SET is_superuser=1 WHERE id=2"); err != nil {
		test.Fatal(err)
	}
	requireCode(test, call(app, "GET", "/radio/new/", 2, nil, false), 200)
	requireCode(test, call(app, "GET", fmt.Sprintf("/radio/%d/edit/", id), 2, nil, false), 200)
	form := url.Values{"name": {"Authorized change"}, "country": {"Germany"}, "url": {"https://radio.example/changed.mp3"}}
	requireCode(test, call(app, "POST", fmt.Sprintf("/radio/%d/edit/", id), 2, form, true), 302)
	station, err := app.Store.RadioStation(test.Context(), 2, id)
	if err != nil || station.Name != "Authorized change" {
		test.Fatal("superuser could not modify a station", err)
	}
	requireCode(test, call(app, "POST", fmt.Sprintf("/radio/%d/delete/", id), 2, url.Values{}, true), 302)
	requireCode(test, call(app, "POST", "/radio/new/", 2, form, true), 302)
}

func TestRadioPlaybackPreservesQueueAndSurvivesRestart(test *testing.T) {
	app := testApp(test)
	mustTag(test, app, 1, "queue", []int64{1, 2})
	requireCode(test, call(app, "POST", "/next_song/", 1, url.Values{}, true), 200)
	before, _ := app.Store.Preferences(test.Context(), 1)
	id := mustRadioStation(test, app, 1, "Concerts", "France", "https://radio.example/live.mp3")
	response := call(app, "POST", fmt.Sprintf("/radio/%d/play/", id), 1, url.Values{}, true)
	requireCode(test, response, 200)
	for _, text := range []string{`data-kind="live"`, `data-title="Concerts"`, `data-artist="France"`, `data-autoplay="true"`, "Return to queue"} {
		if !strings.Contains(response.Body.String(), text) {
			test.Fatal("missing live playback data", text)
		}
	}
	after, _ := app.Store.Preferences(test.Context(), 1)
	if !reflect.DeepEqual(before, after) {
		test.Fatal("radio changed queue position or preferences")
	}
	store, err := openStore(test.Context(), app.Store.Config)
	if err != nil {
		test.Fatal(err)
	}
	defer store.DB.Close()
	restarted, err := newApp(store)
	if err != nil {
		test.Fatal(err)
	}
	for _, path := range []string{"/", "/queue/", "/radio/", "/preferences/", "/music_file_tagger/?q=1"} {
		response = call(restarted, "GET", path, 1, nil, false)
		requireCode(test, response, 200)
		if !strings.Contains(response.Body.String(), `data-kind="live"`) || strings.Contains(response.Body.String(), `data-autoplay="true"`) {
			test.Fatal("full-page restore lost radio or forced autoplay", path)
		}
		if strings.Contains(path, "music_file_tagger") && !strings.Contains(response.Body.String(), "First tune") {
			test.Fatal("radio footer replaced the track being edited")
		}
	}
	response = call(app, "POST", "/radio/queue/", 1, url.Values{}, true)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `data-url="/stream/1/"`) {
		test.Fatal("return to queue advanced past the saved item")
	}
	requireCode(test, call(app, "POST", fmt.Sprintf("/radio/%d/play/", id), 1, url.Values{}, true), 200)
	requireCode(test, call(app, "POST", "/next_song/", 1, url.Values{}, true), 200)
	if station, err := app.Store.currentRadioStation(test.Context(), 1); err != nil || station != nil {
		test.Fatal("queue playback did not clear radio mode", err)
	}
	if _, err = store.DB.Exec("PRAGMA query_only=ON"); err != nil {
		test.Fatal(err)
	}
	requireCode(test, call(restarted, "GET", "/radio/", 1, nil, false), 200)
}

func TestRadioDestinationValidation(test *testing.T) {
	for _, raw := range []string{
		"file:///etc/passwd", "ftp://radio.example/live", "https://user:pass@radio.example/live", "http://localhost/live", "http://station.local/live",
		"http://127.0.0.1/live", "http://10.0.0.1/live", "http://169.254.169.254/live", "http://100.64.0.1/live", "http://192.0.2.1/live", "http://224.0.0.1/live",
		"http://[::1]/live", "http://[::ffff:127.0.0.1]/live", "http://[fc00::1]/live", "http://[64:ff9b::a00:1]/live", "http://radio.example:99999/live", "http://radio.example/live#fragment",
	} {
		if _, err := parseRadioURL(raw); err == nil {
			test.Errorf("accepted unsafe URL %s", raw)
		}
	}
	for _, raw := range []string{"https://icecast.radiofrance.fr/live.mp3", "http://8.8.8.8:8000/live", "https://[2606:4700:4700::1111]/live"} {
		if _, err := parseRadioURL(raw); err != nil {
			test.Errorf("rejected public URL %s: %v", raw, err)
		}
	}
	dialCalls := 0
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		dialCalls++
		if address != "8.8.8.8:8000" {
			test.Fatal("dial repeated DNS resolution or used an unchecked address", address)
		}
		left, right := net.Pipe()
		test.Cleanup(func() { left.Close(); right.Close() })
		return left, nil
	}
	if _, err := radioDialContext(lookup, dial)(test.Context(), "tcp", "radio.example:8000"); err == nil || dialCalls != 0 {
		test.Fatal("mixed public/private DNS results reached dial", err)
	}
	lookup = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}
	if _, err := radioDialContext(lookup, dial)(test.Context(), "tcp", "radio.example:8000"); err != nil || dialCalls != 1 {
		test.Fatal("public destination was not pinned", err)
	}
	client := newRadioClient()
	for _, target := range []string{"http://127.0.0.1/live", "https://user:pass@radio.example/live", "file:///tmp/live"} {
		request, _ := http.NewRequest("GET", target, nil)
		if err := client.CheckRedirect(request, []*http.Request{{}}); err == nil {
			test.Fatal("accepted unsafe redirect", target)
		}
	}
	request, _ := http.NewRequest("GET", "https://radio.example/live", nil)
	if err := client.CheckRedirect(request, make([]*http.Request, 5)); err == nil {
		test.Fatal("unbounded redirects")
	}
}

func TestRadioRelayAcceptsZeroMetadataInterval(test *testing.T) {
	app := testApp(test)
	id := mustRadioStation(test, app, 1, "Nova Jazz", "France", "https://radio.example/live.mp3")
	for _, interval := range []string{"", "0", "000", " 0 ", "16000", "-1", "invalid"} {
		for _, method := range []string{"GET", "HEAD"} {
			test.Run(method+"/interval="+interval, func(test *testing.T) {
				app.RadioClient.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: 200,
						Header:     http.Header{"Content-Type": {"audio/mpeg"}, "Icy-Metaint": {interval}},
						Body:       io.NopCloser(strings.NewReader("audio")),
					}, nil
				})
				response := call(app, method, fmt.Sprintf("/radio/%d/audio/", id), 1, nil, false)
				if interval == "16000" || interval == "-1" || interval == "invalid" {
					requireCode(test, response, 502)
					return
				}
				requireCode(test, response, 200)
				if method == "GET" && response.Body.String() != "audio" {
					test.Fatal("relay did not forward audio without ICY metadata")
				}
				if method == "HEAD" && response.Body.Len() != 0 {
					test.Fatal("relay returned a body for HEAD")
				}
			})
		}
	}
}

func TestRadioRelayHeadersCancellationAndUnsupportedResponses(test *testing.T) {
	app := testApp(test)
	id := mustRadioStation(test, app, 1, "Live", "France", "https://radio.example/live")
	for _, method := range []string{"GET", "HEAD"} {
		var body *delayedAudioBody
		app.RadioClient = &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("Range") != "" || request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" || request.Header.Get("Accept-Encoding") != "identity" || request.Header.Get("Icy-MetaData") != "0" || request.UserAgent() != "Reinhardt/1.0" {
				test.Fatal("relay forwarded sensitive or inappropriate headers", request.Header)
			}
			body = &delayedAudioBody{ctx: request.Context(), data: strings.NewReader("audio")}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"audio/mpeg"}}, Body: body}, nil
		})}
		app.Client.Timeout = time.Millisecond
		request := httptest.NewRequest(method, fmt.Sprintf("/radio/%d/audio/", id), nil)
		request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
		request.Header.Set("Range", "bytes=10-20")
		request.Header.Set("Authorization", "Bearer private")
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		requireCode(test, response, 200)
		if body == nil || !body.closed || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("X-Accel-Buffering") != "no" || response.Header().Get("Accept-Ranges") != "none" {
			test.Fatal("relay did not close body or disable buffering/ranges/cache")
		}
		if (method == "HEAD" && response.Body.Len() != 0) || (method == "GET" && response.Body.String() != "audio") {
			test.Fatal("relay truncated audio or sent a HEAD body")
		}
	}
	for _, headers := range []http.Header{
		{"Content-Type": {"text/html"}}, {"Content-Type": {"application/vnd.apple.mpegurl"}},
		{"Content-Type": {"audio/mpeg"}, "Icy-Metaint": {"16000"}},
	} {
		app.RadioClient.Transport = transportFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader("not audio"))}, nil
		})
		requireCode(test, call(app, "GET", fmt.Sprintf("/radio/%d/audio/", id), 1, nil, false), 502)
	}
	ctx, cancel := context.WithCancel(test.Context())
	var body *delayedAudioBody
	app.RadioClient.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
		body = &delayedAudioBody{ctx: request.Context(), data: strings.NewReader("audio")}
		cancel()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"audio/mpeg"}}, Body: body}, nil
	})
	request := httptest.NewRequest("GET", fmt.Sprintf("/radio/%d/audio/", id), nil).WithContext(ctx)
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if body == nil || !body.closed || response.Body.Len() != 0 {
		test.Fatal("cancelled listener retained upstream or wrote audio")
	}
}
