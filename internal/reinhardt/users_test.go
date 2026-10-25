package reinhardt

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteManagedUser(test *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "current"
		if legacy {
			name = "legacy"
		}
		test.Run(name, func(test *testing.T) {
			root := test.TempDir()
			config := &Config{BaseDir: root, DatabasePath: filepath.Join(root, "users.sqlite3"), SecretKey: "test-secret", AllowedHosts: []string{"*"}}
			if legacy {
				db, err := sql.Open("sqlite", config.DatabasePath)
				if err != nil {
					test.Fatal(err)
				}
				schema, err := assets.ReadFile("schema.sql")
				if err != nil {
					test.Fatal(err)
				}
				// Django performs cascades in Python rather than declaring them in SQL.
				_, err = db.Exec(strings.ReplaceAll(string(schema), " ON DELETE CASCADE", ""))
				db.Close()
				if err != nil {
					test.Fatal(err)
				}
			}
			store, err := openStore(test.Context(), config)
			if err != nil {
				test.Fatal(err)
			}
			test.Cleanup(func() { store.DB.Close() })
			_, err = store.DB.Exec(`INSERT INTO auth_user(id,username) VALUES(1,'alice'),(2,'bob');
INSERT INTO reinhardt_preferences(owner_id,access_token) VALUES(1,'alice-link'),(2,'bob-link');
INSERT INTO go_session(token,owner_id,expires) VALUES('session-1',1,9999999999),('session-2',2,9999999999);
INSERT INTO reinhardt_musicdirectory(id,local_path,created_at,modified_at) VALUES(1,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);
INSERT INTO reinhardt_musicfile(id,music_directory_id,relative_path,artist,title,album,genre,comment,created_at,modified_at)
VALUES(1,1,'track.mp3','artist','track','album','genre','',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);
CREATE TABLE auth_user_groups(id INTEGER PRIMARY KEY,user_id INTEGER REFERENCES auth_user(id),group_id INTEGER);
CREATE TABLE auth_user_user_permissions(id INTEGER PRIMARY KEY,user_id INTEGER REFERENCES auth_user(id),permission_id INTEGER);
CREATE TABLE django_admin_log(id INTEGER PRIMARY KEY,user_id INTEGER REFERENCES auth_user(id));
INSERT INTO auth_user_groups(user_id,group_id) VALUES(1,1),(2,1);
INSERT INTO auth_user_user_permissions(user_id,permission_id) VALUES(1,1),(2,1);
INSERT INTO django_admin_log(user_id) VALUES(1),(2);`, root)
			if err != nil {
				test.Fatal(err)
			}
			musicPath := filepath.Join(root, "track.mp3")
			if err := os.WriteFile(musicPath, []byte("music"), 0600); err != nil {
				test.Fatal(err)
			}
			app, err := newApp(store)
			if err != nil {
				test.Fatal(err)
			}
			for _, label := range []string{"queue", "favorites", "playlist"} {
				mustTag(test, app, 1, label, []int64{1})
				mustTag(test, app, 2, label, []int64{1})
			}
			if err := store.deleteManagedUser(test.Context(), 1, "outdated-name"); err == nil {
				test.Fatal("stale account identity accepted")
			}
			if err := store.deleteManagedUser(test.Context(), 1, "alice"); err != nil {
				test.Fatal(err)
			}
			for _, table := range []string{"auth_user", "reinhardt_preferences", "go_session", "reinhardt_tag", "reinhardt_musicfiletag", "go_autofeed_tag_revision", "auth_user_groups", "auth_user_user_permissions", "django_admin_log", "reinhardt_musicdirectory", "reinhardt_musicfile"} {
				row, err := store.One(test.Context(), "SELECT count(*) AS count FROM "+table)
				want := int64(1)
				if table == "reinhardt_tag" || table == "reinhardt_musicfiletag" || table == "go_autofeed_tag_revision" {
					want = 3
				}
				if err != nil || row.N("count") != want {
					test.Fatalf("%s count=%v, want %d, err=%v", table, row, want, err)
				}
			}
			other, err := store.One(test.Context(), "SELECT u.username,p.access_token FROM auth_user u JOIN reinhardt_preferences p ON p.owner_id=u.id WHERE u.id=2")
			if err != nil || other.S("username") != "bob" || other.S("access_token") != "bob-link" {
				test.Fatalf("another account changed=%v, err=%v", other, err)
			}
			if data, err := os.ReadFile(musicPath); err != nil || string(data) != "music" {
				test.Fatalf("music file changed=%q, err=%v", data, err)
			}
			if rows, err := store.Query(test.Context(), "PRAGMA foreign_key_check"); err != nil || len(rows) != 0 {
				test.Fatalf("orphaned data=%v, err=%v", rows, err)
			}
			requireCode(test, call(app, http.MethodGet, "/invite/?q=alice-link", 0, nil, false), http.StatusNotFound)
			requireCode(test, call(app, http.MethodGet, "/invite/?q=bob-link", 0, nil, false), http.StatusFound)
			if err := store.deleteManagedUser(test.Context(), 1, "alice"); !errors.Is(err, sql.ErrNoRows) {
				test.Fatalf("missing account error=%v", err)
			}
		})
	}
}

