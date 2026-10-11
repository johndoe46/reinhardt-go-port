package reinhardt

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestNextSongLastFMPreference(t *testing.T) {
	for _, mode := range []struct {
		name, path string
		fragment   bool
	}{{"plain", "/next_song/", false}, {"unpoly", "/next_song/", true}, {"api", "/api/dj/next_song/", false}} {
		for _, scenario := range []string{"queued", "queued-disabled", "exhausted-disabled", "empty-disabled", "empty-enabled", "exhausted-enabled", "autofeed-disabled", "explicit-disabled"} {
			t.Run(mode.name+"/"+scenario, func(t *testing.T) {
				a := testApp(t)
				var position int64
				if !strings.HasPrefix(scenario, "empty-") {
					mustTag(t, a, 1, "queue", []int64{1, 2})
					trackID := 2
					if scenario == "queued" || scenario == "queued-disabled" {
						trackID = 1
					}
					item, err := a.S.queueItem(1, "a.music_file_id=?", trackID)
					if err != nil {
						t.Fatal(err)
					}
					position = item.N("id")
				}
				if _, err := a.S.DB.Exec("UPDATE reinhardt_preferences SET current_position=?,autofeed_queue=1,autofeed_similar=1 WHERE owner_id=1", position); err != nil {
					t.Fatal(err)
				}
				similar := !strings.HasSuffix(scenario, "-disabled") || scenario == "autofeed-disabled"
				autofeed := scenario != "autofeed-disabled"
				// Save through the same checkbox form used by the browser. Omitting
				// autofeed_similar must disable it in the persisted preferences.
				form := url.Values{}
				if similar {
					form.Set("autofeed_similar", "on")
				}
				if autofeed {
					form.Set("autofeed_queue", "on")
				}
				w := call(a, "POST", "/preferences/", 1, form, true)
				requireCode(t, w, 200)
				prefs, err := a.S.Preferences(1)
				if err != nil || prefs.B("autofeed_similar") != similar || prefs.B("autofeed_queue") != autofeed {
					t.Fatal("checkbox preferences were not saved correctly", prefs, err)
				}
				calls := 0
				a.Client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.URL.Hostname() != "ws.audioscrobbler.com" || r.URL.Query().Get("method") != "artist.getsimilar" {
						t.Errorf("unexpected external request: %s", r.URL)
						return nil, fmt.Errorf("unexpected external request")
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"similarartists":{"artist":[{"name":"Another"}]}}`)), Header: make(http.Header)}, nil
				})
				selection := url.Values{}
				if scenario == "explicit-disabled" {
					selection.Set("next_track", "3")
				}
				w = call(a, "POST", mode.path, 1, selection, mode.fragment)
				requireCode(t, w, 200)
				wantCalls := 0
				if scenario == "exhausted-enabled" {
					wantCalls = 1
				}
				if calls != wantCalls {
					t.Fatalf("Last.fm calls=%d, want %d (similar=%v, autofeed=%v)", calls, wantCalls, similar, autofeed)
				}
				prefs, err = a.S.Preferences(1)
				if err != nil {
					t.Fatal(err)
				}
				if !autofeed {
					if prefs.N("current_position") != 0 {
						t.Fatal("disabled autofeed selected a song")
					}
				} else {
					item, err := a.S.queueItem(1, "a.id=?", prefs["current_position"])
					if err != nil {
						t.Fatal("no selected queue item", err)
					}
					if (scenario == "exhausted-enabled" || scenario == "explicit-disabled") && item.N("music_file_id") != 3 {
						t.Fatal("wrong selected track", item)
					}
				}
			})
		}
	}
}

func TestNextSongSelectionAndOwnership(t *testing.T) {
	for _, scenario := range []string{"first", "advance", "foreign-current", "foreign-selection", "tied-ranks", "invalid-selection", "autofeed-blacklist", "empty-library"} {
		t.Run(scenario, func(t *testing.T) {
			a := testApp(t)
			mustTag(t, a, 2, "queue", []int64{3})
			queueID := mustTag(t, a, 1, "queue", []int64{1, 2})
			items, err := a.S.Queue(1)
			if err != nil {
				t.Fatal(err)
			}
			position := int64(0)
			form := url.Values{}
			wantTrack, wantRank, wantPosition := int64(1), int64(1), items[0].N("id")
			switch scenario {
			case "advance":
				position = items[0].N("id")
				wantTrack, wantRank, wantPosition = 2, 2, items[1].N("id")
			case "foreign-current":
				foreign, err := a.S.Queue(2)
				if err != nil {
					t.Fatal(err)
				}
				position = foreign[0].N("id")
			case "foreign-selection":
				form.Set("list_item", "3")
			case "tied-ranks":
				position = items[0].N("id")
				wantTrack, wantRank, wantPosition = 0, 0, 0
				if _, err = a.S.DB.Exec("UPDATE reinhardt_musicfiletag SET rank=1 WHERE tag_id=?", queueID); err != nil {
					t.Fatal(err)
				}
			case "invalid-selection":
				position = items[0].N("id")
				form.Set("list_item", "bad")
				wantTrack, wantRank, wantPosition = 2, 2, items[1].N("id")
			case "autofeed-blacklist":
				position = items[1].N("id")
				wantRank = 3
				mustTag(t, a, 1, "blacklist", []int64{2, 3})
			case "empty-library":
				position = items[1].N("id")
				wantTrack, wantRank, wantPosition = 0, 0, 0
				if _, err = a.S.DB.Exec("DELETE FROM reinhardt_musicfile"); err != nil {
					t.Fatal(err)
				}
			}
			autofeed := scenario == "autofeed-blacklist" || scenario == "empty-library"
			if _, err = a.S.DB.Exec("UPDATE reinhardt_preferences SET current_position=?,autofeed_queue=?,autofeed_similar=0 WHERE owner_id=1", position, autofeed); err != nil {
				t.Fatal(err)
			}
			w := call(a, "POST", "/next_song/", 1, form, true)
			requireCode(t, w, 200)
			prefs, err := a.S.Preferences(1)
			if err != nil || prefs.N("current_position") != wantPosition {
				t.Fatal("wrong saved playback position", prefs, err)
			}
			if wantTrack == 0 {
				if strings.Contains(w.Body.String(), `data-url=`) || !strings.Contains(w.Body.String(), `data-position=""`) || !strings.Contains(w.Body.String(), `onclick="playNext()"`) {
					t.Fatal("empty playback state incorrect")
				}
			} else {
				track, err := a.S.Track(wantTrack)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(w.Body.String(), `data-url="`+track.S("playback_url")+`"`) {
					t.Fatal("wrong track selected")
				}
				selected, err := a.S.queueItem(1, "a.id=?", wantPosition)
				if err != nil || selected.N("rank") != wantRank {
					t.Fatal("wrong queue rank", selected, err)
				}
			}
			foreign, err := a.S.Queue(2)
			if err != nil {
				t.Fatal(err)
			}
			if scenario != "empty-library" && (len(foreign) != 1 || foreign[0].N("music_file_id") != 3 || foreign[0].N("rank") != 1) {
				t.Fatal("another user's queue changed")
			}
		})
	}
}
