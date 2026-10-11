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

func coverName(s string, artist bool) string {
	if artist {
		if last, first, ok := strings.Cut(s, ","); ok {
			s = strings.TrimSpace(first) + " " + strings.TrimSpace(last)
		}
	} else {
		s = discSuffix.ReplaceAllString(s, "")
	}
	s = strings.ToLower(s)
	s = strings.NewReplacer("-", " ", "–", " ", "&", " and ").Replace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "the "), "a ")
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) || r == '_' {
			return r
		}
		return -1
	}, s)), " ")
}
func decimalRomans(s string) string {
	words := strings.Fields(s)
	values := map[rune]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100, 'D': 500, 'M': 1000}
	for i, word := range words {
		if !romanWord.MatchString(word) {
			continue
		}
		runes := []rune(strings.ToUpper(word))
		n := 0
		for j, r := range runes {
			v := values[r]
			if j+1 < len(runes) && v < values[runes[j+1]] {
				n -= v
			} else {
				n += v
			}
		}
		words[i] = strconv.Itoa(n)
	}
	return strings.Join(words, " ")
}

// Match the original get-cover-art lookup against Apple's music search API.
// Cache failed lookups as well, since players request several sizes at once.
func (a *App) discoverCover(ctx context.Context, m Row) image.Image {
	if ctx.Err() != nil {
		return nil
	}
	artist, album, title := coverName(m.S("artist"), true), coverName(m.S("album"), false), coverName(m.S("title"), false)
	name := album
	if name == "" {
		name = title
	}
	key := artist + "/" + name
	a.coverMu.Lock()
	if cached, ok := a.coverCache[key]; ok && time.Now().Before(cached.expires) {
		a.coverMu.Unlock()
		return cached.image
	}
	flight := a.coverFlights[key]
	if flight == nil {
		if a.coverFlights == nil {
			a.coverFlights = make(map[string]*coverFlight)
		}
		flight = &coverFlight{done: make(chan struct{})}
		a.coverFlights[key] = flight
		// A cancelled image request must not cancel discovery shared by other
		// sizes or tracks on this album. Keep the existing eight-second bound.
		lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		// A successful lookup writes a sidecar for its first caller's track only.
		// Other tracks on the album reuse the image through the in-memory cache.
		path := m.S("full_path")
		go func() {
			defer cancel()
			im := a.fetchCover(lookupCtx, path, artist, album, title)
			a.coverMu.Lock()
			a.coverCache[key] = coverResult{image: im, expires: time.Now().Add(time.Hour)}
			flight.image = im
			delete(a.coverFlights, key)
			close(flight.done)
			a.coverMu.Unlock()
		}()
	}
	a.coverMu.Unlock()
	select {
	case <-ctx.Done():
		return nil
	case <-flight.done:
		return flight.image
	}
}

func (a *App) fetchCover(ctx context.Context, path, artist, album, title string) image.Image {
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
		b, _, e := a.request(ctx, "GET", "https://itunes.apple.com/search?"+query.Encode(), map[string]string{"User-Agent": "Reinhardt/1.0"}, nil)
		if e != nil {
			return nil
		}
		var data struct{ Results []cover }
		if json.Unmarshal(b, &data) != nil {
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
		sort.SliceStable(matches, func(i, j int) bool { return matches[i].ReleaseDate < matches[j].ReleaseDate })
	}
	art := ""
	for i := len(matches) - 1; i >= 0; i-- {
		candidate := matches[i]
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
	u, e := url.Parse(art)
	if e != nil || u.Scheme != "https" || (u.Hostname() != "mzstatic.com" && !strings.HasSuffix(u.Hostname(), ".mzstatic.com")) {
		return nil
	}
	b, _, e := a.request(ctx, "GET", art, nil, nil)
	if e != nil {
		return nil
	}
	im, _, e := decodedImage(bytes.NewReader(b))
	if e != nil {
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
