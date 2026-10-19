# Music dropdown response time

The slow path was the generic rendering setup in `internal/reinhardt/http.go`,
rather than the dropdown's template or its track lookup.

`internal/assets/static/app.js` requests
`/music_file_dropdown/?q=...&tag=&playing=&context=home` with ordinary `fetch()`.
It does not send `X-Up-Target`. The renderer interpreted
this as a full-page request and selected every distinct artist and album from
the library to prepare the navigation bar's search suggestions. The dropdown
renders `_track_dropdown_menu_items.html`, which has no navigation bar and never
uses those suggestions.

The renderer also called `Preferences()` unnecessarily. Its `INSERT OR IGNORE`
attempt writes even when preferences already exist: SQLite advances the table's
AUTOINCREMENT sequence. That added a write transaction to each dropdown GET.
With `SetMaxOpenConns(1)`, these operations also delay other requests waiting for
the shared connection.

## Measurements

Local warm-request measurements on Linux/amd64, AMD Ryzen 7 5700X, using synthetic
SQLite databases stored on `/tmp` (tmpfs). Each case uses 20 requests. The
measurement invokes the real HTTP handler with `httptest` and test-owned
databases. These are in-process handler measurements, excluding network, proxy,
and browser costs;
they do not measure the production host.

| Library size | Before, plain fetch | After, plain fetch |
| --- | ---: | ---: |
| 3 tracks | 0.409 ms | 0.275 ms |
| 10,000 tracks | 6.559 ms | 0.280 ms |
| 100,000 tracks | 64.908 ms | 0.271 ms |

At 100,000 tracks, about 61 ms of the old 65 ms handler time was spent in SQL and
row scanning. The old request allocated about 16 MB, despite returning only a
small menu. Afterward it allocates about 54 KB. Query count falls from six to
three (session/user, track, favorite status), with zero writes and zero suggestion
queries. Request cost no longer grows with library size in these measurements.

With gzip enabled at 100,000 tracks, handler time drops from 66.929 ms to
0.723 ms. The before/after measurements and SQL trace identify the redundant
library query directly.

## Change and regression coverage

Template loading now records whether a template uses the page layout by examining
its native Go parse tree. Rendering prepares preferences, player-footer state,
and navigation suggestions only for those templates. A plain fetch of a partial
gets the same optimization as a partial requested through Unpoly. No frontend
header change is required.

`TestDropdownAvoidsLayoutQueriesAndWrites` checks both request forms, counts SQL,
and verifies the preferences sequence remains unchanged.

## Reproduce

```sh
CGO_ENABLED=0 go test -mod=vendor ./internal/reinhardt -run '^$' \
  -bench BenchmarkMusicFileDropdown -benchtime=20x -count=1
```

The benchmark creates temporary fixtures and does not read the user's library.
Gzip compressors are now reused as described below. Full pages now
[cache search suggestions](suggestions-performance.md).
Queue and footer query costs are covered in
[the full-page investigation](full-page-queue-performance.md); this change
specifically removes unused work from partials.

## Gzip compressor reuse

HTML compression now borrows a gzip writer from a shared `sync.Pool`, resets it
for the response, and returns it after closing the stream. Cleanup resets the
destination to `io.Discard`, including on failures, so pooled compressor state
does not retain response buffers. The compression level, HTML, headers, and
existing compression threshold are unchanged. Streaming responses retain their
existing range and identity-encoding behavior.

On the same Linux/amd64 Ryzen 7 5700X, 100 warm compressed dropdown requests with
the three-track synthetic fixture measured:

| | Before pooling | After pooling |
| --- | ---: | ---: |
| Handler time | 0.525 ms | 0.295 ms |
| Allocated bytes/request | 1,130,773 | 64,330 |
| Allocations/request | 1,165 | 1,148 |

Both cases make three SQL reads, zero writes, and zero suggestion queries.
Pooling reduces allocated bytes by about 94% in this measurement. Pools can
discard idle writers during garbage collection, and concurrent requests need
separate writers, so occasional compressor allocation remains expected. These
are local handler measurements excluding network/proxy/browser overhead.

Tests compare decompressed HTTP responses with their uncompressed HTML, exercise
independent concurrent streams, and verify recovery after a destination write
error. Existing streaming regressions verify ranged audio remains uncompressed.

```sh
CGO_ENABLED=0 go test -mod=vendor ./internal/reinhardt -run '^$' \
  -bench 'BenchmarkMusicFileDropdown/tracks=3/fetch-gzip$' \
  -benchtime=100x -count=1
```
