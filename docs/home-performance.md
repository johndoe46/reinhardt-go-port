# Home-page performance

The home page displays 25 tracks, one representative per album, with
`GROUP BY album ORDER BY created_at DESC,album`. SQLite searches the
`musicfile_latest_idx` index for nonmissing tracks, then builds temporary B-trees
for both grouping and ordering. The limit does not avoid processing the full
matching library. Unpoly omits the layout, footer and search suggestions, but
previously still paid for this library query on every visit. With the single
SQLite connection, the query can also delay other requests needing the database.

The application caches the ordered representative track IDs for all albums,
plus 26 raw track rows for the first page (25 displayed and one lookahead).
Each request reads one revision counter instead of rerunning the grouped query.
Later pages hydrate only their 26 IDs with primary-key lookups. The shared
selection keeps the original join, grouping and representative-track behavior;
album breaks timestamp ties for consistent pagination. Indexes are unchanged.
The ID list uses memory proportional to album count, rather than retaining all
track metadata or a separate cache for every page or user.

SQLite triggers advance `go_home_revision` on track inserts, updates and deletes,
including writes from a worker, CLI or external process. Music-directory path
changes also invalidate the cache; cascading directory deletion invalidates it
through the track-delete trigger. Rolled-back changes leave the revision intact.
Queue and preference changes do not invalidate the shared library selection.
Concurrent requests share one refresh. Each request receives separate row maps
and derives playback/download paths using the current configuration. Artwork
references are refreshed at render time, independently of the cached metadata.

The next request after an invalidation rebuilds the cache. Startup begins with
an empty cache, so the initial query and its library-size cost still apply.
The counter and triggers are added automatically when opening SQLite, including
existing databases. No new dependencies or configuration are required.
GET and HEAD requests themselves make no database writes once preferences exist.

## Original homepage cache measurements

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

These measurements predate the track grid and load-more feature below.

Tests compare cached rows with the original query, enforce the page limit,
cover metadata/album/date/path/ID/missing changes, imports, deletes, rollback,
external writes, directory cascades, empty results, concurrent refreshes, row
isolation and database failures. HTTP tests cover both users, GET/HEAD and
full/Unpoly requests, check query/write bounds and verify visible edits.

```sh
CGO_ENABLED=0 go test -mod=vendor ./internal/reinhardt -run '^$' \
  -bench BenchmarkHomePage -benchtime=20x -count=1
```

## Load-more investigation

The first load-more implementation reused the homepage cache only at offset
zero. Every later page ran the full grouped track query with `LIMIT 26 OFFSET ?`.
The limit bounds returned rows, but SQLite still groups and sorts the matching
library, including full track metadata in its temporary tables. That work also
occupies the application's single SQLite connection.

Local Apple M1, macOS/arm64 measurements with the real HTTP handler, default
track grid and synthetic libraries of roughly ten tracks per album:

| Library size | Offset | Request before → after | Database before → after |
| --- | ---: | ---: | ---: |
| 10,000 tracks | 25 | 33.30 → 1.88 ms | 31.72 → 0.39 ms |
| 10,000 tracks | 750 | 34.56 → 1.86 ms | 32.95 → 0.38 ms |
| 100,000 tracks | 25 | 343.98 → 1.89 ms | 342.40 → 0.40 ms |
| 100,000 tracks | 750 | 358.49 → 1.89 ms | 356.90 → 0.38 ms |

The shared ID order is built during the initial homepage refresh. Load-more
requests then perform zero library scans and five small reads: authentication,
revision, page metadata, preferences and artwork references. Warm offset-zero
requests remain around 1.6 ms with four reads. Initial cache construction and
refresh still depend on library size. Timings exclude network, image downloads,
browser work and cache construction; they are local measurements, not production
latencies. The benchmark uses a 300 ms minimum per case, so slow baseline cases
have few samples.

Regression tests visit distinct offsets across both accounts to ensure new
pages avoid library scans, compare their rows and order to the original grouped
query, and verify invalidation after editing a later page.

```sh
go test -mod=vendor ./internal/reinhardt -run '^$' \
  -bench '^BenchmarkHomeLoadMore$' -benchtime=300ms -count=1
```
