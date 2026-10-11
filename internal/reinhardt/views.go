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

func (a *App) dispatch(w http.ResponseWriter, r *http.Request, u Row, path string) error {
	s := a.S
	owner := u.N("id")
	q := r.URL.Query().Get("q")
	ctx := TemplateContext{}
	template := ""
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) > 1 {
		switch parts[0] {
		case "shared":
			return a.shared(w, r, u, parts)
		case "stream", "download":
			if e := safeOnly(r); e != nil {
				return e
			}
			return a.audio(w, r, parts)
		case "artwork":
			if e := safeOnly(r); e != nil {
				return e
			}
			return a.artwork(w, r, parts)
		case "artwork_modal":
			ctx["trackid"] = parts[1]
			if len(parts) < 3 {
				return sql.ErrNoRows
			}
			ctx["size"] = strings.TrimSuffix(parts[2], ".jpg")
			return a.render(w, r, u, "reinhardt/artwork_modal.html", ctx, 200)
		case "track_details_modal":
			return a.trackDetails(w, r, u, parts[1])
		case "rename_tag":
			t, e := s.Tag(parts[1], owner)
			if e != nil {
				return e
			}
			if r.Method == "POST" {
				name := label(r.FormValue("tag_label"))
				if name == "" {
					ctx["error_message"] = "tag name is empty"
				} else {
					_, e = s.DB.Exec("UPDATE reinhardt_tag SET label=?,modified_at=? WHERE id=? AND owner_id=?", name, now(), t.N("id"), owner)
					if e == nil {
						a.redirect(w, r, s.URL("tags"))
						return nil
					}
					ctx["error_message"] = "tag already exists"
				}
			}
			ctx["tag"] = t
			return a.render(w, r, u, "reinhardt/rename_tag.html", ctx, 200)
		}
	}
	switch path {
	case "accounts/login/":
		return a.login(w, r, u)
	case "accounts/logout/":
		if e := postOnly(r); e != nil {
			return e
		}
		if c, e := r.Cookie("sessionid"); e == nil {
			if _, e = s.DB.Exec("DELETE FROM go_session WHERE token=?", c.Value); e != nil {
				return e
			}
		}
		a.cookie(w, r, "sessionid", "", true, -1)
		a.redirect(w, r, s.URL("login"))
		return nil
	case "invite/":
		if e := safeOnly(r); e != nil {
			return e
		}
		user, e := s.One(`SELECT u.id FROM auth_user u JOIN reinhardt_preferences p ON p.owner_id=u.id WHERE p.access_token=? AND u.is_active=1`, q)
		if e != nil {
			return e
		}
		sess := token()
		if _, e = s.DB.Exec("INSERT INTO go_session(token,owner_id,expires) VALUES(?,?,?)", sess, user.N("id"), time.Now().Unix()+31556952); e != nil {
			return e
		}
		if _, e = s.DB.Exec("UPDATE auth_user SET last_login=? WHERE id=?", now(), user.N("id")); e != nil {
			return e
		}
		a.cookie(w, r, "sessionid", sess, true, 31556952)
		a.cookie(w, r, "csrftoken", token(), false, 31556952)
		next := r.URL.Query().Get("next")
		if !strings.HasPrefix(next, s.C.Path("")) || strings.HasPrefix(next, "//") {
			next = s.URL("index")
		}
		a.redirect(w, r, next)
		return nil
	case "":
		if e := safeOnly(r); e != nil {
			return e
		}
		tracks, e := a.latestAdditions()
		if e != nil {
			return e
		}
		ctx["music_files"] = tracks
		template = "index"
	case "artists/":
		if e := safeOnly(r); e != nil {
			return e
		}
		letters, e := s.Query("SELECT DISTINCT upper(substr(artist,1,1)) letter FROM reinhardt_musicfile ORDER BY letter")
		if e != nil {
			return e
		}
		ls := []string{}
		for _, v := range letters {
			ls = append(ls, v.S("letter"))
		}
		ctx["letters"] = ls
		template = "artists"
		if q != "" {
			artists, e := s.Query("SELECT DISTINCT artist FROM reinhardt_musicfile WHERE artist LIKE ? AND missing=0 ORDER BY lower(artist)", q+"%")
			if e != nil {
				return e
			}
			ctx["artists"] = artists
			ctx["letter"] = q
			template = "_artists_by_letter"
		}
	case "genres/":
		if e := safeOnly(r); e != nil {
			return e
		}
		genres, e := s.Query("SELECT DISTINCT genre FROM reinhardt_musicfile WHERE missing=0 AND genre!='' ORDER BY lower(genre)")
		if e != nil {
			return e
		}
		for _, v := range genres {
			rs := []rune(strings.ToLower(strings.TrimSpace(v.S("genre"))))
			if len(rs) > 0 {
				v["first_letter"] = string(rs[0])
			}
		}
		ctx["music_genres"] = genres
		template = "genres"
	case "artist/", "album/", "genre/":
		if e := safeOnly(r); e != nil {
			return e
		}
		field := strings.TrimSuffix(path, "/")
		order := "m.artist,m.album,m.track_number"
		if field == "album" {
			order = "m.track_number"
		}
		tracks, e := s.Tracks("WHERE m."+field+"=? AND m.missing=0 ORDER BY "+order, q)
		if e != nil {
			return e
		}
		ctx[field] = q
		ctx["music_files"] = tracks
		template = field
	case "search/":
		if e := safeOnly(r); e != nil {
			return e
		}
		sq := strings.TrimSpace(r.URL.Query().Get("sq"))
		tracks, e := s.Search(sq)
		if e != nil {
			return e
		}
		ctx["sq"] = sq
		ctx["q"] = sq
		ctx["music_files"] = tracks
		count := len(tracks)
		if sq != "" {
			total, e := s.One("SELECT COUNT(*) n FROM reinhardt_musicfile m JOIN music_index f ON CAST(f.id AS INTEGER)=m.id WHERE music_index MATCH ? AND m.missing=0", ftsEscape(sq))
			if e != nil {
				return e
			}
			count = int(total.N("n"))
		}
		ctx["total_count"] = count
		found := count > 0
		for _, field := range []string{"artist", "album", "genre"} {
			values := []string{}
			if sq != "" {
				rows, e := s.Query("SELECT DISTINCT m."+field+" value FROM reinhardt_musicfile m JOIN music_index f ON CAST(f.id AS INTEGER)=m.id WHERE f."+field+" MATCH ? ORDER BY m."+field, ftsEscape(sq))
				if e != nil {
					return e
				}
				for _, v := range rows {
					values = append(values, v.S("value"))
				}
			}
			ctx["found_"+field+"s"] = values
			found = found || len(values) > 0
		}
		ctx["has_results"] = found
		template = "search"
	case "api/music_files/search/":
		return a.searchAPI(w, r)
	case "dj/":
		if e := safeOnly(r); e != nil {
			return e
		}
		template = "music_file_search_page"
	case "tags/":
		if r.Method == "POST" {
			action := r.FormValue("action")
			if action == "add_tag" {
				if _, e := s.AddTag(owner, r.FormValue("tag_label")); e != nil {
					return bad(e.Error())
				}
			} else {
				t, e := s.Tag(r.FormValue("tag_pk"), owner)
				if e != nil {
					return e
				}
				if t.S("label") == "queue" || t.S("label") == "favorites" {
					return forbidden()
				}
				switch action {
				case "remove_tag":
					_, e = s.DB.Exec("DELETE FROM reinhardt_musicfiletag WHERE tag_id=?", t.N("id"))
					if e == nil {
						_, e = s.DB.Exec("DELETE FROM reinhardt_tag WHERE id=?", t.N("id"))
					}
				case "share_tag", "unshare_tag":
					_, e = s.DB.Exec("UPDATE reinhardt_tag SET is_shared=?,modified_at=? WHERE id=?", action == "share_tag", now(), t.N("id"))
				default:
					return bad("Invalid action")
				}
				if e != nil {
					return e
				}
			}
		}
		tags, e := s.Tags(owner, false)
		if e != nil {
			return e
		}
		for _, t := range tags {
			t["share_url"] = a.absolute(r, t.S("share_url"))
		}
		ctx["tags"] = tags
		template = "tags"
	case "tag/", "favorites/":
		if e := safeOnly(r); e != nil {
			return e
		}
		var t Row
		var e error
		if path == "favorites/" {
			id, err := s.AddTag(owner, "favorites")
			if err != nil {
				return err
			}
			t, e = s.Tag(id, owner)
		} else {
			t, e = s.Tag(q, owner)
		}
		if e != nil {
			return e
		}
		tracks, e := s.TagTracks(t.N("id"))
		if e != nil {
			return e
		}
		ctx["music_files"] = tracks
		ctx["tag"] = t
		if t.B("is_shared") {
			ctx["share_url"] = a.absolute(r, t.S("share_url"))
		}
		template = strings.TrimSuffix(path, "/")
	case "queue/", "clear_queue/":
		if path == "clear_queue/" {
			if e := postOnly(r); e != nil {
				return e
			}
			_, e := s.DB.Exec("DELETE FROM reinhardt_musicfiletag WHERE tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=? AND label='queue')", owner)
			if e != nil {
				return e
			}
		}
		queue, e := s.Queue(owner)
		if e != nil {
			return e
		}
		id, e := s.AddTag(owner, "queue")
		if e != nil {
			return e
		}
		tag, e := s.Tag(id, owner)
		if e != nil {
			return e
		}
		total := int64(0)
		for _, i := range queue {
			total += i["music_file"].(Row).N("duration")
		}
		tag["duration"] = total
		ctx["queue"] = queue
		ctx["music_tag"] = tag
		p, e := s.Preferences(owner)
		if e != nil {
			return e
		}
		ctx["now_playing"] = p["current_position"]
		template = "queue"
	case "preferences/":
		if r.Method == "POST" {
			_, e := s.Preferences(owner)
			if e != nil {
				return e
			}
			_, e = s.DB.Exec("UPDATE reinhardt_preferences SET autofeed_queue=?,autofeed_similar=?,show_oscilloscope=? WHERE owner_id=?", r.FormValue("autofeed_queue") == "on", r.FormValue("autofeed_similar") == "on", r.FormValue("show_oscilloscope") == "on", owner)
			if e != nil {
				return e
			}
		}
		template = "preferences"
	case "activate_autofeed/", "deactivate_autofeed/":
		if e := postOnly(r); e != nil {
			return e
		}
		if _, e := s.Preferences(owner); e != nil {
			return e
		}
		active := path == "activate_autofeed/"
		if _, e := s.DB.Exec("UPDATE reinhardt_preferences SET autofeed_queue=? WHERE owner_id=?", active, owner); e != nil {
			return e
		}
		message := "autofeed deactivated"
		if active {
			message = "autofeed activated"
		}
		ctx["action"] = message
		template = "_tag_message"
	case "next_song/", "api/dj/next_song/":
		if e := postOnly(r); e != nil {
			return e
		}
		track, pos, e := a.nextSong(r, u)
		if e != nil {
			return e
		}
		if path == "api/dj/next_song/" {
			var data any
			if track != nil {
				data = serializeTrack(track)
			}
			return jsonResponse(w, Row{"music_file": data, "current_position": pos})
		}
		ctx["music_file"] = track
		ctx["current_position"] = pos
		ctx["autoplay"] = true
		template = "_now_playing"
	case "reorder_queue/":
		return a.reorder(w, r, u)
	case "remove_from_queue/":
		if e := postOnly(r); e != nil {
			return e
		}
		res, e := s.DB.Exec("DELETE FROM reinhardt_musicfiletag WHERE music_file_id=? AND tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=? AND label='queue')", r.FormValue("remove_item"), owner)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return sql.ErrNoRows
		}
		_, e = w.Write([]byte("None"))
		return e
	case "add_to_tag/", "remove_from_tag/", "toggle_tag/":
		return a.tagAction(w, r, u, path)
	case "artist/add_to_tag/", "artist/add_to_favorites/", "artist/add_to_queue/", "search/add_to_tag/", "search/add_to_favorites/", "search/add_to_queue/", "queue/add_to_tag/", "queue/add_to_favorites/":
		return a.bulk(w, r, u, path)
	case "music_file_tagger/", "music_file_dropdown/":
		if e := safeOnly(r); e != nil {
			return e
		}
		id, e := strconv.ParseInt(q, 10, 64)
		if e != nil {
			return bad("Invalid music file ID.")
		}
		track, e := s.Track(id)
		if e != nil {
			return e
		}
		ctx["music_file"] = track
		if path == "music_file_tagger/" {
			tags, e := s.Tags(owner, false)
			if e != nil {
				return e
			}
			ctx["tags"] = tags
			ctx["next_url"] = r.URL.Query().Get("next")
			assigned, e := s.Query(`SELECT t.id FROM reinhardt_tag t JOIN reinhardt_musicfiletag a ON a.tag_id=t.id WHERE t.owner_id=? AND a.music_file_id=? AND t.label NOT IN ('queue','favorites')`, owner, id)
			if e != nil {
				return e
			}
			ctx["assigned_tags_pks"] = ids(assigned)
			template = "music_file_tagger"
		} else {
			fav, e := s.One(`SELECT COUNT(*) n FROM reinhardt_musicfiletag a JOIN reinhardt_tag t ON t.id=a.tag_id WHERE t.owner_id=? AND t.label='favorites' AND a.music_file_id=?`, owner, id)
			if e != nil {
				return e
			}
			ctx["is_favorite"] = fav.N("n") > 0
			ctx["playing"] = r.URL.Query().Get("playing")
			ctx["context"] = r.URL.Query().Get("context")
			if id := r.URL.Query().Get("tag"); id != "" {
				if _, e := strconv.Atoi(id); e != nil {
					return bad("Invalid tag ID.")
				}
				t, e := s.Tag(id, owner)
				if e != nil {
					return e
				}
				ctx["remove_from_tag"] = t["label"]
			}
			template = "_track_dropdown_menu_items"
		}
	case "friends/":
		friends, e := s.Query("SELECT * FROM auth_user WHERE id!=? ORDER BY username", owner)
		if e != nil {
			return e
		}
		visible := []Row{}
		for _, f := range friends {
			tags, e := s.Tags(f.N("id"), true)
			if e != nil {
				return e
			}
			ts := []Row{}
			for _, t := range tags {
				if t.N("track_count") > 0 && (u.B("is_superuser") || t.S("label") != "queue") {
					ts = append(ts, t)
				}
			}
			if len(ts) > 0 {
				visible = append(visible, Row{"user": f, "tags": ts})
			}
		}
		ctx["friends"] = visible
		template = "friends"
	case "friend_tag/":
		t, e := s.One("SELECT * FROM reinhardt_tag WHERE id=?", r.URL.Query().Get("tag"))
		if e != nil {
			return e
		}
		friend, e := s.One("SELECT * FROM auth_user WHERE id=?", t.N("owner_id"))
		if e != nil {
			return e
		}
		tracks, e := s.TagTracks(t.N("id"))
		if e != nil {
			return e
		}
		assocs := []Row{}
		for _, m := range tracks {
			assocs = append(assocs, Row{"music_file": m})
		}
		t["musicfiletag_set"] = assocs
		ctx["friend_tag"] = t
		ctx["friend"] = friend
		template = "friend_tag"
	case "queue_as_mix/":
		if e := safeOnly(r); e != nil {
			return e
		}
		if q == "" {
			t, e := s.One("SELECT id FROM reinhardt_tag WHERE owner_id=? AND label='queue'", owner)
			if e != nil {
				return e
			}
			q = t.S("id")
		}
		t, e := s.Tag(q, owner)
		if e != nil {
			return e
		}
		return a.archive(w, r, t)
	case "preferences/database/":
		if e := safeOnly(r); e != nil {
			return e
		}
		if !u.B("is_superuser") {
			return forbidden()
		}
		return a.databaseBackup(w, r)
	case "similar_artists/":
		if e := safeOnly(r); e != nil {
			return e
		}
		ctx["similar_artists"] = a.similarArtists(r.Context(), q)
		template = "_similar_artists"
	case "user_playlists_contents/":
		if e := safeOnly(r); e != nil {
			return e
		}
		return a.playlists(w, r, q)
	case "upload/":
		return a.upload(w, r, u)
	case "ytrip/", "ytrip/metadata/", "ytrip/failures/":
		return a.youtubeView(w, r, u, path)
	default:
		if len(parts) == 4 && parts[0] == "ytrip" && parts[1] == "failures" && parts[3] == "retry" {
			return a.retryYouTube(w, r, u, parts[2])
		}
		return sql.ErrNoRows
	}
	return a.render(w, r, u, "reinhardt/"+template+".html", ctx, 200)
}

