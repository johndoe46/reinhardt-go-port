package reinhardt

import "sync"

type suggestionCache struct {
	mu       sync.Mutex
	loaded   bool
	revision int64
	words    []string
}

// suggestedData returns an immutable list, shared by full-page renders. SQLite
// triggers advance the revision for relevant library changes, including writes
// from other processes. Unrelated writes do not invalidate this cache.
func (app *App) suggestedData() ([]string, error) {
	cache := &app.suggestions
	cache.mu.Lock()
	defer cache.mu.Unlock()

	version, err := app.Store.One("SELECT revision FROM go_suggestions_revision WHERE id=1")
	if err != nil {
		return nil, err
	}
	revision := version.N("revision")
	if cache.loaded && cache.revision == revision {
		return cache.words, nil
	}
	// Suggestions list sorted artists followed by sorted albums. A name used in both
	// roles appears twice; deduplicating the final list would change its contents.
	rows, err := app.Store.Query(`SELECT word FROM (SELECT DISTINCT artist word,0 category FROM reinhardt_musicfile UNION ALL SELECT DISTINCT album word,1 category FROM reinhardt_musicfile) ORDER BY category,word`)
	if err != nil {
		return nil, err
	}
	words := make([]string, 0, len(rows))
	for _, row := range rows {
		words = append(words, row.S("word"))
	}
	// Read the revision before the list: a concurrent commit can cause an extra
	// refresh on the next request, but cannot label old words with a newer revision.
	cache.words, cache.revision, cache.loaded = words, revision, true
	return words, nil
}
