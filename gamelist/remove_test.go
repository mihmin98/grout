package gamelist

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

const removeFixture = `<?xml version="1.0" encoding="UTF-8"?>
<gameList>
    <game>
        <path>./Grout/Grout.sh</path>
        <name>Grout</name>
    </game>
    <game>
        <path>./Sonic.gba</path>
        <name>Sonic relative</name>
    </game>
    <game>
        <path>/roms/gba/Sonic.gba</path>
        <name>Sonic absolute</name>
    </game>
    <game>
        <path>/roms/gba/Zelda.gba</path>
        <name>Zelda</name>
    </game>
    <game>
        <path>./FF7.m3u</path>
        <name>FF7 top</name>
    </game>
    <game>
        <path>./FF7/FF7.m3u</path>
        <name>FF7 nested</name>
    </game>
    <game>
        <name>No path</name>
    </game>
</gameList>`

func parsed(t *testing.T, doc string) *GameList {
	t.Helper()
	gl := New()
	if err := gl.Parse([]byte(doc)); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return gl
}

func TestRemoveRomEntry(t *testing.T) {
	tests := []struct {
		name      string
		entryPath string
		removed   int
		left      []string
	}{
		{
			// Both spellings of the same file go, the relative one resolved
			// against the list's folder.
			name:      "absolute and relative duplicates",
			entryPath: "/roms/gba/Sonic.gba",
			removed:   2,
			left:      []string{"Grout", "Zelda", "FF7 top", "FF7 nested", "No path"},
		},
		{
			name:      "the relative form names the same file",
			entryPath: "./Zelda.gba",
			removed:   1,
			left:      []string{"Grout", "Sonic relative", "Sonic absolute", "FF7 top", "FF7 nested", "No path"},
		},
		{
			name:      "a top-level playlist is not the nested one",
			entryPath: "/roms/gba/FF7.m3u",
			removed:   1,
			left:      []string{"Grout", "Sonic relative", "Sonic absolute", "Zelda", "FF7 nested", "No path"},
		},
		{
			name:      "a nested playlist is not the top-level one",
			entryPath: "/roms/gba/FF7/FF7.m3u",
			removed:   1,
			left:      []string{"Grout", "Sonic relative", "Sonic absolute", "Zelda", "FF7 top", "No path"},
		},
		{
			name:      "an unknown path removes nothing",
			entryPath: "/roms/gba/Other/Sonic.gba",
			left:      []string{"Grout", "Sonic relative", "Sonic absolute", "Zelda", "FF7 top", "FF7 nested", "No path"},
		},
		{
			name: "an empty path removes nothing",
			left: []string{"Grout", "Sonic relative", "Sonic absolute", "Zelda", "FF7 top", "FF7 nested", "No path"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gl := parsed(t, removeFixture)
			if got := gl.RemoveRomEntry("/roms/gba", tt.entryPath); got != tt.removed {
				t.Errorf("removed = %d, want %d", got, tt.removed)
			}
			if got := gameElements(t, gl); !slices.Equal(got, tt.left) {
				t.Errorf("left = %q, want %q", got, tt.left)
			}
		})
	}
}

func TestRemoveRomEntry_NoRoot(t *testing.T) {
	gl := parsed(t, `<?xml version="1.0"?><notAGameList><game><path>./A.gba</path></game></notAGameList>`)
	if got := gl.RemoveRomEntry("/roms/gba", "/roms/gba/A.gba"); got != 0 {
		t.Errorf("removed = %d from a document with no gameList root", got)
	}
}

func writeList(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), string(GameListFileName))
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRemoveRomEntries(t *testing.T) {
	fixture := `<?xml version="1.0" encoding="UTF-8"?>
<gameList>
    <game><path>./Grout/Grout.sh</path><name>Grout</name></game>
    <game><path>./Sonic.gba</path><name>Sonic</name></game>
    <game><path>./Zelda.gba</path><name>Zelda</name></game>
    <game><path>./Metroid.gba</path><name>Metroid</name></game>
</gameList>`
	listFile := writeList(t, fixture)
	dir := filepath.Dir(listFile)

	removed, err := RemoveRomEntries(listFile, []string{filepath.Join(dir, "Sonic.gba"), filepath.Join(dir, "Metroid.gba")})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}

	data, err := os.ReadFile(listFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := gameElements(t, parsed(t, string(data))); !slices.Equal(got, []string{"Grout", "Zelda"}) {
		t.Errorf("saved list holds %q, want Grout and Zelda", got)
	}
}

func TestRemoveRomEntries_MissingFile(t *testing.T) {
	removed, err := RemoveRomEntries(filepath.Join(t.TempDir(), "gamelist.xml"), []string{"/roms/gba/A.gba"})
	if removed != 0 || err != nil {
		t.Errorf("RemoveRomEntries = %d, %v; want 0, nil", removed, err)
	}
}

// A list that cannot be read as XML belongs to someone else's tool or is
// damaged; it is reported and never rewritten.
func TestRemoveRomEntries_UnparsableFileIsUntouched(t *testing.T) {
	content := "<gameList><game><path>./A.gba</path>"
	listFile := writeList(t, content)

	if _, err := RemoveRomEntries(listFile, []string{filepath.Join(filepath.Dir(listFile), "A.gba")}); err == nil {
		t.Error("expected an error for a list that cannot be parsed")
	}
	data, err := os.ReadFile(listFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte(content)) {
		t.Errorf("the file changed: %q", data)
	}
}

// Nothing to remove means nothing to write: the frontend's own formatting
// stays as it was.
func TestRemoveRomEntries_NothingToRemoveDoesNotRewrite(t *testing.T) {
	content := `<gameList><game><path>./A.gba</path><name>A</name></game></gameList>`
	listFile := writeList(t, content)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(listFile, old, old); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveRomEntries(listFile, []string{"/elsewhere/A.gba"})
	if removed != 0 || err != nil {
		t.Fatalf("RemoveRomEntries = %d, %v; want 0, nil", removed, err)
	}

	info, err := os.Stat(listFile)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("mtime = %v, want %v: the file was rewritten", info.ModTime(), old)
	}
	data, _ := os.ReadFile(listFile)
	if string(data) != content {
		t.Errorf("content changed: %q", data)
	}
}
