package reinhardt

import (
	"bytes"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Session expiry and authentication cookies share the same lifetime in seconds.
const sessionLifetimeSeconds = 31556952

type App struct {
	Store        *Store
	Templates    Templates
	Translations *Translations
	Client       *http.Client
	AudioClient  *http.Client
	RadioClient  *http.Client
	coverMu      sync.Mutex
	coverCache   map[string]coverResult
	coverFlights map[string]*coverFlight
	suggestions  suggestionCache
	home         homeCache
	autofeed     autofeedCache
	similar      similarLookupCache
	artistNames  libraryArtistCache
}
type httpError struct {
	Code    int
	Message string
}

func (httpErr httpError) Error() string { return httpErr.Message }
func bad(message string) error          { return httpError{400, message} }
func forbidden() error                  { return httpError{403, "Permission denied"} }
func newApp(store *Store) (*App, error) {
	templates, err := newTemplates()
	if err != nil {
		return nil, err
	}
	translations, err := newTranslations(assets)
	if err != nil {
		return nil, err
	}
	audioTransport := http.DefaultTransport.(*http.Transport).Clone()
	// Client.Timeout covers body reads too. Use only a header deadline for audio
	// so slow downloads and long tracks can outlive the short API timeout.
	audioTransport.ResponseHeaderTimeout = 35 * time.Second
	return &App{Store: store, Templates: templates, Translations: translations, Client: &http.Client{Timeout: 35 * time.Second}, AudioClient: &http.Client{Transport: audioTransport}, RadioClient: newRadioClient(), coverCache: map[string]coverResult{}}, nil
}
func (app *App) redirect(responseWriter http.ResponseWriter, request *http.Request, path string) {
	// Unpoly uses this header to update its location during fragment navigation;
	// the normal redirect also serves full-page browser requests.
	responseWriter.Header().Set("X-Up-Location", path)
	http.Redirect(responseWriter, request, path, http.StatusFound)
}
func (app *App) absolute(request *http.Request, path string) string {
	if accessURLPrefix := app.Store.Config.AccessURLPrefix; accessURLPrefix != "" {
		return strings.TrimRight(accessURLPrefix, "/") + path
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + request.Host + path
}
func (app *App) sessionUser(request *http.Request) *User {
	sessionCookie, err := request.Cookie("sessionid")
	if err != nil {
		return &User{}
	}
	user, err := app.Store.user(request.Context(), `SELECT u.*,p.language_code,r.station_id radio_station_id FROM auth_user u JOIN go_session s ON s.owner_id=u.id LEFT JOIN reinhardt_preferences p ON p.owner_id=u.id LEFT JOIN go_radio_playback r ON r.owner_id=u.id WHERE s.token=? AND s.expires>? AND u.is_active=1`, sessionCookie.Value, time.Now().Unix())
	if err != nil {
		return &User{}
	}
	user.IsAuthenticated = true
	return user
}
func (app *App) cookie(responseWriter http.ResponseWriter, request *http.Request, name, value string, httpOnly bool, age int) {
	// SameSite=Lax limits when browsers send cookies on cross-site requests.
	// POST requests still need the explicit CSRF checks below. COOKIE_SECURE
	// also supports HTTPS deployments where TLS terminates at a reverse proxy.
	http.SetCookie(responseWriter, &http.Cookie{Name: name, Value: value, Path: app.Store.Config.Path(""), HttpOnly: httpOnly, Secure: request.TLS != nil || app.Store.Config.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: age})
}

// csrf supplies the token for a double-submit cookie check: the browser sends
// the cookie automatically, and forms or JavaScript must submit the same value
// separately. A foreign site cannot normally read that value through the
// browser's same-origin policy. The token is independent of the login session.
func (app *App) csrf(responseWriter http.ResponseWriter, request *http.Request) string {
	csrfCookie, err := request.Cookie("csrftoken")
	if err == nil && len(csrfCookie.Value) == 64 {
		// Reuse the cookie so forms in other tabs keep a matching token.
		return csrfCookie.Value
	}
	// token() encodes 32 cryptographically random bytes as 64 hex characters.
	// HttpOnly is false so JavaScript can read the cookie for X-CSRFToken.
	csrfToken := token()
	app.cookie(responseWriter, request, "csrftoken", csrfToken, false, sessionLifetimeSeconds)
	return csrfToken
}
func (app *App) checkCSRF(request *http.Request) error {
	// When Origin is present, require its host (including any port) to match
	// request.Host, or its full value to match a comma-separated trusted origin.
	// This check compares hosts, not schemes; absent Origin skips only this
	// check, and trusted origins still have to pass token validation below.
	if origin := request.Header.Get("Origin"); origin != "" {
		originURL, err := url.Parse(origin)
		trusted := false
		for _, trustedOrigin := range app.Store.Config.CSRFTrustedOrigins {
			if origin == trustedOrigin {
				trusted = true
			}
		}
		if err != nil || (!trusted && originURL.Host != request.Host) {
			return forbidden()
		}
	}
	csrfCookie, err := request.Cookie("csrftoken")
	if err != nil {
		return forbidden()
	}
	// JavaScript can send the header; ordinary forms use the
	// hidden field. A nonempty header takes precedence over the form value.
	submittedToken := request.Header.Get("X-CSRFToken")
	if submittedToken == "" {
		submittedToken = request.FormValue("csrfmiddlewaretoken")
	}
	// Require both copies to match; the cookie alone is insufficient because
	// browsers attach cookies automatically. Compare in constant time to avoid
	// revealing matching token prefixes through comparison timing.
	if len(csrfCookie.Value) != 64 || subtle.ConstantTimeCompare([]byte(submittedToken), []byte(csrfCookie.Value)) != 1 {
		return forbidden()
	}
	return nil
}
func (app *App) ServeHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	responseWriter.Header().Set("X-Content-Type-Options", "nosniff")
	responseWriter.Header().Set("X-Frame-Options", "DENY")
	host := request.Host
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	allowed := false
	for _, allowedHost := range app.Store.Config.AllowedHosts {
		if allowedHost == "*" || allowedHost == host || (strings.HasPrefix(allowedHost, ".") && strings.HasSuffix(host, allowedHost)) {
			allowed = true
		}
	}
	if !allowed {
		http.Error(responseWriter, "Invalid host", 400)
		return
	}
	prefix := app.Store.Config.Path("")
	if !strings.HasPrefix(request.URL.Path, prefix) {
		http.NotFound(responseWriter, request)
		return
	}
	path := strings.TrimPrefix(request.URL.Path, prefix)
	if strings.HasPrefix(path, "static/") {
		sub, _ := fs.Sub(assets, "static")
		http.StripPrefix(prefix+"static/", http.FileServer(http.FS(sub))).ServeHTTP(responseWriter, request)
		return
	}
	// The same URL can return a full layout, an Unpoly fragment, or another
	// user's state. Proxies must not reuse one of those representations for another.
	responseWriter.Header().Set("Vary", "X-Up-Target, Cookie, Accept-Encoding")
	responseWriter.Header().Set("Cache-Control", "private, no-cache")
	user := app.sessionUser(request)
	public := path == "accounts/login/" || path == "invite/" || strings.HasPrefix(path, "shared/") || strings.HasPrefix(path, "artwork/") || strings.HasPrefix(path, "artwork_modal/") || path == "similar_artists/" || path == "user_playlists_contents/"
	if !public && !user.IsAuthenticated {
		app.redirect(responseWriter, request, app.Store.URL("login")+"?next="+url.QueryEscape(request.URL.RequestURI()))
		return
	}
	if request.Method != "GET" && request.Method != "HEAD" && request.Method != "POST" {
		responseWriter.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(responseWriter, "Method not allowed", 405)
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, app.Store.Config.MaxUploadBytes)
	defer func() {
		if request.MultipartForm != nil {
			request.MultipartForm.RemoveAll()
		}
	}()
	// POST is the only allowed unsafe method. Validate before dispatch, including
	// public POST endpoints such as the login-link form; GET and HEAD skip CSRF.
	if request.Method == "POST" {
		if err := app.checkCSRF(request); err != nil {
			http.Error(responseWriter, err.Error(), 403)
			return
		}
	}
	err := app.dispatch(responseWriter, request, user, path)
	if err != nil {
		code := 500
		message := "Internal server error"
		var httpErr httpError
		if errors.As(err, &httpErr) {
			code = httpErr.Code
			message = httpErr.Message
		} else if errors.Is(err, sql.ErrNoRows) {
			code = 404
			message = "Not found"
		} else {
			log.Printf("%s %s: %v", request.Method, request.URL.Path, err)
		}
		http.Error(responseWriter, message, code)
	}
}
func postOnly(request *http.Request) error {
	if request.Method != "POST" {
		return httpError{405, "POST required"}
	}
	return nil
}
func safeOnly(request *http.Request) error {
	if request.Method != "GET" && request.Method != "HEAD" {
		return httpError{405, "GET or HEAD required"}
	}
	return nil
}
func (app *App) render(responseWriter http.ResponseWriter, request *http.Request, user *User, name string, ctx TemplateContext, status int) error {
	pageTemplate, ok := app.Templates[name]
	if !ok {
		return fmt.Errorf("unknown template %q", name)
	}
	if ctx == nil {
		ctx = TemplateContext{}
	}
	// Enrich the handler's page data with request metadata and shared template
	// helpers, so pages and standalone fragments use the same URLs and language.
	ctx["request"] = Row{"user": user, "get_full_path": request.URL.RequestURI()}
	ctx["request_is_from_unpoly"] = request.Header.Get("X-Up-Target") != ""
	if name == "registration/login.html" {
		// Login needs the full document even after an expired-session fragment
		// request; otherwise the browser can retain an authenticated page shell.
		ctx["request_is_from_unpoly"] = false
	}
	// Every rendered form receives the same token as the browser's CSRF cookie,
	// including pages visited before authentication.
	ctx["csrf_token"] = app.csrf(responseWriter, request)
	ctx["url"] = app.Store.URL
	ctx["static"] = func(path string) string { return app.Store.Config.Path("static/" + path) }
	translator := app.translator(user)
	ctx["LANGUAGE_CODE"] = translator.Code
	ctx["languages"] = app.Translations.Languages
	ctx["preferred_language"] = user.LanguageCode
	ctx["translate"] = translator.Text
	ctx["translate_literal"] = translator.Text
	ctx["localize"] = translator.Message
	ctx["generated_at"] = generatedAt()
	// Keep a usable default for anonymous pages and fragments that skip loading
	// the authenticated layout's preferences below.
	ctx["preferences"] = Row{}
	ctx["translate_format"] = func(formatText string, title any) (string, error) {
		return translator.Format(formatText, map[string]any{"Title": fmt.Sprint(title)})
	}
	// Raw fetch() partials have no X-Up-Target header. Only templates using the
	// page layout need preferences, the player footer, or library suggestions.
	if user.IsAuthenticated && pageTemplate.UsesLayout {
		prefs, err := app.Store.Preferences(request.Context(), user.ID)
		if err != nil {
			return err
		}
		ctx["preferences"] = prefs
		// Ordinary Unpoly navigation preserves the existing player. Resolve its
		// source only for a full document or an explicit player synchronization.
		if request.Header.Get("X-Up-Target") == "" || ctx["sync_player"] == true {
			var station *RadioStation
			if user.RadioStationID != 0 {
				station, err = app.Store.currentRadioStation(request.Context(), user.ID)
				if err != nil {
					return err
				}
			}
			if station != nil {
				ctx["station"] = station
			} else if ctx["sync_player"] == true {
				// Switching away from radio must also restore queue playback state
				// in a fragment response, where full-page initialization is skipped.
				if err = app.queuePlaybackContext(request.Context(), user.ID, prefs, ctx); err != nil {
					return err
				}
			}
		}
		// Populate the initial document's footer and search suggestions. Fragment
		// navigation keeps those parts of the current document in place.
		if fromUnpoly, _ := ctx["request_is_from_unpoly"].(bool); !fromUnpoly {
			// Only these pages restore the saved queue track in the footer.
			nowPlayingPage := false
			for _, page := range []string{"index", "artists", "_artists_by_letter", "genres", "artist", "genre", "album", "favorites", "tag", "tags", "search", "queue", "rename_tag", "ytrip", "ytrip_failures"} {
				if name == "reinhardt/"+page+".html" {
					nowPlayingPage = true
				}
			}
			// Respect playback data supplied by the handler and give radio priority
			// over the saved queue position when choosing the initial player source.
			if _, ok := ctx["music_file"]; !ok && nowPlayingPage && ctx["station"] == nil {
				// A removed queue entry leaves the footer empty rather than failing
				// the page; only other lookup errors should abort rendering.
				ctx["current_position"] = 0
				if position := prefs.CurrentPosition; position != 0 {
					var current *QueueItem
					if queue, ok := ctx["queue"].([]*QueueItem); ok {
						// Queue pages already loaded the tracks for display.
						for _, item := range queue {
							if item.ID == position {
								current = item
								break
							}
						}
					} else {
						current, err = app.Store.queueItem(request.Context(), user.ID, "a.id=?", position)
						if err != nil && err != sql.ErrNoRows {
							return err
						}
						if current != nil {
							current.MusicFile, err = app.Store.Track(request.Context(), current.MusicFileID)
							if err != nil {
								return err
							}
						}
					}
					if current != nil {
						// Preferences store the association ID; player controls use its rank.
						ctx["music_file"] = current.MusicFile
						ctx["current_position"] = current.Rank
					}
				}
			}
			words, err := app.suggestedData(request.Context())
			if err != nil {
				return err
			}
			ctx["suggested_data"] = words
		}
	}
	// Render before committing headers, so a template failure can still produce
	// a clean HTTP error instead of an incomplete successful response.
	var renderedHTML bytes.Buffer
	if err := pageTemplate.ExecuteTemplate(&renderedHTML, name, templateDomainValue(ctx)); err != nil {
		return err
	}
	var err error
	responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Compress only when accepted and large enough to justify the gzip overhead.
	if strings.Contains(request.Header.Get("Accept-Encoding"), "gzip") && renderedHTML.Len() > 200 {
		var compressedHTML bytes.Buffer
		if err = writeGzip(&compressedHTML, renderedHTML.Bytes()); err != nil {
			return err
		}
		responseWriter.Header().Set("Content-Encoding", "gzip")
		renderedHTML = compressedHTML
	}
	responseWriter.WriteHeader(status)
	// HEAD follows the same rendering and header path as GET, but sends no body.
	if request.Method != "HEAD" {
		_, err = responseWriter.Write(renderedHTML.Bytes())
	}
	return err
}
func jsonResponse(responseWriter http.ResponseWriter, data any) error {
	responseWriter.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(responseWriter).Encode(data)
}
func formHTML(next, emailLabel string) template.HTML {
	value := ""
	if next != "" {
		value = ` value="` + html.EscapeString(next) + `"`
	}
	return template.HTML(`<div>
    <label for="id_email">` + html.EscapeString(emailLabel) + `</label>
    <input type="email" name="email" class="form-control" maxlength="320" required id="id_email">
    <input type="hidden" name="next"` + value + ` id="id_next">
</div>`)
}
