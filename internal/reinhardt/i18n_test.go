package reinhardt

import (
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func TestUserLanguageSettings(test *testing.T) {
	app := testApp(test)
	before, err := app.Store.Preferences(1)
	if err != nil {
		test.Fatal(err)
	}
	response := call(app, "GET", "/preferences/", 1, nil, false)
	requireCode(test, response, 200)
	for _, text := range []string{`<html lang="en"`, `up-submit up-autosubmit up-target="main" up-history="false"`, `value="en"`, `value="fr"`, "English", "Français", "Language changes take full effect after a full page refresh."} {
		if !strings.Contains(response.Body.String(), text) {
			test.Fatalf("settings missing %q", text)
		}
	}
	languageForm := regexp.MustCompile(`(?s)<form id="language_preferences".*?</form>`).FindString(response.Body.String())
	if strings.Contains(languageForm, "<button") {
		test.Fatal("language selector must save automatically without a submit button")
	}
	response = call(app, "POST", "/preferences/", 1, url.Values{"language_code": {"fr"}}, true)
	requireCode(test, response, 200)
	if response.Header().Get("Location") != "" || strings.Contains(response.Body.String(), `<m-audio`) || strings.Contains(response.Body.String(), `<nav class="navbar`) {
		test.Fatal("language change must update the main fragment without navigating or replacing the player")
	}
	for _, text := range []string{`value="fr" selected`, "Langue", "Le changement de langue prendra pleinement effet après une actualisation complète de la page."} {
		if !strings.Contains(response.Body.String(), text) {
			test.Fatalf("saved language fragment missing %q", text)
		}
	}
	after, err := app.Store.Preferences(1)
	before["language_code"] = "fr"
	if err != nil || !reflect.DeepEqual(before, after) {
		test.Fatal("changing language changed playback preferences", err)
	}
	response = call(app, "GET", "/preferences/", 1, nil, false)
	requireCode(test, response, 200)
	for _, text := range []string{`<html lang="fr"`, `value="fr" selected`, "Langue", "préférences"} {
		if !strings.Contains(response.Body.String(), text) {
			test.Fatalf("French settings missing %q", text)
		}
	}
	// Another app instance must see the saved preference, without global locale
	// state or dependence on the submitting browser's session.
	store, err := openStore(app.Store.Config)
	if err != nil {
		test.Fatal(err)
	}
	defer store.DB.Close()
	restarted, err := newApp(store)
	if err != nil {
		test.Fatal(err)
	}
	response = call(restarted, "GET", "/", 1, nil, false)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `<html lang="fr"`) || !strings.Contains(response.Body.String(), "accueil") {
		test.Fatal("saved language did not survive app restart")
	}
	response = call(app, "GET", "/", 2, nil, false)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `<html lang="en"`) || !strings.Contains(response.Body.String(), "home") {
		test.Fatal("one user's preference changed another user's language")
	}
	// Older playback-only submissions preserve the saved language.
	requireCode(test, call(app, "POST", "/preferences/", 1, url.Values{"autofeed_queue": {"on"}}, true), 200)
	saved, err := store.Preferences(1)
	if err != nil || saved.S("language_code") != "fr" {
		test.Fatal("playback submission discarded saved language", err)
	}
	app.Store.Config["LANGUAGE_CODE"] = "fr-fr"
	requireCode(test, call(app, "POST", "/preferences/", 1, url.Values{"language_code": {"en"}}, false), 200)
	response = call(app, "GET", "/", 1, nil, false)
	if !strings.Contains(response.Body.String(), `<html lang="en"`) {
		test.Fatal("explicit English preference did not override server default")
	}
	requireCode(test, call(app, "POST", "/preferences/", 1, url.Values{"language_code": {""}}, false), 200)
	response = call(app, "GET", "/", 1, nil, false)
	if !strings.Contains(response.Body.String(), `<html lang="fr"`) {
		test.Fatal("clearing preference did not restore server default")
	}
	saved, err = store.Preferences(1)
	if err != nil || saved["language_code"] != nil {
		test.Fatal("server default must be an unset preference", err)
	}
}

