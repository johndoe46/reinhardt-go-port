package reinhardt

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

func TestAutofeedQueuedArtistCacheModesAndInvalidation(t *testing.T) {
	a := testApp(t)
	stats := measureAppSQL(t, a)
	normal := autofeedKey{owner: 1}
	unique := autofeedKey{owner: 1, excludeQueuedArtists: true}
	check := func(key autofeedKey, want []int64, loads int64) {
		t.Helper()
		before := stats.autofeedLoads.Load()
		for range 2 {
			got, err := a.autofeedIDs(context.Background(), key, false)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("%+v: got %v, want %v: %v", key, got, want, err)
			}
		}
		if got := stats.autofeedLoads.Load() - before; got != loads {
			t.Fatalf("%+v loaded %d pools, want %d", key, got, loads)
		}
	}
	check(normal, []int64{1, 2, 3}, 1)
	check(unique, []int64{1, 2, 3}, 1)
	queue := mustTag(t, a, 1, "queue", []int64{1})
	check(normal, []int64{1, 2, 3}, 0)
	check(unique, []int64{3}, 1)
	check(autofeedKey{owner: 2, excludeQueuedArtists: true}, []int64{1, 2, 3}, 1)
	other, err := sql.Open("sqlite", a.S.C["DATABASE_PATH"]+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err = other.Exec("UPDATE reinhardt_musicfiletag SET music_file_id=3 WHERE tag_id=?", queue); err != nil {
		t.Fatal(err)
	}
	check(unique, []int64{1, 2}, 1)
	if _, err = other.Exec("UPDATE reinhardt_musicfile SET artist='Artist',missing=1 WHERE id=3"); err != nil {
		t.Fatal(err)
	}
	// Missing tracks still represent their artist in the queue.
	check(unique, []int64{}, 1)
	if _, err = other.Exec("DELETE FROM reinhardt_musicfiletag WHERE tag_id=?", queue); err != nil {
		t.Fatal(err)
	}
	check(unique, []int64{1, 2}, 1)
	mustTag(t, a, 1, "blacklist", []int64{2})
	check(unique, []int64{1}, 1)
}

func TestAutofeedRevalidatesQueuedArtist(t *testing.T) {
	for _, key := range []autofeedKey{{owner: 1, excludeQueuedArtists: true}, {owner: 1, similar: true, artist: "Artist"}} {
		a := testApp(t)
		mustTag(t, a, 1, "queue", []int64{1})
		if _, err := a.autofeedIDs(context.Background(), key, false); err != nil {
			t.Fatal(err)
		}
		// Simulate a race after revision checking: this track is not queued,
		// but another track from its artist now is. Revalidation must reject it.
		a.autofeed.mu.Lock()
		entry := a.autofeed.entries[key].Value.(autofeedEntry)
		entry.ids = []int64{2}
		a.autofeed.put(entry)
		a.autofeed.mu.Unlock()
		track, err := a.sampleAutofeedTrack(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if key.similar {
			if track != nil {
				t.Fatal("similar selection repeated a queued artist", track)
			}
		} else if track == nil || track.S("artist") != "Another" {
			t.Fatal("fallback did not reject stale queued-artist candidates", track)
		}
	}
}

func TestNextSongSimilarityExcludesQueuedArtists(t *testing.T) {
	for _, mode := range []struct {
		path     string
		fragment bool
	}{{"/next_song/", false}, {"/next_song/", true}, {"/api/dj/next_song/", false}} {
		for _, scenario := range []string{"similar", "fallback", "offline", "all-artists-queued", "repeat-queued-track", "all-unavailable"} {
			t.Run(mode.path+scenario, func(t *testing.T) {
				a := testApp(t)
				if _, err := a.S.DB.Exec(`INSERT INTO reinhardt_musicfile(id,music_directory_id,relative_path,artist,title,album,genre,comment,created_at,modified_at)
VALUES(4,1,'4.mp3','Fresh','New song','New album','','','2026-01-01','2026-01-01')`); err != nil {
					t.Fatal(err)
				}
				queue := []int64{1, 3}
				if scenario == "all-artists-queued" || scenario == "repeat-queued-track" || scenario == "all-unavailable" {
					queue = append(queue, 4)
				}
				mustTag(t, a, 1, "queue", queue)
				wantTrack, wantLength := int64(4), len(queue)+1
				switch scenario {
				case "all-artists-queued":
					// Only an unqueued song from an already queued artist remains.
					mustTag(t, a, 1, "blacklist", []int64{1, 3, 4})
					wantTrack = 2
				case "repeat-queued-track":
					// The last song itself is the only eligible random choice.
					mustTag(t, a, 1, "blacklist", []int64{1, 2, 3})
					wantLength = len(queue)
				case "all-unavailable":
					if _, err := a.S.DB.Exec("UPDATE reinhardt_musicfile SET missing=1"); err != nil {
						t.Fatal(err)
					}
				}
				// Another user's queue must not exclude the only eligible artist.
				mustTag(t, a, 2, "queue", []int64{4})
				last, err := a.S.queueItem(1, "a.music_file_id=?", queue[len(queue)-1])
				if err != nil {
					t.Fatal(err)
				}
				if _, err = a.S.DB.Exec("UPDATE reinhardt_preferences SET current_position=?,autofeed_queue=1,autofeed_similar=1 WHERE owner_id=1", last["id"]); err != nil {
					t.Fatal(err)
				}
				// A warm unrestricted pool must only be used after all new-artist
				// candidates are exhausted.
				if _, err = a.autofeedIDs(context.Background(), autofeedKey{owner: 1}, false); err != nil {
					t.Fatal(err)
				}
				a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
					if scenario == "offline" {
						return nil, context.DeadlineExceeded
					}
					if scenario == "similar" {
						return remoteResponse(`{"similarartists":{"artist":[{"name":"Artist"},{"name":"Fresh"}]}}`), nil
					}
					return remoteResponse(`{"similarartists":{"artist":[{"name":"Artist"}]}}`), nil
				})
				w := call(a, "POST", mode.path, 1, url.Values{}, mode.fragment)
				requireCode(t, w, 200)
				prefs, err := a.S.Preferences(1)
				if err != nil {
					t.Fatal(err)
				}
				items, err := a.S.Queue(1)
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "all-unavailable" {
					if prefs.N("current_position") != 0 || len(items) != len(queue) {
						t.Fatal("an unavailable library must not enqueue missing tracks")
					}
					return
				}
				selected, err := a.S.queueItem(1, "a.id=?", prefs["current_position"])
				if err != nil || selected.N("music_file_id") != wantTrack || len(items) != wantLength || selected.N("rank") <= last.N("rank") {
					t.Fatal("autofeed did not append the expected similar or random song", selected, err)
				}
			})
		}
	}
}
