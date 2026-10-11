package reinhardt

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestFallbackArtworkDiacriticGlyphsAndNormalization(t *testing.T) {
	for _, r := range "éóÉÓàâäåçñøőűŁřźßάЖй" {
		index, err := fallbackArtworkFont.GlyphIndex(nil, r)
		if err != nil || index == 0 {
			t.Fatalf("embedded font lacks a glyph for %q: %v", r, err)
		}
	}
	render := func(artist, title string) *image.RGBA {
		im := image.NewRGBA(image.Rect(0, 0, 512, 512))
		drawFallbackArtworkText(im, color.Black, artist, title)
		return im
	}
	artist, title := "Béla Bartók", "Étude n° 3 – őű"
	composed := render(artist, title)
	decomposed := render(norm.NFD.String(artist), norm.NFD.String(title))
	if !bytes.Equal(composed.Pix, decomposed.Pix) {
		t.Fatal("combining accents must render identically to composed letters")
	}
	if bytes.Equal(composed.Pix, render("Bela Bartok", "Etude n° 3 – ou").Pix) {
		t.Fatal("diacritics were dropped while drawing artwork labels")
	}
	for _, bounds := range []image.Rectangle{image.Rect(32, 425, 480, 451), image.Rect(32, 453, 480, 485)} {
		found := false
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				found = found || composed.RGBAAt(x, y).A != 0
			}
		}
		if !found {
			t.Fatal("artwork label was not drawn at its existing position", bounds)
		}
	}
	// Normalization must happen before the character limit, preserving accents
	// at the truncation boundary and leaving the right-hand sleeve margin clear.
	long := strings.Repeat("é", 80)
	if !bytes.Equal(render(long, long).Pix, render(norm.NFD.String(long), norm.NFD.String(long)).Pix) {
		t.Fatal("truncation split or miscounted a combining accent")
	}
	truncated := render(long, long)
	for y := 425; y < 485; y++ {
		for x := 480; x < 512; x++ {
			if truncated.RGBAAt(x, y).A != 0 {
				t.Fatal("artwork text exceeded the sleeve margin")
			}
		}
	}
}

func TestFallbackArtworkConcurrentRendering(t *testing.T) {
	m := Row{"artist": "Béla Bartók", "album": "", "title": "No. 3"}
	want := fallbackArtwork(m, 512).(*image.RGBA)
	const renders = 8
	results := make(chan *image.RGBA, renders)
	for range renders {
		go func() { results <- fallbackArtwork(m, 512).(*image.RGBA) }()
	}
	for range renders {
		if got := <-results; !bytes.Equal(want.Pix, got.Pix) {
			t.Fatal("concurrent rendering corrupted artwork text or sleeve pixels")
		}
	}
	// Optional visual inspection artifact; ordinary tests write no preview file.
	if path := os.Getenv("REINHARDT_ARTWORK_PREVIEW"); path != "" {
		var b bytes.Buffer
		if err := png.Encode(&b, want); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
