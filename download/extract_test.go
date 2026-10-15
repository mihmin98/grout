package download

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.uber.org/atomic"

	"grout/archive"
	"grout/cfw"
	"grout/gamelist"
	"grout/library"
	"grout/romm"
	"grout/settings"
)

// zipOf writes an archive holding the named files, each with a byte in it so
// the unpacker has something to copy.
func zipOf(t *testing.T, path string, names ...string) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	w := zip.NewWriter(file)
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

func TestIsArchive(t *testing.T) {
	tests := map[string]bool{
		"Game.zip": true,
		"Game.7z":  true,
		"Game.ZIP": true,
		"Game.chd": false,
		"Game":     false,
		"":         false,
	}

	for name, want := range tests {
		if got := IsArchive(name); got != want {
			t.Errorf("IsArchive(%q) = %v, want %v", name, got, want)
		}
	}
}

// unpackers runs a test against the built-in readers and, where the machine
// has one, the real 7za.
func unpackers(t *testing.T, test func(t *testing.T, u unpacker)) {
	t.Run("builtin", func(t *testing.T) { test(t, builtinUnpacker{}) })
	t.Run("7za", func(t *testing.T) {
		bin, ok := archive.SevenZipPath()
		if !ok {
			t.Skip("7za is not installed")
		}
		test(t, sevenZipCLI{bin: bin})
	})
}

func extract(t *testing.T, u unpacker, archivePath string, mayReplace func(string) bool) (Unpacked, error) {
	t.Helper()
	return extractArchive(context.Background(), u, archivePath, mayReplace, &atomic.Float64{})
}

func TestExtractArchive(t *testing.T) {
	unpackers(t, func(t *testing.T, u unpacker) {
		dir := t.TempDir()
		archivePath := filepath.Join(dir, "Game.zip")
		zipOf(t, archivePath, "Game.sfc")

		unpacked, err := extract(t, u, archivePath, nil)
		if err != nil {
			t.Fatalf("ExtractArchive: %v", err)
		}

		if want := filepath.Join(dir, "Game.sfc"); unpacked.Path != want {
			t.Errorf("path = %q, want %q", unpacked.Path, want)
		}
		if unpacked.Source != archivePath {
			t.Errorf("source = %q, want the archive", unpacked.Source)
		}
		if _, err := os.Stat(filepath.Join(dir, "Game.sfc")); err != nil {
			t.Errorf("the rom was not unpacked: %v", err)
		}
		// Leaving it behind would show the game twice and waste space on the card.
		if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
			t.Error("the archive must be removed once it is unpacked")
		}
		assertNoStaging(t, dir)
	})
}

// The staging folder is hidden, but a leftover one still wastes the card.
func assertNoStaging(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), stagingPrefix) {
			t.Errorf("staging folder %s was left behind", e.Name())
		}
	}
}

// Several discs need the playlist that ties them together, not whichever file
// happens to come first in the archive.
func TestExtractArchive_PrefersThePlaylist(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "FF7.zip")
	zipOf(t, archivePath, "FF7 (Disc 1).chd", "FF7 (Disc 2).chd", "FF7.m3u")

	unpacked, err := extract(t, builtinUnpacker{}, archivePath, nil)
	if err != nil {
		t.Fatalf("ExtractArchive: %v", err)
	}

	if filepath.Base(unpacked.Path) != "FF7.m3u" {
		t.Errorf("path = %q, want the playlist", unpacked.Path)
	}
}

// The tracks are not launchable on their own; the sheet naming them is.
func TestPlayablePath_PrefersTheDiscSheet(t *testing.T) {
	if got := playablePath([]string{"Game (Track 1).bin", "Game (Track 2).bin", "Game.cue"}); got != "Game.cue" {
		t.Errorf("playablePath = %q, want the cue sheet", got)
	}
}

// A broken archive is not worth losing the download to: the file stays put so
// a frontend that can read archives still has something to launch.
func TestExtractArchive_KeepsTheArchiveOnFailure(t *testing.T) {
	unpackers(t, func(t *testing.T, u unpacker) {
		dir := t.TempDir()
		archivePath := filepath.Join(dir, "Broken.zip")
		if err := os.WriteFile(archivePath, []byte("not a zip"), 0644); err != nil {
			t.Fatal(err)
		}

		if _, err := extract(t, u, archivePath, nil); err == nil {
			t.Fatal("expected an error unpacking a file that is not an archive")
		}
		if _, err := os.Stat(archivePath); err != nil {
			t.Error("the archive must survive a failed unpack")
		}
		assertNoStaging(t, dir)
	})
}

