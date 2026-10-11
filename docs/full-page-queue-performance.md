# Full-page queue and player-footer performance

Full-page rendering previously loaded every queue association and made a
separate track query per item to find the current song for the footer. Queue
pages loaded the entire queue twice: once for display and once for the footer.

The footer now fetches only the saved association, scoped to the user's queue,
and its track. An unset position needs no queue lookup. Queue pages reuse their
already loaded items for the footer. `Queue()` loads associations and decorated
tracks with one joined query, preserving rank/id ordering, association metadata,
and the existing inclusion of tracks marked missing. Stale, foreign, and
non-queue positions produce the same empty footer without modifying preferences.

## Local measurements

Linux/amd64, AMD Ryzen 7 5700X, synthetic SQLite fixtures on `/tmp` (tmpfs),
20 warm, uncompressed requests through the real HTTP handler. Before measurements
use the previous committed `http.go` and `store.go` (now under
`internal/reinhardt/`) through a Go build overlay; both versions run the same
benchmark. Network, proxy, and browser costs are
excluded. These are not production measurements.

| Queue size | `/artists/` before → after | SQL calls | `/queue/` before → after | SQL calls |
| --- | ---: | ---: | ---: | ---: |
| 1 | 0.474 → 0.515 ms | 6 → 6 | 0.840 → 1.055 ms | 11 → 8 |
| 100 | 5.810 → 0.587 ms | 105 → 6 | 13.174 → 3.822 ms | 209 → 8 |
| 1,000 | 53.755 → 1.210 ms | 1,005 → 6 | 121.762 → 31.105 ms | 2,009 → 8 |

At 1,000 entries, `/artists/` allocations fall from about 7.86 MB to 128 KB;
`/queue/` allocations fall from 25.28 MB to 15.88 MB. Queue-page rendering still
scales with the displayed items. These measurements precede the subsequent
[search-suggestion caching change](suggestions-performance.md).

Regression tests enforce bounded SQL counts for full pages and Unpoly queue
fragments, validate joined track and association metadata, and cover tied ranks,
ownership, missing tracks, stale positions, and unchanged saved preferences.

## Read-only existing-tag lookups

Queue and favorites pages now look up an existing owner/label pair before
attempting tag creation. The previous `INSERT OR IGNORE` acquired a SQLite write
transaction and could advance the tag AUTOINCREMENT sequence even when the tag
already existed. Existing-tag lookups now use one indexed SELECT, preserve IDs,
timestamps and flags, and avoid the write transaction entirely.

Initialized queue pages make seven SQL calls for a full page or six for an
Unpoly fragment, down from eight and seven respectively, with zero writes.
Missing queue/favorites tags still initialize on first use; creation retains
`INSERT OR IGNORE` followed by a lookup to handle concurrent callers safely.
Existing preferences already use the same read-first approach.

Regression tests run initialized home, queue, favorites, preferences, tags,
artists and dropdown pages with SQLite `query_only=ON`, for both users, GET/HEAD
and full/Unpoly requests. They also check unchanged tag metadata and ID sequence,
first-use defaults, label normalization, owner isolation, lookup errors and
concurrent creation. Authentication actions and explicit database exports retain
their necessary writes.

To reproduce current measurements:

```sh
CGO_ENABLED=0 go test -mod=vendor ./internal/reinhardt -run '^$' \
  -bench BenchmarkFullPageQueue -benchtime=20x -count=1
```
