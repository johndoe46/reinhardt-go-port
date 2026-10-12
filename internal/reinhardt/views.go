package reinhardt

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (app *App) dispatch(responseWriter http.ResponseWriter, request *http.Request, user Row, path string) error {
	store := app.Store
	owner := user.N("id")
	query := request.URL.Query().Get("q")
	ctx := TemplateContext{}
	template := ""
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) > 1 {
		switch parts[0] {
		case "shared":
			return app.shared(responseWriter, request, user, parts)
		case "stream", "download":
			if err := safeOnly(request); err != nil {
				return err
			}
			return app.audio(responseWriter, request, parts)
		case "artwork":
			if err := safeOnly(request); err != nil {
				return err
			}
			return app.artwork(responseWriter, request, parts)
		case "artwork_modal":
			ctx["trackid"] = parts[1]
			if len(parts) < 3 {
				return sql.ErrNoRows
			}
			ctx["size"] = strings.TrimSuffix(parts[2], ".jpg")
			return app.render(responseWriter, request, user, "reinhardt/artwork_modal.html", ctx, 200)
		case "track_details_modal":
			return app.trackDetails(responseWriter, request, user, parts[1])
		case "rename_tag":
			tag, err := store.Tag(parts[1], owner)
			if err != nil {
				return err
			}
			if request.Method == "POST" {
				name := label(request.FormValue("tag_label"))
				if name == "" {
					ctx["error_message"] = "tag name is empty"
				} else {
					_, err = store.DB.Exec("UPDATE reinhardt_tag SET label=?,modified_at=? WHERE id=? AND owner_id=?", name, now(), tag.N("id"), owner)
					if err == nil {
						app.redirect(responseWriter, request, store.URL("tags"))
						return nil
					}
					ctx["error_message"] = "tag already exists"
				}
			}
			ctx["tag"] = tag
			return app.render(responseWriter, request, user, "reinhardt/rename_tag.html", ctx, 200)
		}
	}
	switch path {
	case "accounts/login/":
		return app.login(responseWriter, request, user)
	case "accounts/logout/":
		if err := postOnly(request); err != nil {
			return err
		}
		if cookie, err := request.Cookie("sessionid"); err == nil {
			if _, err = store.DB.Exec("DELETE FROM go_session WHERE token=?", cookie.Value); err != nil {
				return err
			}
		}
		app.cookie(responseWriter, request, "sessionid", "", true, -1)
		app.redirect(responseWriter, request, store.URL("login"))
		return nil
	case "invite/":
		if err := safeOnly(request); err != nil {
			return err
		}
		user, err := store.One(`SELECT u.id FROM auth_user u JOIN reinhardt_preferences p ON p.owner_id=u.id WHERE p.access_token=? AND u.is_active=1`, query)
		if err != nil {
			return err
		}
		sess := token()
		if _, err = store.DB.Exec("INSERT INTO go_session(token,owner_id,expires) VALUES(?,?,?)", sess, user.N("id"), time.Now().Unix()+31556952); err != nil {
			return err
		}
		if _, err = store.DB.Exec("UPDATE auth_user SET last_login=? WHERE id=?", now(), user.N("id")); err != nil {
			return err
		}
		app.cookie(responseWriter, request, "sessionid", sess, true, 31556952)
		// Rotate the CSRF token on successful invite-link login so tokens from
		// before authentication no longer match. The redirect renders fresh forms.
		app.cookie(responseWriter, request, "csrftoken", token(), false, 31556952)
		next := request.URL.Query().Get("next")
		if !strings.HasPrefix(next, store.Config.Path("")) || strings.HasPrefix(next, "//") {
			next = store.URL("index")
		}
		app.redirect(responseWriter, request, next)
		return nil
	case "":
		if err := safeOnly(request); err != nil {
			return err
		}
		tracks, err := app.latestAdditions()
		if err != nil {
			return err
		}
		ctx["music_files"] = tracks
		template = "index"
	case "artists/":
		if err := safeOnly(request); err != nil {
			return err
		}
		letters, err := store.Query("SELECT DISTINCT upper(substr(artist,1,1)) letter FROM reinhardt_musicfile ORDER BY letter")
		if err != nil {
			return err
		}
		ls := []string{}
		for _, letterRow := range letters {
			ls = append(ls, letterRow.S("letter"))
		}
		ctx["letters"] = ls
		template = "artists"
		if query != "" {
			artists, err := store.Query("SELECT DISTINCT artist FROM reinhardt_musicfile WHERE artist LIKE ? AND missing=0 ORDER BY lower(artist)", query+"%")
			if err != nil {
				return err
			}
			ctx["artists"] = artists
			ctx["letter"] = query
			template = "_artists_by_letter"
		}
	case "genres/":
		if err := safeOnly(request); err != nil {
			return err
		}
		genres, err := store.Query("SELECT DISTINCT genre FROM reinhardt_musicfile WHERE missing=0 AND genre!='' ORDER BY lower(genre)")
		if err != nil {
			return err
		}
		for _, genreRow := range genres {
			rs := []rune(strings.ToLower(strings.TrimSpace(genreRow.S("genre"))))
			if len(rs) > 0 {
				genreRow["first_letter"] = string(rs[0])
			}
		}
		ctx["music_genres"] = genres
		template = "genres"
	case "artist/", "album/", "genre/":
		if err := safeOnly(request); err != nil {
			return err
		}
		field := strings.TrimSuffix(path, "/")
		order := "m.artist,m.album,m.track_number"
		if field == "album" {
			order = "m.track_number"
		}
		tracks, err := store.Tracks("WHERE m."+field+"=? AND m.missing=0 ORDER BY "+order, query)
		if err != nil {
			return err
		}
		ctx[field] = query
		ctx["music_files"] = tracks
		template = field
	case "search/":
		if err := safeOnly(request); err != nil {
			return err
		}
		sq := strings.TrimSpace(request.URL.Query().Get("sq"))
		tracks, err := store.Search(sq)
		if err != nil {
			return err
		}
		ctx["sq"] = sq
		ctx["q"] = sq
		ctx["music_files"] = tracks
		count := len(tracks)
		if sq != "" {
			total, err := store.One("SELECT COUNT(*) n FROM reinhardt_musicfile m JOIN music_index f ON CAST(f.id AS INTEGER)=m.id WHERE music_index MATCH ? AND m.missing=0", ftsEscape(sq))
			if err != nil {
				return err
			}
			count = int(total.N("n"))
		}
		ctx["total_count"] = count
		found := count > 0
		for _, field := range []string{"artist", "album", "genre"} {
			values := []string{}
			if sq != "" {
				rows, err := store.Query("SELECT DISTINCT m."+field+" value FROM reinhardt_musicfile m JOIN music_index f ON CAST(f.id AS INTEGER)=m.id WHERE f."+field+" MATCH ? ORDER BY m."+field, ftsEscape(sq))
				if err != nil {
					return err
				}
				for _, suggestionRow := range rows {
					values = append(values, suggestionRow.S("value"))
				}
			}
			ctx["found_"+field+"s"] = values
			found = found || len(values) > 0
		}
		ctx["has_results"] = found
		template = "search"
	case "api/music_files/search/":
		return app.searchAPI(responseWriter, request)
	case "dj/":
		if err := safeOnly(request); err != nil {
			return err
		}
		template = "music_file_search_page"
	case "tags/":
		if request.Method == "POST" {
			action := request.FormValue("action")
			if action == "add_tag" {
				if _, err := store.AddTag(owner, request.FormValue("tag_label")); err != nil {
					return bad(err.Error())
				}
			} else {
				tag, err := store.Tag(request.FormValue("tag_pk"), owner)
				if err != nil {
					return err
				}
				if tag.S("label") == "queue" || tag.S("label") == "favorites" {
					return forbidden()
				}
				switch action {
				case "remove_tag":
					_, err = store.DB.Exec("DELETE FROM reinhardt_musicfiletag WHERE tag_id=?", tag.N("id"))
					if err == nil {
						_, err = store.DB.Exec("DELETE FROM reinhardt_tag WHERE id=?", tag.N("id"))
					}
				case "share_tag", "unshare_tag":
					_, err = store.DB.Exec("UPDATE reinhardt_tag SET is_shared=?,modified_at=? WHERE id=?", action == "share_tag", now(), tag.N("id"))
				default:
					return bad("Invalid action")
				}
				if err != nil {
					return err
				}
			}
		}
		tags, err := store.Tags(owner, false)
		if err != nil {
			return err
		}
		for _, tag := range tags {
			tag["share_url"] = app.absolute(request, tag.S("share_url"))
		}
		ctx["tags"] = tags
		template = "tags"
	case "tag/", "favorites/":
		if err := safeOnly(request); err != nil {
			return err
		}
		var tag Row
		var tagErr error
		if path == "favorites/" {
			id, err := store.AddTag(owner, "favorites")
			if err != nil {
				return err
			}
			tag, tagErr = store.Tag(id, owner)
		} else {
			tag, tagErr = store.Tag(query, owner)
		}
		if tagErr != nil {
			return tagErr
		}
		tracks, tagErr := store.TagTracks(tag.N("id"))
		if tagErr != nil {
			return tagErr
		}
		ctx["music_files"] = tracks
		ctx["tag"] = tag
		if tag.B("is_shared") {
			ctx["share_url"] = app.absolute(request, tag.S("share_url"))
		}
		template = strings.TrimSuffix(path, "/")
	case "queue/", "clear_queue/", "shuffle_queue/":
		if path == "shuffle_queue/" {
			if err := postOnly(request); err != nil {
				return err
			}
			if err := store.ShuffleQueue(owner); err != nil {
				return err
			}
		}
		if path == "clear_queue/" {
			if err := postOnly(request); err != nil {
				return err
			}
			_, err := store.DB.Exec("DELETE FROM reinhardt_musicfiletag WHERE tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=? AND label='queue')", owner)
			if err != nil {
				return err
			}
		}
		queue, err := store.Queue(owner)
		if err != nil {
			return err
		}
		id, err := store.AddTag(owner, "queue")
		if err != nil {
			return err
		}
		tag, err := store.Tag(id, owner)
		if err != nil {
			return err
		}
		total := int64(0)
		for _, queueItem := range queue {
			total += queueItem["music_file"].(Row).N("duration")
		}
		tag["duration"] = total
		ctx["queue"] = queue
		ctx["music_tag"] = tag
		preferences, err := store.Preferences(owner)
		if err != nil {
			return err
		}
		ctx["now_playing"] = preferences["current_position"]
		template = "queue"
	case "preferences/":
		if request.Method == "POST" {
			_, err := store.Preferences(owner)
			if err != nil {
				return err
			}
			_, err = store.DB.Exec("UPDATE reinhardt_preferences SET autofeed_queue=?,autofeed_similar=?,show_oscilloscope=? WHERE owner_id=?", request.FormValue("autofeed_queue") == "on", request.FormValue("autofeed_similar") == "on", request.FormValue("show_oscilloscope") == "on", owner)
			if err != nil {
				return err
			}
		}
		template = "preferences"
	case "activate_autofeed/", "deactivate_autofeed/":
		if err := postOnly(request); err != nil {
			return err
		}
		if _, err := store.Preferences(owner); err != nil {
			return err
		}
		active := path == "activate_autofeed/"
		if _, err := store.DB.Exec("UPDATE reinhardt_preferences SET autofeed_queue=? WHERE owner_id=?", active, owner); err != nil {
			return err
		}
		message := "autofeed deactivated"
		if active {
			message = "autofeed activated"
		}
		ctx["action"] = message
		template = "_tag_message"
	case "next_song/", "api/dj/next_song/":
		if err := postOnly(request); err != nil {
			return err
		}
		track, pos, err := app.nextSong(request, user)
		if err != nil {
			return err
		}
		if path == "api/dj/next_song/" {
			var data any
			if track != nil {
				data = serializeTrack(track)
			}
			return jsonResponse(responseWriter, Row{"music_file": data, "current_position": pos})
		}
		ctx["music_file"] = track
		ctx["current_position"] = pos
		ctx["autoplay"] = true
		template = "_now_playing"
	case "reorder_queue/":
		return app.reorder(responseWriter, request, user)
	case "remove_from_queue/":
		if err := postOnly(request); err != nil {
			return err
		}
		res, err := store.DB.Exec("DELETE FROM reinhardt_musicfiletag WHERE music_file_id=? AND tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=? AND label='queue')", request.FormValue("remove_item"), owner)
		if err != nil {
			return err
		}
		affectedRows, _ := res.RowsAffected()
		if affectedRows == 0 {
			return sql.ErrNoRows
		}
		_, err = responseWriter.Write([]byte("None"))
		return err
	case "add_to_tag/", "remove_from_tag/", "toggle_tag/":
		return app.tagAction(responseWriter, request, user, path)
	case "artist/add_to_tag/", "artist/add_to_favorites/", "artist/add_to_queue/", "search/add_to_tag/", "search/add_to_favorites/", "search/add_to_queue/", "queue/add_to_tag/", "queue/add_to_favorites/":
		return app.bulk(responseWriter, request, user, path)
	case "music_file_tagger/", "music_file_dropdown/":
		if err := safeOnly(request); err != nil {
			return err
		}
		id, err := strconv.ParseInt(query, 10, 64)
		if err != nil {
			return bad("Invalid music file ID.")
		}
		track, err := store.Track(id)
		if err != nil {
			return err
		}
		ctx["music_file"] = track
		if path == "music_file_tagger/" {
			tags, err := store.Tags(owner, false)
			if err != nil {
				return err
			}
			ctx["tags"] = tags
			ctx["next_url"] = request.URL.Query().Get("next")
			assigned, err := store.Query(`SELECT t.id FROM reinhardt_tag t JOIN reinhardt_musicfiletag a ON a.tag_id=t.id WHERE t.owner_id=? AND a.music_file_id=? AND t.label NOT IN ('queue','favorites')`, owner, id)
			if err != nil {
				return err
			}
			ctx["assigned_tags_pks"] = ids(assigned)
			template = "music_file_tagger"
		} else {
			fav, err := store.One(`SELECT COUNT(*) n FROM reinhardt_musicfiletag a JOIN reinhardt_tag t ON t.id=a.tag_id WHERE t.owner_id=? AND t.label='favorites' AND a.music_file_id=?`, owner, id)
			if err != nil {
				return err
			}
			ctx["is_favorite"] = fav.N("n") > 0
			ctx["playing"] = request.URL.Query().Get("playing")
			ctx["context"] = request.URL.Query().Get("context")
			if id := request.URL.Query().Get("tag"); id != "" {
				if _, err := strconv.Atoi(id); err != nil {
					return bad("Invalid tag ID.")
				}
				tag, err := store.Tag(id, owner)
				if err != nil {
					return err
				}
				ctx["remove_from_tag"] = tag["label"]
			}
			template = "_track_dropdown_menu_items"
		}
	case "friends/":
		friends, err := store.Query("SELECT * FROM auth_user WHERE id!=? ORDER BY username", owner)
		if err != nil {
			return err
		}
		visible := []Row{}
		for _, friend := range friends {
			tags, err := store.Tags(friend.N("id"), true)
			if err != nil {
				return err
			}
			ts := []Row{}
			for _, tag := range tags {
				if tag.N("track_count") > 0 && (user.B("is_superuser") || tag.S("label") != "queue") {
					ts = append(ts, tag)
				}
			}
			if len(ts) > 0 {
				visible = append(visible, Row{"user": friend, "tags": ts})
			}
		}
		ctx["friends"] = visible
		template = "friends"
	case "friend_tag/":
		tag, err := store.One("SELECT * FROM reinhardt_tag WHERE id=?", request.URL.Query().Get("tag"))
		if err != nil {
			return err
		}
		friend, err := store.One("SELECT * FROM auth_user WHERE id=?", tag.N("owner_id"))
		if err != nil {
			return err
		}
		tracks, err := store.TagTracks(tag.N("id"))
		if err != nil {
			return err
		}
		assocs := []Row{}
		for _, track := range tracks {
			assocs = append(assocs, Row{"music_file": track})
		}
		tag["musicfiletag_set"] = assocs
		ctx["friend_tag"] = tag
		ctx["friend"] = friend
		template = "friend_tag"
	case "queue_as_mix/":
		if err := safeOnly(request); err != nil {
			return err
		}
		if query == "" {
			tag, err := store.One("SELECT id FROM reinhardt_tag WHERE owner_id=? AND label='queue'", owner)
			if err != nil {
				return err
			}
			query = tag.S("id")
		}
		tag, err := store.Tag(query, owner)
		if err != nil {
			return err
		}
		return app.archive(responseWriter, request, tag)
	case "preferences/database/":
		if err := safeOnly(request); err != nil {
			return err
		}
		if !user.B("is_superuser") {
			return forbidden()
		}
		return app.databaseBackup(responseWriter, request)
	case "similar_artists/":
		if err := safeOnly(request); err != nil {
			return err
		}
		ctx["similar_artists"] = app.similarArtists(request.Context(), query)
		template = "_similar_artists"
	case "user_playlists_contents/":
		if err := safeOnly(request); err != nil {
			return err
		}
		return app.playlists(responseWriter, request, query)
	case "upload/":
		return app.upload(responseWriter, request, user)
	case "ytrip/", "ytrip/metadata/", "ytrip/failures/":
		return app.youtubeView(responseWriter, request, user, path)
	default:
		if len(parts) == 4 && parts[0] == "ytrip" && parts[1] == "failures" && parts[3] == "retry" {
			return app.retryYouTube(responseWriter, request, user, parts[2])
		}
		return sql.ErrNoRows
	}
	return app.render(responseWriter, request, user, "reinhardt/"+template+".html", ctx, 200)
}

