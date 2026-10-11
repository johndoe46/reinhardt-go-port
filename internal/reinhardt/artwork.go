package reinhardt

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type coverResult struct {
	image   image.Image
	expires time.Time
}

type coverFlight struct {
	done  chan struct{}
	image image.Image
}

var discSuffix = regexp.MustCompile(`(?i) [\(\[{]disc [\dIVX]+[}\)\]]`)
var romanWord = regexp.MustCompile(`(?i)^[IVXLCDM]+$`)

func coverName(name string, artist bool) string {
	if artist {
		if last, first, ok := strings.Cut(name, ","); ok {
			name = strings.TrimSpace(first) + " " + strings.TrimSpace(last)
		}
	} else {
		name = discSuffix.ReplaceAllString(name, "")
	}
	name = strings.ToLower(name)
	name = strings.NewReplacer("-", " ", "–", " ", "&", " and ").Replace(name)
	name = strings.TrimPrefix(strings.TrimPrefix(name, "the "), "a ")
	return strings.Join(strings.Fields(strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || unicode.IsSpace(character) || character == '_' {
			return character
		}
		return -1
	}, name)), " ")
}
func decimalRomans(text string) string {
	words := strings.Fields(text)
	values := map[rune]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100, 'D': 500, 'M': 1000}
	for wordIndex, word := range words {
		if !romanWord.MatchString(word) {
			continue
		}
		runes := []rune(strings.ToUpper(word))
		decimalValue := 0
		for characterIndex, character := range runes {
			romanValue := values[character]
			if characterIndex+1 < len(runes) && romanValue < values[runes[characterIndex+1]] {
				decimalValue -= romanValue
			} else {
				decimalValue += romanValue
			}
		}
		words[wordIndex] = strconv.Itoa(decimalValue)
	}
	return strings.Join(words, " ")
}

// Match the original get-cover-art lookup against Apple's music search API.
// Cache failed lookups as well, since players request several sizes at once.
func (app *App) discoverCover(ctx context.Context, track Row) image.Image {
	if ctx.Err() != nil {
		return nil
	}
	artist, album, title := coverName(track.S("artist"), true), coverName(track.S("album"), false), coverName(track.S("title"), false)
	name := album
	if name == "" {
		name = title
	}
	key := artist + "/" + name
	app.coverMu.Lock()
	if cached, ok := app.coverCache[key]; ok && time.Now().Before(cached.expires) {
		app.coverMu.Unlock()
		return cached.image
	}
	flight := app.coverFlights[key]
	if flight == nil {
		if app.coverFlights == nil {
			app.coverFlights = make(map[string]*coverFlight)
		}
		flight = &coverFlight{done: make(chan struct{})}
		app.coverFlights[key] = flight
		// A cancelled image request must not cancel discovery shared by other
		// sizes or tracks on this album. Keep the existing eight-second bound.
		lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		// A successful lookup writes a sidecar for its first caller's track only.
		// Other tracks on the album reuse the image through the in-memory cache.
		path := track.S("full_path")
		go func() {
			defer cancel()
			im := app.fetchCover(lookupCtx, path, artist, album, title)
			app.coverMu.Lock()
			app.coverCache[key] = coverResult{image: im, expires: time.Now().Add(time.Hour)}
			flight.image = im
			delete(app.coverFlights, key)
			close(flight.done)
			app.coverMu.Unlock()
		}()
	}
	app.coverMu.Unlock()
	select {
	case <-ctx.Done():
		return nil
	case <-flight.done:
		return flight.image
	}
}

func (app *App) fetchCover(ctx context.Context, path, artist, album, title string) image.Image {
	if artist == "" || (album == "" && title == "") {
		return nil
	}
	type cover struct {
		ArtistName     string
		CollectionName string
		Artwork        string `json:"artworkUrl100"`
		ReleaseDate    string
	}
	var matches []cover
	for attempt := 0; attempt < 2; attempt++ {
		token := album
		entity := "album"
		if token == "" {
			token = title
			entity = "musicTrack"
		}
		term := artist + " " + token
		if strings.Contains(artist, token) {
			term = artist
		} else if strings.Contains(token, artist) {
			term = token
		}
		query := url.Values{"term": {term}, "media": {"music"}, "entity": {entity}}
		searchResponse, _, err := app.request(ctx, "GET", "https://itunes.apple.com/search?"+query.Encode(), map[string]string{"User-Agent": "Reinhardt/1.0"}, nil)
		if err != nil {
			return nil
		}
		var data struct{ Results []cover }
		if json.Unmarshal(searchResponse, &data) != nil {
			return nil
		}
		matches = data.Results
		if len(matches) > 0 {
			break
		}
		artist = decimalRomans(artist)
		album = decimalRomans(album)
	}
	if album == "" {
		sort.SliceStable(matches, func(firstIndex, secondIndex int) bool {
			return matches[firstIndex].ReleaseDate < matches[secondIndex].ReleaseDate
		})
	}
	art := ""
	for matchIndex := len(matches) - 1; matchIndex >= 0; matchIndex-- {
		candidate := matches[matchIndex]
		foundArtist, foundAlbum := coverName(candidate.ArtistName, true), coverName(candidate.CollectionName, false)
		if !strings.Contains(foundArtist, artist) || (album != "" && !strings.Contains(foundAlbum, album)) {
			continue
		}
		art = strings.ReplaceAll(candidate.Artwork, "100x100bb", "500x500bb")
		if album != "" && foundAlbum == album {
			break
		}
	}
	// Search results supply the image URL. Restrict the follow-up request to
	// Apple's HTTPS image hosts instead of fetching arbitrary returned locations.
	artworkURL, err := url.Parse(art)
	if err != nil || artworkURL.Scheme != "https" || (artworkURL.Hostname() != "mzstatic.com" && !strings.HasSuffix(artworkURL.Hostname(), ".mzstatic.com")) {
		return nil
	}
	imageData, _, err := app.request(ctx, "GET", art, nil, nil)
	if err != nil {
		return nil
	}
	im, _, err := decodedImage(bytes.NewReader(imageData))
	if err != nil {
		return nil
	}
	var encoded bytes.Buffer
	if jpeg.Encode(&encoded, im, &jpeg.Options{Quality: 95}) != nil {
		return nil
	}
	if atomicFile(path+".jpg", bytes.NewReader(encoded.Bytes()), false) != nil {
		return nil
	}
	return im
}
