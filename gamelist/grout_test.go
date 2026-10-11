package gamelist

import (
	"os"
	"path/filepath"
	"testing"
)

// upstreamEntry is the launcher entry upstream Grout writes into the same
// list.
const upstreamEntry = `<?xml version="1.0"?>
<gameList>
    <game>
        <path>./Grout.sh</path>
        <name>Grout</name>
        <desc>Download games wirelessly from your RomM instance</desc>
        <image>./Grout/logo.png</image>
        <developer>The RomM Community</developer>
        <players>1</players>
        <genre>Rom Manager</genre>
    </game>
</gameList>
`

func loadGamelist(t *testing.T, path string) *GameList {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gl := New()
	if err := gl.Parse(data); err != nil {
		t.Fatal(err)
	}
	return gl
}

func entryText(t *testing.T, gl *GameList, name, element string) string {
	t.Helper()
	game := gl.GetGameElementByName(name)
	if game == nil {
		t.Fatalf("no entry named %q", name)
	}
	e := game.FindElement(element)
	if e == nil {
		t.Fatalf("entry %q has no <%s>", name, element)
	}
	return e.Text()
}

// Both builds keep their own entry: this one is added beside upstream's, which
// is left as it was, and running again adds nothing.
func TestAddGroutEntry_KeepsUpstreamEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gamelist.xml")
	if err := os.WriteFile(path, []byte(upstreamEntry), 0o644); err != nil {
		t.Fatal(err)
	}

	AddGroutEntry(path, "./Grout-Save-Sync.sh", "./Grout-Save-Sync/logo.png")
	AddGroutEntry(path, "./Grout-Save-Sync.sh", "./Grout-Save-Sync/logo.png")

	gl := loadGamelist(t, path)
	if got := gameElements(t, gl); len(got) != 2 {
		t.Fatalf("expected upstream's entry and ours, got %v", got)
	}

	if got := entryText(t, gl, "Grout", PathElement); got != "./Grout.sh" {
		t.Errorf("upstream path = %q, want ./Grout.sh", got)
	}
	if got := entryText(t, gl, "Grout", ImageElement); got != "./Grout/logo.png" {
		t.Errorf("upstream image = %q, want ./Grout/logo.png", got)
	}

	if got := entryText(t, gl, GroutEntryGameListName, PathElement); got != "./Grout-Save-Sync.sh" {
		t.Errorf("our path = %q, want ./Grout-Save-Sync.sh", got)
	}
	if got := entryText(t, gl, GroutEntryGameListName, ImageElement); got != "./Grout-Save-Sync/logo.png" {
		t.Errorf("our image = %q, want ./Grout-Save-Sync/logo.png", got)
	}
}

func TestGroutEntryName_DiffersFromUpstream(t *testing.T) {
	if GroutEntryGameListName == "Grout" {
		t.Error("the entry name is upstream's, so the two builds would share one entry")
	}
}