func TestDeleteManagedUserRollback(test *testing.T) {
	for _, failure := range []string{"delete", "commit"} {
		test.Run(failure, func(test *testing.T) {
			app := testApp(test)
			store := app.Store
			mustTag(test, app, 1, "queue", []int64{1, 2})
			link, err := store.userAccessURL(test.Context(), 1, false)
			if err != nil {
				test.Fatal(err)
			}
			query := `CREATE TRIGGER reject_user_delete BEFORE DELETE ON auth_user BEGIN SELECT RAISE(ABORT,'test deletion failure'); END`
			if failure == "commit" {
				query = `CREATE TABLE protected_user(owner_id INTEGER REFERENCES auth_user(id) DEFERRABLE INITIALLY DEFERRED); INSERT INTO protected_user VALUES(1)`
			}
			if _, err = store.DB.Exec(query); err != nil {
				test.Fatal(err)
			}
			if err := store.deleteManagedUser(test.Context(), 1, "alice"); err == nil {
				test.Fatal("deletion unexpectedly succeeded")
			}
			for table, want := range map[string]int64{"auth_user": 2, "reinhardt_preferences": 2, "go_session": 2, "reinhardt_tag": 1, "reinhardt_musicfiletag": 2, "go_autofeed_tag_revision": 1} {
				row, err := store.One(test.Context(), "SELECT count(*) AS count FROM "+table)
				if err != nil || row.N("count") != want {
					test.Fatalf("failed deletion changed %s=%v, err=%v", table, row, err)
				}
			}
			if after, err := store.userAccessURL(test.Context(), 1, false); err != nil || after != link {
				test.Fatalf("failed deletion changed login link=%q, err=%v", after, err)
			}
		})
	}
}

