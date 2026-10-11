package reinhardt

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestAutofeedEligibilityAndInvalidation(t *testing.T) {
	a := testApp(t)
	stats := measureAppSQL(t, a)
	normal := autofeedKey{owner: 1}
	other := autofeedKey{owner: 2}
	artist := autofeedKey{owner: 1, artist: "Artist", similar: true}
	check := func(key autofeedKey, want []int64, loads int64) {
		t.Helper()
		before := stats.autofeedLoads.Load()
		for range 2 {
			ids, err := a.autofeedIDs(context.Background(), key, false)
			if err != nil || !reflect.DeepEqual(ids, want) {
				t.Fatalf("%+v: got %v, want %v: %v", key, ids, want, err)
			}
		}
		if got := stats.autofeedLoads.Load() - before; got != loads {
			t.Fatalf("%+v loaded candidates %d times, want %d", key, got, loads)
		}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := a.S.DB.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	check(normal, []int64{1, 2, 3}, 1)
	check(artist, []int64{1, 2}, 1)
	blacklist := mustTag(t, a, 1, "blacklist", []int64{2})
	check(normal, []int64{1, 3}, 1)
	check(artist, []int64{1}, 1)
	mustTag(t, a, 2, "blacklist", []int64{3})
	check(other, []int64{1, 2}, 1)
	check(normal, []int64{1, 3}, 0)
	queue := mustTag(t, a, 1, "queue", []int64{1})
	check(normal, []int64{1, 3}, 0)
	check(artist, []int64{}, 1)
	exec("UPDATE reinhardt_musicfiletag SET rank=99 WHERE tag_id=?", queue)
	exec("UPDATE reinhardt_musicfile SET title='New title',album='New album' WHERE id=1")
	exec("UPDATE reinhardt_musicfile SET artist=artist,missing=missing")
	check(normal, []int64{1, 3}, 0)
	check(artist, []int64{}, 0)
	exec("DELETE FROM reinhardt_musicfiletag WHERE tag_id=?", queue)
	check(normal, []int64{1, 3}, 0)
	check(artist, []int64{1}, 1)
	exec("UPDATE reinhardt_musicfile SET missing=1 WHERE id=1")
	check(normal, []int64{3}, 1)
	check(artist, []int64{}, 1)
	exec("UPDATE reinhardt_musicfile SET missing=0,artist='Another' WHERE id=1")
	check(normal, []int64{1, 3}, 1)
	check(artist, []int64{}, 1)
	check(autofeedKey{owner: 1, artist: "Another", similar: true}, []int64{1, 3}, 1)
	exec("UPDATE reinhardt_musicfiletag SET music_file_id=3 WHERE tag_id=?", blacklist)
	check(normal, []int64{1, 2}, 1)
	check(artist, []int64{2}, 1)
	// Rename a tag, edit its members, then restore its label between cache reads.
	exec("UPDATE reinhardt_tag SET label='temporary' WHERE id=?", blacklist)
	exec("UPDATE reinhardt_musicfiletag SET music_file_id=2 WHERE tag_id=?", blacklist)
	exec("UPDATE reinhardt_tag SET label='blacklist' WHERE id=?", blacklist)
	check(normal, []int64{1, 3}, 1)
	exec("UPDATE reinhardt_tag SET label='temporary' WHERE id=?", blacklist)
	check(normal, []int64{1, 2, 3}, 1)
	exec("UPDATE reinhardt_tag SET label='blacklist' WHERE id=?", blacklist)
	check(normal, []int64{1, 3}, 1)
	exec("DELETE FROM reinhardt_tag WHERE id=?", blacklist)
	check(normal, []int64{1, 2, 3}, 1)
	blacklist = mustTag(t, a, 1, "blacklist", []int64{1})
	check(normal, []int64{2, 3}, 1)
	exec("DELETE FROM reinhardt_tag WHERE owner_id=2 AND label='blacklist'")
	exec("UPDATE reinhardt_tag SET owner_id=2 WHERE id=?", blacklist)
	check(normal, []int64{1, 2, 3}, 1)
	check(other, []int64{2, 3}, 1)
	// Insert a sparse primary key; every eligible ID must remain selectable.
	exec(`INSERT INTO reinhardt_musicfile(id,music_directory_id,relative_path,artist,title,album,genre,comment,created_at,modified_at)
VALUES(1000000,1,'new.mp3','Artist','New','','','','2026-01-01','2026-01-01')`)
	check(normal, []int64{1, 2, 3, 1000000}, 1)
	exec("DELETE FROM reinhardt_musicfile WHERE id=1000000")
	check(normal, []int64{1, 2, 3}, 1)
	// Moving an association between queue and blacklist updates both pools.
	exec("UPDATE reinhardt_tag SET owner_id=1 WHERE id=?", blacklist)
	check(normal, []int64{2, 3}, 1)
	check(artist, []int64{2}, 1)
	exec("UPDATE reinhardt_musicfiletag SET tag_id=? WHERE tag_id=?", queue, blacklist)
	check(normal, []int64{1, 2, 3}, 1)
	check(autofeedKey{owner: 1, artist: "Another", similar: true}, []int64{}, 1)
}

func TestAutofeedExternalWritesRollbackAndCascades(t *testing.T) {
	a := testApp(t)
	stats := measureAppSQL(t, a)
	key := autofeedKey{owner: 1}
	if _, err := a.autofeedIDs(context.Background(), key, false); err != nil {
		t.Fatal(err)
	}
	tx, err := a.S.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE reinhardt_musicfile SET missing=1"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	stats.autofeedLoads.Store(0)
	if ids, err := a.autofeedIDs(context.Background(), key, false); err != nil || len(ids) != 3 || stats.autofeedLoads.Load() != 0 {
		t.Fatal("rollback invalidated the candidate list", ids, err)
	}
	other, err := sql.Open("sqlite", a.S.C["DATABASE_PATH"]+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	blacklist := mustTag(t, a, 1, "blacklist", []int64{2})
	if _, err = a.autofeedIDs(context.Background(), key, false); err != nil {
		t.Fatal(err)
	}
	if _, err = other.Exec("UPDATE reinhardt_musicfiletag SET music_file_id=3 WHERE tag_id=?", blacklist); err != nil {
		t.Fatal(err)
	}
	if ids, err := a.autofeedIDs(context.Background(), key, false); err != nil || !reflect.DeepEqual(ids, []int64{1, 2}) {
		t.Fatal("external blacklist change was not detected", ids, err)
	}
	if _, err = other.Exec("UPDATE reinhardt_musicfile SET missing=1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if ids, err := a.autofeedIDs(context.Background(), key, false); err != nil || !reflect.DeepEqual(ids, []int64{2}) {
		t.Fatal("external missing status was not detected", ids, err)
	}
	if _, err = other.Exec("DELETE FROM reinhardt_musicdirectory WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if ids, err := a.autofeedIDs(context.Background(), key, false); err != nil || len(ids) != 0 {
		t.Fatal("directory cascade left eligible tracks", ids, err)
	}
	stats.autofeedLoads.Store(0)
	if track, err := a.randomAutofeedTrack(context.Background(), 1, nil); err != nil || track != nil || stats.autofeedLoads.Load() != 0 {
		t.Fatal("empty library was not cached", track, err)
	}
	if _, err = other.Exec("DELETE FROM auth_user WHERE id=1"); err != nil {
		t.Fatal("user cascade failed", err)
	}
}

func TestAutofeedConcurrentColdRequests(t *testing.T) {
	a := testApp(t)
	stats := measureAppSQL(t, a)
	start := make(chan struct{})
	errors := make(chan error, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			track, err := a.randomAutofeedTrack(context.Background(), 1, nil)
			if err != nil {
				errors <- err
			} else if id := track.N("id"); id < 1 || id > 3 {
				errors <- fmt.Errorf("invalid selected track: %v", track)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if stats.autofeedLoads.Load() != 1 {
		t.Fatal("concurrent requests rebuilt the pool repeatedly")
	}
}

func TestAutofeedRevalidatesSampledTrack(t *testing.T) {
	a := testApp(t)
	key := autofeedKey{owner: 1}
	_, err := a.autofeedIDs(context.Background(), key, false)
	if err != nil {
		t.Fatal(err)
	}
	// Model a cached list becoming stale between revision checking and lookup.
	// Only the forbidden track is sampled first; the retry must find an eligible one.
	mustTag(t, a, 1, "blacklist", []int64{1})
	var version autofeedVersion
	err = a.S.DB.QueryRow(`SELECT l.revision,b.id,bv.revision FROM go_autofeed_library_revision l
JOIN reinhardt_tag b ON b.owner_id=1 AND b.label='blacklist'
JOIN go_autofeed_tag_revision bv ON bv.tag_id=b.id WHERE l.id=1`).Scan(&version.library, &version.blacklistID, &version.blacklist)
	if err != nil {
		t.Fatal(err)
	}
	a.autofeed.mu.Lock()
	a.autofeed.put(autofeedEntry{key: key, version: version, ids: []int64{1}})
	a.autofeed.mu.Unlock()
	track, err := a.randomAutofeedTrack(context.Background(), 1, nil)
	if err != nil || track == nil || track.N("id") == 1 {
		t.Fatal("served a stale, blacklisted track", track, err)
	}
}

func TestAutofeedCacheEviction(t *testing.T) {
	var cache autofeedCache
	for owner := int64(1); owner <= autofeedMaxEntries+1; owner++ {
		cache.put(autofeedEntry{key: autofeedKey{owner: owner}, ids: []int64{1}})
	}
	if cache.recent.Len() != autofeedMaxEntries || cache.entries[autofeedKey{owner: 1}] != nil {
		t.Fatal("entry budget did not evict the oldest user")
	}
	large := make([]int64, autofeedMaxIDs/2+1)
	for _, owner := range []int64{200, 201} {
		cache.put(autofeedEntry{key: autofeedKey{owner: owner}, ids: large})
	}
	if cache.recent.Len() != 1 || cache.ids != cap(large) || cache.entries[autofeedKey{owner: 201}] == nil {
		t.Fatal("ID budget was not enforced")
	}
	cache.put(autofeedEntry{key: autofeedKey{owner: 201}, ids: []int64{2}})
	if cache.recent.Len() != 1 || cache.ids != 1 {
		t.Fatal("replacement retained obsolete IDs")
	}
}
