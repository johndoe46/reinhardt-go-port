# Next-song response time

`/next_song/` renders `_now_playing.html`, a partial. Template layout detection
already prevents preferences/footer/suggestion preparation during rendering,
whether or not `X-Up-Target` is present. The player normally submits this request
through Unpoly.

A separate playback bottleneck remained: `nextSong()` called `Queue()`, which
loaded all queue entries and executed an additional track query for every entry.
Selecting one song from a 1,000-track queue therefore made 1,005 SQL calls.

Playback now looks up only the current association and the requested or next
association, then loads the chosen track. Queries enforce the user's ownership
and the `queue` label. Normal advancement uses the existing `(tag_id, rank)`
index and `LIMIT 1`, preserving rank/id ordering. The previous track is loaded
only when similarity-based autofeed actually needs it. Existing preferences are
read before attempting initialization, removing the ignored insert and its
AUTOINCREMENT write from every playback request.

## Local measurements

Linux/amd64, AMD Ryzen 7 5700X, test-owned SQLite databases on `/tmp` (tmpfs),
20 warm requests selecting an existing queued song with the Unpoly header.
Measurements invoke the real HTTP handler and exclude network/proxy latency.

| Queue size | Before | After | SQL calls before → after |
| --- | ---: | ---: | ---: |
| 1 | 0.294 ms | 0.301 ms | 6 → 6 |
| 100 | 5.406 ms | 0.293 ms | 105 → 6 |
| 1,000 | 50.200 ms | 0.338 ms | 1,005 → 6 |

Writes decrease from two to one: the remaining write saves the selected playback
position. Plain POST requests show the same bounded query count and zero
navigation-suggestion queries. The 1,000-track request allocates about 42 KB,
down from about 7.7 MB.

Run `CGO_ENABLED=0 go test -mod=vendor ./internal/reinhardt -run '^$' -bench BenchmarkNextSong
-benchtime=20x -count=1` to reproduce current results.

Regression tests check queue-size-independent SQL counts, ownership, invalid
selections, tied ranks, blacklist autofeed, and empty libraries.

Autofeed is a separate path: at the end of a queue, enabled similarity matching
can wait for a cold or expired Last.fm lookup (up to five seconds). Warm lookups
use the cache described below. Random selection uses cached eligible IDs.
These operations are not performed when a queued song is available.

## Disabled similarity verification

`TestNextSongLastFMPreference` saves preferences through the browser checkbox
form and counts outbound HTTP requests through a fake transport. It covers
plain and Unpoly `/next_song/` requests and `/api/dj/next_song/`, including
available queued songs, exhausted and empty queues, explicit insertion, and
disabled autofeed. With similarity disabled, playback makes zero external HTTP
requests. An exhausted queue with both autofeed and similarity enabled makes
one mocked Last.fm request, providing a positive control. No live services are
contacted by these tests.

The separate `/similar_artists/` endpoint uses the shared Last.fm cache for
artist-page suggestions, fetching on a miss. The preference controls queue autofeed;
it does not disable artist-page suggestions. After playback updates, the browser
also requests artwork and refreshes `/queue/` when viewing the queue page.
Those are separate requests from the playback response.

Before the ID-cache change below, disabled-similarity random autofeed used
`ORDER BY random() LIMIT 1` over eligible library tracks and wrote the queue
association and playback position. Local warm measurements with the same AMD Ryzen 7 5700X and synthetic
SQLite fixtures on `/tmp`, 20 uncompressed Unpoly requests per case:

| Library size | Select queued song | Random autofeed | External calls |
| --- | ---: | ---: | ---: |
| 3 tracks | 0.317 ms | 0.596 ms | 0 |
| 10,000 tracks | 0.305 ms | 6.115 ms | 0 |
| 100,000 tracks | 0.313 ms | 59.708 ms | 0 |

At 100,000 tracks, random autofeed spends about 59.50 ms in SQL and row scanning,
with 11 SQL calls and three writes, compared with six calls and one write for
selecting a queued song. These pre-cache measurements do not establish the cause
of latency on the production server. Reproduce current behavior with:

