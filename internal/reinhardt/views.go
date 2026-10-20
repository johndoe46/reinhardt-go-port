package reinhardt

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (app *App) shared(responseWriter http.ResponseWriter, request *http.Request, user *User, action string) error {
	if err := safeOnly(request); err != nil {
		return err
	}
	tag, err := app.Store.tag(request.Context(), "SELECT * FROM reinhardt_tag WHERE id=? AND is_shared=1", request.PathValue("id"))
	if err != nil {
		return err
	}
	app.Store.decorateTag(tag)
	if subtle.ConstantTimeCompare([]byte(request.PathValue("hash")), []byte(tag.ShareHash)) != 1 {
		return sql.ErrNoRows
	}
	tracks, err := app.Store.TagTracks(request.Context(), tag.ID)
	if err != nil {
		return err
	}
	total := int64(0)
	for _, track := range tracks {
		total += track.Duration
	}
	if action != "" {
		switch action {
		case "track":
			for _, track := range tracks {
				if fmt.Sprint(track.ID) == request.PathValue("track") {
					return serveLocal(responseWriter, request, track, false)
				}
			}
			return sql.ErrNoRows
		case "download":
			if total >= app.Store.Config.SharedPlaylistDownloadMaxSeconds {
				return sql.ErrNoRows
			}
			return app.archive(responseWriter, request, tag)
		default:
			return sql.ErrNoRows
		}
	}
	return app.render(responseWriter, request, user, "reinhardt/shared_tag.html", TemplateContext{"tag": tag, "share_hash": request.PathValue("hash"), "music_files": tracks, "total_duration": total, "can_download_all": total < app.Store.Config.SharedPlaylistDownloadMaxSeconds}, 200)
}