func TestLanguageSettingsValidationAndCSRF(test *testing.T) {
	app := testApp(test)
	requireCode(test, call(app, "POST", "/preferences/", 1, url.Values{"language_code": {"fr"}}, false), 200)
	before, err := app.Store.Preferences(1)
	if err != nil {
		test.Fatal(err)
	}
	for _, code := range []string{"de", "fr-FR", `<script>alert(1)</script>`} {
		requireCode(test, call(app, "POST", "/preferences/", 1, url.Values{"language_code": {code}}, false), 400)
	}
	request := httptest.NewRequest("POST", "/preferences/", strings.NewReader("language_code=en"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	requireCode(test, response, 403)
	requireCode(test, call(app, "POST", "/preferences/", 0, url.Values{"language_code": {"en"}}, false), 302)
	saved, err := app.Store.Preferences(1)
	if err != nil || saved.S("language_code") != "fr" {
		test.Fatal("rejected submission changed language", err)
	}
	after, err := app.Store.Preferences(1)
	if err != nil || !reflect.DeepEqual(before, after) {
		test.Fatal("rejected submission changed playback preferences", err)
	}
}

func TestLanguageSchemaUpgradePreservesExistingPreferences(test *testing.T) {
	app := testApp(test)
	before, err := app.Store.Preferences(1)
	if err != nil {
		test.Fatal(err)
	}
	// Simulate an existing database from before language settings were added.
	if _, err := app.Store.DB.Exec("ALTER TABLE reinhardt_preferences DROP COLUMN language_code"); err != nil {
		test.Fatal(err)
	}
	store, err := openStore(app.Store.Config)
	if err != nil {
		test.Fatal(err)
	}
	defer store.DB.Close()
	after, err := store.Preferences(1)
	if err != nil || !reflect.DeepEqual(before, after) {
		test.Fatal("schema upgrade changed existing preferences", err)
	}
	if err := store.SetUserLanguage(1, "fr"); err != nil {
		test.Fatal("upgraded database cannot save language", err)
	}
	if _, err := store.DB.Exec("DELETE FROM auth_user WHERE id=1"); err != nil {
		test.Fatal(err)
	}
	rows, err := store.Query("SELECT * FROM reinhardt_preferences WHERE owner_id=1")
	if err != nil || len(rows) != 0 {
		test.Fatal("deleted account left a language preference behind", err)
	}
	legacy, err := store.Query("SELECT name FROM sqlite_master WHERE name='go_user_language'")
	if err != nil || len(legacy) != 0 {
		test.Fatal("schema upgrade created a separate language table", err)
	}
}

func TestLegacyLanguageTableMigration(test *testing.T) {
	for _, existingColumn := range []bool{false, true} {
		test.Run(fmt.Sprintf("existingColumn=%v", existingColumn), func(test *testing.T) {
			app := testApp(test)
			if _, err := app.Store.DB.Exec("UPDATE reinhardt_preferences SET autofeed_queue=0,autofeed_similar=0,repeat_queue=0,show_oscilloscope=0,current_position=7,access_token='preserved-token' WHERE owner_id=1"); err != nil {
				test.Fatal(err)
			}
			before, err := app.Store.Preferences(1)
			if err != nil {
				test.Fatal(err)
			}
			if existingColumn {
				if err := app.Store.SetUserLanguage(2, "en"); err != nil {
					test.Fatal(err)
				}
			} else if _, err := app.Store.DB.Exec("ALTER TABLE reinhardt_preferences DROP COLUMN language_code"); err != nil {
				test.Fatal(err)
			}
			// A saved choice must also survive when the old implementation never
			// initialized this account's ordinary preferences.
			if _, err := app.Store.DB.Exec("INSERT INTO auth_user(id,username) VALUES(3,'charlie')"); err != nil {
				test.Fatal(err)
			}
			if _, err := app.Store.DB.Exec(`CREATE TABLE go_user_language(owner_id INTEGER PRIMARY KEY REFERENCES auth_user(id) ON DELETE CASCADE,language_code TEXT NOT NULL);
INSERT INTO go_user_language(owner_id,language_code) VALUES(1,'fr'),(2,'fr'),(3,'en')`); err != nil {
				test.Fatal(err)
			}
			store, err := openStore(app.Store.Config)
			if err != nil {
				test.Fatal(err)
			}
			defer store.DB.Close()
			after, err := store.Preferences(1)
			before["language_code"] = "fr"
			if err != nil || !reflect.DeepEqual(before, after) {
				test.Fatal("migration lost language or changed existing preference data", err)
			}
			for owner, expected := range map[int64]string{2: "fr", 3: "en"} {
				if owner == 2 && existingColumn {
					expected = "en"
				}
				prefs, err := store.Preferences(owner)
				if err != nil || prefs.S("language_code") != expected {
					test.Fatalf("owner %d: language was not preserved: %v %v", owner, prefs, err)
				}
			}
			legacy, err := store.Query("SELECT name FROM sqlite_master WHERE name='go_user_language'")
			if err != nil || len(legacy) != 0 {
				test.Fatal("obsolete language table was not removed", err)
			}
			if err := migrateLanguagePreference(store.DB); err != nil {
				test.Fatal("migration cannot be repeated", err)
			}
			repeated, err := store.Preferences(1)
			if err != nil || !reflect.DeepEqual(after, repeated) {
				test.Fatal("repeat migration changed preferences", err)
			}
		})
	}
}

func TestLanguageMigrationRollback(test *testing.T) {
	app := testApp(test)
	if _, err := app.Store.DB.Exec(`ALTER TABLE reinhardt_preferences DROP COLUMN language_code;
CREATE TABLE go_user_language(owner_id INTEGER PRIMARY KEY,language_code TEXT NOT NULL);
INSERT INTO go_user_language VALUES(1,'fr'),(999,'en')`); err != nil {
		test.Fatal(err)
	}
	// An orphaned legacy row must cause the whole migration to roll back,
	// including the column addition and any copied choices.
	if err := migrateLanguagePreference(app.Store.DB); err == nil {
		test.Fatal("migration accepted an invalid account reference")
	}
	columns, err := app.Store.Query("SELECT name FROM pragma_table_info('reinhardt_preferences') WHERE name='language_code'")
	if err != nil || len(columns) != 0 {
		test.Fatal("failed migration left a partial schema upgrade", err)
	}
	legacy, err := app.Store.Query("SELECT * FROM go_user_language ORDER BY owner_id")
	if err != nil || len(legacy) != 2 || legacy[0].S("language_code") != "fr" {
		test.Fatal("failed migration lost legacy language choices", err)
	}
	if _, err := app.Store.DB.Exec("DELETE FROM go_user_language WHERE owner_id=999"); err != nil {
		test.Fatal(err)
	}
	if err := migrateLanguagePreference(app.Store.DB); err != nil {
		test.Fatal("migration could not be retried after rollback", err)
	}
	prefs, err := app.Store.Preferences(1)
	if err != nil || prefs.S("language_code") != "fr" {
		test.Fatal("retried migration lost the saved language", err)
	}
}

func TestConcurrentLanguageMigration(test *testing.T) {
	app := testApp(test)
	if _, err := app.Store.DB.Exec(`ALTER TABLE reinhardt_preferences DROP COLUMN language_code;
CREATE TABLE go_user_language(owner_id INTEGER PRIMARY KEY REFERENCES auth_user(id),language_code TEXT NOT NULL);
INSERT INTO go_user_language VALUES(1,'fr')`); err != nil {
		test.Fatal(err)
	}
	results := make(chan error, 2)
	for index := 0; index < 2; index++ {
		go func() {
			store, err := openStore(app.Store.Config)
			if err == nil {
				err = store.DB.Close()
			}
			results <- err
		}()
	}
	for index := 0; index < 2; index++ {
		if err := <-results; err != nil {
			test.Errorf("concurrent startup failed: %v", err)
		}
	}
	prefs, err := app.Store.Preferences(1)
	if err != nil || prefs.S("language_code") != "fr" {
		test.Fatal("concurrent upgrade lost saved language", err)
	}
}

func TestLanguageForDocumentsFragmentsAndConcurrentUsers(test *testing.T) {
	app := testApp(test)
	if err := app.Store.SetUserLanguage(1, "fr"); err != nil {
		test.Fatal(err)
	}
	id := mustTag(test, app, 1, "shared", []int64{1})
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_tag SET is_shared=1 WHERE id=?", id); err != nil {
		test.Fatal(err)
	}
	tag, err := app.Store.Tag(id, 1)
	if err != nil {
		test.Fatal(err)
	}
	paths := []string{"/", "/preferences/", "/dj/", "/accounts/login/", tag.S("share_url")}
	for _, path := range paths {
		response := call(app, "GET", path, 1, nil, false)
		requireCode(test, response, 200)
		if !strings.Contains(response.Body.String(), `<html lang="fr"`) {
			test.Fatalf("%s has incorrect document language", path)
		}
	}
	response := call(app, "GET", "/music_file_dropdown/?q=1", 1, nil, true)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), "ajouter à la file d") {
		test.Fatal("fragment did not use user's language")
	}
	// Anonymous pages use the configured default, rather than the playlist
	// owner's preference or the last authenticated request's language.
	for _, path := range []string{"/accounts/login/", tag.S("share_url")} {
		response := call(app, "GET", path, 0, nil, false)
		requireCode(test, response, 200)
		if !strings.Contains(response.Body.String(), `<html lang="en"`) {
			test.Fatalf("anonymous %s inherited a user's language", path)
		}
	}
	var workers sync.WaitGroup
	for owner, code := range map[int]string{1: "fr", 2: "en"} {
		workers.Go(func() {
			for index := 0; index < 5; index++ {
				response := call(app, "GET", "/preferences/", owner, nil, false)
				if response.Code != 200 || !strings.Contains(response.Body.String(), fmt.Sprintf(`<html lang="%s"`, code)) {
					test.Errorf("concurrent user %d received another language", owner)
				}
			}
		})
	}
	workers.Wait()
}

