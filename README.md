# Reinhardt

For installation from release downloads, first-run setup, and everyday use,
see the [Reinhardt user manual](USER_MANUAL.md).

Run `bin/reinhardt --version` to check the executable's release version.

Reinhardt is a music library and streaming application written in Go. SQLite,
templates, translations, and every static asset are included in a single executable.
The server runs with `CGO_ENABLED=0`; its normal streaming, indexing, MP3 tag
editing, playlist, and artwork features require no Python or external programs.

The HTML templates use Go's standard `html/template` package with contextual
escaping. Layouts and partials use native Go template definitions; helpers handle
translations, grouping, URLs, and formatting. Each page has an isolated template
set so its layout overrides cannot affect another page. Runtime translations use
embedded Go message catalogues. URLs, element IDs, `data-*`
attributes, and Unpoly fragment targets support navigation, the persistent
player, oscilloscope, and DJ interface.

## Source layout

- `cmd/reinhardt/`: executable entry point.
- `internal/reinhardt/`: application code and its regression tests.
- `internal/assets/`: embedded schema, HTML templates, static files, and translations.
- `docs/` and `examples/`: performance reports and deployment examples.
- `vendor/`: vendored Go dependencies.
- `bin/`: generated executables and coverage reports, excluded from Git.

Run the Makefile targets from the repository root. For direct Go commands, use
`go build -mod=vendor -o bin/reinhardt ./cmd/reinhardt`,
`go run -mod=vendor ./cmd/reinhardt`, or `go test -mod=vendor ./...`.
Set `CGO_ENABLED=0` for production builds, as the Makefile does automatically.
Assets are embedded in the binary; installed executables need no resource
directories alongside them.

## Direct Go dependencies

The direct dependencies listed in [go.mod](go.mod) serve these purposes:

| Module | Purpose in Reinhardt |
| --- | --- |
| `github.com/bogem/id3v2/v2` | Write MP3 ID3 metadata and embed cover artwork when editing or importing tracks. |
| `github.com/dhowden/tag` | Read audio metadata during indexing and extract embedded cover artwork. |
| `github.com/emersion/go-sasl` | Provide SASL PLAIN authentication for SMTP connections using configured email credentials. |
| `github.com/emersion/go-smtp` | Send login-link emails over SMTP, including STARTTLS and implicit TLS connections. |
| `github.com/gdamore/tcell/v2` | Handle terminal screens and keyboard input for interactive user management. |
| `github.com/gosimple/unidecode` | Transliterate Unicode tags into ASCII for library paths, download filenames, and artist matching. |
| `github.com/joho/godotenv` | Parse `.env` files and `~/.reinhardt/config`. |
| `github.com/nicksnyder/go-i18n/v2` | Load embedded message catalogues and translate messages with language fallback, named variables, and plural rules. |
| `github.com/rivo/tview` | Build the user-management TUI's table, search field, forms, and confirmation dialogs. |
| `github.com/spf13/cobra` | Define CLI commands, flags, aliases, and help output for the server, worker, and management tools. |
| `github.com/tcolgate/mp3` | Scan MP3 frames to calculate track duration during indexing. |
| `golang.org/x/image` | Resize artwork, decode WebP images, and render fallback cover labels with the embedded Go Mono font. |
| `golang.org/x/term` | Check that the TUI is launched from an interactive terminal. |
| `golang.org/x/text` | Match language preferences to available catalogues, normalize Unicode for slug generation, and compose combining accents before rendering cover labels. |
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
| `go` 1.27.3+ and `gofmt` | Building, normal tests and formatting; both come with Go |
| GNU `make` | The documented build, install, run and test targets |
| `git` | Obtaining the source checkout; unnecessary for a copied release binary |
| `node` | Optional tests that execute the shared player's JavaScript |
| `cc` (`gcc` or `clang`) | `make test-race` only; production builds and normal tests disable CGO |

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

Install Go 1.27.3 or newer using the
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
```

`build-essential` supplies the C compiler for race tests; `nodejs` supplies
`node`.

### macOS (Mac OS X)

Install [Homebrew](https://brew.sh/) first, then install media helpers:

```sh
brew install yt-dlp ffmpeg deno
```

Homebrew handles yt-dlp's Python dependencies and EJS component; FFmpeg includes
`ffprobe`. See the [yt-dlp formula](https://formulae.brew.sh/formula/yt-dlp).
For source builds and optional JavaScript tests:

```sh
brew install go make git
# Optional test tools:
brew install node
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
and optional media availability checks.