func (app *App) shared(responseWriter http.ResponseWriter, request *http.Request, user Row, parts []string) error {
	if err := safeOnly(request); err != nil {
		return err
	}
	if len(parts) < 3 {
		return sql.ErrNoRows
	}
	tag, err := app.Store.One("SELECT * FROM reinhardt_tag WHERE id=? AND is_shared=1", parts[1])
	if err != nil {
		return err
	}
	app.Store.decorateTag(tag)
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(tag.S("share_hash"))) != 1 {
		return sql.ErrNoRows
	}
	tracks, err := app.Store.TagTracks(tag.N("id"))
	if err != nil {
		return err
	}
	total := int64(0)
	for _, track := range tracks {
		total += track.N("duration")
	}
	if len(parts) > 3 {
		switch parts[3] {
		case "track":
			if len(parts) != 5 {
				return sql.ErrNoRows
			}
			for _, track := range tracks {
				if track.S("id") == parts[4] {
					return serveLocal(responseWriter, request, track, false)
				}
			}
			return sql.ErrNoRows
		case "download":
			if total >= int64(app.Store.Config.Int("SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS")) {
				return sql.ErrNoRows
			}
			return app.archive(responseWriter, request, tag)
		default:
			return sql.ErrNoRows
		}
	}
	return app.render(responseWriter, request, user, "reinhardt/shared_tag.html", TemplateContext{"tag": tag, "share_hash": parts[2], "music_files": tracks, "total_duration": total, "can_download_all": total < int64(app.Store.Config.Int("SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS"))}, 200)
}

