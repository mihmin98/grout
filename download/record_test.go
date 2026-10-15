package download

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"grout/cfw"
	"grout/library"
	"grout/romm"
	"grout/settings"
)

func TestRecords(t *testing.T) {
	dir := t.TempDir()
	cover := filepath.Join(dir, "Game.png")
	otherCover := filepath.Join(dir, "Other.png")
	for _, path := range []string{cover, otherCover} {
		if err := os.WriteFile(path, []byte("png"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	written := map[int]Unpacked{
		9: {Written: []string{"/roms/gba/Second.gba"}, Source: "/roms/gba/Second.zip"},
		7: {Written: []string{"/roms/psx/Game/Game (Disc 1).chd", "/roms/psx/Game/Game.m3u"}},
	}
	art := []Item{
		{RomID: 7, Location: cover},
		{RomID: 7, Location: filepath.Join(dir, "Game.mp4")}, // the fetch failed
		{RomID: 8, Location: otherCover},                     // the game did not land
	}
	entries := []cfw.MetadataLocation{
		{RomID: 7, File: "/roms/psx/gamelist.xml", Entry: "/roms/psx/Game/Game.m3u"},
		{RomID: 8, File: "/roms/psx/gamelist.xml", Entry: "/roms/psx/Other.chd"},
		{RomID: 9, File: "/info/catalogue/GBA/text/Second.txt"},
		{RomID: 9}, // a firmware without metadata
	}

	got := Records(written, art, entries)

	want := []Record{
		{RomID: 7, Kind: RecordROM, Path: "/roms/psx/Game/Game (Disc 1).chd"},
		{RomID: 7, Kind: RecordROM, Path: "/roms/psx/Game/Game.m3u"},
		{RomID: 9, Kind: RecordROM, Path: "/roms/gba/Second.gba", Detail: "/roms/gba/Second.zip"},
		{RomID: 7, Kind: RecordArt, Path: cover},
		{RomID: 7, Kind: RecordEntry, Path: "/roms/psx/gamelist.xml", Detail: "/roms/psx/Game/Game.m3u"},
		{RomID: 9, Kind: RecordMeta, Path: "/info/catalogue/GBA/text/Second.txt"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Records =\n%+v\nwant\n%+v", got, want)
	}
}

func TestRecords_NothingLanded(t *testing.T) {
	if got := Records(nil, []Item{{RomID: 1, Location: "/x.png"}}, []cfw.MetadataLocation{{RomID: 1, File: "/g.xml", Entry: "/x"}}); len(got) != 0 {
		t.Errorf("Records = %+v, want nothing", got)
	}
}

// The plan carries each game's ID onto its rom, art and metadata entry, which
// is what the record is keyed on.
func TestBuildPlan_CarriesTheRomID(t *testing.T) {
	games := []romm.Rom{{
		ID: 42, Name: "Test Game", FsName: "test.nds", FsNameNoExt: "test",
		PathCoverLarge: "/covers/test.png",
		Files:          []romm.RomFile{{ID: 100, FileName: "test.nds"}},
	}}
	config := settings.Config{DownloadArt: true, ArtKind: library.ArtKindDefault}

	plan, _ := BuildPlan(config, testHost(), testPlatform(), games, 0)

	if len(plan.Roms) != 1 || plan.Roms[0].RomID != 42 {
		t.Errorf("rom items = %+v, want one with ID 42", plan.Roms)
	}
	if len(plan.Art) == 0 {
		t.Fatal("expected art to be planned")
	}
	for _, item := range plan.Art {
		if item.RomID != 42 {
			t.Errorf("art item %s has ID %d, want 42", item.Location, item.RomID)
		}
	}
	if len(plan.Entries) != 1 || plan.Entries[0].RomID != 42 {
		t.Errorf("entries = %+v, want one with ID 42", plan.Entries)
	}

	// ROCKNIX (pinned in TestMain) keeps an EmulationStation gamelist.
	locations := plan.MetadataLocations()
	if len(locations) != 1 || locations[0].RomID != 42 || locations[0].Entry != plan.Roms[0].Location {
		t.Errorf("metadata locations = %+v, want the entry for ID 42 at the rom's path", locations)
	}
}
