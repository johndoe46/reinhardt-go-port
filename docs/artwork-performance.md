# Artwork discovery concurrency

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
artwork precedence and fallback sleeves are preserved. No dependency, database
or configuration change is needed.

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