func (app *App) tagAction(responseWriter http.ResponseWriter, request *http.Request, user Row, path string) error {
	if err := postOnly(request); err != nil {
		return err
	}
	owner := user.N("id")
	name := label(request.FormValue("tag"))
	query := request.FormValue("q")
	kind := request.FormValue("add_type")
	action := "added"
	object := kind
	var tracks []Row
	var operationErr error
	if path == "toggle_tag/" {
		query = request.FormValue("music_file_pk")
		tag, err := app.Store.Tag(request.FormValue("tag_pk"), owner)
		if err != nil {
			return err
		}
		if tag.S("label") == "queue" || tag.S("label") == "favorites" {
			return forbidden()
		}
		name = tag.S("label")
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
		track, err := app.Store.Track(query)
		if err != nil {
			return err
		}
		tracks = []Row{track}
		object = "track"
	case "artist", "genre", "album":
		tracks, operationErr = app.Store.Tracks("WHERE m."+kind+"=? AND m.missing=0 ORDER BY m.artist,m.album,m.track_number", query)
	case "tag", "favorites":
		source := query
		if kind == "favorites" {
			source = "favorites"
		}
		tag, err := app.Store.One("SELECT * FROM reinhardt_tag WHERE owner_id=? AND label=?", owner, source)
		if err != nil {
			return err
		}
		tracks, operationErr = app.Store.TagTracks(tag.N("id"))
	case "friend_tag":
		tracks, operationErr = app.Store.TagTracks(query)
	default:
		return bad("Invalid action type")
	}
	if operationErr != nil {
		return operationErr
	}
	if action == "added" {
		if kind == "music_file" {
			_, operationErr = app.Store.AddTrack(owner, name, tracks[0].N("id"))
		} else {
			_, operationErr = app.Store.AddTracks(owner, name, ids(tracks))
		}
		if operationErr != nil {
			return operationErr
		}
	} else {
		tx, err := app.Store.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, track := range tracks {
			if _, operationErr = tx.Exec("DELETE FROM reinhardt_musicfiletag WHERE music_file_id=? AND tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=? AND label=?)", track.N("id"), owner, name); operationErr != nil {
				return operationErr
			}
		}
		if operationErr = tx.Commit(); operationErr != nil {
			return operationErr
		}
	}
	return app.render(responseWriter, request, user, "reinhardt/_tag_message.html", TemplateContext{"object_label": object, "tag_label": name, "action": action, "object_pk": query}, 200)
}

