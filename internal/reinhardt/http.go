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
	S            *Store
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

func (e httpError) Error() string { return e.Message }
func bad(message string) error    { return httpError{400, message} }
func forbidden() error            { return httpError{403, "Permission denied"} }
func newApp(s *Store) (*App, error) {
	t, e := newTemplates()
	if e != nil {
		return nil, e
	}
	audioTransport := http.DefaultTransport.(*http.Transport).Clone()
	// Client.Timeout covers body reads too. Use only a header deadline for audio
	// so slow downloads and long tracks can outlive the short API timeout.
	audioTransport.ResponseHeaderTimeout = 35 * time.Second
	return &App{S: s, Templates: t, French: translations(), Client: &http.Client{Timeout: 35 * time.Second}, AudioClient: &http.Client{Transport: audioTransport}, coverCache: map[string]coverResult{}}, nil
}
func (a *App) translate(v any) string {
	s := fmt.Sprint(v)
	if strings.HasPrefix(a.S.C["LANGUAGE_CODE"], "fr") {
		if t, ok := a.French[s]; ok {
			return t
		}
	}
	return s
}
func (a *App) redirect(w http.ResponseWriter, r *http.Request, path string) {
	// Unpoly uses this header to update its location during fragment navigation;
	// the normal redirect also serves full-page browser requests.
	w.Header().Set("X-Up-Location", path)
	http.Redirect(w, r, path, http.StatusFound)
}
func (a *App) absolute(r *http.Request, path string) string {
	if p := a.S.C["ACCESS_URL_PREFIX"]; p != "" {
		return strings.TrimRight(p, "/") + path
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}
func (a *App) sessionUser(r *http.Request) Row {
	c, e := r.Cookie("sessionid")
	if e != nil {
		return Row{"is_authenticated": false}
	}
	u, e := a.S.One(`SELECT u.* FROM auth_user u JOIN go_session s ON s.owner_id=u.id WHERE s.token=? AND s.expires>? AND u.is_active=1`, c.Value, time.Now().Unix())
	if e != nil {
		return Row{"is_authenticated": false}
	}
	u["is_authenticated"] = true
	return u
}
func (a *App) cookie(w http.ResponseWriter, r *http.Request, name, value string, httpOnly bool, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: a.S.C.Path(""), HttpOnly: httpOnly, Secure: r.TLS != nil || a.S.C.Bool("COOKIE_SECURE"), SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (a *App) csrf(w http.ResponseWriter, r *http.Request) string {
	c, e := r.Cookie("csrftoken")
	if e == nil && len(c.Value) == 64 {
		return c.Value
	}
	v := token()
	a.cookie(w, r, "csrftoken", v, false, 31556952)
	return v
}
func (a *App) checkCSRF(r *http.Request) error {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		trusted := false
		for _, t := range strings.Split(a.S.C["CSRF_TRUSTED_ORIGINS"], ",") {
			if origin == strings.TrimSpace(t) {
				trusted = true
			}
		}
		if e != nil || (!trusted && u.Host != r.Host) {
			return forbidden()
		}
	}
	c, e := r.Cookie("csrftoken")
	if e != nil {
		return forbidden()
	}
	v := r.Header.Get("X-CSRFToken")
	if v == "" {
		v = r.FormValue("csrfmiddlewaretoken")
	}
	if len(c.Value) != 64 || subtle.ConstantTimeCompare([]byte(v), []byte(c.Value)) != 1 {
		return forbidden()
	}
	return nil
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	host := r.Host
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	allowed := false
	for _, h := range strings.Split(a.S.C["ALLOWED_HOSTS"], ",") {
		h = strings.TrimSpace(h)
		if h == "*" || h == host || (strings.HasPrefix(h, ".") && strings.HasSuffix(host, h)) {
			allowed = true
		}
	}
	if !allowed {
		http.Error(w, "Invalid host", 400)
		return
	}
	prefix := a.S.C.Path("")
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, prefix)
	if strings.HasPrefix(path, "static/") {
		sub, _ := fs.Sub(assets, "static")
		http.StripPrefix(prefix+"static/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
		return
	}
	// The same URL can return a full layout, an Unpoly fragment, or another
	// user's state. Proxies must not reuse one of those representations for another.
	w.Header().Set("Vary", "X-Up-Target, Cookie, Accept-Encoding")
	w.Header().Set("Cache-Control", "private, no-cache")
	u := a.sessionUser(r)
	public := path == "accounts/login/" || path == "invite/" || strings.HasPrefix(path, "shared/") || strings.HasPrefix(path, "artwork/") || strings.HasPrefix(path, "artwork_modal/") || path == "similar_artists/" || path == "user_playlists_contents/"
	if !public && !u.B("is_authenticated") {
		a.redirect(w, r, a.S.URL("login")+"?next="+url.QueryEscape(r.URL.RequestURI()))
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "POST" {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "Method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(a.S.C.Int("MAX_UPLOAD_BYTES")))
	defer func() {
		if r.MultipartForm != nil {
			r.MultipartForm.RemoveAll()
		}
	}()
	if r.Method == "POST" {
		if e := a.checkCSRF(r); e != nil {
			http.Error(w, e.Error(), 403)
			return
		}
	}
	err := a.dispatch(w, r, u, path)
	if err != nil {
		code := 500
		message := "Internal server error"
		var h httpError
		if errors.As(err, &h) {
			code = h.Code
			message = h.Message
		} else if errors.Is(err, sql.ErrNoRows) {
			code = 404
			message = "Not found"
		} else {
			log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		}
		http.Error(w, message, code)
	}
}
func postOnly(r *http.Request) error {
	if r.Method != "POST" {
		return httpError{405, "POST required"}
	}
	return nil
}
func safeOnly(r *http.Request) error {
	if r.Method != "GET" && r.Method != "HEAD" {
		return httpError{405, "GET or HEAD required"}
	}
	return nil
}
func (a *App) render(w http.ResponseWriter, r *http.Request, u Row, name string, ctx TemplateContext, status int) error {
	t, ok := a.Templates[name]
	if !ok {
		return fmt.Errorf("unknown template %q", name)
	}
	if ctx == nil {
		ctx = TemplateContext{}
	}
	ctx["request"] = Row{"user": u, "get_full_path": r.URL.RequestURI()}
	ctx["request_is_from_unpoly"] = r.Header.Get("X-Up-Target") != ""
	if name == "registration/login.html" {
		// Login needs the full document even after an expired-session fragment
		// request; otherwise the browser can retain an authenticated page shell.
		ctx["request_is_from_unpoly"] = false
	}
	ctx["csrf_token"] = a.csrf(w, r)
	ctx["url"] = a.S.URL
	ctx["static"] = func(path string) string { return a.S.C.Path("static/" + path) }
	ctx["translate"] = a.translate
	// Literal catalogue strings are trusted, as they are in Django's translate
	// tag. html/template still applies contextual escaping inside JS and URLs.
	ctx["translate_literal"] = func(v any) template.HTML { return template.HTML(a.translate(v)) }
	ctx["generated_at"] = generatedAt()
	ctx["preferences"] = Row{}
	ctx["translate_format"] = func(s string, title any) string {
		return strings.ReplaceAll(a.translate(s), "%(title)s", fmt.Sprint(title))
	}
	// Raw fetch() partials have no X-Up-Target header. Only templates using the
	// page layout need preferences, the player footer, or library suggestions.
	if u.B("is_authenticated") && t.UsesLayout {
		prefs, e := a.S.Preferences(u.N("id"))
		if e != nil {
			return e
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
						current, e = a.S.queueItem(u.N("id"), "a.id=?", position)
						if e != nil && e != sql.ErrNoRows {
							return e
						}
						if current != nil {
							current["music_file"], e = a.S.Track(current["music_file_id"])
							if e != nil {
								return e
							}
						}
					}
					if current != nil {
						ctx["music_file"] = current["music_file"]
						ctx["current_position"] = current["rank"]
					}
				}
			}
			words, e := a.suggestedData()
			if e != nil {
				return e
			}
			ctx["suggested_data"] = words
		}
	}
	// Render before committing headers, so a template failure can still produce
	// a clean HTTP error instead of an incomplete successful response.
	var b bytes.Buffer
	if e := t.ExecuteTemplate(&b, name, ctx); e != nil {
		return e
	}
	var e error
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") && b.Len() > 200 {
		var z bytes.Buffer
		if e = writeGzip(&z, b.Bytes()); e != nil {
			return e
		}
		w.Header().Set("Content-Encoding", "gzip")
		b = z
	}
	w.WriteHeader(status)
	if r.Method != "HEAD" {
		_, e = w.Write(b.Bytes())
	}
	return e
}
func jsonResponse(w http.ResponseWriter, data any) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(data)
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
