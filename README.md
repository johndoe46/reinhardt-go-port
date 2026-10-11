# Go Reinhardt

For installation from release downloads, first-run setup, and everyday use,
see the [Reinhardt user manual](USER_MANUAL.md).

A Go port of the Django Reinhardt music application. SQLite, templates,
translations, and every static asset are included in a single executable.
The server runs with `CGO_ENABLED=0`; its normal streaming, indexing, MP3 tag
editing, playlist, and artwork features require no Python or external programs.

The HTML templates use Go's standard `html/template` package with contextual
escaping. Layouts and partials use native Go template definitions; helpers handle
translations, grouping, URLs, and formatting. Each page has an isolated template
set so its layout overrides cannot affect another page. Static files and locale
catalogues are copied byte for byte from Django. URLs, element IDs, `data-*`
attributes, Unpoly fragment targets, the persistent player, oscilloscope, and DJ
interface are preserved. The template sources use Go syntax; rendered HTML may
differ in comments, whitespace, and equivalent HTML or JavaScript escaping.

## Source layout

- `cmd/reinhardt/`: executable entry point.
- `internal/reinhardt/`: application code and its regression tests.
- `internal/assets/`: embedded schema, HTML templates, static files, and translations.
- `scripts/`: Django comparison and benchmark tools.
- `docs/` and `examples/`: performance reports and deployment examples.
- `vendor/`: vendored Go dependencies.
- `bin/`: generated executables and coverage reports, excluded from Git.

Run the Makefile targets from the repository root. For direct Go commands, use
`go build -mod=vendor -o bin/reinhardt ./cmd/reinhardt`,
`go run -mod=vendor ./cmd/reinhardt`, or `go test -mod=vendor ./...`.
Set `CGO_ENABLED=0` for production builds, as the Makefile does automatically.
The internal asset paths keep their original names inside the binary; installed
executables need no resource directories alongside them.

## Direct Go dependencies

The direct dependencies listed in [go.mod](go.mod) serve these purposes:

| Module | Purpose in Reinhardt |
| --- | --- |
| `github.com/bogem/id3v2/v2` | Write MP3 ID3 metadata and embed cover artwork when editing or importing tracks. |
| `github.com/dhowden/tag` | Read audio metadata during indexing and extract embedded cover artwork. |
| `github.com/emersion/go-sasl` | Provide SASL PLAIN authentication for SMTP connections using configured email credentials. |
| `github.com/emersion/go-smtp` | Send login-link emails over SMTP, including STARTTLS and implicit TLS connections. |
| `github.com/joho/godotenv` | Parse `.env` files and the original application's configuration file. |
| `github.com/spf13/cobra` | Define CLI commands, flags, aliases, and help output for the server, worker, and management tools. |
| `github.com/tcolgate/mp3` | Scan MP3 frames to calculate track duration during indexing. |
| `golang.org/x/image` | Resize artwork, decode WebP images, and render fallback cover labels with the embedded Go Mono font. |
| `golang.org/x/text` | Normalize Unicode for slug generation and compose combining accents before rendering cover labels. |
| `modernc.org/sqlite` | Provide the SQLite driver for library data, sessions, settings, and durable jobs without requiring CGO. |

These libraries and their transitive dependencies are vendored in `vendor/`
and compiled into the executable. External helper programs are described below.

## Prerequisites

The compiled application runs its core library and streaming features without
helper binaries. Install the following tools for the features you use:

| Binary | Needed for |
| --- | --- |
| `yt-dlp` | YouTube metadata lookup, audio downloads and thumbnails |
| `ffmpeg` | YouTube audio conversion and thumbnail conversion; artwork embedding in non-MP3 formats |
| `ffprobe` | Media inspection during yt-dlp audio extraction; installed with FFmpeg |
| `deno` 2.3+ | JavaScript challenges required by current yt-dlp for full YouTube support |
| `go` 1.26+ and `gofmt` | Building, normal tests and formatting; both come with Go |
| GNU `make` | The documented build, install, run and test targets |
| `git` | Obtaining the source checkout; unnecessary for a copied release binary |
| `node` | Optional tests that execute the shared player's JavaScript |
| `cc` (`gcc` or `clang`) | `make test-race` only; production builds and normal tests disable CGO |
| Python 3.13+ and `uv` | Optional Django parity tests using the adjacent Django checkout |