func (app *App) bulk(responseWriter http.ResponseWriter, request *http.Request, user Row, path string) error {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	source, action := parts[0], parts[1]
	ctx := TemplateContext{}
	value := request.FormValue("artist")
	if source == "search" {
		value = request.FormValue("sq")
	}
	ctx["artist"] = value
	ctx["q"] = value
	tags, operationErr := app.Store.Tags(user.N("id"), false)
	if operationErr != nil {
		return operationErr
	}
	ctx["tags"] = tags
	var target string
	if action != "add_to_tag" {
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
				tag, tagErr := app.Store.Tag(request.FormValue("tag_pk"), user.N("id"))
				if tagErr != nil {
					return tagErr
				}
				if tag.S("label") == "queue" || tag.S("label") == "favorites" {
					return forbidden()
				}
				target = tag.S("label")
			}
		}
		var tracks []Row
		switch source {
		case "artist":
			tracks, operationErr = app.Store.Tracks("WHERE m.artist=? AND m.missing=0 ORDER BY m.album,m.track_number", value)
		case "search":
			tracks, operationErr = app.Store.Search(value)
		case "queue":
			queue, err := app.Store.Queue(user.N("id"))
			operationErr = err
			for _, queueItem := range queue {
				tracks = append(tracks, queueItem["music_file"].(Row))
			}
		}
		if operationErr != nil {
			return operationErr
		}
		count, err := app.Store.AddTracks(user.N("id"), target, ids(tracks))
		if err != nil {
			return err
		}
		ctx["added_count"] = count
		tag, err := app.Store.One("SELECT * FROM reinhardt_tag WHERE owner_id=? AND label=?", user.N("id"), target)
		if err != nil {
			return err
		}
		ctx["selected_tag"] = tag
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

