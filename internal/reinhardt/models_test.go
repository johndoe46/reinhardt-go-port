package reinhardt

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTypedTrackPreservesAPIFieldsAndNullHash(test *testing.T) {
	app := testApp(test)
	for _, hash := range []any{nil, "", "track hash"} {
		if _, err := app.Store.DB.Exec("UPDATE reinhardt_musicfile SET hash=? WHERE id=1", hash); err != nil {
			test.Fatal(err)
		}
		response := call(app, "GET", "/api/music_files/search/?id=1", 1, nil, true)
		requireCode(test, response, 200)
		var payload struct{ Results []Row }
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			test.Fatal(err)
		}
		if len(payload.Results) != 1 {
			test.Fatal("missing track", payload.Results)
		}
		track := payload.Results[0]
		if !reflect.DeepEqual(track["hash"], hash) {
			test.Fatalf("hash=%v, want %v", track["hash"], hash)
		}
		if len(track) != 16 || track["id"] != float64(1) || track["music_directory"] != float64(1) || track["missing"] != false || track["duration"] != float64(10) || track["artist"] != "Artist" {
			test.Fatal("typed track changed its public representation", track)
		}
	}
}
