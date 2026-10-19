# Search-suggestion performance

Authenticated full-page renders previously queried every distinct artist and
album on every request. The Go template also copied the page context and built
loop metadata for each option, although the option uses only the suggested word.

The application now shares an immutable suggestion list across full-page
renders. Each request checks a single indexed revision row; the library query
runs only on first use or after a relevant change. Concurrent requests share
one refresh. The datalist uses a direct native template range, keeping its
options, ordering, and HTML escaping while avoiding those per-option maps.

## Invalidation and database compatibility

Opening a database automatically adds `go_suggestions_revision` and three SQLite
triggers. Inserts, deletes, and changes to artist or album advance the revision
in the same transaction as the library write. This covers indexing, imports,
metadata edits, cascading deletes, and writes from separate worker, CLI, or
external processes. Rolled-back writes do not invalidate the list. Preferences,
queue changes, other metadata, and unchanged artist/album updates leave it cached.

Existing databases require no manual migration or search reindex for
this change. Suggestions retain the original query semantics: distinct artists
followed by distinct albums, including empty strings, missing tracks, and a word
appearing in both categories. Empty libraries are cached too. Partials and
Unpoly fragments skip suggestion preparation.

## Local measurements

Linux/amd64, AMD Ryzen 7 5700X, synthetic SQLite databases on `/tmp` (tmpfs),
20 warm, uncompressed `/preferences/` requests through the real HTTP handler.
Before measurements use a build overlay retaining the renderer, base template,
and schema from immediately before this change. Both versions run the same
benchmark. Network, proxy, and browser costs are excluded.

| Library size | Before | After |
| --- | ---: | ---: |
| 3 tracks | 0.383 ms | 0.282 ms |
| 10,000 tracks | 16.575 ms | 2.517 ms |
| 100,000 tracks | 167.890 ms | 21.034 ms |

At 100,000 tracks, SQL and row-scanning time falls from 63.28 ms to about 0.39 ms;
request allocations fall from 73.95 MB to 15.74 MB. Cached requests make zero
suggestion-list queries, retaining three small SQL reads for session/user,
preferences, and revision. Caching alone measured 100.419 ms at that size before
simplifying the template loop. The complete datalist still renders and transfers
on full-page loads, so response size and rendering cost still grow with the
number of distinct suggestions. These are local measurements, not production
timings.

Regression tests cover ordering, duplicate words across categories, empty
values/libraries, missing tracks, local and external edits, rollback, cascading
deletion, concurrent refreshes, shared caches across users, and HTML escaping.

To reproduce current measurements:

```sh
CGO_ENABLED=0 go test -mod=vendor ./internal/reinhardt -run '^$' \
  -bench BenchmarkFullPageSuggestions -benchtime=20x -count=1
```
