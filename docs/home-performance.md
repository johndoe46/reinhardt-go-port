# Home-page performance

The home page selects 25 tracks, one representative per album, with
`GROUP BY album ORDER BY created_at DESC LIMIT 25`. SQLite searches the
`musicfile_latest_idx` index for nonmissing tracks, then builds temporary B-trees
for both grouping and ordering. The limit does not avoid processing the full
matching library. Unpoly omits the layout, footer and search suggestions, but
previously still paid for this library query on every visit. With the single
SQLite connection, the query can also delay other requests needing the database.

The application now caches the original query's results, limited to 25 raw track
rows. Each request reads one revision counter instead of rerunning the grouped
query. The query and indexes remain unchanged to preserve SQLite's existing
representative-track selection, ordering, timestamps, missing-track behavior,
empty-album grouping and rendered HTML.

SQLite triggers advance `go_home_revision` on track inserts, updates and deletes,
including writes from a worker, CLI or external process. Music-directory path
changes also invalidate the cache; cascading directory deletion invalidates it
through the track-delete trigger. Rolled-back changes leave the revision intact.
Queue and preference changes do not invalidate the shared library selection.
Concurrent requests share one refresh. Each request receives separate row maps
and derives playback/download paths using the current configuration.

The next request after an invalidation rebuilds the cache. Startup begins with
an empty cache, so the initial query and its library-size cost still apply.
The counter and triggers are added automatically when opening SQLite, including
existing databases. No new dependencies or configuration are required.
GET and HEAD requests themselves make no database writes once preferences exist.

## Local measurements

Linux/amd64, AMD Ryzen 7 5700X, synthetic SQLite fixtures on `/tmp`, 20 warm,
uncompressed requests through the real HTTP handler. The fixture has roughly
ten tracks per album and 100 per artist. Measurements use the same benchmark
before and after adding the cache and exclude network, proxy, browser,
initial construction and refresh costs.

| Library size | Full page before → after | Unpoly before → after |
| --- | ---: | ---: |
| 3 tracks | 0.442 → 0.344 ms | 0.301 → 0.219 ms |
| 10,000 tracks | 15.739 → 1.670 ms | 14.307 → 0.579 ms |
| 100,000 tracks | 191.228 → 9.174 ms | 183.303 → 0.565 ms |

At 100,000 tracks, warm Unpoly database time falls from 182.7 ms to 0.110 ms.
SQL counts remain three for Unpoly and four for full pages with no saved current
song: session/user, home revision, preferences, and full-page suggestion revision.
The expensive query is replaced by the counter lookup, with zero warm library
loads and zero writes. Full pages still render every search-suggestion option,
so their rendering and response size grow with the number of artists/albums.
These are local handler measurements, not production timings.

Tests compare cached rows with the original query, enforce the 25-track limit,
cover metadata/album/date/path/ID/missing changes, imports, deletes, rollback,
external writes, directory cascades, empty results, concurrent refreshes, row
isolation and database failures. HTTP tests cover both users, GET/HEAD and
full/Unpoly requests, check query/write bounds and verify visible edits.

```sh
CGO_ENABLED=0 go test -mod=vendor ./internal/reinhardt -run '^$' \
  -bench BenchmarkHomePage -benchtime=20x -count=1
```
