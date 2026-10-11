package reinhardt

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/text/unicode/norm"
)

var fallbackArtworkFont = func() *opentype.Font {
	// The vendored font is compiled into the executable, without system fonts.
	parsed, err := opentype.Parse(gomono.TTF)
	if err != nil {
		panic(fmt.Errorf("parse embedded artwork font: %w", err))
	}
	return parsed
}()

func drawFallbackArtworkText(dst draw.Image, ink color.Color, artist, title string) {
	// The parsed font is immutable; rasterizing faces have mutable buffers and
	// must belong to a single render. At 12px, 62 monospaced glyphs fit the sleeve.
	face, err := opentype.NewFace(fallbackArtworkFont, &opentype.FaceOptions{Size: 12, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(fmt.Errorf("create artwork font face: %w", err))
	}
	defer face.Close()
	drawer := font.Drawer{Dst: dst, Src: image.NewUniform(ink), Face: face}
	for i, text := range []string{artist, title} {
		// Compose base letters and combining marks before glyph lookup/truncation.
		runes := []rune(norm.NFC.String(text))
		if len(runes) > 62 {
			runes = append(runes[:59], '.', '.', '.')
		}
		drawer.Dot = fixed.P(32, 445+i*28)
		drawer.DrawString(string(runes))
	}
}
