package reinhardt

import (
	"fmt"
	"reflect"
	"testing"
)

func TestInitializedPagesDoNotWriteOnGETOrHEAD(t *testing.T) {
	a := testApp(t)
	for _, owner := range []int64{1, 2} {
		mustTag(t, a, owner, "queue", []int64{owner})
		mustTag(t, a, owner, "favorites", []int64{owner})
	}
	before, err := a.S.Query("SELECT * FROM reinhardt_tag ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := a.S.One("SELECT seq FROM sqlite_sequence WHERE name='reinhardt_tag'")
	if err != nil {
		t.Fatal(err)
	}
	stats := measureAppSQL(t, a)
	// A write attempt must fail even when INSERT OR IGNORE would affect no rows.
	// The test connection pool has one connection, so every request uses this mode.
	if _, err = a.S.DB.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []int{1, 2} {
		for _, method := range []string{"GET", "HEAD"} {
			for _, fragment := range []bool{false, true} {
				for _, path := range []string{"/", "/queue/", "/favorites/", "/preferences/", "/tags/", "/artists/", "/music_file_dropdown/?q=1"} {
					t.Run(fmt.Sprintf("owner=%d/%s/unpoly=%v%s", owner, method, fragment, path), func(t *testing.T) {
						stats.writes.Store(0)
						requireCode(t, call(a, method, path, owner, nil, fragment), 200)
						if writes := stats.writes.Load(); writes != 0 {
							t.Fatalf("read request attempted %d database writes", writes)
						}
					})
				}
			}
		}
	}
	after, err := a.S.Query("SELECT * FROM reinhardt_tag ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("page reads changed tag IDs, metadata, or timestamps")
	}
	afterSequence, err := a.S.One("SELECT seq FROM sqlite_sequence WHERE name='reinhardt_tag'")
	if err != nil {
		t.Fatal(err)
	}
	if sequence.N("seq") != afterSequence.N("seq") {
		t.Fatal("page reads advanced the tag ID sequence")
	}
}

func TestTagPagesInitializeOnlyMissingTags(t *testing.T) {
	a := testApp(t)
	stats := measureAppSQL(t, a)
	for _, owner := range []int{1, 2} {
		for _, name := range []string{"queue", "favorites"} {
			stats.writes.Store(0)
			requireCode(t, call(a, "GET", "/"+name+"/", owner, nil, false), 200)
			if writes := stats.writes.Load(); writes != 1 {
				t.Fatalf("first %s visit for owner %d made %d writes, want one tag creation", name, owner, writes)
			}
			tag, err := a.S.One("SELECT * FROM reinhardt_tag WHERE owner_id=? AND label=?", owner, name)
			if err != nil {
				t.Fatal(err)
			}
			if tag.B("is_playlist") || tag.B("is_shared") || tag.S("created_at") == "" || tag.S("modified_at") == "" {
				t.Fatal("new tag defaults or timestamps changed")
			}
			stats.writes.Store(0)
			requireCode(t, call(a, "GET", "/"+name+"/", owner, nil, true), 200)
			if writes := stats.writes.Load(); writes != 0 {
				t.Fatalf("repeat %s visit made %d unnecessary writes", name, writes)
			}
		}
	}
}

func TestExistingTagLookupPreservesNormalizationAndOwner(t *testing.T) {
	a := testApp(t)
	id := mustTag(t, a, 1, "my list", nil)
	otherID := mustTag(t, a, 2, "my list", nil)
	if _, err := a.S.DB.Exec("UPDATE reinhardt_tag SET is_playlist=1,is_shared=1 WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	stats := measureAppSQL(t, a)
	for _, owner := range []int64{1, 2} {
		stats.calls.Store(0)
		stats.writes.Store(0)
		got, err := a.S.AddTag(owner, "  MY\t List\n")
		if err != nil {
			t.Fatal(err)
		}
		want := id
		if owner == 2 {
			want = otherID
		}
		if got != want || stats.calls.Load() != 1 || stats.writes.Load() != 0 {
			t.Fatalf("owner %d: id=%d, want %d; SQL=%d writes=%d", owner, got, want, stats.calls.Load(), stats.writes.Load())
		}
	}
	tag, err := a.S.Tag(id, 1)
	if err != nil || !tag.B("is_playlist") || !tag.B("is_shared") {
		t.Fatal("lookup changed existing tag flags", err)
	}
	stats.calls.Store(0)
	if _, err := a.S.AddTag(1, " \t\n"); err == nil || stats.calls.Load() != 0 {
		t.Fatal("empty normalized label must fail before any SQL")
	}
	if err := a.S.DB.Close(); err != nil {
		t.Fatal(err)
	}
	stats.writes.Store(0)
	if _, err := a.S.AddTag(1, "new list"); err == nil || stats.writes.Load() != 0 {
		t.Fatal("lookup errors must be returned without attempting creation")
	}
}

func TestConcurrentTagCreationReturnsOneID(t *testing.T) {
	a := testApp(t)
	a.S.DB.SetMaxOpenConns(8)
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
			id, err := a.S.AddTag(1, "  Concurrent\t List  ")
			results <- result{id, err}
		}()
	}
	close(start)
	var want int64
	for range callers {
		r := <-results
		if r.err != nil {
			t.Error(r.err)
			continue
		}
		if want == 0 {
			want = r.id
		}
		if r.id != want || r.id == 0 {
			t.Errorf("concurrent lookup returned %d, want %d", r.id, want)
		}
	}
	tags, err := a.S.Query("SELECT id FROM reinhardt_tag WHERE owner_id=1 AND label='concurrent list'")
	if err != nil || len(tags) != 1 || tags[0].N("id") != want {
		t.Fatal("concurrent creation must leave exactly one shared tag", tags, err)
	}
}
