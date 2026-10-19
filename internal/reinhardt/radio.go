package reinhardt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RadioStation is a live source, independent of indexed files and queue items.
// OwnerID zero identifies a shared station from the bundled catalogue.
type RadioStation struct {
	ID      int64
	OwnerID int64
	Name    string
	Country string
	URL     string
}

func radioStationFromRow(row Row) *RadioStation {
	return &RadioStation{ID: row.N("id"), OwnerID: row.N("owner_id"), Name: row.S("name"), Country: row.S("country"), URL: row.S("url")}
}

func (station *RadioStation) templateRow() Row {
	if station == nil {
		return nil
	}
	version := sha256.Sum256([]byte(station.URL))
	return Row{"id": station.ID, "owner_id": station.OwnerID, "name": station.Name, "country": station.Country, "url": station.URL, "version": fmt.Sprintf("%x", version[:8])}
}

func (store *Store) seedRadioStations(ctx context.Context) error {
	// A single INSERT ... SELECT avoids thousands of individual inserts. Startup
	// and worker processes serialize initialization; the marker commits with data.
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Acquire the write lock before checking the marker so two startup processes
	// cannot both observe an empty catalogue and race to populate it.
	result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO go_settings(key,value) VALUES('radio_seed_v1','pending')")
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 1 {
		data, readErr := assets.ReadFile("radio/stations.json")
		if readErr != nil {
			return readErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO go_radio_station(name,country,url)
SELECT json_extract(value,'$.name'),json_extract(value,'$.country'),json_extract(value,'$.url') FROM json_each(?)`, string(data)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE go_settings SET value='2115163a9da8a5fd6c1046943f58d065d2a5bba8' WHERE key='radio_seed_v1'")
	}
	if err != nil {
		return err
	}
	if err = seedRadioAdditions(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Curated additions apply to both new and previously initialized databases.
// Each entry has its own marker, preserving personal edits and hidden stations
// on subsequent starts, even after the upstream snapshot is regenerated.
func seedRadioAdditions(ctx context.Context, tx *sql.Tx) error {
	data, err := assets.ReadFile("radio/additions.json")
	if err != nil {
		return err
	}
	var additions []struct {
		Key string
		RadioStation
	}
	if err = json.Unmarshal(data, &additions); err != nil {
		return err
	}
	for _, addition := range additions {
		key := "radio_addition_" + addition.Key
		result, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO go_settings(key,value) VALUES(?,'applied')", key)
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 0 {
			continue
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO go_radio_station(name,country,url)
SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM go_radio_station WHERE owner_id IS NULL AND url=?)`, addition.Name, addition.Country, addition.URL, addition.URL); err != nil {
			return err
		}
	}
	return nil
}

const visibleRadioSQL = `(s.owner_id=? OR (s.owner_id IS NULL AND NOT EXISTS
(SELECT 1 FROM go_radio_hidden h WHERE h.owner_id=? AND h.station_id=s.id)))`

func (store *Store) RadioStation(ctx context.Context, owner int64, id any) (*RadioStation, error) {
	row, err := store.One(ctx, "SELECT s.* FROM go_radio_station s WHERE s.id=? AND "+visibleRadioSQL, id, owner, owner)
	if err != nil {
		return nil, err
	}
	return radioStationFromRow(row), nil
}

func (store *Store) currentRadioStation(ctx context.Context, owner int64) (*RadioStation, error) {
	row, err := store.One(ctx, `SELECT s.* FROM go_radio_playback p JOIN go_radio_station s ON s.id=p.station_id
WHERE p.owner_id=? AND `+visibleRadioSQL, owner, owner, owner)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return radioStationFromRow(row), nil
}

func validateRadioStation(station *RadioStation) error {
	station.Name = strings.TrimSpace(station.Name)
	station.Country = strings.TrimSpace(station.Country)
	station.URL = strings.TrimSpace(station.URL)
	for _, field := range []struct {
		value string
		limit int
	}{{station.Name, 200}, {station.Country, 120}} {
		if field.value == "" || !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.limit || strings.ContainsFunc(field.value, unicode.IsControl) {
			return bad("Enter a station name and country within the length limits.")
		}
	}
	if _, err := parseRadioURL(station.URL); err != nil {
		return bad("Enter a public HTTP or HTTPS stream URL without credentials.")
	}
	return nil
}

