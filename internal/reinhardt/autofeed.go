package reinhardt

import (
	"container/list"
	"context"
	"math/rand/v2"
	"sync"
)

type autofeedKey struct {
	// An empty artist can still be a similarity restriction. Keep explicit mode
	// flags so neither it nor the new-artist fallback shares the unrestricted pool.
	owner                int64
	artist               string
	similar              bool
	excludeQueuedArtists bool
}

type autofeedVersion struct {
	// Tag identity matters as well as revision: deleting and recreating a reserved
	// tag can reset its revision while changing the owner's eligible tracks.
	library, blacklistID, blacklist, queueID, queue int64
}

type autofeedEntry struct {
	key     autofeedKey
	version autofeedVersion
	ids     []int64
}

type autofeedCache struct {
	mu      sync.Mutex
	entries map[autofeedKey]*list.Element
	recent  list.List
	ids     int
}

// Bound both artist/user entries and retained ID storage. Keep a single larger
// library usable even if it exceeds the ID budget; obsolete versions are replaced.
const autofeedMaxEntries, autofeedMaxIDs = 128, 1_000_000

func (cache *autofeedCache) put(entry autofeedEntry) {
	if cache.entries == nil {
		cache.entries = make(map[autofeedKey]*list.Element)
	}
	if old := cache.entries[entry.key]; old != nil {
		cache.ids -= cap(old.Value.(autofeedEntry).ids)
		cache.recent.Remove(old)
	}
	cache.entries[entry.key] = cache.recent.PushFront(entry)
	cache.ids += cap(entry.ids)
	for cache.recent.Len() > 1 && (cache.recent.Len() > autofeedMaxEntries || cache.ids > autofeedMaxIDs) {
		last := cache.recent.Back()
		old := last.Value.(autofeedEntry)
		delete(cache.entries, old.key)
		cache.ids -= cap(old.ids)
		cache.recent.Remove(last)
	}
}

func autofeedFilter(key autofeedKey) (string, []any) {
	clause := `m.missing=0 AND NOT EXISTS (
SELECT 1 FROM reinhardt_musicfiletag a JOIN reinhardt_tag t ON t.id=a.tag_id
WHERE a.music_file_id=m.id AND t.owner_id=? AND t.label='blacklist')`
	args := []any{key.owner}
	if key.similar {
		clause += " AND m.artist=?"
		args = append(args, key.artist)
	}
	if key.similar || key.excludeQueuedArtists {
		// Materialize the queued artist set once when building an ID pool, rather
		// than walking the queue separately for every candidate track.
		clause += ` AND m.artist NOT IN (
SELECT queued.artist FROM reinhardt_musicfiletag a
JOIN reinhardt_tag t ON t.id=a.tag_id
JOIN reinhardt_musicfile queued ON queued.id=a.music_file_id
WHERE t.owner_id=? AND t.label='queue')`
		args = append(args, key.owner)
	}
	return clause, args
}

func (app *App) autofeedIDs(ctx context.Context, key autofeedKey, refresh bool) ([]int64, error) {
	cache := &app.autofeed
	// Hold the lock through revision checking and pool construction so concurrent
	// requests share one scan. Published ID slices must never be modified in place.
	cache.mu.Lock()
	defer cache.mu.Unlock()
	var version autofeedVersion
	err := app.Store.DB.QueryRowContext(ctx, `SELECT l.revision,COALESCE(b.id,0),COALESCE(bv.revision,0),COALESCE(q.id,0),COALESCE(qv.revision,0)
FROM go_autofeed_library_revision l
LEFT JOIN reinhardt_tag b ON b.owner_id=? AND b.label='blacklist'
LEFT JOIN go_autofeed_tag_revision bv ON bv.tag_id=b.id
LEFT JOIN reinhardt_tag q ON q.owner_id=? AND q.label='queue' AND ?
LEFT JOIN go_autofeed_tag_revision qv ON qv.tag_id=q.id
WHERE l.id=1`, key.owner, key.owner, key.similar || key.excludeQueuedArtists).Scan(&version.library, &version.blacklistID, &version.blacklist, &version.queueID, &version.queue)
	if err != nil {
		return nil, err
	}
	if cached := cache.entries[key]; !refresh && cached != nil {
		entry := cached.Value.(autofeedEntry)
		if entry.version == version {
			cache.recent.MoveToFront(cached)
			return entry.ids, nil
		}
	}
	clause, args := autofeedFilter(key)
	rows, err := app.Store.DB.QueryContext(ctx, "SELECT m.id FROM reinhardt_musicfile m WHERE "+clause+" ORDER BY m.id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// Reading the version first can cause another refresh after a concurrent
	// commit, but never marks an older list as belonging to a newer revision.
	cache.put(autofeedEntry{key: key, version: version, ids: ids})
	return ids, nil
}

func (app *App) randomAutofeedTrack(ctx context.Context, owner int64, artist *string) (Row, error) {
	key := autofeedKey{owner: owner}
	if artist != nil {
		key.artist, key.similar = *artist, true
	}
	return app.sampleAutofeedTrack(ctx, key)
}

func (app *App) sampleAutofeedTrack(ctx context.Context, key autofeedKey) (Row, error) {
	clause, args := autofeedFilter(key)
	for attempt := range 3 {
		ids, err := app.autofeedIDs(ctx, key, attempt > 0)
		if err != nil || len(ids) == 0 {
			return nil, err
		}
		// Sample array positions uniformly; gaps in primary keys introduce no bias.
		id := ids[rand.IntN(len(ids))]
		selectedArgs := append([]any{id}, args...)
		rows, err := app.Store.DB.QueryContext(ctx, tracksSQL+"WHERE m.id=? AND "+clause, selectedArgs...)
		if err != nil {
			return nil, err
		}
		tracks, err := scanRows(rows)
		if err != nil {
			return nil, err
		}
		if len(tracks) > 0 {
			app.Store.decorateTrack(tracks[0])
			return tracks[0], nil
		}
		// A concurrent delete, blacklist edit, or missing/artist change can make
		// the sampled ID ineligible. Rebuild and retry instead of serving it.
	}
	return nil, nil
}