func TestMessageCataloguesDiscoverLanguagesAndSupportFallbackAndPlurals(test *testing.T) {
	english, err := assets.ReadFile("messages/active.en.json")
	if err != nil {
		test.Fatal(err)
	}
	files := fstest.MapFS{
		"messages/active.en.json": {Data: english},
		"messages/active.pt-br.json": {Data: []byte(`{
			"language_name": {"other": "Português (Brasil)"},
			"Language": {"other": "Idioma brasileiro"}
		}`)},
		"messages/active.pt-PT.json": {Data: []byte(`{
			"language_name": {"other": "Português (Portugal)"},
			"Language": {"other": "Idioma português"}
		}`)},
		"messages/active.es.json": {Data: []byte(`{
			"language_name": {"other": "Español"},
			"Language": {"other": "Idioma"},
			"greeting": {"other": "Hola {{.Name}}"},
			"track_count": {"one": "{{.Count}} pista", "other": "{{.Count}} pistas"}
		}`)},
	}
	translations, err := newTranslations(files)
	if err != nil {
		test.Fatal(err)
	}
	if !translations.supports("es") || translations.Languages[1].Name != "Español" {
		test.Fatal("new catalogue language was not discovered")
	}
	// BCP-47 normalization can reorder tags compared with file names. Sorting
	// the settings list must not alter the bundle's own matcher indices.
	if translations.forLanguage("pt-BR").Text("Language") != "Idioma brasileiro" || translations.forLanguage("pt-PT").Text("Language") != "Idioma português" {
		test.Fatal("catalogue sorting mixed up regional languages")
	}
	translator := translations.forLanguage("es-MX")
	if translator.Code != "es" || translator.Text("Language") != "Idioma" || translator.Text("Server default") != "Server default" {
		test.Fatal("regional matching or English fallback failed")
	}
	if translations.forLanguage("zz").Code != "en" || translations.forLanguage("invalid!").Code != "en" {
		test.Fatal("unsupported languages must fall back to English")
	}
	if got := translator.Text("unknown {{.Name}}"); got != "unknown {{.Name}}" {
		test.Fatal("unknown message was interpreted as a template", got)
	}
	greeting, err := translator.Format("greeting", map[string]any{"Name": "Alice"})
	if err != nil || greeting != "Hola Alice" {
		test.Fatal("named interpolation failed", greeting, err)
	}
	for count, want := range map[int]string{1: "1 pista", 2: "2 pistas"} {
		got, err := translator.Message("track_count", map[string]any{"Count": count}, count)
		if err != nil || got != want {
			test.Fatal("plural selection failed", got, err)
		}
	}
	app := testApp(test)
	app.Translations = translations
	requireCode(test, call(app, "POST", "/preferences/", 1, url.Values{"language_code": {"es"}}, false), 200)
	response := call(app, "GET", "/preferences/", 1, nil, false)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `<html lang="es"`) || !strings.Contains(response.Body.String(), `value="es" selected`) || !strings.Contains(response.Body.String(), "Idioma") {
		test.Fatal("new language could not be selected without application changes")
	}
	page, err := template.New("catalogue.html").Funcs(template.FuncMap{"dict": templateDict}).Parse(
		`{{ call .localize "greeting" (dict "Name" .name) nil }} / {{ call .localize "track_count" (dict "Count" .count) .count }}`)
	if err != nil {
		test.Fatal(err)
	}
	app.Templates["catalogue.html"] = PageTemplate{Template: page}
	response = httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/", nil)
	if err := app.render(response, request, Row{"language_code": "es"}, "catalogue.html", TemplateContext{"name": `<script>alert(1)</script>`, "count": 2}, 200); err != nil {
		test.Fatal(err)
	}
	if got := response.Body.String(); got != "Hola &lt;script&gt;alert(1)&lt;/script&gt; / 2 pistas" {
		test.Fatal("template interpolation, plurals, or escaping failed", got)
	}
}