func (app *App) reorder(responseWriter http.ResponseWriter, request *http.Request, user Row) error {
	if err := postOnly(request); err != nil {
		return err
	}
	tag, err := app.Store.Tag(request.FormValue("list_pk"), user.N("id"))
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
	tx, err := app.Store.DB.Begin()
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
		res, err := tx.Exec("UPDATE reinhardt_musicfiletag SET rank=? WHERE music_file_id=? AND tag_id=?", item.Rank, item.Item, tag.N("id"))
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
	_, err = responseWriter.Write([]byte("ok"))
	return err
}

func (app *App) nextSong(request *http.Request, user Row) (Row, any, error) {
	store := app.Store
	owner := user.N("id")
	prefs, operationErr := store.Preferences(owner)
	if operationErr != nil {
		return nil, 0, operationErr
	}
	current := int64(0)
	var currentItem Row
	// The persisted position is an association id, while advancement compares
	// ranks. Reordering the queue changes rank without changing the saved position.
	if position := prefs.N("current_position"); position != 0 {
		currentItem, operationErr = store.queueItem(owner, "a.id=?", position)
		if operationErr != nil && operationErr != sql.ErrNoRows {
			return nil, 0, operationErr
		}
		if currentItem != nil {
			current = currentItem.N("rank")
		}
	}
	var chosen Row
	var track Row
	if item := request.FormValue("list_item"); item != "" {
		// list_item comes from the player's track controls, so it identifies a
		// music file, not the association id stored in preferences.
		if id, err := strconv.ParseInt(item, 10, 64); err == nil && strconv.FormatInt(id, 10) == item {
			chosen, operationErr = store.queueItem(owner, "a.music_file_id=?", id)
			if operationErr != nil && operationErr != sql.ErrNoRows {
				return nil, 0, operationErr
			}
		}
	}
	if chosen == nil {
		if next := request.FormValue("next_track"); next != "" {
			track, operationErr = store.Track(next)
			if operationErr != nil {
				return nil, 0, operationErr
			}
			chosen, operationErr = store.AddTrack(owner, "queue", track.N("id"))
			if operationErr != nil {
				return nil, 0, operationErr
			}
		} else {
			chosen, operationErr = store.queueItem(owner, "a.rank>?", current)
			if operationErr != nil && operationErr != sql.ErrNoRows {
				return nil, 0, operationErr
			}
		}
	}
	if chosen == nil && prefs.B("autofeed_queue") {
		// Keep Last.fm behind both the preference and queue-exhaustion checks.
		// Normal advancement and disabled similarity must never contact Last.fm.
		if prefs.B("autofeed_similar") && currentItem != nil {
			last, err := store.Track(currentItem["music_file_id"])
			if err != nil {
				return nil, 0, err
			}
			similar := app.similarArtists(request.Context(), last.S("artist"))
			for _, name := range similar {
				track, operationErr = app.randomAutofeedTrack(request.Context(), owner, &name)
				if operationErr != nil {
					return nil, 0, operationErr
				}
				if track != nil {
					break
				}
			}
		}
		if track == nil {
			track, operationErr = app.sampleAutofeedTrack(request.Context(), autofeedKey{owner: owner, excludeQueuedArtists: prefs.B("autofeed_similar")})
			if operationErr != nil {
				return nil, 0, operationErr
			}
		}
		if track == nil && prefs.B("autofeed_similar") {
			// Once every eligible artist is represented, keep autofeed playing
			// with a random nonmissing, nonblacklisted track from any artist.
			track, operationErr = app.randomAutofeedTrack(request.Context(), owner, nil)
			if operationErr != nil {
				return nil, 0, operationErr
			}
		}
		if track != nil {
			chosen, operationErr = store.AddTrack(owner, "queue", track.N("id"))
			if operationErr != nil {
				return nil, 0, operationErr
			}
		}
	}
	position := int64(0)
	var rank any = ""
	if chosen != nil {
		position = chosen.N("id")
		if track == nil {
			track, operationErr = store.Track(chosen["music_file_id"])
			if operationErr != nil {
				return nil, 0, operationErr
			}
		}
		rank = chosen["rank"]
	}
	_, operationErr = store.DB.Exec("UPDATE reinhardt_preferences SET current_position=? WHERE owner_id=?", position, owner)
	return track, rank, operationErr
}