// A file of another game, or a save, is never overwritten; nothing is moved
// in at all, so the folder is as it was.
func TestExtractArchive_RefusesToOverwrite(t *testing.T) {
	unpackers(t, func(t *testing.T, u unpacker) {
		dir := t.TempDir()
		archivePath := filepath.Join(dir, "Game.zip")
		zipOf(t, archivePath, "Game.gbc", "Game.srm")
		existing := filepath.Join(dir, "Game.srm")
		if err := os.WriteFile(existing, []byte("my save"), 0644); err != nil {
			t.Fatal(err)
		}

		_, err := extract(t, u, archivePath, func(string) bool { return false })
		var conflict *ConflictError
		if !errors.As(err, &conflict) || conflict.Path != existing {
			t.Fatalf("err = %v, want a conflict on %s", err, existing)
		}

		if got, _ := os.ReadFile(existing); string(got) != "my save" {
			t.Errorf("the save was overwritten: %q", got)
		}
		if _, err := os.Stat(filepath.Join(dir, "Game.gbc")); !os.IsNotExist(err) {
			t.Error("nothing may be moved in when one file clashes")
		}
		if _, err := os.Stat(archivePath); err != nil {
			t.Error("the archive must survive a clash")
		}
		assertNoStaging(t, dir)
	})
}

// The game's own earlier copy may be replaced.
func TestExtractArchive_ReplacesWhatItMay(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "Game.zip")
	zipOf(t, archivePath, "Game.gbc")
	old := filepath.Join(dir, "Game.gbc")
	if err := os.WriteFile(old, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := extract(t, builtinUnpacker{}, archivePath, func(p string) bool { return p == old }); err != nil {
		t.Fatalf("ExtractArchive: %v", err)
	}
	if got, _ := os.ReadFile(old); string(got) != "rom" {
		t.Errorf("Game.gbc = %q, want the unpacked one", got)
	}
}

// An entry naming a path outside the folder would write anywhere on the card.
func TestExtractArchive_RefusesEscapingPaths(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "Evil.zip")
	zipOf(t, archivePath, "../outside.gbc")

	if _, err := extract(t, builtinUnpacker{}, archivePath, nil); err == nil {
		t.Fatal("expected an archive escaping its folder to be refused")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "outside.gbc")); !os.IsNotExist(err) {
		t.Error("a file was written outside the rom folder")
	}
}

func TestCheckResources(t *testing.T) {
	dir := t.TempDir()

	var noSpace *NoSpaceError
	if err := checkResources([]archive.Entry{{Name: "Huge.iso", Size: 1 << 62}}, dir); !errors.As(err, &noSpace) {
		t.Errorf("err = %v, want NoSpaceError", err)
	}

	if _, ok := archive.AvailableMemory(); ok {
		var noMemory *NoMemoryError
		err := checkResources([]archive.Entry{{Name: "Game.iso", Size: 1, Method: "LZMA2:4000g"}}, dir)
		if !errors.As(err, &noMemory) {
			t.Errorf("err = %v, want NoMemoryError", err)
		}
	}

	if err := checkResources([]archive.Entry{{Name: "Game.gbc", Size: 1, Method: "LZMA2:16m"}}, dir); err != nil {
		t.Errorf("a small archive was refused: %v", err)
	}
}

func TestKeepsArchive(t *testing.T) {
	tests := []struct {
		slug, dir string
		want      bool
	}{
		{"arcade", "/roms/arcade", true},
		{"neogeoaes", "/roms/neogeo", true},
		{"dos", "/roms/pc", true},
		{"cps2", "", true},
		{"gba", "/roms/gba", false},
		{"gb", "/roms/MAME", true}, // a folder mapped by hand still counts
		{"psx", "", false},
	}
	for _, tt := range tests {
		if got := KeepsArchive(tt.slug, tt.dir); got != tt.want {
			t.Errorf("KeepsArchive(%q, %q) = %v, want %v", tt.slug, tt.dir, got, tt.want)
		}
	}
}

