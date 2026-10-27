package reinhardt

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (app *App) logout(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	store := app.Store
	if cookie, err := request.Cookie("sessionid"); err == nil {
		if _, err = store.DB.ExecContext(request.Context(), "DELETE FROM go_session WHERE token=?", cookie.Value); err != nil {
			return err
		}
	}
	app.cookie(responseWriter, request, "sessionid", "", true, -1)
	app.redirect(responseWriter, request, store.URL("login"))
	return nil
}

func (app *App) invite(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	store := app.Store
	query := request.URL.Query().Get("q")
	invitedUser, err := store.One(request.Context(), `SELECT u.id FROM auth_user u JOIN reinhardt_preferences p ON p.owner_id=u.id WHERE p.access_token=? AND u.is_active=1`, query)
	if err != nil {
		return err
	}
	sess := token()
	if _, err = store.DB.ExecContext(request.Context(), "INSERT INTO go_session(token,owner_id,expires) VALUES(?,?,?)", sess, invitedUser.N("id"), time.Now().Unix()+sessionLifetimeSeconds); err != nil {
		return err
	}
	if _, err = store.DB.ExecContext(request.Context(), "UPDATE auth_user SET last_login=? WHERE id=?", now(), invitedUser.N("id")); err != nil {
		return err
	}
	app.cookie(responseWriter, request, "sessionid", sess, true, sessionLifetimeSeconds)
	// Rotate the CSRF token on successful invite-link login so tokens from
	// before authentication no longer match. The redirect renders fresh forms.
	app.cookie(responseWriter, request, "csrftoken", token(), false, sessionLifetimeSeconds)
	next := request.URL.Query().Get("next")
	if !strings.HasPrefix(next, store.Config.Path("")) || strings.HasPrefix(next, "//") {
		next = store.URL("index")
	}
	app.redirect(responseWriter, request, next)
	return nil
}

func (app *App) indexPage(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	ctx := TemplateContext{}
	offset := 0
	if value := request.URL.Query().Get("offset"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return bad("Invalid offset")
		}
		offset = parsed
	}
	tracks, err := app.latestAdditionsAt(request.Context(), offset)
	if err != nil {
		return err
	}
	if len(tracks) > latestAdditionsPageSize {
		tracks = tracks[:latestAdditionsPageSize]
		ctx["latest_additions_next"] = app.Store.URL("index") + "?offset=" + strconv.Itoa(offset+len(tracks))
	}
	// Discovery does not change the cached home metadata revision. Refresh the
	// artwork references in a batch at render time if the page displays images.
	for _, track := range tracks {
		track.artworkHash, track.artworkHashLoaded = "", false
	}
	ctx["music_files"] = tracks
	return app.render(responseWriter, request, user, "reinhardt/index.html", ctx, 200)
}

func (app *App) artistsPage(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	store := app.Store
	query := request.URL.Query().Get("q")
	ctx := TemplateContext{}
	letters, err := store.Query(request.Context(), "SELECT DISTINCT upper(substr(artist,1,1)) letter FROM reinhardt_musicfile ORDER BY letter")
	if err != nil {
		return err
	}
	ls := []string{}
	for _, letterRow := range letters {
		ls = append(ls, letterRow.S("letter"))
	}
	ctx["letters"] = ls
	template := "artists"
	if query != "" {
		artists, err := store.Query(request.Context(), "SELECT DISTINCT artist FROM reinhardt_musicfile WHERE artist LIKE ? AND missing=0 ORDER BY lower(artist)", query+"%")
		if err != nil {
			return err
		}
		ctx["artists"] = artists
		ctx["letter"] = query
		template = "_artists_by_letter"
	}
	return app.render(responseWriter, request, user, "reinhardt/"+template+".html", ctx, 200)
}

func (app *App) genresPage(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	store := app.Store
	ctx := TemplateContext{}
	genres, err := store.Query(request.Context(), "SELECT DISTINCT genre FROM reinhardt_musicfile WHERE missing=0 AND genre!='' ORDER BY lower(genre)")
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
	return app.render(responseWriter, request, user, "reinhardt/genres.html", ctx, 200)
}