func (store *Store) saveRadioStation(ctx context.Context, owner int64, station *RadioStation) (int64, error) {
	if err := validateRadioStation(station); err != nil {
		return 0, err
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id := station.ID
	if id != 0 {
		var savedOwner sql.NullInt64
		if err = tx.QueryRowContext(ctx, "SELECT owner_id FROM go_radio_station WHERE id=?", id).Scan(&savedOwner); err != nil {
			return 0, err
		}
		if savedOwner.Valid && savedOwner.Int64 != owner {
			return 0, sql.ErrNoRows
		}
		var hidden bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM go_radio_hidden WHERE owner_id=? AND station_id=?)", owner, id).Scan(&hidden); err != nil {
			return 0, err
		}
		if hidden {
			return 0, sql.ErrNoRows
		}
		if !savedOwner.Valid {
			if _, err = tx.ExecContext(ctx, "INSERT INTO go_radio_hidden(owner_id,station_id) VALUES(?,?)", owner, id); err != nil {
				return 0, err
			}
			id = 0
		}
	}
	if id == 0 {
		result, insertErr := tx.ExecContext(ctx, "INSERT INTO go_radio_station(owner_id,name,country,url) VALUES(?,?,?,?)", owner, station.Name, station.Country, station.URL)
		if insertErr != nil {
			return 0, insertErr
		}
		id, err = result.LastInsertId()
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE go_radio_station SET name=?,country=?,url=?,modified_at=? WHERE id=? AND owner_id=?", station.Name, station.Country, station.URL, now(), id, owner)
	}
	if err != nil {
		return 0, err
	}
	if station.ID != 0 && station.ID != id {
		if _, err = tx.ExecContext(ctx, "UPDATE go_radio_playback SET station_id=? WHERE owner_id=? AND station_id=?", id, owner, station.ID); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE go_radio_favorite SET station_id=? WHERE owner_id=? AND station_id=?", id, owner, station.ID); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

func (store *Store) deleteRadioStation(ctx context.Context, owner int64, id any) error {
	station, err := store.RadioStation(ctx, owner, id)
	if err != nil {
		return err
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if station.OwnerID == 0 {
		_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO go_radio_hidden(owner_id,station_id) VALUES(?,?)", owner, station.ID)
	} else {
		_, err = tx.ExecContext(ctx, "DELETE FROM go_radio_station WHERE id=? AND owner_id=?", station.ID, owner)
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM go_radio_playback WHERE owner_id=? AND station_id=?", owner, station.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM go_radio_favorite WHERE owner_id=? AND station_id=?", owner, station.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (app *App) radio(responseWriter http.ResponseWriter, request *http.Request, user *User, parts []string) error {
	managesStations := (len(parts) == 2 && parts[1] == "new") ||
		(len(parts) == 3 && (parts[2] == "edit" || parts[2] == "delete"))
	if managesStations && !user.IsSuperuser {
		return forbidden()
	}
	ctx := TemplateContext{}
	if len(parts) == 1 {
		if err := safeOnly(request); err != nil {
			return err
		}
		return app.radioPage(responseWriter, request, user, ctx)
	}
	if len(parts) == 2 && parts[1] == "queue" {
		if err := postOnly(request); err != nil {
			return err
		}
		// Selecting the saved association clears radio through the queue-selection
		// trigger without advancing the queue or changing its membership.
		prefs, err := app.Store.Preferences(request.Context(), user.ID)
		if err != nil {
			return err
		}
		if _, err = app.Store.DB.ExecContext(request.Context(), "UPDATE reinhardt_preferences SET current_position=? WHERE owner_id=?", prefs.CurrentPosition, user.ID); err != nil {
			return err
		}
		if err = app.queuePlaybackContext(request.Context(), user.ID, prefs, ctx); err != nil {
			return err
		}
		ctx["autoplay"] = true
		return app.render(responseWriter, request, user, "reinhardt/_now_playing.html", ctx, 200)
	}
	if len(parts) == 2 && parts[1] == "new" {
		return app.radioEdit(responseWriter, request, user, &RadioStation{})
	}
	if len(parts) != 3 {
		return sql.ErrNoRows
	}
	station, err := app.Store.RadioStation(request.Context(), user.ID, parts[1])
	if err != nil {
		return err
	}
	switch parts[2] {
	case "favorite":
		if err := postOnly(request); err != nil {
			return err
		}
		switch request.PostFormValue("favorite") {
		case "1":
			_, err = app.Store.DB.ExecContext(request.Context(), "INSERT OR IGNORE INTO go_radio_favorite(owner_id,station_id) VALUES(?,?)", user.ID, station.ID)
		case "0":
			_, err = app.Store.DB.ExecContext(request.Context(), "DELETE FROM go_radio_favorite WHERE owner_id=? AND station_id=?", user.ID, station.ID)
		default:
			return bad("Invalid radio favorite state.")
		}
		if err != nil {
			return err
		}
		page, _ := strconv.Atoi(request.PostForm.Get("page"))
		app.redirect(responseWriter, request, app.Store.radioPageURL(request.PostForm.Get("q"), request.PostForm.Get("country"), request.PostForm.Get("favorites") == "1", page))
		return nil
	case "edit":
		return app.radioEdit(responseWriter, request, user, station)
	case "audio":
		if err := safeOnly(request); err != nil {
			return err
		}
		return app.radioAudio(responseWriter, request, station)
	case "play":
		if err := postOnly(request); err != nil {
			return err
		}
		_, err = app.Store.DB.ExecContext(request.Context(), `INSERT INTO go_radio_playback(owner_id,station_id) VALUES(?,?)
ON CONFLICT(owner_id) DO UPDATE SET station_id=excluded.station_id`, user.ID, station.ID)
		if err != nil {
			return err
		}
		ctx["station"] = station
		ctx["autoplay"] = true
		return app.render(responseWriter, request, user, "reinhardt/_now_playing.html", ctx, 200)
	case "delete":
		if err := postOnly(request); err != nil {
			return err
		}
		if err = app.Store.deleteRadioStation(request.Context(), user.ID, station.ID); err != nil {
			return err
		}
		app.redirect(responseWriter, request, app.Store.URL("radio"))
		return nil
	}
	return sql.ErrNoRows
}

func (app *App) radioEdit(responseWriter http.ResponseWriter, request *http.Request, user *User, station *RadioStation) error {
	ctx := TemplateContext{"station_form": station}
	status := 200
	if request.Method == "POST" {
		station.Name = request.PostForm.Get("name")
		station.Country = request.PostForm.Get("country")
		station.URL = request.PostForm.Get("url")
		if err := validateRadioStation(station); err != nil {
			ctx["error_message"] = err.Error()
			status = 422
		} else {
			if _, err = app.Store.saveRadioStation(request.Context(), user.ID, station); err != nil {
				return err
			}
			app.redirect(responseWriter, request, app.Store.URL("radio"))
			return nil
		}
	}
	return app.render(responseWriter, request, user, "reinhardt/radio_edit.html", ctx, status)
}

func (app *App) radioPage(responseWriter http.ResponseWriter, request *http.Request, user *User, ctx TemplateContext) error {
	const pageSize = 50
	query := strings.TrimSpace(request.URL.Query().Get("q"))
	country := request.URL.Query().Get("country")
	favoritesOnly := request.URL.Query().Get("favorites") == "1"
	page, _ := strconv.Atoi(request.URL.Query().Get("page"))
	if page < 1 || page > 1000000 {
		page = 1
	}
	where := " WHERE " + visibleRadioSQL
	args := []any{user.ID, user.ID}
	if query != "" {
		where += " AND (instr(lower(s.name),lower(?))>0 OR instr(lower(s.country),lower(?))>0)"
		args = append(args, query, query)
	}
	if country != "" {
		where += " AND s.country=?"
		args = append(args, country)
	}
	if favoritesOnly {
		where += " AND EXISTS(SELECT 1 FROM go_radio_favorite f WHERE f.owner_id=? AND f.station_id=s.id)"
		args = append(args, user.ID)
	}
	count, err := app.Store.One(request.Context(), "SELECT count(*) total FROM go_radio_station s"+where, args...)
	if err != nil {
		return err
	}
	total := count.N("total")
	lastPage := max(1, (int(total)+pageSize-1)/pageSize)
	page = min(page, lastPage)
	rows, err := app.Store.Query(request.Context(), `SELECT s.*,EXISTS(SELECT 1 FROM go_radio_favorite f WHERE f.owner_id=? AND f.station_id=s.id) is_favorite
FROM go_radio_station s`+where+" ORDER BY s.country COLLATE NOCASE,s.name COLLATE NOCASE,s.id LIMIT ? OFFSET ?", append(append([]any{user.ID}, args...), pageSize, (page-1)*pageSize)...)
	if err != nil {
		return err
	}
	countries, err := app.Store.Query(request.Context(), "SELECT DISTINCT s.country FROM go_radio_station s WHERE "+visibleRadioSQL+" ORDER BY s.country COLLATE NOCASE", user.ID, user.ID)
	if err != nil {
		return err
	}
	ctx["stations"], ctx["countries"] = rows, countries
	ctx["radio_query"], ctx["country"], ctx["station_total"] = query, country, total
	ctx["radio_favorites"], ctx["radio_page"] = favoritesOnly, page
	if page > 1 {
		ctx["previous_page"] = app.Store.radioPageURL(query, country, favoritesOnly, page-1)
	}
	if page < lastPage {
		ctx["next_page"] = app.Store.radioPageURL(query, country, favoritesOnly, page+1)
	}
	// Radio mutation forms target both main and the footer; ordinary navigation
	// targets only main and leaves the persistent player entirely alone.
	ctx["sync_player"] = true
	return app.render(responseWriter, request, user, "reinhardt/radio.html", ctx, 200)
}

func (store *Store) radioPageURL(query, country string, favoritesOnly bool, page int) string {
	if page < 1 || page > 1000000 {
		page = 1
	}
	values := url.Values{"q": {query}, "country": {country}, "page": {fmt.Sprint(page)}}
	if favoritesOnly {
		values.Set("favorites", "1")
	}
	return store.URL("radio") + "?" + values.Encode()
}

func (app *App) queuePlaybackContext(ctx context.Context, owner int64, prefs *Preferences, values TemplateContext) error {
	values["current_position"] = 0
	if prefs.CurrentPosition == 0 {
		return nil
	}
	item, err := app.Store.queueItem(ctx, owner, "a.id=?", prefs.CurrentPosition)
	if err == sql.ErrNoRows || (err == nil && item == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	track, err := app.Store.Track(ctx, item.MusicFileID)
	if err != nil {
		return err
	}
	values["music_file"], values["current_position"] = track, item.Rank
	return nil
}
