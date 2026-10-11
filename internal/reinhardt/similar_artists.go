package reinhardt

import (
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// Empty and failed lookups are useful cache entries: they prevent every next
	// song from repeating an unavailable request, but expire sooner than successes.
	similarSuccessTTL = time.Hour
	similarEmptyTTL   = 5 * time.Minute
	similarFailureTTL = time.Minute
	similarMaxEntries = 128
	similarMaxBytes   = 4 << 20
)

type similarLookupEntry struct {
	artist  string
	names   []string
	expires time.Time
}

func (e similarLookupEntry) size() int {
	// Include string-array capacity as well as text; 16 bytes per string header
	// matches the supported amd64/arm64 builds and conservatively bounds 32-bit builds.
	n := len(e.artist) + cap(e.names)*16
	for _, name := range e.names {
		n += len(name)
	}
	return n
}

type similarLookupFlight struct {
	done  chan struct{}
	names []string
}

type similarLookupCache struct {
	mu       sync.Mutex
	entries  map[string]*list.Element
	recent   list.List
	bytes    int
	inflight map[string]*similarLookupFlight
}

func (c *similarLookupCache) put(entry similarLookupEntry) {
	if c.entries == nil {
		c.entries = make(map[string]*list.Element)
	}
	if old := c.entries[entry.artist]; old != nil {
		c.bytes -= old.Value.(similarLookupEntry).size()
		c.recent.Remove(old)
	}
	c.entries[entry.artist] = c.recent.PushFront(entry)
	c.bytes += entry.size()
	for c.recent.Len() > 1 && (c.recent.Len() > similarMaxEntries || c.bytes > similarMaxBytes) {
		last := c.recent.Back()
		old := last.Value.(similarLookupEntry)
		delete(c.entries, old.artist)
		c.bytes -= old.size()
		c.recent.Remove(last)
	}
}

func (a *App) cachedSimilarNames(ctx context.Context, artist string) []string {
	if ctx.Err() != nil {
		return nil
	}
	key := strings.ToLower(strings.TrimSpace(artist))
	cache := &a.similar
	cache.mu.Lock()
	if cached := cache.entries[key]; cached != nil {
		entry := cached.Value.(similarLookupEntry)
		if time.Now().Before(entry.expires) {
			cache.recent.MoveToFront(cached)
			cache.mu.Unlock()
			// These are immutable remote names. Filtering against the library must
			// build a separate result, so imports can take effect during this TTL.
			return entry.names
		}
	}
	flight := cache.inflight[key]
	if flight == nil {
		if cache.inflight == nil {
			cache.inflight = make(map[string]*similarLookupFlight)
		}
		flight = &similarLookupFlight{done: make(chan struct{})}
		cache.inflight[key] = flight
		// A cancelled caller must not cancel the lookup shared by other callers.
		// The independent five-second deadline still bounds every external lookup.
		lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		go func() {
			defer cancel()
			names, err := a.fetchSimilarNames(lookupCtx, artist)
			ttl := similarSuccessTTL
			if err != nil {
				names, ttl = nil, similarFailureTTL
			} else if len(names) == 0 {
				ttl = similarEmptyTTL
			}
			cache.mu.Lock()
			cache.put(similarLookupEntry{artist: key, names: names, expires: time.Now().Add(ttl)})
			flight.names = names
			delete(cache.inflight, key)
			// Closing done publishes names to waiters, which read without the mutex.
			// Assign the result before closing the channel.
			close(flight.done)
			cache.mu.Unlock()
		}()
	}
	cache.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil
	case <-flight.done:
		return flight.names
	}
}

func (a *App) fetchSimilarNames(ctx context.Context, artist string) ([]string, error) {
	query := url.Values{"method": {"artist.getsimilar"}, "artist": {artist}, "api_key": {"6dd57d914fac3c0f2107cad48833db39"}, "format": {"json"}}
	b, _, err := a.request(ctx, "GET", "https://ws.audioscrobbler.com/2.0/?"+query.Encode(), nil, nil)
	if err != nil {
		return nil, err
	}
	var data struct {
		Error   int
		Message string
		Similar struct {
			Artists []struct{ Name string } `json:"artist"`
		} `json:"similarartists"`
	}
	if err = json.Unmarshal(b, &data); err != nil {
		return nil, err
	}
	if data.Error != 0 {
		return nil, fmt.Errorf("Last.fm error %d: %s", data.Error, data.Message)
	}
	names := make([]string, 0, len(data.Similar.Artists))
	for _, artist := range data.Similar.Artists {
		names = append(names, artist.Name)
	}
	return names, nil
}

type libraryArtistCache struct {
	mu       sync.Mutex
	loaded   bool
	revision int64
	names    map[string]string
}

func (a *App) libraryArtistNames(ctx context.Context) (map[string]string, error) {
	cache := &a.artistNames
	cache.mu.Lock()
	defer cache.mu.Unlock()
	var revision int64
	if err := a.S.DB.QueryRowContext(ctx, "SELECT revision FROM go_autofeed_library_revision WHERE id=1").Scan(&revision); err != nil {
		return nil, err
	}
	if cache.loaded && cache.revision == revision {
		return cache.names, nil
	}
	rows, err := a.S.DB.QueryContext(ctx, "SELECT DISTINCT artist FROM reinhardt_musicfile")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		// Slugs intentionally collapse spelling variants. Collisions use the last
		// row returned; sorting this query can change the selected library spelling.
		names[slug(name)] = name
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// Publish an immutable map. A concurrent commit can cause an extra refresh,
	// but reading the revision first cannot label older names with a newer revision.
	cache.names, cache.revision, cache.loaded = names, revision, true
	return names, nil
}
