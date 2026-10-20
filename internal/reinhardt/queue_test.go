package reinhardt

import (
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestQueueMutationAcknowledgementsAreRenderable(test *testing.T) {
	for _, action := range []string{"reorder", "remove"} {
		test.Run(action, func(test *testing.T) {
			app := testApp(test)
			tagID := mustTag(test, app, 1, "queue", []int64{1, 2, 3})
			mustTag(test, app, 2, "queue", []int64{3, 2})
			otherQueue, err := app.Store.Queue(test.Context(), 2)
			if err != nil {
				test.Fatal(err)
			}
			current, err := app.Store.queueItem(test.Context(), 1, "a.music_file_id=?", 2)
			if err != nil {
				test.Fatal(err)
			}
			if _, err = app.Store.DB.ExecContext(test.Context(), "UPDATE reinhardt_preferences SET current_position=? WHERE owner_id=1", current.ID); err != nil {
				test.Fatal(err)
			}
			path, body := "/reorder_queue/", "ok"
			form := url.Values{"list_pk": {fmt.Sprint(tagID)}, "new_order": {`[{"item":3,"rank":1},{"item":1,"rank":2},{"item":2,"rank":3}]`}}
			want := []int64{3, 1, 2}
			if action == "remove" {
				path, body = "/remove_from_queue/", "None"
				form = url.Values{"remove_item": {"1"}}
				want = []int64{2, 3}
			}
			csrf := strings.Repeat("a", 64)
			form.Set("csrfmiddlewaretoken", csrf)
			request := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("X-Up-Target", ":none")
			request.AddCookie(&http.Cookie{Name: "sessionid", Value: "session-1"})
			request.AddCookie(&http.Cookie{Name: "csrftoken", Value: csrf})
			response := httptest.NewRecorder()
			app.ServeHTTP(response, request)
			requireCode(test, response, http.StatusOK)
			contentType, _, err := mime.ParseMediaType(response.Header().Get("Content-Type"))
			if err != nil || contentType != "text/html" {
				test.Fatalf("Unpoly requires an HTML acknowledgement even for :none; got %q", response.Header().Get("Content-Type"))
			}
			if response.Body.String() != body {
				test.Fatalf("unexpected acknowledgement: %q", response.Body.String())
			}
			queue, err := app.Store.Queue(test.Context(), 1)
			if err != nil {
				test.Fatal(err)
			}
			got := []int64{}
			for _, item := range queue {
				got = append(got, item.MusicFileID)
			}
			if !reflect.DeepEqual(got, want) {
				test.Fatalf("queue=%v, want %v", got, want)
			}
			prefs, err := app.Store.Preferences(test.Context(), 1)
			if err != nil || prefs.CurrentPosition != current.ID {
				test.Fatal("queue mutation changed the playing track", err)
			}
			afterOtherQueue, err := app.Store.Queue(test.Context(), 2)
			if err != nil || !reflect.DeepEqual(otherQueue, afterOtherQueue) {
				test.Fatal("queue mutation changed another user's queue", err)
			}
		})
	}
}

func TestShuffleQueue(test *testing.T) {
	app := testApp(test)
	populateQueue(test, app, 20)
	mustTag(test, app, 2, "queue", []int64{3, 2, 1})
	mustTag(test, app, 1, "favorites", []int64{2, 1})
	before, err := app.Store.Queue(test.Context(), 1)
	if err != nil {
		test.Fatal(err)
	}
	otherTags, err := app.Store.Query(test.Context(), "SELECT * FROM reinhardt_musicfiletag WHERE tag_id != ? ORDER BY id", before[0].TagID)
	if err != nil {
		test.Fatal(err)
	}
	position := before[0].ID
	if _, err = app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=? WHERE owner_id=1", position); err != nil {
		test.Fatal(err)
	}
	app.Store.Config.LanguageCode = "fr-fr"
	response := call(app, "GET", "/queue/", 1, nil, true)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `action="/shuffle_queue/"`) || !strings.Contains(response.Body.String(), "Mélanger") {
		test.Fatal("missing French shuffle menu action")
	}
	requireCode(test, call(app, "GET", "/shuffle_queue/", 1, nil, true), 405)
	requireCode(test, call(app, "POST", "/shuffle_queue/", 1, nil, true), 403)
	requireCode(test, call(app, "POST", "/shuffle_queue/", 0, url.Values{}, true), 302)
	unchanged, err := app.Store.Queue(test.Context(), 1)
	if err != nil || !reflect.DeepEqual(before, unchanged) {
		test.Fatal("rejected shuffle changed the queue", err)
	}
	response = call(app, "POST", "/shuffle_queue/", 1, url.Values{}, true)
	requireCode(test, response, 200)
	if !strings.Contains(response.Body.String(), `value="queue"`) || strings.Contains(response.Body.String(), `<m-audio`) {
		test.Fatal("shuffle must refresh the queue without replacing the player")
	}
	after, err := app.Store.Queue(test.Context(), 1)
	if err != nil || len(after) != len(before) {
		test.Fatal("shuffle changed the queue size", err)
	}
	originals := make(map[int64]*QueueItem, len(before))
	for _, item := range before {
		originals[item.ID] = item
	}
	changed := false
	for index, item := range after {
		if item.Rank != int64(index+1) {
			test.Fatal("shuffled ranks are not consecutive", item)
		}
		original, ok := originals[item.ID]
		if !ok {
			test.Fatal("shuffle added or duplicated an item", item)
		}
		delete(originals, item.ID)
		changed = changed || item.ID != before[index].ID
		original.Rank = item.Rank
		if !reflect.DeepEqual(original, item) {
			test.Fatal("shuffle changed fields other than rank", item)
		}
	}
	// With 20 tracks, an unchanged random permutation has probability 1/20!.
	if !changed {
		test.Fatal("queue order did not change")
	}
	unchangedTags, err := app.Store.Query(test.Context(), "SELECT * FROM reinhardt_musicfiletag WHERE tag_id != ? ORDER BY id", before[0].TagID)
	if err != nil || !reflect.DeepEqual(otherTags, unchangedTags) {
		test.Fatal("shuffle changed another user's queue or favorites", err)
	}
	prefs, err := app.Store.Preferences(test.Context(), 1)
	if err != nil || prefs.CurrentPosition != position {
		test.Fatal("shuffle changed the playing track", err)
	}
	response = call(app, "GET", "/queue/", 1, nil, true)
	requireCode(test, response, 200)
	lastOffset := -1
	for _, item := range after {
		offset := strings.Index(response.Body.String(), fmt.Sprintf(`id="music_files_row_%d"`, item.MusicFileID))
		if offset <= lastOffset {
			test.Fatal("queue page did not retain shuffled order")
		}
		lastOffset = offset
	}
}

