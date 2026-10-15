package catalog

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/atomic"

	"grout/cache"
	"grout/cfw"
	"grout/romm"
	"grout/settings"
)

func gbGame() romm.Rom {
	return romm.Rom{
		ID: 7, Name: "Crystal", PlatformFSSlug: "gb", PlatformDisplayName: "Game Boy",
		FsName: "Crystal.zip", FsNameNoExt: "Crystal",
		Files: []romm.RomFile{{ID: 70, FileName: "Crystal.zip"}},
	}
}

func writeZip(t *testing.T, path string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for _, name := range names {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("rom")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStaleExtraction(t *testing.T) {
	dir := "/roms/gb"
	archive := filepath.Join(dir, "Crystal.zip")
	tagged := func(p, source string) cache.DownloadedFile {
		return cache.DownloadedFile{RomID: 7, Kind: cache.DownloadedROM, Path: p, Detail: source}
	}

	rows := []cache.DownloadedFile{
		tagged(filepath.Join(dir, "Crystal.gbc"), archive),                       // unpacked from it
		tagged(filepath.Join(dir, "Extras", "readme.txt"), archive),              // likewise, in a folder
		tagged(filepath.Join(dir, "Other.gbc"), filepath.Join(dir, "Other.zip")), // from another archive
		romRow(7, archive),                                // the archive itself
		romRow(7, filepath.Join(dir, "Legacy.gbc")),       // unpacked before sources were recorded
		romRow(7, filepath.Join(dir, "Crystal.7z")),       // an archive, not something unpacked
		romRow(7, filepath.Join("/roms/gbc", "Away.gbc")), // not beside the archive
		{RomID: 7, Kind: cache.DownloadedEntry, Path: filepath.Join(dir, "gamelist.xml"), Detail: filepath.Join(dir, "Crystal.gbc")},
	}

	got := staleExtraction(gbGame(), archive, rows)
	want := []cache.DownloadedFile{rows[0], rows[1], rows[4]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("single version:\n%+v\nwant\n%+v", got, want)
	}

	// With several versions, an untagged file may be another version's.
	versions := gbGame()
	versions.Files = []romm.RomFile{{ID: 1, FileName: "Crystal.zip"}, {ID: 2, FileName: "Crystal (Europe).zip"}}
	got = staleExtraction(versions, archive, rows)
	want = []cache.DownloadedFile{rows[0], rows[1]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("several versions:\n%+v\nwant\n%+v", got, want)
	}
}

// An unpacked archive no longer goes by RomM's file name; the record still
// finds it.
func TestDownloadState_UnpackedArchive(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := gbGame()
	dir := RomDirectory(config, game)
	unpacked := filepath.Join(dir, "Crystal.gbc")
	touch(t, unpacked)

	if got := downloadStateOf(config, game, nil); got != NotDownloaded {
		t.Errorf("without a record = %v, want NotDownloaded", got)
	}
	record := downloadRecord{7: {{RomID: 7, Kind: cache.DownloadedROM, Path: unpacked, Detail: filepath.Join(dir, "Crystal.zip")}}}
	if got := downloadStateOf(config, game, record); got != FullyDownloaded {
		t.Errorf("with a record = %v, want FullyDownloaded", got)
	}

	// A recorded file that is gone no longer counts.
	os.Remove(unpacked)
	if got := downloadStateOf(config, game, record); got != NotDownloaded {
		t.Errorf("record of a deleted file = %v, want NotDownloaded", got)
	}
}

// A version is found by the archive it was unpacked from, not by any file of
// the game.
func TestDownloadState_UnpackedVersion(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := gbGame()
	game.HasNestedSingleFile = true
	game.Files = []romm.RomFile{{ID: 1, FileName: "Crystal (USA).zip"}, {ID: 2, FileName: "Crystal (Europe).zip"}}
	dir := RomDirectory(config, game)
	usa := filepath.Join(dir, "Crystal (USA).gbc")
	touch(t, usa)

	record := downloadRecord{7: {{RomID: 7, Kind: cache.DownloadedROM, Path: usa, Detail: filepath.Join(dir, "Crystal (USA).zip")}}}
	if got := downloadStateOf(config, game, record); got != PartlyDownloaded {
		t.Errorf("state = %v, want PartlyDownloaded", got)
	}
	if !isFileDownloaded(config, game, "Crystal (USA).zip", record) || isFileDownloaded(config, game, "Crystal (Europe).zip", record) {
		t.Error("only the USA version was unpacked")
	}
}

func TestLocalArchive(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := gbGame()
	archive := filepath.Join(RomDirectory(config, game), "Crystal.zip")

	if _, ok := LocalArchive(config, game); ok {
		t.Error("no archive is on the card yet")
	}

	touch(t, archive)
	if got, ok := LocalArchive(config, game); !ok || got != archive {
		t.Errorf("LocalArchive = %q, %v; want %q", got, ok, archive)
	}

	arcade := game
	arcade.PlatformFSSlug = "arcade"
	touch(t, filepath.Join(RomDirectory(config, arcade), "Crystal.zip"))
	if _, ok := LocalArchive(config, arcade); ok {
		t.Error("an arcade set must never be offered for unpacking")
	}

	multi := game
	multi.HasMultipleFiles = true
	if _, ok := LocalArchive(config, multi); ok {
		t.Error("a multi-file game is unpacked on its own path")
	}
}

// Unpacking from Game Options keeps the gamelist entry, and what the frontend
// stored on it, pointing at the file that came out.
func TestExtractLocalArchive_RepointsTheGamelist(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := gbGame()
	dir := RomDirectory(config, game)
	archive := filepath.Join(dir, "Crystal.zip")
	writeZip(t, archive, "Crystal.gbc")

	listFile := filepath.Join(dir, "gamelist.xml")
	list := `<?xml version="1.0" encoding="UTF-8"?>
<gameList>
    <game><path>./Crystal.zip</path><name>Crystal</name><favorite>true</favorite></game>
</gameList>`
	if err := os.WriteFile(listFile, []byte(list), 0644); err != nil {
		t.Fatal(err)
	}

	unpacked, err := ExtractLocalArchive(context.Background(), game, archive, &atomic.Float64{})
	if err != nil {
		t.Fatalf("ExtractLocalArchive: %v", err)
	}
	if want := filepath.Join(dir, "Crystal.gbc"); unpacked.Path != want {
		t.Errorf("path = %q, want %q", unpacked.Path, want)
	}
	assertExist(t, true, unpacked.Path)
	assertExist(t, false, archive)

	data, err := os.ReadFile(listFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<path>./Crystal.gbc</path>", "<favorite>true</favorite>"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("gamelist lacks %s:\n%s", want, data)
		}
	}
}
