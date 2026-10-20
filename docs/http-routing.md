# HTTP routing

Application routes are registered in `internal/reinhardt/routes.go` using Go's
`http.ServeMux`. The mux is created once by `newApp`, with the configured
`URL_PREFIX`. Configure the prefix before creating the application.

Routes declare their HTTP methods. GET patterns also accept HEAD. Page patterns
ending in `/` use `{$}` to match that exact page rather than its descendants.
Media, shared playlists, radio actions, and retry URLs use named path parameters
read through `request.PathValue`.

ServeMux supplies path-cleaning and trailing-slash redirects, default 404s, and
405 responses with an `Allow` header. Extra path segments are rejected instead
of being ignored, and an encoded slash is not treated as a separator between
route segments. Generated application URLs keep their existing form.

`App.ServeHTTP` still checks hosts and URL prefixes, serves static assets, loads
the session, enforces authentication and allowed request methods, bounds request
bodies, and validates POST CSRF tokens before invoking the mux. Consequently,
anonymous private requests still redirect to login before route matching, and
invalid CSRF tokens still return 403 before route-specific method handling.

The view adapter passes the request's user to existing error-returning handlers.
View errors use the shared response mapping; SQL no-row errors remain 404s.
Radio station adapters retain superuser checks for management and user-scoped
station lookups for playback and favorites. Router method checks precede those
view checks, so unsupported methods can return 405 before a permission or
missing-station response.

Rendering, Unpoly fragment targets, persistent-player updates, download
redirects, and streaming/range handling remain in their existing handlers.
Route-specific arguments such as collection column names are bound to constants
at registration, keeping request data out of dynamically constructed SQL names.

Run `make test TEST=TestServeMux` for routing-specific coverage. The full suite
also covers page/fragment rendering, permissions, CSRF, shared playlists, audio
streaming, queue advancement, and database query counts.