func (app *App) collectionPage(responseWriter http.ResponseWriter, request *http.Request, user *User, path string) error {
	store := app.Store
	query := request.URL.Query().Get("q")
	ctx := TemplateContext{}
	field := strings.TrimSuffix(path, "/")
	order := "m.artist,m.album,m.track_number"
	if field == "album" {
		order = "m.track_number"
	}
	tracks, err := store.Tracks(request.Context(), "WHERE m."+field+"=? AND m.missing=0 ORDER BY "+order, query)
	if err != nil {
		return err
	}
	ctx[field] = query
	ctx["music_files"] = tracks
	return app.render(responseWriter, request, user, "reinhardt/"+field+".html", ctx, 200)
}

func (app *App) setAutofeed(responseWriter http.ResponseWriter, request *http.Request, user *User, path string) error {
	store := app.Store
	owner := user.ID
	ctx := TemplateContext{}
	if _, err := store.Preferences(request.Context(), owner); err != nil {
		return err
	}
	active := path == "activate_autofeed/"
	if _, err := store.DB.ExecContext(request.Context(), "UPDATE reinhardt_preferences SET autofeed_queue=? WHERE owner_id=?", active, owner); err != nil {
		return err
	}
	message := "autofeed deactivated"
	if active {
		message = "autofeed activated"
	}
	ctx["action"] = message
	return app.render(responseWriter, request, user, "reinhardt/_tag_message.html", ctx, 200)
}

func (app *App) nextSongPage(responseWriter http.ResponseWriter, request *http.Request, user *User, path string) error {
	ctx := TemplateContext{}
	track, rank, err := app.nextSong(request, user)
	if err != nil {
		return err
	}
	// Responses use an empty string for the position when playback ends.
	var position any = ""
	if track != nil {
		position = rank
	}
	if path == "api/dj/next_song/" {
		var data any
		if track != nil {
			data = serializeTrack(track)
		}
		return jsonResponse(responseWriter, Row{"music_file": data, "current_position": position})
	}
	ctx["music_file"] = track
	ctx["current_position"] = position
	ctx["autoplay"] = true
	return app.render(responseWriter, request, user, "reinhardt/_now_playing.html", ctx, 200)
}

func (app *App) removeFromQueue(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	store := app.Store
	owner := user.ID
	res, err := store.DB.ExecContext(request.Context(), "DELETE FROM reinhardt_musicfiletag WHERE music_file_id=? AND tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=? AND label='queue')", request.FormValue("remove_item"), owner)
	if err != nil {
		return err
	}
	affectedRows, _ := res.RowsAffected()
	if affectedRows == 0 {
		return sql.ErrNoRows
	}
	// Unpoly checks the response type even when the form targets :none.
	responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = responseWriter.Write([]byte("None"))
	return err
}

func (app *App) trackMenu(responseWriter http.ResponseWriter, request *http.Request, user *User, path string) error {
	store := app.Store
	owner := user.ID
	query := request.URL.Query().Get("q")
	ctx := TemplateContext{}
	template := ""
	id, err := strconv.ParseInt(query, 10, 64)
	if err != nil {
		return bad("Invalid music file ID.")
	}
	track, err := store.Track(request.Context(), id)
	if err != nil {
		return err
	}
	ctx["music_file"] = track
	if path == "music_file_tagger/" {
		tags, err := store.Tags(request.Context(), owner, false)
		if err != nil {
			return err
		}
		ctx["tags"] = tags
		ctx["next_url"] = request.URL.Query().Get("next")
		assigned, err := store.Query(request.Context(), `SELECT t.id FROM reinhardt_tag t JOIN reinhardt_musicfiletag a ON a.tag_id=t.id WHERE t.owner_id=? AND a.music_file_id=? AND t.label NOT IN ('queue','favorites')`, owner, id)
		if err != nil {
			return err
		}
		ctx["assigned_tags_pks"] = rowIDs(assigned)
		template = "music_file_tagger"
	} else {
		fav, err := store.One(request.Context(), `SELECT COUNT(*) n FROM reinhardt_musicfiletag a JOIN reinhardt_tag t ON t.id=a.tag_id WHERE t.owner_id=? AND t.label='favorites' AND a.music_file_id=?`, owner, id)
		if err != nil {
			return err
		}
		ctx["is_favorite"] = fav.N("n") > 0
		ctx["playing"] = request.URL.Query().Get("playing")
		ctx["context"] = request.URL.Query().Get("context")
		ctx["grid_item"] = request.URL.Query().Get("grid") == "1"
		if id := request.URL.Query().Get("tag"); id != "" {
			if _, err := strconv.Atoi(id); err != nil {
				return bad("Invalid tag ID.")
			}
			tag, err := store.Tag(request.Context(), id, owner)
			if err != nil {
				return err
			}
			ctx["remove_from_tag"] = tag.Label
		}
		template = "_track_dropdown_menu_items"
	}
	return app.render(responseWriter, request, user, "reinhardt/"+template+".html", ctx, 200)
}