func TestUserManagementWithLegacyColumnDefaults(test *testing.T) {
	// Django-created installations do not have the SQL defaults in schema.sql.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		test.Fatal(err)
	}
	test.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE auth_user (
id INTEGER PRIMARY KEY, password TEXT NOT NULL, last_login DATETIME,
username TEXT NOT NULL UNIQUE, email TEXT NOT NULL, first_name TEXT NOT NULL, last_name TEXT NOT NULL,
is_superuser BOOL NOT NULL, is_staff BOOL NOT NULL, is_active BOOL NOT NULL, date_joined DATETIME NOT NULL);
CREATE TABLE reinhardt_preferences (
id INTEGER PRIMARY KEY, owner_id INTEGER NOT NULL UNIQUE REFERENCES auth_user(id),
autofeed_queue BOOL NOT NULL, autofeed_similar BOOL NOT NULL, repeat_queue BOOL NOT NULL,
show_oscilloscope BOOL NOT NULL, show_track_grid BOOL NOT NULL, current_position INTEGER NOT NULL, access_token TEXT UNIQUE, language_code TEXT);`)
	if err != nil {
		test.Fatal(err)
	}
	store := &Store{DB: db, Config: &Config{}}
	id, err := store.saveManagedUser(test.Context(), User{Username: "legacy", IsActive: true})
	if err != nil {
		test.Fatal(err)
	}
	if _, err = store.userAccessURL(test.Context(), id, false); err != nil {
		test.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM reinhardt_preferences WHERE owner_id=?", id); err != nil {
		test.Fatal(err)
	}
	if _, err = store.userAccessURL(test.Context(), id, false); err != nil {
		test.Fatalf("initializing legacy preferences: %v", err)
	}
}

func TestManageUsers(test *testing.T) {
	app := testApp(test)
	store := app.Store
	ctx := test.Context()
	id, err := store.saveManagedUser(ctx, User{Username: "  CAROL  ", Email: " carol@example.com ", IsActive: true, IsSuperuser: true})
	if err != nil {
		test.Fatal(err)
	}
	user, err := store.user(ctx, "SELECT * FROM auth_user WHERE id=?", id)
	if err != nil || user.Username != "carol" || user.Email != "carol@example.com" || !user.IsSuperuser || !user.IsStaff || !user.IsActive {
		test.Fatalf("created user=%+v, err=%v", user, err)
	}
	prefs, err := store.Preferences(ctx, id)
	if err != nil || len(prefs.AccessToken) != 64 {
		test.Fatalf("created preferences=%+v, err=%v", prefs, err)
	}
	if _, err := store.DB.Exec("UPDATE reinhardt_preferences SET language_code='fr',repeat_queue=0 WHERE owner_id=?", id); err != nil {
		test.Fatal(err)
	}
	user.Username, user.Email, user.FirstName, user.LastName = "caroline", "", "Carol", "Example"
	user.IsSuperuser = false
	if _, err := store.saveManagedUser(ctx, *user); err != nil {
		test.Fatal(err)
	}
	updated, err := store.One(ctx, "SELECT * FROM auth_user WHERE id=?", id)
	if err != nil || updated.S("username") != "caroline" || updated.S("email") != "" || updated.S("first_name") != "Carol" || updated.S("last_name") != "Example" || updated.B("is_superuser") || updated.B("is_staff") || updated.S("password") != "!" {
		test.Fatalf("updated user=%v, err=%v", updated, err)
	}
	after, err := store.Preferences(ctx, id)
	if err != nil || after.AccessToken != prefs.AccessToken || after.LanguageCode == nil || *after.LanguageCode != "fr" || after.RepeatQueue {
		test.Fatalf("preferences changed=%+v, err=%v", after, err)
	}
	for _, username := range []string{" ", strings.Repeat("é", 151), "alice"} {
		user.Username = username
		if _, err := store.saveManagedUser(ctx, *user); err == nil {
			test.Fatalf("accepted invalid or duplicate username %q", username)
		}
	}
	unchanged, err := store.user(ctx, "SELECT * FROM auth_user WHERE id=?", id)
	if err != nil || unchanged.Username != "caroline" {
		test.Fatalf("failed edit modified account=%+v, err=%v", unchanged, err)
	}
	if _, err := store.saveManagedUser(ctx, User{Username: strings.Repeat("é", 150), IsActive: true}); err != nil {
		test.Fatalf("150-character Unicode username rejected: %v", err)
	}
	if _, err := store.saveManagedUser(ctx, User{ID: 99999, Username: "missing"}); !errors.Is(err, sql.ErrNoRows) {
		test.Fatalf("missing account error=%v", err)
	}
}

func TestManagedUserAccessAndSessions(test *testing.T) {
	app := testApp(test)
	store := app.Store
	ctx := test.Context()
	store.Config.AccessURLPrefix = "https://music.example.com"
	store.Config.URLPrefix = "music/"
	link, err := store.userAccessURL(ctx, 1, false)
	if err != nil || !strings.HasPrefix(link, "https://music.example.com/music/invite/?q=") {
		test.Fatalf("login link=%q, err=%v", link, err)
	}
	if stable, err := store.userAccessURL(ctx, 1, false); err != nil || stable != link {
		test.Fatalf("view changed login link=%q, err=%v", stable, err)
	}
	app, err = newApp(store)
	if err != nil {
		test.Fatal(err)
	}
	requireCode(test, call(app, http.MethodGet, link, 0, nil, false), http.StatusFound)
	reset, err := store.userAccessURL(ctx, 1, true)
	if err != nil || reset == link {
		test.Fatalf("reset link=%q, err=%v", reset, err)
	}
	requireCode(test, call(app, http.MethodGet, link, 0, nil, false), http.StatusNotFound)
	requireCode(test, call(app, http.MethodGet, reset, 0, nil, false), http.StatusFound)
	row, err := store.One(ctx, "SELECT count(*) AS count FROM go_session WHERE owner_id=1 AND token='session-1'")
	if err != nil || row.N("count") != 0 {
		test.Fatalf("reset left old session: %v, %v", row, err)
	}
	user, err := store.user(ctx, "SELECT * FROM auth_user WHERE id=1")
	if err != nil {
		test.Fatal(err)
	}
	user.IsActive = false
	if _, err = store.saveManagedUser(ctx, *user); err != nil {
		test.Fatal(err)
	}
	requireCode(test, call(app, http.MethodGet, reset, 0, nil, false), http.StatusNotFound)
	user.IsActive = true
	if _, err = store.saveManagedUser(ctx, *user); err != nil {
		test.Fatal(err)
	}
	row, err = store.One(ctx, "SELECT count(*) AS count FROM go_session WHERE owner_id=1")
	if err != nil || row.N("count") != 0 {
		test.Fatalf("deactivation left sessions: %v, %v", row, err)
	}
	row, err = store.One(ctx, "SELECT count(*) AS count FROM go_session WHERE owner_id=2")
	if err != nil || row.N("count") != 1 {
		test.Fatalf("another user's sessions changed: %v, %v", row, err)
	}
	requireCode(test, call(app, http.MethodGet, reset, 0, nil, false), http.StatusFound)
	if _, err := store.DB.Exec("DELETE FROM reinhardt_preferences WHERE owner_id=2"); err != nil {
		test.Fatal(err)
	}
	if _, err := store.userAccessURL(ctx, 2, false); err != nil {
		test.Fatalf("cannot initialize missing preferences: %v", err)
	}
}

func TestUserManagementRollsBackOnSessionError(test *testing.T) {
	app := testApp(test)
	store := app.Store
	ctx := test.Context()
	link, err := store.userAccessURL(ctx, 1, false)
	if err != nil {
		test.Fatal(err)
	}
	if _, err = store.DB.Exec(`CREATE TRIGGER fail_session_delete BEFORE DELETE ON go_session BEGIN SELECT RAISE(ABORT,'test session failure'); END`); err != nil {
		test.Fatal(err)
	}
	if _, err := store.userAccessURL(ctx, 1, true); err == nil {
		test.Fatal("reset unexpectedly succeeded")
	}
	if after, err := store.userAccessURL(ctx, 1, false); err != nil || after != link {
		test.Fatalf("failed reset changed link=%q, err=%v", after, err)
	}
	if _, err := store.saveManagedUser(ctx, User{ID: 1, Username: "renamed", IsActive: false}); err == nil {
		test.Fatal("deactivation unexpectedly succeeded")
	}
	user, err := store.user(ctx, "SELECT * FROM auth_user WHERE id=1")
	if err != nil || !user.IsActive || user.Username != "alice" || !user.IsSuperuser {
		test.Fatalf("failed edit changed user=%+v, err=%v", user, err)
	}
}
