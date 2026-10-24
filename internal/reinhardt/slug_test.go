package reinhardt

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestSlugTransliteratesUnicode(test *testing.T) {
	for _, scenario := range []struct {
		text, want string
	}{
		{"  Artist__Name -- Track 01!  ", "artist__name-track-01"},
		{"Björk - Déjà vu", "bjork-deja-vu"},
		{"Łódź", "lodz"},
		{"구룡", "guryong"},
		{"구중승천", "gujungseungceon"},
		{"백두의 소환", "baegduyi-sohwan"},
		{"왕조의 용맹", "wangjoyi-yongmaeng"},
		{norm.NFD.String("구룡"), "guryong"},
		{"東京", "dong-jing"},
		{"Привет мир", "privet-mir"},
		{"../../구룡\\", "guryong"},
		{"🎵", ""},
		{" --_ ", ""},
	} {
		test.Run(scenario.text, func(test *testing.T) {
			if got := slug(scenario.text); got != scenario.want {
				test.Fatalf("slug(%q)=%q, want %q", scenario.text, got, scenario.want)
			}
		})
	}
}

func TestLibraryPathRejectsEmptySlugs(test *testing.T) {
	for _, track := range []Track{
		{Artist: "", Title: "Title"},
		{Artist: "Artist", Title: ""},
		{Artist: "🎵", Title: "Title"},
		{Artist: "Artist", Title: "..."},
	} {
		if _, err := libraryPath(&track, ".mp3"); err == nil {
			test.Fatalf("accepted empty artist or title slug: %#v", track)
		}
	}
}