func TestShuffleSmallQueue(test *testing.T) {
	for _, count := range []int{0, 1} {
		test.Run(fmt.Sprint(count), func(test *testing.T) {
			app := testApp(test)
			if count == 1 {
				mustTag(test, app, 1, "queue", []int64{1})
			}
			requireCode(test, call(app, "POST", "/shuffle_queue/", 1, url.Values{}, true), 200)
			queue, err := app.Store.Queue(test.Context(), 1)
			if err != nil || len(queue) != count {
				test.Fatal("shuffle changed small queue contents", queue, err)
			}
		})
	}
}

func populateQueue(testingContext testing.TB, app *App, count int) {
	testingContext.Helper()
	if count > 3 {
		_, err := app.Store.DB.Exec(`WITH RECURSIVE n(id) AS (SELECT 4 UNION ALL SELECT id+1 FROM n WHERE id<?)
INSERT INTO reinhardt_musicfile(music_directory_id,relative_path,artist,title,album,genre,comment,year,duration,track_number,created_at,modified_at,missing)
SELECT 1,printf('%d.mp3',id),'Artist',printf('Tune %d',id),'Album','jazz','','2001',10,id,'2026-01-01','2026-01-01',0 FROM n`, count)
		if err != nil {
			testingContext.Fatal(err)
		}
	}
	tag, err := app.Store.AddTag(testingContext.Context(), 1, "queue")
	if err != nil {
		testingContext.Fatal(err)
	}
	if _, err = app.Store.DB.Exec(`INSERT INTO reinhardt_musicfiletag(tag_id,music_file_id,rank,created_at,modified_at)
SELECT ?,id,id,'2026-01-01','2026-01-02' FROM reinhardt_musicfile WHERE id<=?`, tag, count); err != nil {
		testingContext.Fatal(err)
	}
}