func serializeTrack(track Row) Row {
	serializedTrack := Row{}
	for _, field := range []string{"id", "relative_path", "artist", "title", "album", "genre", "year", "comment", "duration", "track_number", "created_at", "modified_at", "hash", "download_url"} {
		serializedTrack[field] = track[field]
	}
	serializedTrack["music_directory"] = track["music_directory_id"]
	serializedTrack["missing"] = track.B("missing")
	return serializedTrack
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
	for parameterName, vs := range params {
		if parameterName == "sq" || parameterName == "limit" || parameterName == "offset" || parameterName == "order_by" {
			continue
		}
		field, ok := fields[parameterName]
		if !ok {
			return bad("Unknown query parameter \"" + parameterName + "\".")
		}
		value := vs[len(vs)-1]
		var parsed any = value
		switch field {
		case "id", "music_directory_id", "duration", "track_number":
			integerValue, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return bad("Invalid integer value")
			}
			parsed = integerValue
		case "missing":
			switch strings.ToLower(value) {
			case "1", "true", "yes", "on":
				parsed = 1
			case "0", "false", "no", "off":
				parsed = 0
			default:
				return bad("Invalid boolean value")
			}
		case "created_at", "modified_at":
			timestamp, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return bad("Invalid ISO-8601 datetime value")
			}
			parsed = timestamp.UTC().Format("2006-01-02 15:04:05.000000")
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
	count, err := app.Store.One("SELECT COUNT(*) n FROM reinhardt_musicfile m"+join+where, args...)
	if err != nil {
		return err
	}
	args = append(args, limit, offset)
	tracks, err := app.Store.Tracks(join+where+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
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
	user, err := app.Store.One("SELECT id FROM auth_user WHERE username=?", username)
	if err == sql.ErrNoRows {
		return jsonResponse(responseWriter, []Row{})
	}
	if err != nil {
		return err
	}
	tags, err := app.Store.Tags(user.N("id"), true)
	if err != nil {
		return err
	}
	result := []Row{}
	for _, tag := range tags {
		if tag.S("label") == "queue" || tag.S("label") == "blacklist" || tag.S("label") == "corrompus" {
			continue
		}
		tracks, err := app.Store.TagTracks(tag.N("id"))
		if err != nil {
			return err
		}
		items := []Row{}
		for _, track := range tracks {
			items = append(items, Row{"artist": track["artist"], "title": track["title"], "album": track["album"], "url": track["download_url"], "file_name": slug(track.S("artist")) + "_-_" + slug(track.S("title")) + fileExtension(track)})
		}
		result = append(result, Row{"label": tag["label"], "items": items})
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