```sh
CGO_ENABLED=0 go test -mod=vendor ./internal/reinhardt -run '^$' \
  -bench BenchmarkNextSongDisabledSimilarity -benchtime=20x -count=1
```

## Cached eligible IDs

Autofeed now caches eligible track IDs rather than sorting the library randomly
for each selection. Normal autofeed uses a stable user-ID key. Similarity
autofeed uses the user ID and artist, with a separate mode so an empty artist
name is distinct from unrestricted selection. Its initial random fallback has a
separate mode excluding queued artists. Each entry stores its library
revision, blacklist tag ID/revision, and, for both artist-excluding modes, queue tag
ID/revision. Uniform random array-index selection in Go followed by a primary-key
lookup preserves equal probability for eligible tracks despite gaps in IDs.

SQLite triggers maintain revisions for library insertion, deletion, artist,
missing-status, and ID changes, plus tag membership changes. Membership tracking
also covers tags temporarily renamed or transferred between users. Existing
database rows need no backfill: initial cache construction reads current contents,
and reserved tag identities detect creation, deletion, renaming, and ownership
changes. Startup adds the revision tables and triggers automatically; no manual
migration or search rebuild is required. External worker and CLI writes
invalidate caches too, while rolled-back writes do not.

Normal autofeed includes already queued tracks, matching the previous behavior;
queue growth and reordering therefore leave its cache valid. Similarity autofeed
and its initial random fallback exclude every artist represented in the owner's
queue, as well as blacklisted tracks, so membership changes refresh their pools.
If no new artist remains, a final random fallback uses the unrestricted cached
pool, still excluding missing and blacklisted tracks.
Unrelated metadata updates and another user's blacklist edits leave a warm pool
valid. Selected tracks are rechecked against current eligibility to handle a
concurrent delete, missing-status change, blacklist edit, or queued-artist change. Invalid selections
refresh and retry, up to three attempts, instead of returning stale tracks.

Concurrent requests share cache refreshes. Least-recently-used eviction bounds
storage to 128 entries and one million allocated ID slots (about 8 MB of array
storage), retaining a single entry if the library itself exceeds that budget.
Entries are replaced rather than accumulated for every revision. Initial loads,
invalidation, and eviction require an eligible-ID scan; warm selections do not.
Last.fm lookups remain separately controlled by the similarity preference.

Using the same local benchmark, after the cache change:

| Library size | Random autofeed before | Random autofeed after | Warm candidate-list loads |
| --- | ---: | ---: | ---: |
| 3 tracks | 0.596 ms | 0.623 ms | 0 |
| 10,000 tracks | 6.115 ms | 0.643 ms | 0 |
| 100,000 tracks | 59.708 ms | 0.644 ms | 0 |

The 100,000-track case spends about 0.49 ms in SQL, with 12 small SQL calls,
three writes, about 48 KB allocated per request, and zero external requests.
The additional SQL call checks revisions; the previous library-wide random
selection is replaced by a primary-key lookup. These are warm handler timings
excluding network/proxy/browser costs and cache construction.

Regression tests cover user and artist isolation, queue and blacklist edits,
tag renaming/ownership/membership changes, missing tracks, sparse IDs, external
writes, rollback, cascading deletes, empty pools, concurrent refreshes, stale
selection revalidation, eviction, and warm endpoint query bounds. All existing
Last.fm preference checks remain in place.

## Cached similar-artist lookups

Artist pages and similarity autofeed share a process-local cache of raw Last.fm
names, keyed by the artist's lowercase, trimmed name. Successful nonempty results
last one hour, successful empty lists five minutes, and transport, HTTP, JSON,
and Last.fm API failures one minute. Failed lookups keep the previous empty-result
behavior while avoiding repeated requests during an outage. Restarting the
application clears the remote cache.

Concurrent callers for the same artist share one in-flight lookup. Unrelated
artists can fetch concurrently; no cache mutex is held during network I/O.
Individual callers can cancel their wait without cancelling the lookup for
others. Shared requests retain an independent five-second deadline. Expired
entries refresh on the next request. Least-recently-used eviction limits the
cache to 128 entries and approximately 4 MiB of retained name/key storage,
including string-array capacity; one larger result may remain on its own.