func TestQueueLoadsTracksInOneQuery(test *testing.T) {
	app := testApp(test)
	mustTag(test, app, 2, "queue", []int64{3})
	mustTag(test, app, 1, "favorites", []int64{1})
	populateQueue(test, app, 3)
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET missing=1 WHERE id=2"); err != nil {
		test.Fatal(err)
	}
	if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfiletag SET rank=1 WHERE music_file_id=2 AND tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=1 AND label='queue')"); err != nil {
		test.Fatal(err)
	}
	stats := measureAppSQL(test, app)
	items, err := app.Store.Queue(test.Context(), 1)
	if err != nil || len(items) != 3 {
		test.Fatal("wrong queue contents", items, err)
	}
	if stats.calls.Load() != 1 || stats.writes.Load() != 0 {
		test.Fatalf("queue made %d SQL calls and %d writes", stats.calls.Load(), stats.writes.Load())
	}
	for index, item := range items {
		if item.MusicFileID != int64(index+1) || item.ID <= 0 {
			test.Fatal("queue ordering or association ID changed", item)
		}
		association, err := app.Store.One(test.Context(), "SELECT * FROM reinhardt_musicfiletag WHERE id=?", item.ID)
		if err != nil {
			test.Fatal(err)
		}
		for key, value := range association {
			if !reflect.DeepEqual(item.templateRow()[key], value) {
				test.Fatalf("association field %s changed: got %v, want %v", key, item.templateRow()[key], value)
			}
		}
		track, err := app.Store.Track(test.Context(), item.MusicFileID)
		if err != nil || !reflect.DeepEqual(item.MusicFile, track) {
			test.Fatal("joined track differs from individual lookup", item.MusicFile, track, err)
		}
	}
	stats.calls.Store(0)
	empty, err := app.Store.Queue(test.Context(), 99)
	if err != nil || len(empty) != 0 || stats.calls.Load() != 1 {
		test.Fatal("empty queue lookup failed", empty, err)
	}
}

func TestFullPagesAvoidQueueQueryGrowth(test *testing.T) {
	for _, count := range []int{1, 1000} {
		test.Run(fmt.Sprintf("queue=%d", count), func(test *testing.T) {
			app := testApp(test)
			populateQueue(test, app, count)
			item, err := app.Store.queueItem(test.Context(), 1, "a.music_file_id=?", count)
			if err != nil {
				test.Fatal(err)
			}
			if _, err = app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=? WHERE owner_id=1", item.ID); err != nil {
				test.Fatal(err)
			}
			if _, err = app.suggestedData(test.Context()); err != nil {
				test.Fatal(err)
			}
			stats := measureAppSQL(test, app)
			for _, page := range []struct {
				path     string
				fragment bool
				maxCalls int64
			}{{"/artists/", false, 6}, {"/queue/", false, 7}, {"/queue/", true, 6}} {
				stats.calls.Store(0)
				stats.writes.Store(0)
				response := call(app, "GET", page.path, 1, nil, page.fragment)
				requireCode(test, response, 200)
				if calls := stats.calls.Load(); calls > page.maxCalls {
					test.Fatalf("%s fragment=%v: %d SQL calls, want at most %d", page.path, page.fragment, calls, page.maxCalls)
				}
				if writes := stats.writes.Load(); writes != 0 {
					test.Fatalf("%s fragment=%v: %d unnecessary writes", page.path, page.fragment, writes)
				}
				if !page.fragment {
					for _, want := range []string{`id="now_playing"`, fmt.Sprintf(`data-position="%d"`, count), fmt.Sprintf(`data-url="/stream/%d/"`, count)} {
						if !strings.Contains(response.Body.String(), want) {
							test.Fatal("missing footer playback state", want)
						}
					}
				}
			}
		})
	}
}