func (app *App) friendsPage(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	store := app.Store
	owner := user.ID
	ctx := TemplateContext{}
	friendRows, err := store.Query(request.Context(), "SELECT * FROM auth_user WHERE id!=? ORDER BY username", owner)
	if err != nil {
		return err
	}
	visible := []Row{}
	for _, row := range friendRows {
		friend := userFromRow(row)
		tags, err := store.Tags(request.Context(), friend.ID, true)
		if err != nil {
			return err
		}
		ts := []*Tag{}
		for _, tag := range tags {
			if tag.TrackCount > 0 && (user.IsSuperuser || tag.Label != "queue") {
				ts = append(ts, tag)
			}
		}
		if len(ts) > 0 {
			visible = append(visible, Row{"user": friend, "tags": ts})
		}
	}
	ctx["friends"] = visible
	return app.render(responseWriter, request, user, "reinhardt/friends.html", ctx, 200)
}

func (app *App) friendTagPage(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	store := app.Store
	ctx := TemplateContext{}
	tag, err := store.tag(request.Context(), "SELECT * FROM reinhardt_tag WHERE id=?", request.URL.Query().Get("tag"))
	if err != nil {
		return err
	}
	friend, err := store.user(request.Context(), "SELECT * FROM auth_user WHERE id=?", tag.OwnerID)
	if err != nil {
		return err
	}
	tracks, err := store.TagTracks(request.Context(), tag.ID)
	if err != nil {
		return err
	}
	ctx["music_files"] = tracks
	ctx["friend_tag"] = tag
	ctx["friend"] = friend
	return app.render(responseWriter, request, user, "reinhardt/friend_tag.html", ctx, 200)
}

func (app *App) queueArchive(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	store := app.Store
	owner := user.ID
	query := request.URL.Query().Get("q")
	if query == "" {
		tag, err := store.One(request.Context(), "SELECT id FROM reinhardt_tag WHERE owner_id=? AND label='queue'", owner)
		if err != nil {
			return err
		}
		query = tag.S("id")
	}
	tag, err := store.Tag(request.Context(), query, owner)
	if err != nil {
		return err
	}
	return app.archive(responseWriter, request, tag)
}

func (app *App) downloadDatabase(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	if !user.IsSuperuser {
		return forbidden()
	}
	return app.databaseBackup(responseWriter, request)
}

func (app *App) similarArtistsPage(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	query := request.URL.Query().Get("q")
	ctx := TemplateContext{}
	ctx["similar_artists"] = app.similarArtists(request.Context(), query)
	return app.render(responseWriter, request, user, "reinhardt/_similar_artists.html", ctx, 200)
}

func (app *App) renameTag(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	store := app.Store
	owner := user.ID
	ctx := TemplateContext{}
	tag, err := store.Tag(request.Context(), request.PathValue("id"), owner)
	if err != nil {
		return err
	}
	if request.Method == "POST" {
		name := label(request.FormValue("tag_label"))
		if name == "" {
			ctx["error_message"] = "tag name is empty"
		} else {
			_, err = store.DB.ExecContext(request.Context(), "UPDATE reinhardt_tag SET label=?,modified_at=? WHERE id=? AND owner_id=?", name, now(), tag.ID, owner)
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

func (app *App) artworkModal(responseWriter http.ResponseWriter, request *http.Request, user *User) error {
	track, err := app.Store.Track(request.Context(), request.PathValue("id"))
	if err != nil {
		return err
	}
	ctx := TemplateContext{
		"music_file": track,
		"size":       strings.TrimSuffix(request.PathValue("size"), ".jpg"),
	}
	return app.render(responseWriter, request, user, "reinhardt/artwork_modal.html", ctx, 200)
}