Remote names are filtered against a separately cached normalized library-artist
map. The existing SQLite library revision invalidates this map after imports,
artist edits, deletes, and external writer changes. The original distinct-artist
query, sanitization, slug matching, ordering, duplicate/self filtering, and
inclusion of missing tracks are preserved. No matching-result list is retained,
so new or renamed local artists appear without another Last.fm request. Album,
title, preference, queue, and blacklist changes do not rebuild the artist map.
Returning a new filtered list also prevents callers from modifying cached names.
No additional database migration, dependencies, or configuration are needed.

Local warm `/similar_artists/?q=Artist` measurements on the same Ryzen 7 5700X,
20 uncompressed Unpoly requests with synthetic SQLite fixtures on `/tmp`:

| Library size | Before | After | Last.fm calls/request | Artist-map loads/request |
| --- | ---: | ---: | ---: | ---: |
| 3 tracks | 10.422 ms | 0.121 ms | 1 → 0 | 1 → 0 |
| 10,000 tracks | 12.234 ms | 0.124 ms | 1 → 0 | 1 → 0 |
| 100,000 tracks | 29.206 ms | 0.139 ms | 1 → 0 | 1 → 0 |

The API transport is mocked with a controlled 10 ms delay; no live Last.fm
requests occur. Before measurements use a build overlay with the previous
uncached lookup, and both versions run the same benchmark. Warm requests make
two small SQL reads (session/user and library revision). At 100,000 tracks,
allocations fall from about 6.21 MB to 21 KB per request. These timings exclude
network/proxy/browser costs, initial lookup/map construction, and expiration
refreshes, and are not production measurements.

Tests cover cache expiry, empty results, transport/HTTP/JSON/API failures,
concurrent requests, cancellation, independent artists, eviction, normalization,
result isolation, library edits/imports/deletes/rollback/external writes, and
sharing between artist-page requests and playback. Existing disabled-similarity
checks continue to enforce zero Last.fm calls during playback.

```sh
CGO_ENABLED=0 go test -mod=vendor ./internal/reinhardt -run '^$' \
  -bench BenchmarkSimilarArtists -benchtime=20x -count=1
```

## Avoiding existing-tag writes

The read-first tag lookup used by queue/favorites pages also removes an ignored
tag insert during autofeed. Warm random autofeed now uses eleven SQL calls and
two writes, down from twelve calls and three writes in the earlier cache
measurements. Only the necessary queue association and playback-position writes
remain. Regression tests enforce these limits for normal, Unpoly and DJ API
requests. Selecting an existing queue item is unchanged.

## Excluding artists already queued

With `autofeed_similar` enabled, Last.fm candidate selection and the initial
random fallback reject tracks whose artist is already represented anywhere in the
user's queue, including by tracks marked missing. Another unqueued track by the
same artist is also rejected. Another user's queue does not restrict selection.
The cached candidate query and the final sampled-track lookup apply the same
artist exclusion; queued artists are gathered in one SQL subquery rather than
loading and decorating every queue track in Go.

The fallback cache has its own mode and tracks queue identity/membership revisions,
so it cannot reuse an unrestricted pool created with similarity disabled. Existing
library revisions detect artist edits, and existing queue revisions detect
membership changes from the web app, workers, CLI or external writers. If no
eligible new artist remains, enabled autofeed picks a random available,
nonblacklisted track from any artist using the existing unrestricted pool.
An unqueued track is appended; an already queued track moves to the end and
plays again. Only a library with no eligible tracks produces the empty-player
response and resets the playback position. Ordinary queue advancement and
explicit selections retain their existing behavior.

Regression tests cover similar results, exhausted recommendations, Last.fm
failure, all artists already queued, random requeueing, unavailable tracks,
user isolation, mode separation, cached queue edits, external artist/membership
changes, missing queued tracks, blacklist
filters and stale-sample revalidation across normal, Unpoly and DJ API requests.