func TestFullPageFooterQueueOwnershipAndStalePosition(test *testing.T) {
	for _, scenario := range []string{"unset", "deleted", "foreign", "other-tag", "missing-track"} {
		test.Run(scenario, func(test *testing.T) {
			app := testApp(test)
			mustTag(test, app, 1, "queue", []int64{1})
			mustTag(test, app, 2, "queue", []int64{2})
			mustTag(test, app, 1, "favorites", []int64{3})
			var position int64
			wantTrack := false
			switch scenario {
			case "deleted":
				position = 999
			case "foreign", "other-tag", "missing-track":
				trackID := map[string]int{"foreign": 2, "other-tag": 3, "missing-track": 1}[scenario]
				item, err := app.Store.One(test.Context(), "SELECT id FROM reinhardt_musicfiletag WHERE music_file_id=?", trackID)
				if err != nil {
					test.Fatal(err)
				}
				position = item.N("id")
				if scenario == "missing-track" {
					wantTrack = true
					if _, err = app.Store.DB.Exec("UPDATE reinhardt_musicfile SET missing=1 WHERE id=1"); err != nil {
						test.Fatal(err)
					}
				}
			}
			if _, err := app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=? WHERE owner_id=1", position); err != nil {
				test.Fatal(err)
			}
			for _, path := range []string{"/artists/", "/queue/"} {
				response := call(app, "GET", path, 1, nil, false)
				requireCode(test, response, 200)
				if strings.Contains(response.Body.String(), `data-url="/stream/1/"`) != wantTrack {
					test.Fatal("wrong footer track", scenario, path)
				}
				if !wantTrack && !strings.Contains(response.Body.String(), `id="now_playing" data-position="0"`) {
					test.Fatal("stale or foreign position was not reset in footer", scenario, path)
				}
				if strings.Contains(response.Body.String(), `data-url="/stream/2/"`) || strings.Contains(response.Body.String(), `data-url="/stream/3/"`) {
					test.Fatal("footer exposed another user's queue or a non-queue tag")
				}
			}
			prefs, err := app.Store.Preferences(test.Context(), 1)
			if err != nil || prefs.CurrentPosition != position {
				test.Fatal("rendering changed the saved playback position", prefs, err)
			}
		})
	}
}

func BenchmarkFullPageQueue(benchmark *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		benchmark.Run(fmt.Sprintf("queue=%d", count), func(benchmark *testing.B) {
			app := testApp(benchmark)
			populateQueue(benchmark, app, count)
			item, err := app.Store.queueItem(benchmark.Context(), 1, "a.music_file_id=?", count)
			if err != nil {
				benchmark.Fatal(err)
			}
			if _, err = app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=? WHERE owner_id=1", item.ID); err != nil {
				benchmark.Fatal(err)
			}
			stats := measureAppSQL(benchmark, app)
			for _, path := range []string{"/artists/", "/queue/"} {
				benchmark.Run(strings.Trim(path, "/"), func(benchmark *testing.B) {
					if response := call(app, "GET", path, 1, nil, false); response.Code != 200 {
						benchmark.Fatal(response.Body.String())
					}
					stats.calls.Store(0)
					stats.writes.Store(0)
					stats.duration.Store(0)
					benchmark.ReportAllocs()
					benchmark.ResetTimer()
					for index := 0; index < benchmark.N; index++ {
						if response := call(app, "GET", path, 1, nil, false); response.Code != 200 {
							benchmark.Fatal(response.Body.String())
						}
					}
					benchmark.StopTimer()
					benchmark.ReportMetric(float64(stats.calls.Load())/float64(benchmark.N), "sql/op")
					benchmark.ReportMetric(float64(stats.writes.Load())/float64(benchmark.N), "writes/op")
					benchmark.ReportMetric(float64(stats.duration.Load())/float64(benchmark.N)/1e6, "db-ms/op")
				})
			}
		})
	}
}