func TestMessageCatalogueErrors(test *testing.T) {
	for name, files := range map[string]fstest.MapFS{
		"missing English": {"messages/active.fr.json": {Data: []byte(`{"language_name":"Français"}`)}},
		"invalid JSON":    {"messages/active.en.json": {Data: []byte(`{`)}},
		"missing name":    {"messages/active.en.json": {Data: []byte(`{"home":"home"}`)}},
	} {
		test.Run(name, func(test *testing.T) {
			if _, err := newTranslations(files); err == nil {
				test.Fatal("invalid catalogue configuration was accepted")
			}
		})
	}
}

func TestEnglishCatalogueCoversTemplateMessages(test *testing.T) {
	translations, err := newTranslations(assets)
	if err != nil {
		test.Fatal(err)
	}
	message := regexp.MustCompile(`call \.translate(?:_literal|_format)? "([^"]+)"`)
	for _, directory := range []string{"reinhardt", "registration"} {
		files, err := assets.ReadDir("templates/" + directory)
		if err != nil {
			test.Fatal(err)
		}
		for _, file := range files {
			data, err := assets.ReadFile("templates/" + directory + "/" + file.Name())
			if err != nil {
				test.Fatal(err)
			}
			for _, match := range message.FindAllStringSubmatch(string(data), -1) {
				if _, err := translations.forLanguage("en").Format(match[1], map[string]any{"Title": "Track"}); err != nil {
					test.Errorf("%s message %q missing from English catalogue: %v", file.Name(), match[1], err)
				}
			}
		}
	}
}

