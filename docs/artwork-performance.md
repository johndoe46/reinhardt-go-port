# Artwork caching

## Shared thumbnail storage

Thumbnail filenames use `thumb-v1-<sha256>-<size>-square.jpg`, where the digest
covers the source image's dimensions and canonical 8-bit RGBA pixels. Track IDs,
artist/album tags, and requested sizes do not contribute to image identity.
Identical covers embedded in different tracks or stored as lossless sidecars
therefore share one thumbnail at each requested size. Different covers on tracks
with the same album tags stay separate. Visually similar images with different
pixels, such as independently re-encoded JPEGs, may have different hashes.

The additive `go_track_artwork` SQLite table maps tracks to image hashes. A warm
request reads that reference and serves the finished JPEG without reopening the
audio file, decoding an image, or writing SQLite. References and thumbnails both
survive restarts. If a size has not been generated yet or a cache file has been
removed, the server reads the original artwork and generates the missing file.
The `v1` component can be changed when cropping or JPEG encoding changes.

Cold requests retain the artwork source order: a track-specific sidecar,
embedded artwork, a nearby directory image, then online discovery. When multiple
sidecar formats exist for a track, the newest file wins, allowing a new PNG
upload to override an older discovery JPG without removing either library file.
Discovered images use their saved JPEG's pixels consistently, including after a
restart. Thumbnail generation for the same hash and size is shared among
concurrent callers. Files are published atomically; another process publishing
the same thumbnail is harmless.

Uploads and library metadata/path edits invalidate the affected track's
reference. SQLite triggers advance a revision so an older request cannot restore
its artwork after an edit, even when another server or CLI made that edit.
Invalidation does not delete shared thumbnails used by other tracks. Changes
rolled back in SQLite also roll back reference invalidation.

### Browser cache keys

Resolved artwork uses `/artwork/v1/<sha256>/<size>.jpg` in track grids, artwork
modals, shared playlists and player/Media Session metadata. Every track sharing
that image therefore uses the same browser cache entry. Successful hash responses
send `Cache-Control: public, max-age=31536000, immutable` and a stable ETag;
HEAD and conditional requests retain those headers. Warm hash requests serve
the file without reading SQLite or the source audio.

Unresolved tracks use the existing track lookup endpoint with
`?v=v1:<modified_at>`. Including the rendering version bypasses old per-track
image responses cached before redirects were introduced. After resolving and
generating artwork, the endpoint redirects to its canonical hash URL. The
redirect has a one-hour lifetime, and an upload changes the lookup version.
Ordinary track queries include the current artwork reference without another
SQL round trip. Cached home metadata refreshes its mappings in a batch, only
when artwork URLs are needed. Each thumbnail size therefore does not need a
separate mapping query, and cached metadata does not retain stale image URLs.

A hash request at a new size can render it from another track carrying the same
artwork. Its decoded pixels must match the requested hash before publication.
Missing images, unsupported versions and invalid hashes return uncached errors;
a fallback or an externally changed source is never served as immutable artwork.
Uploads change the edited track's hash URL without changing any existing URL's
image bytes. Legacy thumbnails without originals still use their track URL.

Generated sleeves share only concurrent rendering work, keyed separately by
artist, album, title, size, and rendering version. They are never persisted or
mapped as real artwork, and keep their one-hour HTTP cache lifetime. Actual
artwork uses the immutable one-year hash response policy above.

Existing databases adopt the new table and triggers at startup without changing
library metadata. Per-track thumbnails are replaced lazily after artwork can be
resolved and a shared thumbnail is ready. If originals and discovery are
unavailable, the server continues serving the legacy cached image.

The background worker checks the cache hourly and removes unreferenced shared
thumbnails older than 24 hours. It also removes obsolete legacy thumbnails once a
track has a shared reference, or after that track is deleted. Unresolved legacy
tracks retain their cached artwork; unrelated files are left alone. A SQLite
write transaction protects the reference check and unlink against another
process registering a reference. The grace period protects newly generated
files. Deployments using `serve --no-worker` need a separate worker for automatic
cleanup. No original audio or sidecar files are pruned.

## Artwork discovery concurrency

Cover discovery previously held one application-wide mutex throughout Apple
search requests, artwork downloads, image decoding, JPEG encoding and sidecar
creation. A slow cache miss blocked discovery for unrelated albums and even
requests whose discovery result was already cached.

The mutex now protects only the completed-result cache and in-flight lookup map.
External requests, decoding, encoding and file writes run outside it. Each
normalized artist/album key has one shared lookup; a title replaces the album
when the album is empty. Different keys proceed concurrently, while multiple
tracks or requested thumbnail sizes for the same key share the search/download
and decoded image. Completed flights are removed after publishing their result.

Each caller can stop waiting when its request context is cancelled. The shared
lookup uses an independent eight-second network deadline so a cancelled caller
does not abort another caller's work or cache a cancellation as a failed search.
Successes and misses retain the one-hour cache lifetime. The existing matching,
image validation, atomic sidecar creation, thumbnail behavior, local/embedded
artwork precedence and fallback sleeves are preserved. Discovery itself needs no
extra configuration or dependency.

Regression tests use mocked transports and temporary media directories. They
block one search while unrelated discovery and cached results complete, issue
16 concurrent artwork requests across six sizes and two tracks in the same
album with exactly one search and download, validate returned JPEGs, and cover
leader cancellation, the shared deadline, cached failures, expiry, and flight
cleanup. Existing upload/cache tests continue to check local artwork precedence
and fallback behavior. Tests do not contact Apple or other external services.

```sh
make test TEST='Test.*Artwork' TEST_FLAGS=-count=1
make test-race TEST='Test.*Artwork' TEST_FLAGS=-count=1
```

## Generated sleeve labels

Fallback sleeves previously used the ASCII-only `basicfont.Face7x13`, which
rendered accented characters as missing-glyph boxes. Labels now use the embedded
Go Mono font from the existing `golang.org/x/image` dependency, with the OpenType
rasterizer from the same module. The parsed font is shared; each render creates
its own face because glyph buffers are mutable. This preserves concurrent
artwork rendering without a font lock.

The [Go font character set](https://go.dev/blog/go-fonts) covers European accented
Latin letters, Greek, Cyrillic and common symbols. Labels are normalized to NFC
before drawing and rune truncation, so composed letters and base letters with
combining accents render alike. The 12px monospaced font fits the existing
62-character limit, margins and two baseline positions. The record motif,
colors, thumbnail sizes and fallback cache behavior are unchanged.

Font data and rasterizer packages are vendored and compiled into the executable;
dependency versions are unchanged. No installed fonts, font downloads, helper
programs or CGO are needed. Tests check glyph coverage, visible accents,
normalization, truncation/margins and deterministic concurrent sleeve rendering.