func TestExtractMultiFile(t *testing.T) {
	base := t.TempDir()
	t.Setenv("BASE_PATH", base)
	t.Setenv(cfw.EnvVar, string(cfw.MuOS))

	romDirectory := filepath.Join(base, "ROMS", "psx")
	if err := os.MkdirAll(romDirectory, 0755); err != nil {
		t.Fatal(err)
	}

	game := romm.Rom{ID: 42, Name: "FF7", FsName: "FF7.zip", FsNameNoExt: "FF7", HasMultipleFiles: true}

	// The download writes the archive to the temp path the plan chose.
	archivePath := MultiFileArchivePath(game)
	if err := os.MkdirAll(filepath.Dir(archivePath), 0755); err != nil {
		t.Fatal(err)
	}
	zipOf(t, archivePath, "FF7 (Disc 1).chd", "FF7.m3u")

	unpacked, err := ExtractMultiFile(game, romDirectory, &atomic.Float64{})
	if err != nil {
		t.Fatalf("ExtractMultiFile: %v", err)
	}
	path := unpacked.Path

	if filepath.Ext(path) != ".m3u" {
		t.Errorf("path = %q, want the playlist", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the path handed to the frontend does not exist: %v", err)
	}
	// The archive was only ever a staging file.
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Error("the downloaded archive must be removed once it is unpacked")
	}
}

// The archive is staged outside the rom directory so a failure leaves nothing
// there for the frontend to trip over.
func TestMultiFileArchivePath_IsNotInTheRomDirectory(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.MuOS))

	game := romm.Rom{ID: 42, Name: "FF7"}
	path := MultiFileArchivePath(game)

	if filepath.Ext(path) != ".zip" {
		t.Errorf("path = %q, want a .zip", path)
	}
	if got := MultiFileArchivePath(game); got != path {
		t.Error("the path must be the same every time, since planning and unpacking both derive it")
	}
}

func TestPlan_RomLocationAndSetGamePath(t *testing.T) {
	plan := Plan{
		Roms: []Item{
			{GameName: "Mario", Location: "/roms/snes/Mario.zip"},
			{GameName: "Zelda", Location: "/roms/snes/Zelda (USA).sfc"},
		},
	}

	if got := plan.RomLocation("Zelda"); got != "/roms/snes/Zelda (USA).sfc" {
		t.Errorf("RomLocation = %q, want the version that was actually downloaded", got)
	}
	if got := plan.RomLocation("Sonic"); got != "" {
		t.Errorf("RomLocation = %q, want empty for a game not in the plan", got)
	}
}

// Unpacking moves a rom, and the metadata entry is written afterwards, so it
// has to be told where the file ended up.
func TestPlan_SetGamePath(t *testing.T) {
	plan := Plan{Entries: []gamelist.RomGameEntry{
		{Game: library.Game{FileName: "Mario.zip", Path: "/roms/snes/Mario.zip"}},
		{Game: library.Game{FileName: "Zelda.zip", Path: "/roms/snes/Zelda.zip"}},
	}}

	plan.SetGamePath("Zelda.zip", "/roms/snes/Zelda.sfc")

	if got := plan.Entries[1].Game.Path; got != "/roms/snes/Zelda.sfc" {
		t.Errorf("path = %q, want the unpacked file", got)
	}
	if got := plan.Entries[0].Game.Path; got != "/roms/snes/Mario.zip" {
		t.Errorf("Mario moved to %q, but only Zelda was unpacked", got)
	}
}

// A game with no fs_name has its metadata entry named after its first file.
// Unpacking has to find the entry by that same name, or the entry keeps
// pointing at the archive that unpacking just deleted.
func TestPlan_SetGamePathWithoutFsName(t *testing.T) {
	game := romm.Rom{
		ID: 7, Name: "Zelda", FsNameNoExt: "Zelda",
		Files: []romm.RomFile{{ID: 1, FileName: "Zelda.zip"}},
	}
	plan, _ := BuildPlan(settings.Config{}, testHost(), testPlatform(), []romm.Rom{game}, 0)
	if len(plan.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(plan.Entries))
	}

	plan.SetGamePath(game.EntryFileName(), "/roms/nds/Zelda.nds")

	if got := plan.Entries[0].Game.Path; got != "/roms/nds/Zelda.nds" {
		t.Errorf("entry path = %q, want the unpacked file", got)
	}
}

// The record of a download lists what came out of the archive and nothing
// else that happens to sit in the rom directory.
func TestExtractArchive_ReportsWhatItWrote(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Game.srm"), []byte("save"), 0644); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "Game.zip")
	zipOf(t, archivePath, "Game.cue", "Game (Track 1).bin")

	unpacked, err := extract(t, builtinUnpacker{}, archivePath, nil)
	if err != nil {
		t.Fatalf("ExtractArchive: %v", err)
	}

	want := []string{filepath.Join(dir, "Game.cue"), filepath.Join(dir, "Game (Track 1).bin")}
	if !slices.Equal(unpacked.Written, want) {
		t.Errorf("written = %q, want %q", unpacked.Written, want)
	}
}

