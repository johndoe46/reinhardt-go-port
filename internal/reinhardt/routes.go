package reinhardt

import (
	"net/http"
	"strings"
)

type requestUserKey struct{}

type viewHandler func(http.ResponseWriter, *http.Request, *User) error

// routes is built once with the configured URL prefix. Method patterns also
// serve HEAD for GET routes; {$} keeps page routes from matching child paths.
func (app *App) routes() *http.ServeMux {
	mux := http.NewServeMux()
	route := func(methods, path string, handler viewHandler) {
		pattern := app.Store.Config.Path(path)
		if strings.HasSuffix(pattern, "/") {
			pattern += "{$}"
		}
		for method := range strings.FieldsSeq(methods) {
			mux.HandleFunc(method+" "+pattern, app.handleView(handler))
		}
	}

	route("GET", "favicon.ico", func(writer http.ResponseWriter, request *http.Request, user *User) error {
		http.ServeFileFS(writer, request, assets, "static/reinhardt.png")
		return nil
	})
	route("GET POST", "accounts/login/", app.login)
	route("POST", "accounts/logout/", app.logout)
	route("GET", "invite/", app.invite)
	route("GET", "", app.indexPage)
	route("GET", "artists/", app.artistsPage)
	route("GET", "genres/", app.genresPage)
	for _, path := range []string{"artist/", "album/", "genre/"} {
		route("GET", path, bindView(app.collectionPage, path))
	}
	route("GET", "search/", app.searchPage)
	route("GET", "api/music_files/search/", func(writer http.ResponseWriter, request *http.Request, user *User) error {
		return app.searchAPI(writer, request)
	})
	route("GET", "dj/", func(writer http.ResponseWriter, request *http.Request, user *User) error {
		return app.render(writer, request, user, "reinhardt/music_file_search_page.html", TemplateContext{}, 200)
	})
	route("GET POST", "tags/", app.tagsPage)
	for _, path := range []string{"tag/", "favorites/"} {
		route("GET", path, bindView(app.tagPage, path))
	}
	route("GET", "queue/", bindView(app.queuePage, "queue/"))
	for _, path := range []string{"clear_queue/", "shuffle_queue/"} {
		route("POST", path, bindView(app.queuePage, path))
	}
	route("GET POST", "preferences/", app.preferencesPage)
	for _, path := range []string{"activate_autofeed/", "deactivate_autofeed/"} {
		route("POST", path, bindView(app.setAutofeed, path))
	}
	for _, path := range []string{"next_song/", "api/dj/next_song/"} {
		route("POST", path, bindView(app.nextSongPage, path))
	}
	route("POST", "reorder_queue/", app.reorder)
	route("POST", "remove_from_queue/", app.removeFromQueue)
	for _, path := range []string{"add_to_tag/", "remove_from_tag/", "toggle_tag/"} {
		route("POST", path, bindView(app.tagAction, path))
	}
	for _, path := range []string{"artist/add_to_tag/", "search/add_to_tag/", "queue/add_to_tag/"} {
		route("GET POST", path, bindView(app.bulkAddTracks, path))
	}
	for _, path := range []string{"artist/add_to_favorites/", "artist/add_to_queue/", "search/add_to_favorites/", "search/add_to_queue/", "queue/add_to_favorites/"} {
		route("POST", path, bindView(app.bulkAddTracks, path))
	}
	for _, path := range []string{"music_file_tagger/", "music_file_dropdown/"} {
		route("GET", path, bindView(app.trackMenu, path))
	}
	route("GET", "friends/", app.friendsPage)
	route("GET", "friend_tag/", app.friendTagPage)
	route("GET", "queue_as_mix/", app.queueArchive)
	route("GET", "preferences/database/", app.downloadDatabase)
	route("GET", "similar_artists/", app.similarArtistsPage)
	route("GET", "user_playlists_contents/", func(writer http.ResponseWriter, request *http.Request, user *User) error {
		return app.playlists(writer, request, request.URL.Query().Get("q"))
	})
	route("GET POST", "upload/", app.upload)
	for _, path := range []string{"ytrip/", "ytrip/failures/", "ytrip/metadata/"} {
		methods := "GET"
		if path == "ytrip/" {
			methods = "GET POST"
		}
		route(methods, path, bindView(app.youtubeView, path))
	}
	route("POST", "ytrip/failures/{id}/retry/", func(writer http.ResponseWriter, request *http.Request, user *User) error {
		return app.retryYouTube(writer, request, user, request.PathValue("id"))
	})

	route("GET", "stream/{id}/", func(writer http.ResponseWriter, request *http.Request, user *User) error {
		return app.audio(writer, request, false)
	})
	route("GET", "download/{id}/{filename}", func(writer http.ResponseWriter, request *http.Request, user *User) error {
		return app.audio(writer, request, true)
	})
	route("GET", "artwork/{id}/{size}", func(writer http.ResponseWriter, request *http.Request, user *User) error {
		return app.artwork(writer, request)
	})
	route("GET", "artwork/{version}/{hash}/{size}", func(writer http.ResponseWriter, request *http.Request, user *User) error {
		return app.artworkByHash(writer, request)
	})
	route("GET", "artwork_modal/{id}/{size}", app.artworkModal)
	route("GET POST", "track_details_modal/{id}", func(writer http.ResponseWriter, request *http.Request, user *User) error {
		return app.trackDetails(writer, request, user, request.PathValue("id"))
	})
	route("GET POST", "rename_tag/{id}", app.renameTag)
	route("GET", "shared/{id}/{hash}/", bindView(app.shared, ""))
	route("GET", "shared/{id}/{hash}/download/", bindView(app.shared, "download"))
	route("GET", "shared/{id}/{hash}/track/{track}/", bindView(app.shared, "track"))

	route("GET", "radio/", func(writer http.ResponseWriter, request *http.Request, user *User) error {
		return app.radioPage(writer, request, user, TemplateContext{})
	})
	route("POST", "radio/queue/", app.radioQueue)
	route("GET POST", "radio/new/", func(writer http.ResponseWriter, request *http.Request, user *User) error {
		if !user.IsSuperuser {
			return forbidden()
		}
		return app.radioEdit(writer, request, user, &RadioStation{})
	})
	route("GET POST", "radio/{id}/edit/", app.stationView(true, app.radioEdit))
	route("POST", "radio/{id}/delete/", app.stationView(true, app.radioDelete))
	route("POST", "radio/{id}/play/", app.stationView(false, app.radioPlay))
	route("POST", "radio/{id}/favorite/", app.stationView(false, app.radioFavorite))
	route("GET", "radio/{id}/audio/", app.stationView(false, func(writer http.ResponseWriter, request *http.Request, user *User, station *RadioStation) error {
		return app.radioAudio(writer, request, station)
	}))
	return mux
}

// Keep the application's user-aware, error-returning views behind a small
// adapter, leaving ServeMux responsible for matching and method responses.
func (app *App) handleView(handler viewHandler) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		user := request.Context().Value(requestUserKey{}).(*User)
		if err := handler(writer, request, user); err != nil {
			app.respondError(writer, request, err)
		}
	}
}

// Bind route-specific behavior at registration time rather than reparsing paths
// in the view. In particular, collection column names remain fixed by the router.
func bindView(handler func(http.ResponseWriter, *http.Request, *User, string) error, value string) viewHandler {
	return func(writer http.ResponseWriter, request *http.Request, user *User) error {
		return handler(writer, request, user, value)
	}
}

func (app *App) stationView(manage bool, handler func(http.ResponseWriter, *http.Request, *User, *RadioStation) error) viewHandler {
	return func(writer http.ResponseWriter, request *http.Request, user *User) error {
		if manage && !user.IsSuperuser {
			return forbidden()
		}
		station, err := app.Store.RadioStation(request.Context(), user.ID, request.PathValue("id"))
		if err != nil {
			return err
		}
		return handler(writer, request, user, station)
	}
}
