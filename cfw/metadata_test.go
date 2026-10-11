package cfw

import (
	"os"
	"path/filepath"
	"strings"
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

const twoGames = `<?xml version="1.0" encoding="UTF-8"?>
<gameList>
    <game><path>./Sonic.gba</path><name>Sonic</name></game>
    <game><path>./Zelda.gba</path><name>Zelda</name></game>
</gameList>`

// On EmulationStation the frontend has to reload to drop the entry, and only
// when one actually went.
func TestRemoveGamesMetadata_EmulationStation(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(EnvVar, string(ROCKNIX))

	romDir := t.TempDir()
	listFile := filepath.Join(romDir, "gamelist.xml")
	writeFile(t, listFile, twoGames)

	removed, err := RemoveGamesMetadata([]MetadataLocation{
		{RomID: 1, File: listFile, Entry: filepath.Join(romDir, "Other.gba")},
	})
	if removed != 0 || err != nil {
		t.Fatalf("RemoveGamesMetadata = %d, %v; want 0, nil", removed, err)
	}
	if _, err := os.Stat(esRestartFlag); !os.IsNotExist(err) {
		t.Error("an ES restart was requested though nothing was removed")
	}

	removed, err = RemoveGamesMetadata([]MetadataLocation{
		{RomID: 1, File: listFile, Entry: filepath.Join(romDir, "Sonic.gba")},
	})
	if removed != 1 || err != nil {
		t.Fatalf("RemoveGamesMetadata = %d, %v; want 1, nil", removed, err)
	}
	if _, err := os.Stat(esRestartFlag); err != nil {
		t.Errorf("no ES restart requested after removing an entry: %v", err)
	}

	data, err := os.ReadFile(listFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Sonic") || !strings.Contains(string(data), "Zelda") {
		t.Errorf("list after removal:\n%s", data)
	}
}

func TestRemoveGamesMetadata_Miyoo(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(EnvVar, string(Spruce))

	romDir := t.TempDir()
	listFile := filepath.Join(romDir, "miyoogamelist.xml")
	writeFile(t, listFile, twoGames)

	removed, err := RemoveGamesMetadata([]MetadataLocation{
		{RomID: 1, File: listFile, Entry: filepath.Join(romDir, "Zelda.gba")},
	})
	if removed != 1 || err != nil {
		t.Fatalf("RemoveGamesMetadata = %d, %v; want 1, nil", removed, err)
	}
	if _, err := os.Stat(esRestartFlag); !os.IsNotExist(err) {
		t.Error("an ES restart was requested on a firmware without EmulationStation")
	}
}

// Only the firmware's own gamelist is edited, whatever a location names.
func TestRemoveGamesMetadata_RefusesAnotherFile(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv(EnvVar, string(ROCKNIX))

	romDir := t.TempDir()
	other := filepath.Join(romDir, "miyoogamelist.xml")
	writeFile(t, other, twoGames)

	removed, err := RemoveGamesMetadata([]MetadataLocation{
		{RomID: 1, File: other, Entry: filepath.Join(romDir, "Sonic.gba")},
	})
	if removed != 0 || err == nil {
		t.Errorf("RemoveGamesMetadata = %d, %v; want 0 and an error", removed, err)
	}
	if data, _ := os.ReadFile(other); string(data) != twoGames {
		t.Errorf("the other file changed:\n%s", data)
	}
}

func TestRemoveGamesMetadata_MuOS(t *testing.T) {
	t.Setenv("BASE_PATH", t.TempDir())
	t.Setenv(EnvVar, string(MuOS))

	textDir := muos.GetTextDirectory("gba", "Game Boy Advance")
	text := filepath.Join(textDir, "Game.txt")
	keep := filepath.Join(textDir, "Other.txt")
	writeFile(t, text, "Name: Game\n")
	writeFile(t, keep, "Name: Other\n")

	// Not a description text: outside the catalogue, or not a .txt.
	outside := filepath.Join(t.TempDir(), "text", "Game.txt")
	notText := filepath.Join(textDir, "Game.png")
	writeFile(t, outside, "x")
	writeFile(t, notText, "x")

	removed, err := RemoveGamesMetadata([]MetadataLocation{
		{RomID: 1, File: text},
		{RomID: 1, File: filepath.Join(textDir, "Gone.txt")}, // already gone
		{RomID: 1, File: outside},
		{RomID: 1, File: notText},
	})
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if err == nil {
		t.Error("expected an error for the locations that are not muOS texts")
	}

	if _, err := os.Stat(text); !os.IsNotExist(err) {
		t.Error("the game's text file is still there")
	}
	for _, path := range []string{keep, outside, notText} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed: %v", path, err)
		}
	}
}

func TestIsMuOSText(t *testing.T) {
	catalogue := "/mnt/mmc/MUOS/info/catalogue"
	tests := map[string]bool{
		catalogue + "/Nintendo Game Boy Advance/text/Game.txt": true,
		catalogue + "/GBA/text/sub/Game.txt":                   false,
		catalogue + "/GBA/box/Game.txt":                        false,
		catalogue + "/GBA/text/Game.png":                       false,
		catalogue + "/../text/Game.txt":                        false,
		"/mnt/mmc/MUOS/info/text/Game.txt":                     false,
		"GBA/text/Game.txt":                                    false,
	}
	for path, want := range tests {
		if got := isMuOSText(catalogue, path); got != want {
			t.Errorf("isMuOSText(%q) = %v, want %v", path, got, want)
		}
	}
}