// A re-download unpacks into a folder that may already hold the game's save
// and save states (ROCKNIX writes them beside the launched file). They must not
// be recorded as the download's, or deleting the game would take them.
func TestExtractMultiFile_ReDownloadLeavesSavesOut(t *testing.T) {
	tests := []struct {
		cfw  cfw.CFW
		want func(romDir string) []string
	}{
		{cfw.ROCKNIX, func(romDir string) []string {
			return []string{
				filepath.Join(romDir, "FF7", "FF7 (Disc 1).chd"),
				filepath.Join(romDir, "FF7", "FF7 (Disc 2).chd"),
				filepath.Join(romDir, "FF7", "FF7.m3u"),
			}
		}},
		// muOS renames the folder and lifts the playlist into the rom directory.
		{cfw.MuOS, func(romDir string) []string {
			return []string{
				filepath.Join(romDir, "_FF7", "FF7 (Disc 1).chd"),
				filepath.Join(romDir, "_FF7", "FF7 (Disc 2).chd"),
				filepath.Join(romDir, "FF7.m3u"),
			}
		}},
	}

	for i, tt := range tests {
		t.Run(string(tt.cfw), func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("BASE_PATH", base)
			t.Setenv(cfw.EnvVar, string(tt.cfw))

			romDirectory := filepath.Join(base, "ROMS", "psx")
			gameDir := filepath.Join(romDirectory, "FF7")
			if err := os.MkdirAll(gameDir, 0755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"FF7.srm", "FF7.state1"} {
				if err := os.WriteFile(filepath.Join(gameDir, name), []byte("save"), 0644); err != nil {
					t.Fatal(err)
				}
			}

			game := romm.Rom{ID: 900 + i, Name: "FF7", FsName: "FF7.zip", FsNameNoExt: "FF7", HasMultipleFiles: true}
			archivePath := MultiFileArchivePath(game)
			if err := os.MkdirAll(filepath.Dir(archivePath), 0755); err != nil {
				t.Fatal(err)
			}
			zipOf(t, archivePath, "FF7 (Disc 1).chd", "FF7 (Disc 2).chd", "FF7.m3u")

			unpacked, err := ExtractMultiFile(game, romDirectory, &atomic.Float64{})
			if err != nil {
				t.Fatalf("ExtractMultiFile: %v", err)
			}

			if want := tt.want(romDirectory); !slices.Equal(unpacked.Written, want) {
				t.Errorf("written = %q, want %q", unpacked.Written, want)
			}
			for _, path := range unpacked.Written {
				if strings.Contains(path, ".srm") || strings.Contains(path, ".state") {
					t.Errorf("a save was recorded as downloaded: %s", path)
				}
			}
		})
	}
}

func TestMultiFileWritten(t *testing.T) {
	write := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name            string
		onDisk          []string // relative to romDir
		entries         []string
		playlistExisted bool
		want            []string // relative to romDir
	}{
		{
			name:    "in place",
			onDisk:  []string{"G/G (Disc 1).chd", "G/G.m3u"},
			entries: []string{"G (Disc 1).chd", "G.m3u"},
			want:    []string{"G/G (Disc 1).chd", "G/G.m3u"},
		},
		{
			name:    "renamed by muOS",
			onDisk:  []string{"_G/G (Disc 1).chd", "_G/sub/G (Disc 2).chd"},
			entries: []string{"G (Disc 1).chd", "sub/G (Disc 2).chd"},
			want:    []string{"_G/G (Disc 1).chd", "_G/sub/G (Disc 2).chd"},
		},
		{
			name:    "lifted playlist is new",
			onDisk:  []string{"_G/G (Disc 1).chd", "G.m3u"},
			entries: []string{"G (Disc 1).chd", "G.m3u"},
			want:    []string{"_G/G (Disc 1).chd", "G.m3u"},
		},
		{
			name:            "playlist was already there",
			onDisk:          []string{"_G/G (Disc 1).chd", "G.m3u"},
			entries:         []string{"G (Disc 1).chd", "G.m3u"},
			playlistExisted: true,
			want:            []string{"_G/G (Disc 1).chd"},
		},
		{
			name:    "missing entry left out",
			onDisk:  []string{"G/G (Disc 1).chd"},
			entries: []string{"G (Disc 1).chd", "G (Disc 2).chd"},
			want:    []string{"G/G (Disc 1).chd"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			romDir := t.TempDir()
			for _, rel := range tt.onDisk {
				write(t, filepath.Join(romDir, rel))
			}

			got := multiFileWritten(tt.entries, filepath.Join(romDir, "G"), romDir, "G", tt.playlistExisted)

			want := make([]string, 0, len(tt.want))
			for _, rel := range tt.want {
				want = append(want, filepath.Join(romDir, rel))
			}
			if !slices.Equal(got, want) {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}
