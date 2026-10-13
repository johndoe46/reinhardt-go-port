# Repository Guidelines

## Project Structure & Module Organization

Reinhardt is a Go music application, embedding SQLite resources and assets in one executable.

- `cmd/reinhardt/`: executable entry point.
- `internal/reinhardt/`: application logic and adjacent `*_test.go` regression tests.
- `internal/assets/`: embedded SQL schema, Go HTML templates, static assets, and JSON message catalogues.
- `docs/`, `USER_MANUAL.md`, and `examples/`: documentation, screenshots, and deployment examples.
- `vendor/`: dependency sources; `bin/`: ignored build and coverage outputs.

## Build, Test, and Development Commands

Run commands from the repository root with Go 1.26+ and GNU Make:

- `make setup`: verify vendored dependencies, create `.env` if absent, and build.
- `make build`: produce `bin/reinhardt` with embedded assets and CGO disabled.
- `make run LISTEN=127.0.0.1:8000`: start HTTP and background jobs.
- `make dev`: rebuild and restart on source or asset changes; requires Air.
- `make fmt` / `make check`: format application Go code / check formatting and run `go vet`.
- `make ci`: run checks, regression tests, and a release build.
- `make tidy`: reconcile module files and refresh `vendor/` after dependency changes.

## Coding Style & Naming Conventions

Use `gofmt`, including tabs for Go indentation. Follow Go naming conventions and existing package organization. Prefer descriptive variable names over single-letter names. Keep production builds compatible with `CGO_ENABLED=0`; direct Go commands should use `-mod=vendor`. Regenerate vendored dependencies rather than editing them manually.

Preserve contextual HTML escaping, Unpoly fragment targets, and persistent-player behavior. Add translations through `internal/assets/messages/active.<language-tag>.json`; follow the catalogue README.

## Testing Guidelines

Use `testing` and `httptest`, naming tests `Test<Behavior>` in adjacent `*_test.go` files. Cover changed behavior with isolated fixtures.

Run `make test`, or target a test with `make test TEST=TestUserLanguageSettings`. Use `make test-race` for concurrency changes; it requires a C compiler. `make test-coverage` generates reports without an enforced percentage threshold.

## Commit & Pull Request Guidelines

History uses short, imperative summaries, usually lowercase, without mandatory prefixes. List substantive changes in the commit body. Pull requests should explain behavior and validation, link relevant issues, and include screenshots for UI changes. Update documentation when settings or commands change.

## Configuration & Data Safety

Keep secrets out of Git. Use `.env.example` as the configuration reference. Develop against an isolated database using `DATABASE=/path/to/copy.sqlite3`; defaults reuse `~/.reinhardt` data. Preserve existing installations with tested, repeatable schema migrations.