func (a *App) shared(w http.ResponseWriter, r *http.Request, u Row, parts []string) error {
	if e := safeOnly(r); e != nil {
		return e
	}
	if len(parts) < 3 {
		return sql.ErrNoRows
	}
	tag, e := a.S.One("SELECT * FROM reinhardt_tag WHERE id=? AND is_shared=1", parts[1])
	if e != nil {
		return e
	}
	a.S.decorateTag(tag)
	if subtle.ConstantTimeCompare([]byte(parts[2]), []byte(tag.S("share_hash"))) != 1 {
		return sql.ErrNoRows
	}
	tracks, e := a.S.TagTracks(tag.N("id"))
	if e != nil {
		return e
	}
	total := int64(0)
	for _, m := range tracks {
		total += m.N("duration")
	}
	if len(parts) > 3 {
		switch parts[3] {
		case "track":
			if len(parts) != 5 {
				return sql.ErrNoRows
			}
			for _, m := range tracks {
				if m.S("id") == parts[4] {
					return serveLocal(w, r, m, false)
				}
			}
			return sql.ErrNoRows
		case "download":
			if total >= int64(a.S.C.Int("SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS")) {
				return sql.ErrNoRows
			}
			return a.archive(w, r, tag)
		default:
			return sql.ErrNoRows
		}
	}
	return a.render(w, r, u, "reinhardt/shared_tag.html", TemplateContext{"tag": tag, "share_hash": parts[2], "music_files": tracks, "total_duration": total, "can_download_all": total < int64(a.S.C.Int("SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS"))}, 200)
}