The Go application invokes only `yt-dlp` and `ffmpeg` directly. yt-dlp invokes
`ffprobe` and its JavaScript runtime. Deno is enabled by default by yt-dlp;
Reinhardt passes `--ignore-config`, so configuring Node.js as an alternative in
yt-dlp's configuration file does not affect application calls. Install Deno for
YouTube even if Node.js is already available for tests. The EJS solver scripts
are also needed; they are a yt-dlp component, not another executable. The
installation methods below include them. See
[yt-dlp's dependencies](https://github.com/yt-dlp/yt-dlp#dependencies) and its
[JavaScript setup guide](https://github.com/yt-dlp/yt-dlp/wiki/EJS).

### Debian

For YouTube and non-MP3 artwork embedding:

```sh
sudo apt update
sudo apt install -y ffmpeg python3 pipx curl ca-certificates unzip
pipx install 'yt-dlp[default]'
pipx ensurepath
curl -fsSL https://deno.land/install.sh | DENO_INSTALL="$HOME/.local" sh
export PATH="$HOME/.local/bin:$PATH"
```

Run the user-level installs as the account that runs Reinhardt. `pipx` keeps a
current yt-dlp and its EJS component in an isolated Python environment; the
distribution's yt-dlp package may lag behind YouTube changes. Python 3 is needed
by this helper installation, not by the Go application. `curl`, CA certificates
and `unzip` support the Deno installer. Setting `DENO_INSTALL` puts Deno beside
yt-dlp in `~/.local/bin`, matching the example systemd unit's `PATH`.
The FFmpeg package supplies both `ffmpeg` and `ffprobe`. See the
[yt-dlp installation guide](https://github.com/yt-dlp/yt-dlp/wiki/Installation),
[Deno installer](https://docs.deno.com/runtime/getting_started/installation/)
and [Debian FFmpeg package](https://packages.debian.org/stable/ffmpeg).

For source builds:

```sh
sudo apt install -y make git
```

Install Go 1.26 or newer using the
[official Go instructions](https://go.dev/doc/install). Select the Linux archive
for your architecture (`amd64` for x86-64 or `arm64` for aarch64), extract it
into `/usr/local` as instructed there, then enable and verify it:

```sh
export PATH="/usr/local/go/bin:$PATH"
go version
```

Verify the version before building; the distribution's `golang-go` package may
be older than this project's minimum. Add the relevant `PATH` settings to your
shell profile to retain them in new sessions.

Optional additional test tools:

```sh
sudo apt install -y build-essential nodejs
pipx install uv
```

`build-essential` supplies the C compiler for race tests; `nodejs` supplies
`node`. `uv` prepares the Python 3.13+ Django parity environment as shown below.

### macOS (Mac OS X)

Install [Homebrew](https://brew.sh/) first, then install media helpers:

```sh
brew install yt-dlp ffmpeg deno
```

Homebrew handles yt-dlp's Python dependencies and EJS component; FFmpeg includes
`ffprobe`. See the [yt-dlp formula](https://formulae.brew.sh/formula/yt-dlp).
For source builds and optional JavaScript/parity tests:

```sh
brew install go make git
# Optional test tools:
brew install node uv
```

Homebrew's GNU Make is named `gmake`; use `gmake` in place of `make` throughout
this README, or put its `gnubin` directory on `PATH`:

```sh
export PATH="$(brew --prefix make)/libexec/gnubin:$PATH"
go version
```

See the [GNU Make formula](https://formulae.brew.sh/formula/make). For race tests,
install Apple's Command Line Tools, which provide `clang`/`cc`:

```sh
xcode-select --install
```

### Optional Django parity environment

With the original Django checkout at `../reinhardt`, prepare its environment on
either platform after installing `uv`:

```sh
uv python install 3.13
uv sync --project ../reinhardt --python 3.13 --group dev
make test-parity
```

On macOS, use `gmake test-parity` if GNU Make is not on `PATH` as `make`.
[uv installation options](https://docs.astral.sh/uv/getting-started/installation/)
include both pipx and Homebrew. These Python dependencies are only for comparing
the Go application with Django; they are unnecessary to deploy the Go binary.

### Verify and update helpers

Run these checks as the service account, with the same `PATH` used by the service:

```sh
yt-dlp --version
ffmpeg -version
ffprobe -version
deno --version
```

For Debian's pipx installation, update with `pipx upgrade yt-dlp` and
`deno upgrade`. On macOS, use `brew upgrade yt-dlp ffmpeg deno`. Services must
receive the helper directories in their own `PATH`; an interactive shell's
profile is not automatically used by systemd. Use `make doctor` for the build
and existing media/parity availability checks.

## Build and start

Install the build tools listed under [Prerequisites](#prerequisites) first.

Go dependency sources are included in `vendor/`. Build and verification targets
use `-mod=vendor`, so dependencies do not need to be downloaded. After changing
`go.mod`, refresh the vendored sources with `make vendor`.

```sh
make setup
make cli ARGS='addmusicdir /path/to/music'
make cli ARGS='indexmusic'
make cli ARGS='giveaccess example-user --email user@example.com --superuser'
make run
```

`make` or `make help` lists every target with its purpose, configurable variables,
and examples. `make setup` checks the vendored dependencies, creates `.env` if
absent, and builds the executable. It preserves existing configuration. Edit
`.env` before running against your music library.

`make install` installs the binary in `~/.local/bin` by default. Set
`PREFIX=/usr/local` or `BINDIR=/your/bin` to choose another location. Packaging
can stage installation with `DESTDIR=/path/to/staging`. `make doctor` reports
the Go toolchain and optional media/parity prerequisites.

Use `make run LISTEN=127.0.0.1:8000` to choose a listen address, or run HTTP and
jobs separately with `make run-http` and `make worker`. `make worker-once`
processes currently ready jobs and exits. `make cli ARGS='command arguments'`
runs any Cobra command. All app targets accept `ENV_FILE=/path/to/.env` and
`DATABASE=/path/to/db.sqlite3`; `DATABASE` overrides the configured database.
`ARGS` also passes additional arguments to the server and worker targets.

For development, install [Air](https://github.com/air-verse/air) with
`go install github.com/air-verse/air@latest` and ensure Go's bin directory is on
`PATH`. Run `make dev` to start the server and background jobs, then automatically
rebuild and restart when Go sources or embedded templates, static files, or
translations change. It accepts the same `ENV_FILE`, `DATABASE`, `LISTEN`, and
`ARGS` options as `make run`; for example,
`make dev LISTEN=127.0.0.1:8000 ARGS=--no-worker`. Press Ctrl-C to stop.
Development builds are stored under `bin/dev` (or `BUILD_DIR/dev`).

`make build-debug` writes `bin/reinhardt-debug` with debugger symbols. Both
release and debug builds disable CGO and embed all assets. Use
`make build-linux-arm64` to cross-compile for Linux/aarch64, producing
`bin/reinhardt-linux-arm64`. Override the output with `LINUX_ARM64_BIN`.
For other platforms, set `GOOS` and `GOARCH` when running `make build`.

Open the login link printed by `giveaccess` at your server's address. Configure
`ACCESS_URL_PREFIX`, for example `http://127.0.0.1:8000`, to print absolute links.
Accounts can also request a login link by email after SMTP is configured.
The binary can be copied elsewhere and run without the source or asset folders.

`serve` defaults to `127.0.0.1:8000` and starts the job worker alongside HTTP.
Use `--listen :8000`, `LISTEN_ADDR`, or the optional positional address to change
the bind address. Set `ALLOWED_HOSTS` to the public hostname. TLS can terminate at
a reverse proxy; use `COOKIE_SECURE=true` for HTTPS deployments. `URL_PREFIX`
supports mounting the app below a path, for example `music/`.

## Run with systemd

[examples/reinhardt.service](examples/reinhardt.service) provides a service unit
for HTTP and background jobs. Replace `example-user` and `/home/example-user`
with the existing Unix account and home directory that own your application
data. Adjust `/usr/local/bin/reinhardt` to the installed binary's location.
The working directory must exist; the application reads `~/.reinhardt/config`
and an optional `.env` there, and uses `~/.reinhardt` for data by default.

After adapting the example:

```sh
sudo cp examples/reinhardt.service /etc/systemd/system/reinhardt.service
sudo systemctl daemon-reload
sudo systemctl enable --now reinhardt.service
sudo journalctl -u reinhardt.service -f
```

Restart after replacing the executable with
`sudo systemctl restart reinhardt.service`. Set `LISTEN_ADDR` in the unit to
change the bind address. The example's `PATH` includes the account's
`~/.local/bin` so locally installed `yt-dlp` can be found.

## Configuration and existing databases

Configuration precedence, highest first:

1. Command flags (`--database`, `--listen`).
2. Process environment.
3. `.env`, or the file selected with `--env-file`.
4. The original application's `~/.reinhardt/config`.
5. Built-in defaults.

Print the effective application settings with `bin/reinhardt config` or
`make cli ARGS=config`. Output is sorted, with quoted values; credentials are
redacted unless `--show-secrets` is supplied. `--env-file` and `--database` apply
as usual. The command does not open or modify SQLite. If `SECRET_KEY` is absent
from configuration, it is labeled as database-managed rather than reading or
generating the database's key. Unrelated process environment variables are omitted.

Non-Django application environment variables retain their names and meanings;
see [.env.example](.env.example) for the full list, including Jama, Dropbox,
SMTP, import directory, URL prefixes, sanitization, language, and playlist
download limits. `DISCOGS_TOKEN` is accepted for compatibility; the original
Discogs helper is unused by application routes. Django implementation settings
such as `USE_DJANGO_ADMIN` and `STATIC_ROOT` are unnecessary in this port.

The default data directory is `~/.reinhardt`. The port uses the existing
`~/.reinhardt/db.sqlite3`, `~/.reinhardt/covers`, and `~/.reinhardt/config`
by default. `BASE_DIR` controls this directory, `DATABASE_PATH` selects SQLite,
and `COVERS_DIR` can override the artwork cache location. No configuration is
written to the original application. If `SECRET_KEY` is absent, a random key is
generated once and stored in the Go database.

The port uses the original `auth_user` and `reinhardt_*` tables. To migrate an
existing installation:

1. Apply the original Django migrations through `0017`.
2. Back up the database and stop Django and its Huey worker before switching.
   Use Django's preferences page or SQLite's backup API for a consistent backup;
   a raw copy of a live database can miss its WAL contents.
3. Keep `~/.reinhardt/config` to preserve the original `SECRET_KEY`. The existing
   `~/.reinhardt/db.sqlite3` is selected automatically; set `BASE_DIR` or
   `DATABASE_PATH` if your Django installation stores data elsewhere.
4. Run `make migrate`, then `make run`.

Existing users, preferences, login tokens, tags, shared links, ranks, and library
paths are retained. Music stays in its registered directories. Go adds its own
session, settings, and job tables and FTS synchronization triggers. Sessions and
pending Huey jobs are not imported; users log in using their existing invite
links. Running both applications against the same live SQLite file is unsupported.
For an isolated trial, set `DATABASE_PATH` to a consistent database copy. Earlier
Go builds used `~/.go-reinhardt`; set `BASE_DIR` explicitly to keep using that
directory.

## Commands and jobs

Every original application management command is available under its original
name:

| Command | Purpose |
| --- | --- |
| `addmusicdir <music_dir>` | Register a directory |
| `indexmusic` | Read MP3/M4A metadata, mark missing files, rebuild FTS |
| `giveaccess <username>` | Create an account and print its login link |
| `listaccess` | Print existing login links |
| `listtags` | List tags with their owners |
| `fetchtagfiles <tag_id> <destination>` | Copy tracks in playlist order |
| `tagsexport <csv_file_output>` | Export library metadata as CSV |
| `populateyears` | Update only the year column from file metadata |
| `dedup` | Report duplicate files without deleting them |
| `jamasync` | Synchronize the library with Jama |
| `dbxrefreshtoken` | Obtain a Dropbox refresh token interactively |
| `ytrip <url> <artist> <title>` | Download and import a YouTube track |

`ytrip` keeps `-a/--album`, `-g/--genre`, and `-y/--year`, and adds `--comment`.
`giveaccess` adds `--email` and `--superuser`. Run any command with `--help`
for usage.

Additional commands are `serve` (alias `runserver`), `worker` (alias `run_huey`),
`gathermusic`, `migrate`, `check`, `jobs`, and `config`. Use `serve --no-worker`
when running a separate `worker`. `worker --once` processes ready jobs and exits.

With the similar-artists preference enabled, autofeed excludes artists already
represented in your queue and prefers a new artist during random fallback.
Once no eligible new artist remains, autofeed continues with a random available,
nonblacklisted song, allowing an artist already queued. Playback stops only if
no eligible song remains. Queue advancement and explicit track selections keep
their existing behavior.

Jobs persist in SQLite and retry twice at ten-minute intervals. Import scanning
runs every five minutes when `MUSIC_IMPORT_DIR` is configured. Uploads are
restricted to superusers and land in that directory. ZIP extraction rejects
path traversal and symlinks and limits expanded size. Imports refuse to
overwrite existing music. Failed YouTube downloads appear on the original
failure/retry screen. `jobs` reports pending and exhausted integration jobs.

## Media and integrations

Local and delegated audio support streaming GET/HEAD and byte ranges without
buffering entire tracks. Delegated streaming forwards ranges using identity
encoding. Shared links only serve tracks assigned to the enabled shared tag;
playlist ZIP downloads respect `SHARED_PLAYLIST_DOWNLOAD_MAX_SECONDS`.

Delegated audio uses a separate HTTP client with connection/TLS and response-header
timeouts, allowing its body to stream beyond the API client's 35-second total
timeout. Browser disconnects cancel and close the upstream stream. Upstream
transfer failures log the track ID and byte counts; intentional client
cancellations do not generate relay-error logs. Firefox may report
`NS_ERROR_NET_PARTIAL_TRANSFER` when an audio response is interrupted; a fixed
cutoff warrants checking upstream/reverse-proxy timeouts and relay logs.

Metadata reading, MP3 metadata/artwork writing, embedded-artwork reading, image
validation, square thumbnails, and fallback covers run in Go. Artwork and audio
edits use temporary files and atomic replacement. The fallback sleeve retains
the record motif and metadata labels; it is generated by Go and is visually
different from Pillow's rendering.
Labels use the embedded [Go Mono font](https://go.dev/blog/go-fonts), supporting
European accented letters, Greek and Cyrillic. Combining accents are composed
before rendering and truncation. The font and rasterizer are vendored and run
with CGO disabled; no system font or additional helper binary is needed.

YouTube features use `yt-dlp`, `ffmpeg`, `ffprobe` and Deno on the service's `PATH`;
see [Prerequisites](#prerequisites) for Debian and macOS installation commands.
Artwork embedding in non-MP3 formats uses `ffmpeg`; the
validated sidecar image remains available even if embedding is unsuccessful.
Automatic cover discovery uses Apple's music search API, matching the original
`get-cover-art` integration. Embedded covers, adjacent images, uploaded images,
and YouTube thumbnails are supported. The artwork search link remains available
in track details. Failed external lookups are cached and fall back to a generated
sleeve.
Independent cover lookups run concurrently. Requests for the same normalized
artist and album (or title when no album exists) share one search/download;
cancelling one request does not cancel discovery for the others. Shared network
lookups keep an eight-second deadline, and successes and misses remain cached
for one hour. See [artwork concurrency](docs/artwork-performance.md).

YouTube downloads also require a registered destination in the database used by
the server. Register an existing music directory with
`make cli ARGS='addmusicdir /path/to/music'`, using the same `DATABASE` or
`DATABASE_PATH` as `serve`. Existing Django directory registrations are reused
when using its database; a fresh database has no registrations. The web form
reports a missing destination before queuing a download. After registering one,
retry an existing failure from the failed-downloads screen; pending jobs also
retry automatically.

Jama synchronization uses its existing RPC/chunk-upload protocol. Dropbox uses
refresh-token authentication and bounded upload-session chunks. SMTP supports
plain connections, STARTTLS, implicit TLS, and authentication. External services
are mocked in tests; live account integration checks were not performed.

## Verification

```sh
make test
make check
make check-app
```

`make ci` combines formatting checks, `go vet`, regression tests, and a release
build. Select tests with `make test TEST=TestStreamingAndDelegate`, and pass
additional Go test options with `TEST_FLAGS`, for example `TEST_FLAGS=-count=1`.
`make test-verbose` adds verbose output. `make test-coverage` produces
`bin/coverage.out` and `bin/coverage.html`. `make test-race` runs the race detector;
this diagnostic target requires a C compiler and enables CGO for the test
executable. Application builds and all other test targets retain CGO disabled.

`make fmt` formats application sources without touching vendor. `make clean`
removes the release/debug binaries and coverage reports. `make tidy` reconciles
the module files and refreshes vendor; this maintenance command may download
modules.

Tests cover pages and fragments, ownership checks, CSRF, login links, API
validation, FTS updates, queues, shared access, ranged streaming, image uploads,
native metadata indexing, durable job failures, archive traversal, and SQLite
snapshots. Template tests cover layout isolation, missing values, query encoding,
and escaping of untrusted metadata. If Node.js is available, they also execute
the rendered shared-player title assignment to check JavaScript injection and
round-trip values. When the Django checkout is adjacent, tests also verify that
embedded static files and translations are byte-identical.

For differential checks using the original Django migrations and views:

```sh
make test-parity
```

This optional target uses the adjacent Django virtual environment and test-owned
temporary files. It compares full pages, Unpoly fragments, and API JSON with
Django responses in English and French across populated, empty, and
special-character libraries, plus playback state transitions (388 comparisons).
It normalizes comments,
whitespace, CSRF tokens, and equivalent HTML/JavaScript escaping.
It is not required to build or run the Go application.

For dropdown response-time measurements and reproducible Go/Django benchmarks,
see [the performance investigation](docs/dropdown-performance.md).
Playback query counts and timings are documented in
[the next-song investigation](docs/next-song-performance.md).
For queue loading and full-page player-footer measurements, see
[the full-page investigation](docs/full-page-queue-performance.md).
Home-page album selection is cached with SQLite-trigger invalidation; see
[home-page measurements](docs/home-performance.md).
Initialized queue and favorites pages use read-only tag lookups; missing tags
are still created on first use without changing their defaults.
Search-suggestion caching, automatic invalidation, and measurements are documented
in [the suggestions investigation](docs/suggestions-performance.md).
`make verify` runs both normal CI and the Django comparisons. Set
`DJANGO_PYTHON=/path/to/python` if the original virtual environment is elsewhere.
