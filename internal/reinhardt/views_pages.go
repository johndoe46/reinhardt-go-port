package reinhardt

import (
	"net/http"
	"strings"
)

func (app *App) searchPage(responseWriter http.ResponseWriter, request *http.Request, user *User, ctx TemplateContext) error {
	store := app.Store
	if err := safeOnly(request); err != nil {
		return err
	}
	sq := strings.TrimSpace(request.URL.Query().Get("sq"))
	tracks, err := store.Search(request.Context(), sq)
	if err != nil {
		return err
	}
	ctx["sq"] = sq
	ctx["q"] = sq
	ctx["music_files"] = tracks
	count := len(tracks)
	if sq != "" {
		total, err := store.One(request.Context(), "SELECT COUNT(*) n FROM reinhardt_musicfile m JOIN music_index f ON CAST(f.id AS INTEGER)=m.id WHERE music_index MATCH ? AND m.missing=0", ftsEscape(sq))
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
			rows, err := store.Query(request.Context(), "SELECT DISTINCT m."+field+" value FROM reinhardt_musicfile m JOIN music_index f ON CAST(f.id AS INTEGER)=m.id WHERE f."+field+" MATCH ? ORDER BY m."+field, ftsEscape(sq))
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
	return app.render(responseWriter, request, user, "reinhardt/search.html", ctx, 200)
}

func (app *App) tagsPage(responseWriter http.ResponseWriter, request *http.Request, user *User, ctx TemplateContext) error {
	store := app.Store
	owner := user.ID
	if request.Method == "POST" {
		action := request.FormValue("action")
		if action == "add_tag" {
			if _, err := store.AddTag(request.Context(), owner, request.FormValue("tag_label")); err != nil {
				return bad(err.Error())
			}
		} else {
			tag, err := store.Tag(request.Context(), request.FormValue("tag_pk"), owner)
			if err != nil {
				return err
			}
			if tag.Label == "queue" || tag.Label == "favorites" {
				return forbidden()
			}
			switch action {
			case "remove_tag":
				_, err = store.DB.ExecContext(request.Context(), "DELETE FROM reinhardt_musicfiletag WHERE tag_id=?", tag.ID)
				if err == nil {
					_, err = store.DB.ExecContext(request.Context(), "DELETE FROM reinhardt_tag WHERE id=?", tag.ID)
				}
			case "share_tag", "unshare_tag":
				_, err = store.DB.ExecContext(request.Context(), "UPDATE reinhardt_tag SET is_shared=?,modified_at=? WHERE id=?", action == "share_tag", now(), tag.ID)
			default:
				return bad("Invalid action")
			}
			if err != nil {
				return err
			}
		}
	}
	tags, err := store.Tags(request.Context(), owner, false)
	if err != nil {
		return err
	}
	for _, tag := range tags {
		tag.ShareURL = app.absolute(request, tag.ShareURL)
	}
	ctx["tags"] = tags
	return app.render(responseWriter, request, user, "reinhardt/tags.html", ctx, 200)
}

func (app *App) tagPage(responseWriter http.ResponseWriter, request *http.Request, user *User, path, query string, ctx TemplateContext) error {
	store := app.Store
	owner := user.ID
	if err := safeOnly(request); err != nil {
		return err
	}
	var tag *Tag
	var tagErr error
	if path == "favorites/" {
		id, err := store.AddTag(request.Context(), owner, "favorites")
		if err != nil {
			return err
		}
		tag, tagErr = store.Tag(request.Context(), id, owner)
	} else {
		tag, tagErr = store.Tag(request.Context(), query, owner)
	}
	if tagErr != nil {
		return tagErr
	}
	tracks, tagErr := store.TagTracks(request.Context(), tag.ID)
	if tagErr != nil {
		return tagErr
	}
	ctx["music_files"] = tracks
	ctx["tag"] = tag
	if tag.IsShared {
		ctx["share_url"] = app.absolute(request, tag.ShareURL)
	}
	return app.render(responseWriter, request, user, "reinhardt/"+strings.TrimSuffix(path, "/")+".html", ctx, 200)
}

func (app *App) queuePage(responseWriter http.ResponseWriter, request *http.Request, user *User, path string, ctx TemplateContext) error {
	store := app.Store
	owner := user.ID
	if path == "shuffle_queue/" {
		if err := postOnly(request); err != nil {
			return err
		}
		if err := store.ShuffleQueue(request.Context(), owner); err != nil {
			return err
		}
	}
	if path == "clear_queue/" {
		if err := postOnly(request); err != nil {
			return err
		}
		_, err := store.DB.ExecContext(request.Context(), "DELETE FROM reinhardt_musicfiletag WHERE tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=? AND label='queue')", owner)
		if err != nil {
			return err
		}
	}
	queue, err := store.Queue(request.Context(), owner)
	if err != nil {
		return err
	}
	id, err := store.AddTag(request.Context(), owner, "queue")
	if err != nil {
		return err
	}
	tag, err := store.Tag(request.Context(), id, owner)
	if err != nil {
		return err
	}
	total := int64(0)
	for _, queueItem := range queue {
		total += queueItem.MusicFile.Duration
	}
	tag.Duration = total
	ctx["queue"] = queue
	ctx["music_tag"] = tag
	preferences, err := store.Preferences(request.Context(), owner)
	if err != nil {
		return err
	}
	ctx["now_playing"] = preferences.CurrentPosition
	return app.render(responseWriter, request, user, "reinhardt/queue.html", ctx, 200)
}

func (app *App) preferencesPage(responseWriter http.ResponseWriter, request *http.Request, user *User, ctx TemplateContext) error {
	store := app.Store
	owner := user.ID
	if request.Method == "POST" {
		if request.PostForm.Has("language_code") {
			code := request.PostForm.Get("language_code")
			if code != "" && !app.Translations.supports(code) {
				return bad(app.translator(user).Text("Unsupported language"))
			}
			if err := store.SetUserLanguage(request.Context(), owner, code); err != nil {
				return err
			}
			// Render the updated language in the main fragment while Unpoly
			// keeps the current player and the rest of the document in place.
			user.LanguageCode = code
			return app.render(responseWriter, request, user, "reinhardt/preferences.html", ctx, 200)
		}
		_, err := store.Preferences(request.Context(), owner)
		if err != nil {
			return err
		}
		_, err = store.DB.ExecContext(request.Context(), "UPDATE reinhardt_preferences SET autofeed_queue=?,autofeed_similar=?,show_oscilloscope=? WHERE owner_id=?", request.FormValue("autofeed_queue") == "on", request.FormValue("autofeed_similar") == "on", request.FormValue("show_oscilloscope") == "on", owner)
		if err != nil {
			return err
		}
	}
	return app.render(responseWriter, request, user, "reinhardt/preferences.html", ctx, 200)
}
