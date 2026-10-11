package cfw

import (
	"path/filepath"
	"testing"

	"grout/cfw/muos"
	"grout/gamelist"
	"grout/library"
)

// MetadataLocationFor has to name what FillGamesMetadata writes on each
// firmware, or a deletion leaves the entry behind.
func TestMetadataLocationFor(t *testing.T) {
	t.Setenv("BASE_PATH", t.TempDir())

	entry := gamelist.RomGameEntry{
		Game:         library.Game{FileName: "Game.gba", BaseName: "Game", Path: "/roms/gba/Game.gba"},
		Platform:     library.Platform{FSSlug: "gba", Name: "Game Boy Advance"},
		RomDirectory: "/roms/gba",
		RomID:        7,
	}

	tests := []struct {
		cfw  CFW
		want func() MetadataLocation
	}{
		{ROCKNIX, func() MetadataLocation {
			return MetadataLocation{RomID: 7, File: "/roms/gba/gamelist.xml", Entry: "/roms/gba/Game.gba"}
		}},
		{Spruce, func() MetadataLocation {
			return MetadataLocation{RomID: 7, File: "/roms/gba/miyoogamelist.xml", Entry: "/roms/gba/Game.gba"}
		}},
		{MuOS, func() MetadataLocation {
			return MetadataLocation{RomID: 7, File: filepath.Join(muos.GetTextDirectory("gba", "Game Boy Advance"), "Game.txt")}
		}},
		{NextUI, func() MetadataLocation { return MetadataLocation{RomID: 7} }},
	}

	for _, tt := range tests {
		t.Run(string(tt.cfw), func(t *testing.T) {
			t.Setenv(EnvVar, string(tt.cfw))
			if got, want := MetadataLocationFor(entry), tt.want(); got != want {
				t.Errorf("MetadataLocationFor = %+v, want %+v", got, want)
			}
		})
	}
}

// An entry without a path cannot be found again by it.
func TestMetadataLocationFor_NoPath(t *testing.T) {
	t.Setenv(EnvVar, string(ROCKNIX))
	entry := gamelist.RomGameEntry{Game: library.Game{FileName: "Game.gba"}, RomDirectory: "/roms/gba", RomID: 7}
	if got := MetadataLocationFor(entry); got != (MetadataLocation{RomID: 7}) {
		t.Errorf("MetadataLocationFor = %+v, want no file", got)
	}
}