func TestLoginCatalogueMessages(test *testing.T) {
	app := testApp(test)
	app.Store.Config["LANGUAGE_CODE"] = "fr-fr"
	response := call(app, "POST", "/accounts/login/", 0, url.Values{"email": {""}}, false)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), "Ce champ est obligatoire.") || !strings.Contains(response.Body.String(), `<html lang="fr"`) {
		test.Fatal("anonymous validation did not use the server's catalogue")
	}
	if err := app.Store.SetUserLanguage(1, "en"); err != nil {
		test.Fatal(err)
	}
	response = call(app, "POST", "/accounts/login/", 1, url.Values{"email": {"invalid"}}, false)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), "Enter a valid email address.") || !strings.Contains(response.Body.String(), `<html lang="en"`) {
		test.Fatal("validation did not use the signed-in user's catalogue")
	}
	link := "https://example.com/invite/?q=token&next=%2F"
	translator := app.translator(Row{"language_code": "fr"})
	body, err := translator.Format("Use this link to log in: %(url)s", map[string]any{"URL": link})
	if err != nil || body != "Utilisez ce lien pour vous connecter : "+link || translator.Text("Your Reinhardt login link") != "Votre lien de connexion Reinhardt" {
		test.Fatal("login email did not preserve its translation and URL", body, err)
	}
}

func TestCatalogueDateFormatting(test *testing.T) {
	translations, err := newTranslations(assets)
	if err != nil {
		test.Fatal(err)
	}
	for _, example := range []struct{ code, raw, zone, want string }{
		{"en", "2026-10-13T13:05:00Z", "UTC", "Oct. 13, 2026, 1:05 p.m."},
		{"fr", "2026-10-13T13:05:00Z", "Europe/Paris", "13 octobre 2026 15:05"},
		{"en", "2026-10-13T00:00:00Z", "UTC", "Oct. 13, 2026, midnight"},
		{"fr", "2026-10-13T12:00:00Z", "UTC", "13 octobre 2026 12:00"},
		{"en", "2026-10-13T09:00:00Z", "UTC", "Oct. 13, 2026, 9 a.m."},
		{"fr", "invalid", "UTC", "invalid"},
	} {
		if got := translations.forLanguage(example.code).displayDate(example.raw, example.zone); got != example.want {
			test.Errorf("%s date: got %q, want %q", example.code, got, example.want)
		}
	}
}
