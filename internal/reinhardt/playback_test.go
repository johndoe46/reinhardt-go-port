package reinhardt

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestNextSongLastFMPreference(test *testing.T) {
	for _, mode := range []struct {
		name, path string
		fragment   bool
	}{{"plain", "/next_song/", false}, {"unpoly", "/next_song/", true}, {"api", "/api/dj/next_song/", false}} {
		for _, scenario := range []string{"queued", "queued-disabled", "exhausted-disabled", "empty-disabled", "empty-enabled", "exhausted-enabled", "autofeed-disabled", "explicit-disabled"} {
			test.Run(mode.name+"/"+scenario, func(test *testing.T) {
				app := testApp(test)
				var position int64
				if !strings.HasPrefix(scenario, "empty-") {
					mustTag(test, app, 1, "queue", []int64{1, 2})
					trackID := 2
					if scenario == "queued" || scenario == "queued-disabled" {
						trackID = 1
					}
					item, err := app.Store.queueItem(1, "a.music_file_id=?", trackID)
					if err != nil {
						test.Fatal(err)
					}
					position = item.ID
				}
				if _, err := app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=?,autofeed_queue=1,autofeed_similar=1 WHERE owner_id=1", position); err != nil {
					test.Fatal(err)
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
				response := call(app, "POST", "/preferences/", 1, form, true)
				requireCode(test, response, 200)
				prefs, err := app.Store.Preferences(1)
				if err != nil || prefs.AutofeedSimilar != similar || prefs.AutofeedQueue != autofeed {
					test.Fatal("checkbox preferences were not saved correctly", prefs, err)
				}
				calls := 0
				app.Client.Transport = transportFunc(func(request *http.Request) (*http.Response, error) {
					calls++
					if request.URL.Hostname() != "ws.audioscrobbler.com" || request.URL.Query().Get("method") != "artist.getsimilar" {
						test.Errorf("unexpected external request: %s", request.URL)
						return nil, fmt.Errorf("unexpected external request")
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"similarartists":{"artist":[{"name":"Another"}]}}`)), Header: make(http.Header)}, nil
				})
				selection := url.Values{}
				if scenario == "explicit-disabled" {
					selection.Set("next_track", "3")
				}
				response = call(app, "POST", mode.path, 1, selection, mode.fragment)
				requireCode(test, response, 200)
				wantCalls := 0
				if scenario == "exhausted-enabled" {
					wantCalls = 1
				}
				if calls != wantCalls {
					test.Fatalf("Last.fm calls=%d, want %d (similar=%v, autofeed=%v)", calls, wantCalls, similar, autofeed)
				}
				prefs, err = app.Store.Preferences(1)
				if err != nil {
					test.Fatal(err)
				}
				if !autofeed {
					if prefs.CurrentPosition != 0 {
						test.Fatal("disabled autofeed selected a song")
					}
				} else {
					item, err := app.Store.queueItem(1, "a.id=?", prefs.CurrentPosition)
					if err != nil {
						test.Fatal("no selected queue item", err)
					}
					if (scenario == "exhausted-enabled" || scenario == "explicit-disabled") && item.MusicFileID != 3 {
						test.Fatal("wrong selected track", item)
					}
				}
			})
		}
	}
}

func TestNextSongSelectionAndOwnership(test *testing.T) {
	for _, scenario := range []string{"first", "advance", "foreign-current", "foreign-selection", "tied-ranks", "invalid-selection", "autofeed-blacklist", "empty-library"} {
		test.Run(scenario, func(test *testing.T) {
			app := testApp(test)
			mustTag(test, app, 2, "queue", []int64{3})
			queueID := mustTag(test, app, 1, "queue", []int64{1, 2})
			items, err := app.Store.Queue(1)
			if err != nil {
				test.Fatal(err)
			}
			position := int64(0)
			form := url.Values{}
			wantTrack, wantRank, wantPosition := int64(1), int64(1), items[0].ID
			switch scenario {
			case "advance":
				position = items[0].ID
				wantTrack, wantRank, wantPosition = 2, 2, items[1].ID
			case "foreign-current":
				foreign, err := app.Store.Queue(2)
				if err != nil {
					test.Fatal(err)
				}
				position = foreign[0].ID
			case "foreign-selection":
				form.Set("list_item", "3")
			case "tied-ranks":
				position = items[0].ID
				wantTrack, wantRank, wantPosition = 0, 0, 0
				if _, err = app.Store.DB.Exec("UPDATE reinhardt_musicfiletag SET rank=1 WHERE tag_id=?", queueID); err != nil {
					test.Fatal(err)
				}
			case "invalid-selection":
				position = items[0].ID
				form.Set("list_item", "bad")
				wantTrack, wantRank, wantPosition = 2, 2, items[1].ID
			case "autofeed-blacklist":
				position = items[1].ID
				wantRank = 3
				mustTag(test, app, 1, "blacklist", []int64{2, 3})
			case "empty-library":
				position = items[1].ID
				wantTrack, wantRank, wantPosition = 0, 0, 0
				if _, err = app.Store.DB.Exec("DELETE FROM reinhardt_musicfile"); err != nil {
					test.Fatal(err)
				}
			}
			autofeed := scenario == "autofeed-blacklist" || scenario == "empty-library"
			if _, err = app.Store.DB.Exec("UPDATE reinhardt_preferences SET current_position=?,autofeed_queue=?,autofeed_similar=0 WHERE owner_id=1", position, autofeed); err != nil {
				test.Fatal(err)
			}
			response := call(app, "POST", "/next_song/", 1, form, true)
			requireCode(test, response, 200)
			prefs, err := app.Store.Preferences(1)
			if err != nil || prefs.CurrentPosition != wantPosition {
				test.Fatal("wrong saved playback position", prefs, err)
			}
			if wantTrack == 0 {
				if strings.Contains(response.Body.String(), `data-url=`) || !strings.Contains(response.Body.String(), `data-position=""`) || !strings.Contains(response.Body.String(), `onclick="playNext()"`) {
					test.Fatal("empty playback state incorrect")
				}
			} else {
				track, err := app.Store.Track(wantTrack)
				if err != nil {
					test.Fatal(err)
				}
				if !strings.Contains(response.Body.String(), `data-url="`+track.PlaybackURL+`"`) {
					test.Fatal("wrong track selected")
				}
				selected, err := app.Store.queueItem(1, "a.id=?", wantPosition)
				if err != nil || selected.Rank != wantRank {
					test.Fatal("wrong queue rank", selected, err)
				}
			}
			foreign, err := app.Store.Queue(2)
			if err != nil {
				test.Fatal(err)
			}
			if scenario != "empty-library" && (len(foreign) != 1 || foreign[0].MusicFileID != 3 || foreign[0].Rank != 1) {
				test.Fatal("another user's queue changed")
			}
		})
	}
}
