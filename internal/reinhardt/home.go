package reinhardt

import (
	"context"
	"strings"
	"sync"
)

const latestAdditionsPageSize = 25

// Fetch one extra album to detect whether another page is available. Album
// breaks timestamp ties so successive pages use the same ordering.
const latestAdditionsOrder = "WHERE m.missing=0 GROUP BY m.album ORDER BY m.created_at DESC,m.album"
const latestAdditionsClause = latestAdditionsOrder + " LIMIT 26"
const latestAdditionsIDsSQL = "SELECT m.id FROM reinhardt_musicfile m JOIN reinhardt_musicdirectory d ON d.id=m.music_directory_id " + latestAdditionsOrder

type homeCache struct {
	mu sync.Mutex
	// An empty library is a cached result too, not a reason to repeat the scan.
	loaded   bool
	revision int64
	tracks   []*Track
	ids      []int64
}

func (app *App) latestAdditions(ctx context.Context) ([]*Track, error) {
	return app.latestAdditionsAt(ctx, 0)
}

func (app *App) latestAdditionsAt(ctx context.Context, offset int) ([]*Track, error) {
	cache := &app.home
	cache.mu.Lock()
	defer cache.mu.Unlock()

	var revision int64
	if err := app.Store.DB.QueryRowContext(ctx, "SELECT revision FROM go_home_revision WHERE id=1").Scan(&revision); err != nil {
		return nil, err
	}
	if !cache.loaded || cache.revision != revision {
		// Group only IDs and ordering fields, rather than copying full track rows
		// into SQLite's temporary sort tables. Keep the join and representative
		// selection of the original query, then hydrate just the requested page.
		ids, err := app.latestAdditionIDs(ctx)
		if err != nil {
			return nil, err
		}
		tracks, err := app.homeTracks(ctx, ids[:min(len(ids), latestAdditionsPageSize+1)])
		if err != nil {
			return nil, err
		}
		// Reading the revision first allows a concurrent commit to trigger an
		// extra refresh, but never marks old tracks with a newer revision.
		cache.tracks, cache.revision, cache.loaded = tracks, revision, true
		cache.ids = ids
	}
	selected := cache.tracks
	if offset > 0 {
		if offset >= len(cache.ids) {
			return []*Track{}, nil
		}
		end := offset + min(latestAdditionsPageSize+1, len(cache.ids)-offset)
		var err error
		selected, err = app.homeTracks(ctx, cache.ids[offset:end])
		if err != nil {
			return nil, err
		}
	}
	// Each request owns its rows. Derive URLs from the current configuration,
	// without retaining request changes or decorated paths in the shared cache.
	tracks := make([]*Track, 0, len(selected))
	for _, cached := range selected {
		track := *cached
		if cached.Hash != nil {
			hash := *cached.Hash
			track.Hash = &hash
		}
		app.Store.decorateTrack(&track)
		tracks = append(tracks, &track)
	}
	return tracks, nil
}

func (app *App) latestAdditionIDs(ctx context.Context) ([]int64, error) {
	rows, err := app.Store.DB.QueryContext(ctx, latestAdditionsIDsSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (app *App) homeTracks(ctx context.Context, ids []int64) ([]*Track, error) {
	if len(ids) == 0 {
		return []*Track{}, nil
	}
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := app.Store.Query(ctx, tracksSQL+"WHERE m.id IN ("+placeholders+")", args...)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*Track, len(ids))
	for _, track := range tracksFromRows(rows) {
		byID[track.ID] = track
	}
	tracks := make([]*Track, 0, len(ids))
	for _, id := range ids {
		if track := byID[id]; track != nil {
			tracks = append(tracks, track)
		}
	}
	return tracks, nil
}
