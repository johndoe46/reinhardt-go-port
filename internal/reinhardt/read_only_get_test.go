package reinhardt

import (
	"fmt"
	"reflect"
	"testing"
)

func TestInitializedPagesDoNotWriteOnGETOrHEAD(test *testing.T) {
	app := testApp(test)
	for _, owner := range []int64{1, 2} {
		mustTag(test, app, owner, "queue", []int64{owner})
		mustTag(test, app, owner, "favorites", []int64{owner})
	}
	before, err := app.Store.Query(test.Context(), "SELECT * FROM reinhardt_tag ORDER BY id")
	if err != nil {
		test.Fatal(err)
	}
	sequence, err := app.Store.One(test.Context(), "SELECT seq FROM sqlite_sequence WHERE name='reinhardt_tag'")
	if err != nil {
		test.Fatal(err)
	}
	stats := measureAppSQL(test, app)
	// A write attempt must fail even when INSERT OR IGNORE would affect no rows.
	// The test connection pool has one connection, so every request uses this mode.
	if _, err = app.Store.DB.Exec("PRAGMA query_only=ON"); err != nil {
		test.Fatal(err)
	}
	for _, owner := range []int{1, 2} {
		for _, method := range []string{"GET", "HEAD"} {
			for _, fragment := range []bool{false, true} {
				for _, path := range []string{"/", "/queue/", "/favorites/", "/preferences/", "/tags/", "/artists/", "/music_file_dropdown/?q=1"} {
					test.Run(fmt.Sprintf("owner=%d/%s/unpoly=%v%s", owner, method, fragment, path), func(test *testing.T) {
						stats.writes.Store(0)
						requireCode(test, call(app, method, path, owner, nil, fragment), 200)
						if writes := stats.writes.Load(); writes != 0 {
							test.Fatalf("read request attempted %d database writes", writes)
						}
					})
				}
			}
		}
	}
	after, err := app.Store.Query(test.Context(), "SELECT * FROM reinhardt_tag ORDER BY id")
	if err != nil {
		test.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		test.Fatal("page reads changed tag IDs, metadata, or timestamps")
	}
	afterSequence, err := app.Store.One(test.Context(), "SELECT seq FROM sqlite_sequence WHERE name='reinhardt_tag'")
	if err != nil {
		test.Fatal(err)
	}
	if sequence.N("seq") != afterSequence.N("seq") {
		test.Fatal("page reads advanced the tag ID sequence")
	}
}

func TestTagPagesInitializeOnlyMissingTags(test *testing.T) {
	app := testApp(test)
	stats := measureAppSQL(test, app)
	for _, owner := range []int{1, 2} {
		for _, name := range []string{"queue", "favorites"} {
			stats.writes.Store(0)
			requireCode(test, call(app, "GET", "/"+name+"/", owner, nil, false), 200)
			if writes := stats.writes.Load(); writes != 1 {
				test.Fatalf("first %s visit for owner %d made %d writes, want one tag creation", name, owner, writes)
			}
			tag, err := app.Store.tag(test.Context(), "SELECT * FROM reinhardt_tag WHERE owner_id=? AND label=?", owner, name)
			if err != nil {
				test.Fatal(err)
			}
			if tag.IsPlaylist || tag.IsShared || tag.CreatedAt == "" || tag.ModifiedAt == "" {
				test.Fatal("new tag defaults or timestamps changed")
			}
			stats.writes.Store(0)
			requireCode(test, call(app, "GET", "/"+name+"/", owner, nil, true), 200)
			if writes := stats.writes.Load(); writes != 0 {
				test.Fatalf("repeat %s visit made %d unnecessary writes", name, writes)
			}
		}
	}
}

func TestExistingTagLookupPreservesNormalizationAndOwner(test *testing.T) {
	app := testApp(test)
	id := mustTag(test, app, 1, "my list", nil)
	otherID := mustTag(test, app, 2, "my list", nil)
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_tag SET is_playlist=1,is_shared=1 WHERE id=?", id); err != nil {
		test.Fatal(err)
	}
	stats := measureAppSQL(test, app)
	for _, owner := range []int64{1, 2} {
		stats.calls.Store(0)
		stats.writes.Store(0)
		got, err := app.Store.AddTag(test.Context(), owner, "  MY\t List\n")
		if err != nil {
			test.Fatal(err)
		}
		want := id
		if owner == 2 {
			want = otherID
		}
		if got != want || stats.calls.Load() != 1 || stats.writes.Load() != 0 {
			test.Fatalf("owner %d: id=%d, want %d; SQL=%d writes=%d", owner, got, want, stats.calls.Load(), stats.writes.Load())
		}
	}
	tag, err := app.Store.Tag(test.Context(), id, 1)
	if err != nil || !tag.IsPlaylist || !tag.IsShared {
		test.Fatal("lookup changed existing tag flags", err)
	}
	stats.calls.Store(0)
	if _, err := app.Store.AddTag(test.Context(), 1, " \t\n"); err == nil || stats.calls.Load() != 0 {
		test.Fatal("empty normalized label must fail before any SQL")
	}
	if err := app.Store.DB.Close(); err != nil {
		test.Fatal(err)
	}
	stats.writes.Store(0)
	if _, err := app.Store.AddTag(test.Context(), 1, "new list"); err == nil || stats.writes.Load() != 0 {
		test.Fatal("lookup errors must be returned without attempting creation")
	}
}

func TestConcurrentTagCreationReturnsOneID(test *testing.T) {
	app := testApp(test)
	app.Store.DB.SetMaxOpenConns(8)
	type result struct {
		id  int64
		err error
	}
	const callers = 32
	start := make(chan struct{})
	results := make(chan result, callers)
	for range callers {
		go func() {
			<-start
			id, err := app.Store.AddTag(test.Context(), 1, "  Concurrent\t List  ")
			results <- result{id, err}
		}()
	}
	close(start)
	var want int64
	for range callers {
		result := <-results
		if result.err != nil {
			test.Error(result.err)
			continue
		}
		if want == 0 {
			want = result.id
		}
		if result.id != want || result.id == 0 {
			test.Errorf("concurrent lookup returned %d, want %d", result.id, want)
		}
	}
	tags, err := app.Store.Query(test.Context(), "SELECT id FROM reinhardt_tag WHERE owner_id=1 AND label='concurrent list'")
	if err != nil || len(tags) != 1 || tags[0].N("id") != want {
		test.Fatal("concurrent creation must leave exactly one shared tag", tags, err)
	}
}