## Build and start

Install the build tools listed under [Prerequisites](#prerequisites) first.

Go dependency sources are included in `vendor/`. Build and verification targets
use `-mod=vendor`, so dependencies do not need to be downloaded. After changing
`go.mod`, refresh the vendored sources with `make vendor`.
The module requires Go 1.27.3. Make defaults `GOTOOLCHAIN` to `go1.27.3`, which
may download that toolchain if it is not installed; override `GOTOOLCHAIN` to
select another compatible toolchain.

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
the Go toolchain and optional media tools.

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
4. `~/.reinhardt/config`.
5. Built-in defaults.

Print the effective application settings with `bin/reinhardt config` or
`make cli ARGS=config`. Output is sorted, with quoted values; credentials are
redacted unless `--show-secrets` is supplied. `--env-file` and `--database` apply
as usual. The command does not open or modify SQLite. If `SECRET_KEY` is absent
from configuration, it is labeled as database-managed rather than reading or
generating the database's key. Unrelated process environment variables are omitted.

See [.env.example](.env.example) for application settings, including Jama,
Dropbox, SMTP, import directory, URL prefixes, sanitization, language, and
playlist download limits.

### Languages and message catalogues

Users can select English or French in **Preferences > Language**; the choice
saves automatically. It is stored in `reinhardt_preferences.language_code`, which
is added automatically to existing databases. Saved choices from the initial
`go_user_language` table are migrated into preferences before that table is removed.
An unset language uses the server default. The language selector saves independently
of the playback switches. Changing language updates the Preferences fragment through
Unpoly while playback continues. The form explains that a full page refresh is
needed to apply the change throughout navigation, the player, and the document's
`lang` attribute.
**Server default** clears the account override and uses `LANGUAGE_CODE` (default
`en-us`). Anonymous pages also use that setting. Regional tags such as `fr-fr`
match the available catalogue; unsupported languages fall back to English.

Translations use [go-i18n/v2](https://github.com/nicksnyder/go-i18n), with embedded
JSON catalogues in [`internal/assets/messages`](internal/assets/messages).
The English catalogue supplies fallback messages; the French catalogue
provides French translations.

To add a language, create `active.<BCP-47-language-tag>.json` in that directory.
Include a `language_name` message containing the language's own name, translate
the desired English message IDs, and rebuild. The loader discovers catalogues
and adds their languages to the settings selector automatically. Missing messages
fall back to English. See the [catalogue guide](internal/assets/messages/README.md)
for named variables, plurals, and template helpers.

The default data directory is `~/.reinhardt`, containing `db.sqlite3`,
`covers`, and an optional `config` file. `BASE_DIR` controls this directory,
`DATABASE_PATH` selects SQLite, and `COVERS_DIR` can override the artwork cache
location. If `SECRET_KEY` is absent, a random key is generated once and stored
in the database.

Database initialization is additive: existing users, preferences, login tokens,
tags, shared links, ranks, and library paths are retained. Missing tables,
indexes, and cache revision triggers are added automatically. Music stays in its
registered directories. To update an existing installation:

1. Back up the database before updating. Use the preferences page or SQLite's
   backup API for a consistent snapshot; a raw copy of a live database can miss
   its WAL contents.
2. Stop the server and any separately running workers.
3. Keep `~/.reinhardt/config` to preserve settings such as `SECRET_KEY`, which
   protects shared playlist links. Set `BASE_DIR` or `DATABASE_PATH` if your
   installation stores data elsewhere.
4. Install the new executable, run `bin/reinhardt migrate`, then restart the
   server and workers.

For an isolated trial, set `DATABASE_PATH` to a consistent database copy.

## Commands and jobs

The executable includes these management commands:

| Command | Purpose |
| --- | --- |
| `addmusicdir <music_dir>` | Register a directory |
| `indexmusic` | Read MP3/M4A metadata, mark missing files, rebuild FTS |
| `giveaccess <username>` | Create an account and print its login link |
| `listaccess` | Print existing login links |
| `tui` | Interactively list, search, create, edit, and delete users and manage login links |
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

Additional commands are `serve` (alias `runserver`), `worker`,
`gathermusic`, `migrate`, `check`, `jobs`, and `config`. Use `serve --no-worker`
when running a separate `worker`. `worker --once` processes ready jobs and exits.

Run `./bin/reinhardt tui` or `make cli ARGS=tui` in an interactive terminal to
manage accounts. The TUI uses the same `--env-file` and `--database` options as
other commands, and shows the database path at the top. Use the arrow keys to
select a user, `Enter` to edit, `n` to create, `/` to search usernames or email
addresses, `l` to view a login link, and `r` to reset it. Press `d` to permanently
delete the selected account. `f` refreshes the list;
`q`, `Esc`, or `Ctrl-C` exits. Within forms, use `Tab`/`Shift-Tab` to move,
`Space` to toggle checkboxes, and `Esc` to cancel.
The TUI uses `LANGUAGE_CODE` from your configuration, including regional
matching and English fallback, just like the web app. For example, launch it
in French with `LANGUAGE_CODE=fr ./bin/reinhardt tui`. Keyboard shortcuts stay
the same in every language.

Account edits preserve preferences and login links. Clearing **Active** blocks
sign-in and removes existing sessions when saved; reactivating the account
allows its existing login link to work again. **Administrator** controls both
superuser and staff permissions. Resetting a login link requires confirmation
and revokes the previous link and all of that account's sessions.

Permanent deletion requires typing the exact username and choosing **Delete
account**. It removes the account, login link, sessions, preferences, queue,
favorites, tags, and playlists in one transaction. Music files and other
accounts are preserved. This cannot be undone.

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
overwrite existing music. Library paths transliterate artist, album, and title
tags into ASCII (for example, `구룡` becomes `guryong`); stored tags retain their
original script. Failed YouTube downloads appear on the
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
New YouTube imports are peak-normalized to a sample peak of -1 dBFS before MP3
encoding. Reinhardt downloads the original audio, scans its decoded samples,
then applies one constant gain across all channels during the sole 320 kb/s MP3
encode. This preserves dynamics and channel balance; silence stays silent.
The scan writes no intermediate audio file. MP3 encoding can shift the final
decoded peaks slightly, so -1 dBFS is a pre-encoding target, not a true-peak limit.

If YouTube requires authentication or bot verification, set the optional
`YTDLP_COOKIES_FILE=/path/to/youtube-cookies.txt` in `.env` or the process
environment to a Netscape-format cookies file. Reinhardt passes it explicitly
to yt-dlp for metadata lookup, audio downloads and thumbnails, while continuing
to ignore yt-dlp's own configuration. Leave it empty to use no cookies.
Configure both the server and worker if they run separately, then restart them.
Their accounts need read/write access because yt-dlp can refresh the file.
Keep the file private and outside version control. See
[yt-dlp's cookie instructions](https://github.com/yt-dlp/yt-dlp/wiki/FAQ#how-do-i-pass-cookies-to-yt-dlp).

Artwork embedding in non-MP3 formats uses `ffmpeg`; the
validated sidecar image remains available even if embedding is unsuccessful.
Automatic cover discovery uses Apple's music search API.
Embedded covers, adjacent images, uploaded images,
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
`DATABASE_PATH` as `serve`. Existing directory registrations are reused; a
fresh database has no registrations. The web form reports a missing destination
before queuing a download. After registering one,
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

`make vulncheck` uses [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)
to scan application packages, tests, and their vendored dependencies for known
vulnerabilities, including the Go standard library. It uses the online Go
vulnerability database and fails when a vulnerability is reachable from the
scanned code. On first use, it installs govulncheck v1.9.0 under `bin/tools/`,
without changing the application's module files or requiring changes to `PATH`:

```sh
make vulncheck
```

Set `GOVULNCHECK_VERSION` to select another scanner version, or set
`GOVULNCHECK=/path/to/govulncheck` to use an existing installation.
This network-dependent check runs separately from `make ci`.

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
round-trip values.

For dropdown response-time measurements and reproducible benchmarks,
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
`make verify` is an alias for `make ci`.
