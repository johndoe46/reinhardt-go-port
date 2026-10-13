package reinhardt

import "sync"

const latestAdditionsClause = "WHERE m.missing=0 GROUP BY m.album ORDER BY m.created_at DESC LIMIT 25"

type homeCache struct {
	mu sync.Mutex
	// An empty library is a cached result too, not a reason to repeat the scan.
	loaded   bool
	revision int64
	tracks   []Row
}

func (app *App) latestAdditions() ([]Row, error) {
	cache := &app.home
	cache.mu.Lock()
	defer cache.mu.Unlock()

	var revision int64
	if err := app.Store.DB.QueryRow("SELECT revision FROM go_home_revision WHERE id=1").Scan(&revision); err != nil {
		return nil, err
	}
	if !cache.loaded || cache.revision != revision {
		// Keep the original SQLite grouping and representative-track selection:
		// changing the query or its indexes can change the displayed rows.
		tracks, err := app.Store.Query(tracksSQL + latestAdditionsClause)
		if err != nil {
			return nil, err
		}
		// Reading the revision first allows a concurrent commit to trigger an
		// extra refresh, but never marks old tracks with a newer revision.
		cache.tracks, cache.revision, cache.loaded = tracks, revision, true
	}
	// Each request owns its rows. Derive URLs from the current configuration,
	// without retaining request changes or decorated paths in the shared cache.
	tracks := make([]Row, 0, len(cache.tracks))
	for _, cached := range cache.tracks {
		track := make(Row, len(cached)+4)
		for key, value := range cached {
			track[key] = value
		}
		app.Store.decorateTrack(track)
		tracks = append(tracks, track)
	}
	return tracks, nil
}