func (app *App) tagAction(responseWriter http.ResponseWriter, request *http.Request, user *User, path string) error {
	if err := postOnly(request); err != nil {
		return err
	}
	owner := user.ID
	name := label(request.FormValue("tag"))
	query := request.FormValue("q")
	kind := request.FormValue("add_type")
	action := "added"
	object := kind
	var tracks []*Track
	var operationErr error
	if path == "toggle_tag/" {
		query = request.FormValue("music_file_pk")
		tag, err := app.Store.Tag(request.Context(), request.FormValue("tag_pk"), owner)
		if err != nil {
			return err
		}
		if tag.Label == "queue" || tag.Label == "favorites" {
			return forbidden()
		}
		name = tag.Label
		kind = "music_file"
		if request.FormValue("has_tag") != "on" {
			action = "removed"
		}
	}
	if path == "remove_from_tag/" {
		kind = request.FormValue("remove_type")
		action = "removed"
	}
	switch kind {
	case "music_file":
		track, err := app.Store.Track(request.Context(), query)
		if err != nil {
			return err
		}
		tracks = []*Track{track}
		object = "track"
	case "artist", "genre", "album":
		tracks, operationErr = app.Store.Tracks(request.Context(), "WHERE m."+kind+"=? AND m.missing=0 ORDER BY m.artist,m.album,m.track_number", query)
	case "tag", "favorites":
		source := query
		if kind == "favorites" {
			source = "favorites"
		}
		tag, err := app.Store.tag(request.Context(), "SELECT * FROM reinhardt_tag WHERE owner_id=? AND label=?", owner, source)
		if err != nil {
			return err
		}
		tracks, operationErr = app.Store.TagTracks(request.Context(), tag.ID)
	case "friend_tag":
		tracks, operationErr = app.Store.TagTracks(request.Context(), query)
	default:
		return bad("Invalid action type")
	}
	if operationErr != nil {
		return operationErr
	}
	if action == "added" {
		if kind == "music_file" {
			_, operationErr = app.Store.AddTrack(request.Context(), owner, name, tracks[0].ID)
		} else {
			_, operationErr = app.Store.AddTracks(request.Context(), owner, name, ids(tracks))
		}
		if operationErr != nil {
			return operationErr
		}
	} else {
		tx, err := app.Store.DB.BeginTx(request.Context(), nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, track := range tracks {
			if _, operationErr = tx.ExecContext(request.Context(), "DELETE FROM reinhardt_musicfiletag WHERE music_file_id=? AND tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=? AND label=?)", track.ID, owner, name); operationErr != nil {
				return operationErr
			}
		}
		if operationErr = tx.Commit(); operationErr != nil {
			return operationErr
		}
	}
	return app.render(responseWriter, request, user, "reinhardt/_tag_message.html", TemplateContext{"object_label": object, "tag_label": name, "action": action, "object_pk": query}, 200)
}

// bulkAddTracks adds an artist's tracks, search results, or the current queue to
// a destination tag belonging to the current user. The route identifies the
// source and action. Adding to a custom tag displays a selection/creation form
// and processes its submission; adding to queue or favorites requires POST and
// returns a confirmation fragment. Store.AddTracks preserves existing memberships.
func (app *App) bulkAddTracks(responseWriter http.ResponseWriter, request *http.Request, user *User, path string) error {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	source, action := parts[0], parts[1]
	ctx := TemplateContext{}
	value := request.FormValue("artist")
	if source == "search" {
		value = request.FormValue("sq")
	}
	var target string
	if action == "add_to_tag" {
		// Only the tag-selection form needs the user's available tags.
		ctx["artist"] = value
		ctx["q"] = value
		tags, err := app.Store.Tags(request.Context(), user.ID, false)
		if err != nil {
			return err
		}
		ctx["tags"] = tags
	} else {
		if err := postOnly(request); err != nil {
			return err
		}
		target = strings.TrimPrefix(action, "add_to_")
	}
	if request.Method == "POST" {
		if action == "add_to_tag" {
			choice := request.FormValue("tag_choice")
			if choice == "new" || (choice == "" && request.FormValue("tag_pk") == "") {
				target = label(request.FormValue("new_tag"))
				if target == "" {
					return bad("Invalid tag name.")
				}
			} else {
				tag, tagErr := app.Store.Tag(request.Context(), request.FormValue("tag_pk"), user.ID)
				if tagErr != nil {
					return tagErr
				}
				if tag.Label == "queue" || tag.Label == "favorites" {
					return forbidden()
				}
				target = tag.Label
			}
		}
		var tracks []*Track
		var operationErr error
		switch source {
		case "artist":
			tracks, operationErr = app.Store.Tracks(request.Context(), "WHERE m.artist=? AND m.missing=0 ORDER BY m.album,m.track_number", value)
		case "search":
			tracks, operationErr = app.Store.Search(request.Context(), value)
		case "queue":
			queue, err := app.Store.Queue(request.Context(), user.ID)
			operationErr = err
			for _, queueItem := range queue {
				tracks = append(tracks, queueItem.MusicFile)
			}
		}
		if operationErr != nil {
			return operationErr
		}
		count, err := app.Store.AddTracks(request.Context(), user.ID, target, ids(tracks))
		if err != nil {
			return err
		}
		if action == "add_to_tag" {
			// Redisplay the form with the destination and number of newly added tracks.
			ctx["added_count"] = count
			tag, err := app.Store.tag(request.Context(), "SELECT * FROM reinhardt_tag WHERE owner_id=? AND label=?", user.ID, target)
			if err != nil {
				return err
			}
			ctx["selected_tag"] = tag
		}
	}
	if action == "add_to_tag" {
		return app.render(responseWriter, request, user, "reinhardt/"+source+"_add_to_tag.html", ctx, 200)
	}
	object := source
	if source == "search" {
		object = "search results"
	}
	return app.render(responseWriter, request, user, "reinhardt/_tag_message.html", TemplateContext{"object_label": object, "tag_label": target, "action": "added"}, 200)
}

func (app *App) reorder(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	if err := postOnly(request); err != nil {
		return err
	}
	tag, err := app.Store.Tag(request.Context(), request.FormValue("list_pk"), user.ID)
	if err != nil {
		return err
	}
	var order []struct {
		Item int64 `json:"item"`
		Rank int64 `json:"rank"`
	}
	if err = json.Unmarshal([]byte(request.FormValue("new_order")), &order); err != nil {
		return bad("Invalid order")
	}
	tx, err := app.Store.DB.BeginTx(request.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := map[int64]bool{}
	for _, item := range order {
		if seen[item.Item] || item.Rank < 0 {
			return bad("Invalid order")
		}
		seen[item.Item] = true
		res, err := tx.ExecContext(request.Context(), "UPDATE reinhardt_musicfiletag SET rank=? WHERE music_file_id=? AND tag_id=?", item.Rank, item.Item, tag.ID)
		if err != nil {
			return err
		}
		affectedRows, _ := res.RowsAffected()
		if affectedRows != 1 {
			return bad("Invalid queue item")
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	// Unpoly checks the response type even when the form targets :none.
	responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = responseWriter.Write([]byte("ok"))
	return err
}

// nextSong returns the selected track and its queue rank. A nil track means
// playback has ended; the returned rank is then zero.
func (app *App) nextSong(request *http.Request, user *User) (*Track, int64, error) {
	store := app.Store
	owner := user.ID
	prefs, operationErr := store.Preferences(request.Context(), owner)
	if operationErr != nil {
		return nil, 0, operationErr
	}
	currentRank := int64(0)
	var currentItem *QueueItem
	// The persisted position is an association id, while advancement compares
	// ranks. Reordering the queue changes rank without changing the saved position.
	if position := prefs.CurrentPosition; position != 0 {
		currentItem, operationErr = store.queueItem(request.Context(), owner, "a.id=?", position)
		if operationErr != nil && operationErr != sql.ErrNoRows {
			return nil, 0, operationErr
		}
		if currentItem != nil {
			currentRank = currentItem.Rank
		}
	}
	var selectedItem *QueueItem
	var track *Track
	if item := request.FormValue("list_item"); item != "" {
		// list_item comes from the player's track controls, so it identifies a
		// music file, not the association id stored in preferences.
		if id, err := strconv.ParseInt(item, 10, 64); err == nil && strconv.FormatInt(id, 10) == item {
			selectedItem, operationErr = store.queueItem(request.Context(), owner, "a.music_file_id=?", id)
			if operationErr != nil && operationErr != sql.ErrNoRows {
				return nil, 0, operationErr
			}
		}
	}
	if selectedItem == nil {
		if next := request.FormValue("next_track"); next != "" {
			track, operationErr = store.Track(request.Context(), next)
			if operationErr != nil {
				return nil, 0, operationErr
			}
			selectedItem, operationErr = store.AddTrack(request.Context(), owner, "queue", track.ID)
			if operationErr != nil {
				return nil, 0, operationErr
			}
		} else {
			selectedItem, operationErr = store.queueItem(request.Context(), owner, "a.rank>?", currentRank)
			if operationErr != nil && operationErr != sql.ErrNoRows {
				return nil, 0, operationErr
			}
		}
	}
	if selectedItem == nil && prefs.AutofeedQueue {
		// Keep Last.fm behind both the preference and queue-exhaustion checks.
		// Normal advancement and disabled similarity must never contact Last.fm.
		track, operationErr = app.selectAutofeedTrack(request.Context(), owner, prefs, currentItem)
		if operationErr != nil {
			return nil, 0, operationErr
		}
		if track != nil {
			selectedItem, operationErr = store.AddTrack(request.Context(), owner, "queue", track.ID)
			if operationErr != nil {
				return nil, 0, operationErr
			}
		}
	}
	position := int64(0)
	var rank int64
	if selectedItem != nil {
		position = selectedItem.ID
		if track == nil {
			track, operationErr = store.Track(request.Context(), selectedItem.MusicFileID)
			if operationErr != nil {
				return nil, 0, operationErr
			}
		}
		rank = selectedItem.Rank
	}
	_, operationErr = store.DB.ExecContext(request.Context(), "UPDATE reinhardt_preferences SET current_position=? WHERE owner_id=?", position, owner)
	return track, rank, operationErr
}

func serializeTrack(track *Track) Row {
	return Row{
		"id": track.ID, "relative_path": track.RelativePath,
		"artist": track.Artist, "title": track.Title, "album": track.Album,
		"genre": track.Genre, "year": track.Year, "comment": track.Comment,
		"duration": track.Duration, "track_number": track.TrackNumber,
		"created_at": track.CreatedAt, "modified_at": track.ModifiedAt,
		"hash": track.Hash, "download_url": track.DownloadURL,
		"music_directory": track.MusicDirectoryID, "missing": track.Missing,
	}
}

func parseSearchFieldValue(field, value string) (any, error) {
	var parsed any = value
	switch field {
	case "id", "music_directory_id", "duration", "track_number":
		integerValue, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, bad("Invalid integer value")
		}
		parsed = integerValue
	case "missing":
		switch strings.ToLower(value) {
		case "1", "true", "yes", "on":
			parsed = 1
		case "0", "false", "no", "off":
			parsed = 0
		default:
			return nil, bad("Invalid boolean value")
		}
	case "created_at", "modified_at":
		timestamp, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return nil, bad("Invalid ISO-8601 datetime value")
		}
		parsed = timestamp.UTC().Format(databaseTimestampLayout)
	}
	return parsed, nil
}

func (app *App) searchAPI(responseWriter http.ResponseWriter, request *http.Request) error {
	if err := safeOnly(request); err != nil {
		return err
	}
	params := request.URL.Query()
	limit, offset := 1000, 0
	var err error
	if limitValue := params.Get("limit"); params.Has("limit") {
		limit, err = strconv.Atoi(limitValue)
		if err != nil {
			return bad("`limit` and `offset` must be integers.")
		}
	}
	if offsetValue := params.Get("offset"); params.Has("offset") {
		offset, err = strconv.Atoi(offsetValue)
		if err != nil {
			return bad("`limit` and `offset` must be integers.")
		}
	}
	if limit < 0 || offset < 0 {
		return bad("`limit` and `offset` must be non-negative.")
	}
	if limit > 1000 {
		limit = 1000
	}
	fields := map[string]string{}
	for _, field := range []string{"id", "relative_path", "artist", "title", "album", "genre", "year", "comment", "duration", "track_number", "created_at", "modified_at", "missing", "hash", "music_directory_id"} {
		fields[field] = field
	}
	fields["pk"] = "id"
	fields["music_directory"] = "music_directory_id"
	clauses := []string{}
	args := []any{}
	order := "m.id"
	join := ""
	if query := strings.TrimSpace(params.Get("sq")); query != "" {
		join = " JOIN music_index f ON CAST(f.id AS INTEGER)=m.id"
		clauses = append(clauses, "music_index MATCH ?")
		args = append(args, ftsEscape(query))
		order = "f.rank"
	}
	for parameterName, values := range params {
		if parameterName == "sq" || parameterName == "limit" || parameterName == "offset" || parameterName == "order_by" {
			continue
		}
		field, ok := fields[parameterName]
		if !ok {
			return bad("Unknown query parameter \"" + parameterName + "\".")
		}
		value := values[len(values)-1]
		parsed, err := parseSearchFieldValue(field, value)
		if err != nil {
			return err
		}
		clauses = append(clauses, "m."+field+"=?")
		args = append(args, parsed)
	}
	if !params.Has("missing") {
		clauses = append(clauses, "m.missing=0")
	}
	if orderValue := strings.TrimSpace(params.Get("order_by")); orderValue != "" {
		if orderValue != "artist" && orderValue != "album" && orderValue != "genre" && orderValue != "title" {
			return bad("Invalid order_by")
		}
		order = "m." + orderValue + ",m.id"
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	count, err := app.Store.One(request.Context(), "SELECT COUNT(*) n FROM reinhardt_musicfile m"+join+where, args...)
	if err != nil {
		return err
	}
	args = append(args, limit, offset)
	tracks, err := app.Store.Tracks(request.Context(), join+where+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if err != nil {
		return err
	}
	result := []Row{}
	for _, track := range tracks {
		result = append(result, serializeTrack(track))
	}
	return jsonResponse(responseWriter, Row{"count": count.N("n"), "limit": limit, "offset": offset, "results": result})
}

func (app *App) playlists(responseWriter http.ResponseWriter, request *http.Request, username string) error {
	user, err := app.Store.One(request.Context(), "SELECT id FROM auth_user WHERE username=?", username)
	if err == sql.ErrNoRows {
		return jsonResponse(responseWriter, []Row{})
	}
	if err != nil {
		return err
	}
	tags, err := app.Store.Tags(request.Context(), user.N("id"), true)
	if err != nil {
		return err
	}
	result := []Row{}
	for _, tag := range tags {
		if tag.Label == "queue" || tag.Label == "blacklist" || tag.Label == "corrompus" {
			continue
		}
		tracks, err := app.Store.TagTracks(request.Context(), tag.ID)
		if err != nil {
			return err
		}
		items := []Row{}
		for _, track := range tracks {
			items = append(items, Row{"artist": track.Artist, "title": track.Title, "album": track.Album, "url": track.DownloadURL, "file_name": slug(track.Artist) + "_-_" + slug(track.Title) + fileExtension(track)})
		}
		result = append(result, Row{"label": tag.Label, "items": items})
	}
	return jsonResponse(responseWriter, result)
}

func inviteQuery(token, next string) string {
	values := url.Values{"q": {token}}
	if next != "" {
		values.Set("next", next)
	}
	return values.Encode()
}
