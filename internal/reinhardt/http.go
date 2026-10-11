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

type App struct {
	Store        *Store
	Templates    Templates
	French       map[string]string
	Client       *http.Client
	AudioClient  *http.Client
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
	audioTransport := http.DefaultTransport.(*http.Transport).Clone()
	// Client.Timeout covers body reads too. Use only a header deadline for audio
	// so slow downloads and long tracks can outlive the short API timeout.
	audioTransport.ResponseHeaderTimeout = 35 * time.Second
	return &App{Store: store, Templates: templates, French: translations(), Client: &http.Client{Timeout: 35 * time.Second}, AudioClient: &http.Client{Transport: audioTransport}, coverCache: map[string]coverResult{}}, nil
}
func (app *App) translate(value any) string {
	text := fmt.Sprint(value)
	if strings.HasPrefix(app.Store.Config["LANGUAGE_CODE"], "fr") {
		if translation, ok := app.French[text]; ok {
			return translation
		}
	}
	return text
}
func (app *App) redirect(responseWriter http.ResponseWriter, request *http.Request, path string) {
	// Unpoly uses this header to update its location during fragment navigation;
	// the normal redirect also serves full-page browser requests.
	responseWriter.Header().Set("X-Up-Location", path)
	http.Redirect(responseWriter, request, path, http.StatusFound)
}
func (app *App) absolute(request *http.Request, path string) string {
	if accessURLPrefix := app.Store.Config["ACCESS_URL_PREFIX"]; accessURLPrefix != "" {
		return strings.TrimRight(accessURLPrefix, "/") + path
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + request.Host + path
}
func (app *App) sessionUser(request *http.Request) Row {
	sessionCookie, err := request.Cookie("sessionid")
	if err != nil {
		return Row{"is_authenticated": false}
	}
	user, err := app.Store.One(`SELECT u.* FROM auth_user u JOIN go_session s ON s.owner_id=u.id WHERE s.token=? AND s.expires>? AND u.is_active=1`, sessionCookie.Value, time.Now().Unix())
	if err != nil {
		return Row{"is_authenticated": false}
	}
	user["is_authenticated"] = true
	return user
}
func (app *App) cookie(responseWriter http.ResponseWriter, request *http.Request, name, value string, httpOnly bool, age int) {
	http.SetCookie(responseWriter, &http.Cookie{Name: name, Value: value, Path: app.Store.Config.Path(""), HttpOnly: httpOnly, Secure: request.TLS != nil || app.Store.Config.Bool("COOKIE_SECURE"), SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (app *App) csrf(responseWriter http.ResponseWriter, request *http.Request) string {
	csrfCookie, err := request.Cookie("csrftoken")
	if err == nil && len(csrfCookie.Value) == 64 {
		return csrfCookie.Value
	}
	csrfToken := token()
	app.cookie(responseWriter, request, "csrftoken", csrfToken, false, 31556952)
	return csrfToken
}
func (app *App) checkCSRF(request *http.Request) error {
	if origin := request.Header.Get("Origin"); origin != "" {
		originURL, err := url.Parse(origin)
		trusted := false
		for _, trustedOrigin := range strings.Split(app.Store.Config["CSRF_TRUSTED_ORIGINS"], ",") {
			if origin == strings.TrimSpace(trustedOrigin) {
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
	submittedToken := request.Header.Get("X-CSRFToken")
	if submittedToken == "" {
		submittedToken = request.FormValue("csrfmiddlewaretoken")
	}
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
	for _, allowedHost := range strings.Split(app.Store.Config["ALLOWED_HOSTS"], ",") {
		allowedHost = strings.TrimSpace(allowedHost)
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
	if !public && !user.B("is_authenticated") {
		app.redirect(responseWriter, request, app.Store.URL("login")+"?next="+url.QueryEscape(request.URL.RequestURI()))
		return
	}
	if request.Method != "GET" && request.Method != "HEAD" && request.Method != "POST" {
		responseWriter.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(responseWriter, "Method not allowed", 405)
		return
	}
	request.Body = http.MaxBytesReader(responseWriter, request.Body, int64(app.Store.Config.Int("MAX_UPLOAD_BYTES")))
	defer func() {
		if request.MultipartForm != nil {
			request.MultipartForm.RemoveAll()
		}
	}()
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
func (app *App) render(responseWriter http.ResponseWriter, request *http.Request, user Row, name string, ctx TemplateContext, status int) error {
	pageTemplate, ok := app.Templates[name]
	if !ok {
		return fmt.Errorf("unknown template %q", name)
	}
	if ctx == nil {
		ctx = TemplateContext{}
	}
	ctx["request"] = Row{"user": user, "get_full_path": request.URL.RequestURI()}
	ctx["request_is_from_unpoly"] = request.Header.Get("X-Up-Target") != ""
	if name == "registration/login.html" {
		// Login needs the full document even after an expired-session fragment
		// request; otherwise the browser can retain an authenticated page shell.
		ctx["request_is_from_unpoly"] = false
	}
	ctx["csrf_token"] = app.csrf(responseWriter, request)
	ctx["url"] = app.Store.URL
	ctx["static"] = func(path string) string { return app.Store.Config.Path("static/" + path) }
	ctx["translate"] = app.translate
	// Literal catalogue strings are trusted, as they are in Django's translate
	// tag. html/template still applies contextual escaping inside JS and URLs.
	ctx["translate_literal"] = func(value any) template.HTML { return template.HTML(app.translate(value)) }
	ctx["generated_at"] = generatedAt()
	ctx["preferences"] = Row{}
	ctx["translate_format"] = func(formatText string, title any) string {
		return strings.ReplaceAll(app.translate(formatText), "%(title)s", fmt.Sprint(title))
	}
	// Raw fetch() partials have no X-Up-Target header. Only templates using the
	// page layout need preferences, the player footer, or library suggestions.
	if user.B("is_authenticated") && pageTemplate.UsesLayout {
		prefs, err := app.Store.Preferences(user.N("id"))
		if err != nil {
			return err
		}
		ctx["preferences"] = prefs
		if fromUnpoly, _ := ctx["request_is_from_unpoly"].(bool); !fromUnpoly {
			nowPlayingPage := false
			for _, page := range []string{"index", "artists", "_artists_by_letter", "genres", "artist", "genre", "album", "favorites", "tag", "tags", "search", "queue", "rename_tag", "ytrip", "ytrip_failures"} {
				if name == "reinhardt/"+page+".html" {
					nowPlayingPage = true
				}
			}
			if _, ok := ctx["music_file"]; !ok && nowPlayingPage {
				ctx["current_position"] = 0
				if position := prefs.N("current_position"); position != 0 {
					var current Row
					if queue, ok := ctx["queue"].([]Row); ok {
						// Queue pages already loaded the tracks for display.
						for _, item := range queue {
							if item.N("id") == position {
								current = item
								break
							}
						}
					} else {
						current, err = app.Store.queueItem(user.N("id"), "a.id=?", position)
						if err != nil && err != sql.ErrNoRows {
							return err
						}
						if current != nil {
							current["music_file"], err = app.Store.Track(current["music_file_id"])
							if err != nil {
								return err
							}
						}
					}
					if current != nil {
						ctx["music_file"] = current["music_file"]
						ctx["current_position"] = current["rank"]
					}
				}
			}
			words, err := app.suggestedData()
			if err != nil {
				return err
			}
			ctx["suggested_data"] = words
		}
	}
	// Render before committing headers, so a template failure can still produce
	// a clean HTTP error instead of an incomplete successful response.
	var renderedHTML bytes.Buffer
	if err := pageTemplate.ExecuteTemplate(&renderedHTML, name, ctx); err != nil {
		return err
	}
	var err error
	responseWriter.Header().Set("Content-Type", "text/html; charset=utf-8")
	if strings.Contains(request.Header.Get("Accept-Encoding"), "gzip") && renderedHTML.Len() > 200 {
		var compressedHTML bytes.Buffer
		if err = writeGzip(&compressedHTML, renderedHTML.Bytes()); err != nil {
			return err
		}
		responseWriter.Header().Set("Content-Encoding", "gzip")
		renderedHTML = compressedHTML
	}
	responseWriter.WriteHeader(status)
	if request.Method != "HEAD" {
		_, err = responseWriter.Write(renderedHTML.Bytes())
	}
	return err
}
func jsonResponse(responseWriter http.ResponseWriter, data any) error {
	responseWriter.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(responseWriter).Encode(data)
}
func formHTML(next, emailLabel, language string) template.HTML {
	value := ""
	suffix := ":"
	if strings.HasPrefix(language, "fr") {
		suffix = "\u00a0:"
	}
	if next != "" {
		value = ` value="` + html.EscapeString(next) + `"`
	}
	return template.HTML(`<div>
    <label for="id_email">` + html.EscapeString(emailLabel+suffix) + `</label>
    <input type="email" name="email" class="form-control" maxlength="320" required id="id_email">
    <input type="hidden" name="next"` + value + ` id="id_next">
</div>`)
}
