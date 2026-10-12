package ui

import (
	"strings"
	"testing"

	"grout/romm"

	gaba "github.com/BrandonKowalski/gabagool/v2/pkg/gabagool"
)

func labelled(facts []gaba.MetadataItem, label string) (string, bool) {
	for _, fact := range facts {
		if fact.Label == label {
			return fact.Value, true
		}
	}
	return "", false
}

// A game RomM knows little about must not show a column of blank rows.
func TestGameFacts_SkipsWhatIsNotKnown(t *testing.T) {
	facts := gameFacts(romm.Rom{Name: "Homebrew"})

	if len(facts) != 0 {
		t.Errorf("facts = %v, want none for a game with no metadata", facts)
	}
}

func TestGameFacts_Values(t *testing.T) {
	game := romm.Rom{
		Name:        "Mario",
		Regions:     []string{"USA", "EUR"},
		Languages:   []string{"En"},
		FsSizeBytes: 4 * 1024 * 1024,
	}
	game.Metadatum.Genres = []string{"Platform", "Adventure"}
	game.Metadatum.AverageRating = 92.4

	facts := gameFacts(game)

	want := map[string]string{
		"Genres":         "Platform, Adventure",
		"Regions":        "USA, EUR",
		"Languages":      "En",
		"Average Rating": "92.4/100",
	}
	for label, value := range want {
		got, ok := labelled(facts, label)
		if !ok {
			t.Errorf("no %q row", label)
			continue
		}
		if got != value {
			t.Errorf("%s = %q, want %q", label, got, value)
		}
	}

	if size, ok := labelled(facts, "File Size"); !ok || !strings.Contains(size, "MB") {
		t.Errorf("File Size = %q, want it in readable units", size)
	}
}

// The rows keep a fixed order so the screen does not rearrange itself
// depending on which metadata a game happens to carry.
func TestGameFacts_Order(t *testing.T) {
	game := romm.Rom{
		Regions:     []string{"USA"},
		Languages:   []string{"En"},
		FsSizeBytes: 1024,
		Files:       []romm.RomFile{{FileName: "Mario.nes"}},
	}
	game.Metadatum.Genres = []string{"Platform"}

	facts := gameFacts(game)

	order := make([]string, 0, len(facts))
	for _, fact := range facts {
		order = append(order, fact.Label)
	}

	want := []string{"Genres", "Regions", "Languages", "Filename", "File Size"}
	for i, label := range want {
		if i >= len(order) || order[i] != label {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// A multi-file game says so, and one that is not does not carry an empty row
// for it.
func TestGameFacts_MultiFile(t *testing.T) {
	if _, ok := labelled(gameFacts(romm.Rom{Name: "Mario"}), "Type"); ok {
		t.Error("a single-file game must have no Type row")
	}

	facts := gameFacts(romm.Rom{Name: "FF7", HasMultipleFiles: true})
	if value, ok := labelled(facts, "Type"); !ok || value == "" {
		t.Errorf("Type = %q, want a multi-file game to say so", value)
	}
}

// The filename is the one the game goes by on the card, not RomM's entry name.
func TestGameFacts_Filename(t *testing.T) {
	tests := []struct {
		name string
		game romm.Rom
		want string // empty: no Filename row
	}{
		{
			name: "single file",
			game: romm.Rom{FsName: "Mario.nes", Files: []romm.RomFile{{FileName: "Mario.nes"}}},
			want: "Mario.nes",
		},
		{
			name: "extras listed first are skipped",
			game: romm.Rom{Files: []romm.RomFile{
				{FileName: "Manual.pdf", Category: "manual"},
				{FileName: "Mario.nes"},
			}},
			want: "Mario.nes",
		},
		{
			name: "nested single file uses the file, not the folder",
			game: romm.Rom{
				FsName: "Mario", HasNestedSingleFile: true,
				Files: []romm.RomFile{{FileName: "Mario (USA).nes"}},
			},
			want: "Mario (USA).nes",
		},
		{
			name: "multi-disc uses the folder it unpacks into",
			game: romm.Rom{
				FsName: "FF7.zip", FsNameNoExt: "FF7", HasMultipleFiles: true,
				Files: []romm.RomFile{{FileName: "FF7 (Disc 1).chd"}, {FileName: "FF7 (Disc 2).chd"}},
			},
			want: "FF7",
		},
		{
			name: "several versions leave it to the picker",
			game: romm.Rom{
				FsName: "Mario", HasNestedSingleFile: true,
				Files: []romm.RomFile{{FileName: "Mario (USA).nes"}, {FileName: "Mario (EUR).nes"}},
			},
		},
		{
			name: "no file metadata",
			game: romm.Rom{FsName: "Mario.nes"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := labelled(gameFacts(tt.game), "Filename")
			if tt.want == "" {
				if ok {
					t.Errorf("Filename = %q, want no row", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("Filename = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDownloadLabel(t *testing.T) {
	if downloadLabel(true) == downloadLabel(false) {
		t.Error("having a game already and not having it must read differently")
	}
}
