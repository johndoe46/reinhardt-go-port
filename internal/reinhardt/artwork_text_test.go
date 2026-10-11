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

func TestFallbackArtworkDiacriticGlyphsAndNormalization(test *testing.T) {
	for _, character := range "éóÉÓàâäåçñøőűŁřźßάЖй" {
		index, err := fallbackArtworkFont.GlyphIndex(nil, character)
		if err != nil || index == 0 {
			test.Fatalf("embedded font lacks a glyph for %q: %v", character, err)
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
		test.Fatal("combining accents must render identically to composed letters")
	}
	if bytes.Equal(composed.Pix, render("Bela Bartok", "Etude n° 3 – ou").Pix) {
		test.Fatal("diacritics were dropped while drawing artwork labels")
	}
	for _, bounds := range []image.Rectangle{image.Rect(32, 425, 480, 451), image.Rect(32, 453, 480, 485)} {
		found := false
		for pixelY := bounds.Min.Y; pixelY < bounds.Max.Y; pixelY++ {
			for pixelX := bounds.Min.X; pixelX < bounds.Max.X; pixelX++ {
				found = found || composed.RGBAAt(pixelX, pixelY).A != 0
			}
		}
		if !found {
			test.Fatal("artwork label was not drawn at its existing position", bounds)
		}
	}
	// Normalization must happen before the character limit, preserving accents
	// at the truncation boundary and leaving the right-hand sleeve margin clear.
	long := strings.Repeat("é", 80)
	if !bytes.Equal(render(long, long).Pix, render(norm.NFD.String(long), norm.NFD.String(long)).Pix) {
		test.Fatal("truncation split or miscounted a combining accent")
	}
	truncated := render(long, long)
	for pixelY := 425; pixelY < 485; pixelY++ {
		for pixelX := 480; pixelX < 512; pixelX++ {
			if truncated.RGBAAt(pixelX, pixelY).A != 0 {
				test.Fatal("artwork text exceeded the sleeve margin")
			}
		}
	}
}

func TestFallbackArtworkConcurrentRendering(test *testing.T) {
	track := Row{"artist": "Béla Bartók", "album": "", "title": "No. 3"}
	want := fallbackArtwork(track, 512).(*image.RGBA)
	const renders = 8
	results := make(chan *image.RGBA, renders)
	for range renders {
		go func() { results <- fallbackArtwork(track, 512).(*image.RGBA) }()
	}
	for range renders {
		if got := <-results; !bytes.Equal(want.Pix, got.Pix) {
			test.Fatal("concurrent rendering corrupted artwork text or sleeve pixels")
		}
	}
	// Optional visual inspection artifact; ordinary tests write no preview file.
	if path := os.Getenv("REINHARDT_ARTWORK_PREVIEW"); path != "" {
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, want); err != nil {
			test.Fatal(err)
		}
		if err := os.WriteFile(path, buffer.Bytes(), 0600); err != nil {
			test.Fatal(err)
		}
	}
}
