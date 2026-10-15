package gamelist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repointFixture = `<?xml version="1.0" encoding="UTF-8"?>
<gameList>
    <game>
        <path>./Crystal.zip</path>
        <name>Crystal</name>
        <favorite>true</favorite>
        <playcount>12</playcount>
    </game>
    <game>
        <path>/roms/gb/Tetris.zip</path>
        <name>Tetris</name>
    </game>
    <game>
        <path>/roms/gb/Zelda.gb</path>
        <name>Zelda</name>
    </game>
</gameList>
`

func writeRepointList(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "roms", "gb")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "gamelist.xml")
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return file
}

func readRepointList(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// What EmulationStation stored on the entry survives, and a relative path
// stays relative.
func TestRepointRomEntries_KeepsTheEntry(t *testing.T) {
	file := writeRepointList(t, repointFixture)
	dir := filepath.Dir(file)

	changed, err := RepointRomEntries(file, []string{filepath.Join(dir, "Crystal.zip")}, filepath.Join(dir, "Crystal.gbc"))
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Errorf("changed = %d, want 1", changed)
	}

	got := readRepointList(t, file)
	for _, want := range []string{"<path>./Crystal.gbc</path>", "<favorite>true</favorite>", "<playcount>12</playcount>", "<path>/roms/gb/Tetris.zip</path>"} {
		if !strings.Contains(got, want) {
			t.Errorf("gamelist lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Crystal.zip") {
		t.Errorf("the old path is still listed:\n%s", got)
	}
}

// An entry already naming the new file and the repointed one would list the
// game twice; the first is kept.
func TestRepointRomEntries_RemovesTheDuplicate(t *testing.T) {
	file := writeRepointList(t, strings.Replace(repointFixture, "</gameList>", `    <game>
        <path>./Crystal.gbc</path>
        <name>Crystal again</name>
    </game>
</gameList>`, 1))
	dir := filepath.Dir(file)

	if _, err := RepointRomEntries(file, []string{filepath.Join(dir, "Crystal.zip")}, filepath.Join(dir, "Crystal.gbc")); err != nil {
		t.Fatal(err)
	}

	got := readRepointList(t, file)
	if n := strings.Count(got, "Crystal.gbc"); n != 1 {
		t.Errorf("Crystal.gbc listed %d times, want once:\n%s", n, got)
	}
	if !strings.Contains(got, "<playcount>12</playcount>") {
		t.Errorf("the first entry, with its play count, should be the one kept:\n%s", got)
	}
}

func TestRepointRomEntries_NothingToChange(t *testing.T) {
	file := writeRepointList(t, repointFixture)
	before := readRepointList(t, file)

	changed, err := RepointRomEntries(file, []string{"/roms/gb/Other.zip"}, "/roms/gb/Other.gb")
	if err != nil || changed != 0 {
		t.Errorf("changed, err = %d, %v; want 0, nil", changed, err)
	}
	if readRepointList(t, file) != before {
		t.Error("the file was rewritten with nothing to change")
	}

	if changed, err := RepointRomEntries(filepath.Join(t.TempDir(), "gamelist.xml"), []string{"/a"}, "/b"); err != nil || changed != 0 {
		t.Errorf("missing file: changed, err = %d, %v; want 0, nil", changed, err)
	}
}