func (a *App) tagAction(w http.ResponseWriter, r *http.Request, u Row, path string) error {
	if e := postOnly(r); e != nil {
		return e
	}
	owner := u.N("id")
	name := label(r.FormValue("tag"))
	q := r.FormValue("q")
	kind := r.FormValue("add_type")
	action := "added"
	object := kind
	var tracks []Row
	var e error
	if path == "toggle_tag/" {
		q = r.FormValue("music_file_pk")
		t, err := a.S.Tag(r.FormValue("tag_pk"), owner)
		if err != nil {
			return err
		}
		if t.S("label") == "queue" || t.S("label") == "favorites" {
			return forbidden()
		}
		name = t.S("label")
		kind = "music_file"
		if r.FormValue("has_tag") != "on" {
			action = "removed"
		}
	}
	if path == "remove_from_tag/" {
		kind = r.FormValue("remove_type")
		action = "removed"
	}
	switch kind {
	case "music_file":
		t, err := a.S.Track(q)
		if err != nil {
			return err
		}
		tracks = []Row{t}
		object = "track"
	case "artist", "genre", "album":
		tracks, e = a.S.Tracks("WHERE m."+kind+"=? AND m.missing=0 ORDER BY m.artist,m.album,m.track_number", q)
	case "tag", "favorites":
		source := q
		if kind == "favorites" {
			source = "favorites"
		}
		t, err := a.S.One("SELECT * FROM reinhardt_tag WHERE owner_id=? AND label=?", owner, source)
		if err != nil {
			return err
		}
		tracks, e = a.S.TagTracks(t.N("id"))
	case "friend_tag":
		tracks, e = a.S.TagTracks(q)
	default:
		return bad("Invalid action type")
	}
	if e != nil {
		return e
	}
	if action == "added" {
		if kind == "music_file" {
			_, e = a.S.AddTrack(owner, name, tracks[0].N("id"))
		} else {
			_, e = a.S.AddTracks(owner, name, ids(tracks))
		}
		if e != nil {
			return e
		}
	} else {
		tx, err := a.S.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, m := range tracks {
			if _, e = tx.Exec("DELETE FROM reinhardt_musicfiletag WHERE music_file_id=? AND tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=? AND label=?)", m.N("id"), owner, name); e != nil {
				return e
			}
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	return a.render(w, r, u, "reinhardt/_tag_message.html", TemplateContext{"object_label": object, "tag_label": name, "action": action, "object_pk": q}, 200)
}

func (a *App) bulk(w http.ResponseWriter, r *http.Request, u Row, path string) error {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	source, action := parts[0], parts[1]
	ctx := TemplateContext{}
	value := r.FormValue("artist")
	if source == "search" {
		value = r.FormValue("sq")
	}
	ctx["artist"] = value
	ctx["q"] = value
	tags, e := a.S.Tags(u.N("id"), false)
	if e != nil {
		return e
	}
	ctx["tags"] = tags
	var target string
	if action != "add_to_tag" {
		if e := postOnly(r); e != nil {
			return e
		}
		target = strings.TrimPrefix(action, "add_to_")
	}
	if r.Method == "POST" {
		if action == "add_to_tag" {
			choice := r.FormValue("tag_choice")
			if choice == "new" || (choice == "" && r.FormValue("tag_pk") == "") {
				target = label(r.FormValue("new_tag"))
				if target == "" {
					return bad("Invalid tag name.")
				}
			} else {
				t, e := a.S.Tag(r.FormValue("tag_pk"), u.N("id"))
				if e != nil {
					return e
				}
				if t.S("label") == "queue" || t.S("label") == "favorites" {
					return forbidden()
				}
				target = t.S("label")
			}
		}
		var tracks []Row
		switch source {
		case "artist":
			tracks, e = a.S.Tracks("WHERE m.artist=? AND m.missing=0 ORDER BY m.album,m.track_number", value)
		case "search":
			tracks, e = a.S.Search(value)
		case "queue":
			queue, err := a.S.Queue(u.N("id"))
			e = err
			for _, i := range queue {
				tracks = append(tracks, i["music_file"].(Row))
			}
		}
		if e != nil {
			return e
		}
		count, e := a.S.AddTracks(u.N("id"), target, ids(tracks))
		if e != nil {
			return e
		}
		ctx["added_count"] = count
		t, e := a.S.One("SELECT * FROM reinhardt_tag WHERE owner_id=? AND label=?", u.N("id"), target)
		if e != nil {
			return e
		}
		ctx["selected_tag"] = t
	}
	if action == "add_to_tag" {
		return a.render(w, r, u, "reinhardt/"+source+"_add_to_tag.html", ctx, 200)
	}
	object := source
	if source == "search" {
		object = "search results"
	}
	return a.render(w, r, u, "reinhardt/_tag_message.html", TemplateContext{"object_label": object, "tag_label": target, "action": "added"}, 200)
}

func (a *App) reorder(w http.ResponseWriter, r *http.Request, u Row) error {
	if e := postOnly(r); e != nil {
		return e
	}
	t, e := a.S.Tag(r.FormValue("list_pk"), u.N("id"))
	if e != nil {
		return e
	}
	var order []struct {
		Item int64 `json:"item"`
		Rank int64 `json:"rank"`
	}
	if e = json.Unmarshal([]byte(r.FormValue("new_order")), &order); e != nil {
		return bad("Invalid order")
	}
	tx, e := a.S.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	seen := map[int64]bool{}
	for _, item := range order {
		if seen[item.Item] || item.Rank < 0 {
			return bad("Invalid order")
		}
		seen[item.Item] = true
		res, e := tx.Exec("UPDATE reinhardt_musicfiletag SET rank=? WHERE music_file_id=? AND tag_id=?", item.Rank, item.Item, t.N("id"))
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return bad("Invalid queue item")
		}
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	_, e = w.Write([]byte("ok"))
	return e
}

func (a *App) nextSong(r *http.Request, u Row) (Row, any, error) {
	s := a.S
	owner := u.N("id")
	prefs, e := s.Preferences(owner)
	if e != nil {
		return nil, 0, e
	}
	current := int64(0)
	var currentItem Row
	// The persisted position is an association id, while advancement compares
	// ranks. Reordering the queue changes rank without changing the saved position.
	if position := prefs.N("current_position"); position != 0 {
		currentItem, e = s.queueItem(owner, "a.id=?", position)
		if e != nil && e != sql.ErrNoRows {
			return nil, 0, e
		}
		if currentItem != nil {
			current = currentItem.N("rank")
		}
	}
	var chosen Row
	var track Row
	if item := r.FormValue("list_item"); item != "" {
		// list_item comes from the player's track controls, so it identifies a
		// music file, not the association id stored in preferences.
		if id, err := strconv.ParseInt(item, 10, 64); err == nil && strconv.FormatInt(id, 10) == item {
			chosen, e = s.queueItem(owner, "a.music_file_id=?", id)
			if e != nil && e != sql.ErrNoRows {
				return nil, 0, e
			}
		}
	}
	if chosen == nil {
		if next := r.FormValue("next_track"); next != "" {
			track, e = s.Track(next)
			if e != nil {
				return nil, 0, e
			}
			chosen, e = s.AddTrack(owner, "queue", track.N("id"))
			if e != nil {
				return nil, 0, e
			}
		} else {
			chosen, e = s.queueItem(owner, "a.rank>?", current)
			if e != nil && e != sql.ErrNoRows {
				return nil, 0, e
			}
		}
	}
	if chosen == nil && prefs.B("autofeed_queue") {
		// Keep Last.fm behind both the preference and queue-exhaustion checks.
		// Normal advancement and disabled similarity must never contact Last.fm.
		if prefs.B("autofeed_similar") && currentItem != nil {
			last, err := s.Track(currentItem["music_file_id"])
			if err != nil {
				return nil, 0, err
			}
			similar := a.similarArtists(r.Context(), last.S("artist"))
			for _, name := range similar {
				track, e = a.randomAutofeedTrack(r.Context(), owner, &name)
				if e != nil {
					return nil, 0, e
				}
				if track != nil {
					break
				}
			}
		}
		if track == nil {
			track, e = a.sampleAutofeedTrack(r.Context(), autofeedKey{owner: owner, excludeQueuedArtists: prefs.B("autofeed_similar")})
			if e != nil {
				return nil, 0, e
			}
		}
		if track == nil && prefs.B("autofeed_similar") {
			// Once every eligible artist is represented, keep autofeed playing
			// with a random nonmissing, nonblacklisted track from any artist.
			track, e = a.randomAutofeedTrack(r.Context(), owner, nil)
			if e != nil {
				return nil, 0, e
			}
		}
		if track != nil {
			chosen, e = s.AddTrack(owner, "queue", track.N("id"))
			if e != nil {
				return nil, 0, e
			}
		}
	}
	position := int64(0)
	var rank any = ""
	if chosen != nil {
		position = chosen.N("id")
		if track == nil {
			track, e = s.Track(chosen["music_file_id"])
			if e != nil {
				return nil, 0, e
			}
		}
		rank = chosen["rank"]
	}
	_, e = s.DB.Exec("UPDATE reinhardt_preferences SET current_position=? WHERE owner_id=?", position, owner)
	return track, rank, e
}

func serializeTrack(t Row) Row {
	r := Row{}
	for _, f := range []string{"id", "relative_path", "artist", "title", "album", "genre", "year", "comment", "duration", "track_number", "created_at", "modified_at", "hash", "download_url"} {
		r[f] = t[f]
	}
	r["music_directory"] = t["music_directory_id"]
	r["missing"] = t.B("missing")
	return r
}

func (a *App) searchAPI(w http.ResponseWriter, r *http.Request) error {
	if e := safeOnly(r); e != nil {
		return e
	}
	params := r.URL.Query()
	limit, offset := 1000, 0
	var e error
	if v := params.Get("limit"); params.Has("limit") {
		limit, e = strconv.Atoi(v)
		if e != nil {
			return bad("`limit` and `offset` must be integers.")
		}
	}
	if v := params.Get("offset"); params.Has("offset") {
		offset, e = strconv.Atoi(v)
		if e != nil {
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
	for _, f := range []string{"id", "relative_path", "artist", "title", "album", "genre", "year", "comment", "duration", "track_number", "created_at", "modified_at", "missing", "hash", "music_directory_id"} {
		fields[f] = f
	}
	fields["pk"] = "id"
	fields["music_directory"] = "music_directory_id"
	clauses := []string{}
	args := []any{}
	order := "m.id"
	join := ""
	if q := strings.TrimSpace(params.Get("sq")); q != "" {
		join = " JOIN music_index f ON CAST(f.id AS INTEGER)=m.id"
		clauses = append(clauses, "music_index MATCH ?")
		args = append(args, ftsEscape(q))
		order = "f.rank"
	}
	for k, vs := range params {
		if k == "sq" || k == "limit" || k == "offset" || k == "order_by" {
			continue
		}
		field, ok := fields[k]
		if !ok {
			return bad("Unknown query parameter \"" + k + "\".")
		}
		value := vs[len(vs)-1]
		var parsed any = value
		switch field {
		case "id", "music_directory_id", "duration", "track_number":
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return bad("Invalid integer value")
			}
			parsed = n
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
			t, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return bad("Invalid ISO-8601 datetime value")
			}
			parsed = t.UTC().Format("2006-01-02 15:04:05.000000")
		}
		clauses = append(clauses, "m."+field+"=?")
		args = append(args, parsed)
	}
	if !params.Has("missing") {
		clauses = append(clauses, "m.missing=0")
	}
	if v := strings.TrimSpace(params.Get("order_by")); v != "" {
		if v != "artist" && v != "album" && v != "genre" && v != "title" {
			return bad("Invalid order_by")
		}
		order = "m." + v + ",m.id"
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	count, e := a.S.One("SELECT COUNT(*) n FROM reinhardt_musicfile m"+join+where, args...)
	if e != nil {
		return e
	}
	args = append(args, limit, offset)
	tracks, e := a.S.Tracks(join+where+" ORDER BY "+order+" LIMIT ? OFFSET ?", args...)
	if e != nil {
		return e
	}
	result := []Row{}
	for _, t := range tracks {
		result = append(result, serializeTrack(t))
	}
	return jsonResponse(w, Row{"count": count.N("n"), "limit": limit, "offset": offset, "results": result})
}

func (a *App) playlists(w http.ResponseWriter, r *http.Request, username string) error {
	u, e := a.S.One("SELECT id FROM auth_user WHERE username=?", username)
	if e == sql.ErrNoRows {
		return jsonResponse(w, []Row{})
	}
	if e != nil {
		return e
	}
	tags, e := a.S.Tags(u.N("id"), true)
	if e != nil {
		return e
	}
	result := []Row{}
	for _, t := range tags {
		if t.S("label") == "queue" || t.S("label") == "blacklist" || t.S("label") == "corrompus" {
			continue
		}
		tracks, e := a.S.TagTracks(t.N("id"))
		if e != nil {
			return e
		}
		items := []Row{}
		for _, m := range tracks {
			items = append(items, Row{"artist": m["artist"], "title": m["title"], "album": m["album"], "url": m["download_url"], "file_name": slug(m.S("artist")) + "_-_" + slug(m.S("title")) + fileExtension(m)})
		}
		result = append(result, Row{"label": t["label"], "items": items})
	}
	return jsonResponse(w, result)
}

func inviteQuery(token, next string) string {
	values := url.Values{"q": {token}}
	if next != "" {
		values.Set("next", next)
	}
	return values.Encode()
}
