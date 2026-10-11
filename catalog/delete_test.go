package catalog

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"grout/cache"
	"grout/cfw"
	"grout/download"
	"grout/gamelist"
	"grout/library"
	"grout/romm"
	"grout/settings"
)

// onFirmware points the firmware's folders into a temp card and runs the test
// from a temp folder, where an ES restart request lands.
func onFirmware(t *testing.T, firmware cfw.CFW) {
	t.Helper()
	t.Setenv("BASE_PATH", t.TempDir())
	t.Setenv(cfw.EnvVar, string(firmware))
	t.Chdir(t.TempDir())
}

func touch(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(filepath.Base(p)), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertExist(t *testing.T, want bool, paths ...string) {
	t.Helper()
	for _, p := range paths {
		_, err := os.Lstat(p)
		if got := err == nil; got != want {
			t.Errorf("%s exists = %v, want %v", p, got, want)
		}
	}
}

// forgetSpy stands in for the cache's ForgetDownloadedFiles.
type forgetSpy struct {
	romID int
	rows  []cache.DownloadedFile
}

func (s *forgetSpy) forget(romID int, rows []cache.DownloadedFile) error {
	s.romID = romID
	s.rows = append(s.rows, rows...)
	return nil
}

func noneShared(string) bool { return false }

func romRow(id int, p string) cache.DownloadedFile {
	return cache.DownloadedFile{RomID: id, Kind: cache.DownloadedROM, Path: p}
}

func artRow(id int, p string) cache.DownloadedFile {
	return cache.DownloadedFile{RomID: id, Kind: cache.DownloadedArt, Path: p}
}

func gbaGame() romm.Rom {
	return romm.Rom{
		ID: 7, Name: "Game", PlatformFSSlug: "gba", PlatformDisplayName: "Game Boy Advance",
		FsName: "Game.gba", FsNameNoExt: "Game",
		Files: []romm.RomFile{{ID: 70, FileName: "Game.gba"}},
	}
}

func psxGame() romm.Rom {
	return romm.Rom{
		ID: 9, Name: "Game", PlatformFSSlug: "psx", PlatformDisplayName: "PlayStation",
		FsName: "Game", FsNameNoExt: "Game", FsPath: "psx", HasMultipleFiles: true,
		Files: []romm.RomFile{
			{ID: 90, FileName: "Game (Disc 1).chd", FilePath: "psx/Game"},
			{ID: 91, FileName: "Game (Disc 2).chd", FilePath: "psx/Game"},
		},
	}
}

func roots(config settings.Config, game romm.Rom) []string {
	return cfw.ManagedDirectories(config, game.PlatformFSSlug, game.PlatformDisplayName)
}

const gameAndOther = `<?xml version="1.0" encoding="UTF-8"?>
<gameList>
    <game><path>./Game.gba</path><name>Game</name></game>
    <game><path>./Other.gba</path><name>Other</name></game>
</gameList>`

func TestDeletion_RecordedSingleFile(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := gbaGame()
	romDir := RomDirectory(config, game)

	rom := filepath.Join(romDir, "Game.gba")
	cover := filepath.Join(romDir, "images", "Game-image.png")
	video := filepath.Join(romDir, "videos", "Game.mp4")
	other := filepath.Join(romDir, "Other.gba")
	listFile := filepath.Join(romDir, "gamelist.xml")
	touch(t, rom, cover, video, other)
	if err := os.WriteFile(listFile, []byte(gameAndOther), 0644); err != nil {
		t.Fatal(err)
	}

	rows := []cache.DownloadedFile{
		romRow(7, rom), artRow(7, cover), artRow(7, video),
		{RomID: 7, Kind: cache.DownloadedEntry, Path: listFile, Detail: rom},
	}
	plan := planDeletion(game, romDir, rows, roots(config, game), noneShared, false)
	if len(plan.Kept) != 0 || len(plan.Dirs) != 0 {
		t.Fatalf("plan kept %v, dirs %v; want none", plan.Kept, plan.Dirs)
	}

	spy := &forgetSpy{}
	report := executeDeletion(plan, spy.forget)

	assertExist(t, false, rom, cover, video)
	assertExist(t, true, other, listFile)
	if len(report.Deleted) != 3 || len(report.Failed) != 0 || report.EntriesRemoved != 1 {
		t.Errorf("report = %+v", report)
	}
	data, err := os.ReadFile(listFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "./Game.gba") || !strings.Contains(string(data), "./Other.gba") {
		t.Errorf("gamelist after deletion:\n%s", data)
	}
	if spy.romID != 7 || !slices.Equal(spy.rows, rows) {
		t.Errorf("forgot %d %v, want 7 %v", spy.romID, spy.rows, rows)
	}
	assertExist(t, true, "es_restart_request")
}

// RetroArch on ROCKNIX writes saves and states beside the disc it launched,
// inside the game's folder. They stay, and so does the folder.
func TestDeletion_RocknixMultiDiscKeepsSaves(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := psxGame()
	romDir := RomDirectory(config, game)
	folder := filepath.Join(romDir, "Game")

	discs := []string{
		filepath.Join(folder, "Game (Disc 1).chd"),
		filepath.Join(folder, "Game (Disc 2).chd"),
		filepath.Join(folder, "Game.m3u"),
	}
	playerFiles := []string{
		filepath.Join(folder, "Game.srm"),
		filepath.Join(folder, "Game.rtc"),
		filepath.Join(folder, "Game.state1"),
		filepath.Join(folder, "Game.state1.png"),
	}
	touch(t, discs...)
	touch(t, playerFiles...)

	var rows []cache.DownloadedFile
	for _, p := range discs {
		rows = append(rows, romRow(9, p))
	}
	plan := planDeletion(game, romDir, rows, roots(config, game), noneShared, false)
	if !slices.Equal(plan.Dirs, []string{folder}) {
		t.Fatalf("Dirs = %v, want [%s]", plan.Dirs, folder)
	}

	report := executeDeletion(plan, (&forgetSpy{}).forget)

	assertExist(t, false, discs...)
	assertExist(t, true, playerFiles...)
	if got := report.KeptDirs[folder]; got != 4 {
		t.Errorf("KeptDirs[%s] = %d, want 4 (all: %v)", folder, got, report.KeptDirs)
	}
	if len(report.Failed) != 0 {
		t.Errorf("Failed = %v", report.Failed)
	}
}

// A record must never cost a save, even a wrong one.
func TestDeletion_RecordListingASaveKeepsIt(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := gbaGame()
	romDir := RomDirectory(config, game)
	rom, save := filepath.Join(romDir, "Game.gba"), filepath.Join(romDir, "Game.srm")
	touch(t, rom, save)

	rows := []cache.DownloadedFile{romRow(7, rom), romRow(7, save)}
	plan := planDeletion(game, romDir, rows, roots(config, game), noneShared, false)
	if want := []KeptFile{{Path: save, Reason: KeptSave}}; !slices.Equal(plan.Kept, want) {
		t.Fatalf("Kept = %v, want %v", plan.Kept, want)
	}

	spy := &forgetSpy{}
	executeDeletion(plan, spy.forget)
	assertExist(t, false, rom)
	assertExist(t, true, save)
	// The wrong row goes with the game: the save is not the game's to delete.
	if !slices.Equal(spy.rows, rows) {
		t.Errorf("forgot %v, want %v", spy.rows, rows)
	}
}

func TestDeletion_PathsOutsideTheFirmwareFolders(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := gbaGame()
	romDir := RomDirectory(config, game)
	escape := romDir + "/../x"
	other := filepath.Join(romDir, "Other.gba")
	touch(t, filepath.Clean(escape), other)

	rows := []cache.DownloadedFile{
		romRow(7, "/etc/hostname"),
		romRow(7, escape),
		romRow(7, romDir),
		romRow(7, "relative/Game.gba"),
	}
	plan := planDeletion(game, romDir, rows, roots(config, game), noneShared, false)
	want := []KeptFile{
		{Path: "/etc/hostname", Reason: KeptOutside},
		{Path: escape, Reason: KeptOutside},
		{Path: romDir, Reason: KeptDirectory},
		{Path: "relative/Game.gba", Reason: KeptOutside},
	}
	if !slices.Equal(plan.Kept, want) || len(plan.Files) != 0 || len(plan.Dirs) != 0 {
		t.Fatalf("plan = files %v, dirs %v, kept %v; want only kept %v", plan.Files, plan.Dirs, plan.Kept, want)
	}

	report := executeDeletion(plan, (&forgetSpy{}).forget)
	if len(report.Deleted) != 0 {
		t.Errorf("Deleted = %v", report.Deleted)
	}
	assertExist(t, true, filepath.Clean(escape), romDir, other)
}

// Two RomM entries with one base name share their art names.
func TestDeletion_SharedArtIsKeptForBoth(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	zip, sevenZip := gbaGame(), gbaGame()
	zip.ID, sevenZip.ID = 1, 2
	romDir := RomDirectory(config, zip)
	cover := filepath.Join(romDir, "images", "Game-image.png")
	touch(t, cover)

	owners := map[string][]int{cover: {1, 2}}
	sharedFor := func(romID int) func(string) bool {
		return func(p string) bool {
			return slices.ContainsFunc(owners[p], func(id int) bool { return id != romID })
		}
	}

	for _, game := range []romm.Rom{zip, sevenZip} {
		plan := planDeletion(game, romDir, []cache.DownloadedFile{artRow(game.ID, cover)},
			roots(config, game), sharedFor(game.ID), false)
		if want := []KeptFile{{Path: cover, Reason: KeptShared}}; !slices.Equal(plan.Kept, want) {
			t.Errorf("rom %d: Kept = %v, want %v", game.ID, plan.Kept, want)
		}
	}
}

func TestDeletion_FilesAlreadyGone(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := gbaGame()
	romDir := RomDirectory(config, game)
	rows := []cache.DownloadedFile{
		romRow(7, filepath.Join(romDir, "Game.gba")),
		artRow(7, filepath.Join(romDir, "images", "Game-image.png")),
	}

	spy := &forgetSpy{}
	report := executeDeletion(planDeletion(game, romDir, rows, roots(config, game), noneShared, false), spy.forget)
	if len(report.Missing) != 2 || len(report.Deleted) != 0 || len(report.Failed) != 0 {
		t.Errorf("report = %+v", report)
	}
	if !slices.Equal(spy.rows, rows) {
		t.Errorf("forgot %v, want %v", spy.rows, rows)
	}
	assertExist(t, false, "es_restart_request")
}

// A game downloaded before the record: every version on the card goes, with
// the art the current settings name and the gamelist entries.
func TestDeletion_LegacySingleFileVersions(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{DownloadArt: true}
	game := gbaGame()
	game.HasNestedSingleFile = true
	game.URLCover = "https://example.invalid/cover.png"
	game.Files = []romm.RomFile{{ID: 1, FileName: "Game (USA).gba"}, {ID: 2, FileName: "Game (Europe).gba"}, {ID: 3, FileName: "Game (Japan).gba"}}
	romDir := RomDirectory(config, game)

	usa, europe := filepath.Join(romDir, "Game (USA).gba"), filepath.Join(romDir, "Game (Europe).gba")
	art := download.ArtLocations(config, game, gamePlatform(game))
	if len(art) != 1 {
		t.Fatalf("ArtLocations = %v, want the cover only", art)
	}
	touch(t, usa, europe, art[0])
	listFile := filepath.Join(romDir, "gamelist.xml")
	list := `<?xml version="1.0" encoding="UTF-8"?>
<gameList>
    <game><path>./Game (USA).gba</path><name>Game</name></game>
    <game><path>./Other.gba</path><name>Other</name></game>
</gameList>`
	if err := os.WriteFile(listFile, []byte(list), 0644); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanDeletion(config, game)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Legacy || plan.Records != nil {
		t.Errorf("Legacy = %v, Records = %v; want a legacy plan without records", plan.Legacy, plan.Records)
	}
	if want := []string{usa, europe, art[0]}; !slices.Equal(plan.Files, want) {
		t.Errorf("Files = %v, want %v", plan.Files, want)
	}

	report := ExecuteDeletion(plan)
	assertExist(t, false, usa, europe, art[0])
	if report.EntriesRemoved != 1 || len(report.Failed) != 0 || report.MetadataErr != nil {
		t.Errorf("report = %+v", report)
	}
}

// Without a record only RomM's files and the playlist go; anything else in the
// folder keeps it.
func TestDeletion_LegacyMultiFile(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := psxGame()
	game.Files = append(game.Files, romm.RomFile{ID: 92, FileName: "track.bin", FilePath: "psx/Game/audio"})
	romDir := RomDirectory(config, game)
	folder := filepath.Join(romDir, "Game")

	listed := []string{
		filepath.Join(folder, "Game (Disc 1).chd"),
		filepath.Join(folder, "Game (Disc 2).chd"),
		filepath.Join(folder, "audio", "track.bin"),
		filepath.Join(folder, "Game.m3u"),
	}
	extra := filepath.Join(folder, "notes.txt")
	touch(t, listed...)
	touch(t, extra)

	plan, err := PlanDeletion(config, game)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Legacy {
		t.Error("plan is not legacy")
	}
	if want := []string{filepath.Join(folder, "audio"), folder}; !slices.Equal(plan.Dirs, want) {
		t.Errorf("Dirs = %v, want %v", plan.Dirs, want)
	}

	report := ExecuteDeletion(plan)
	assertExist(t, false, listed...)
	assertExist(t, false, filepath.Join(folder, "audio"))
	assertExist(t, true, extra)
	if report.KeptDirs[folder] != 1 {
		t.Errorf("KeptDirs = %v, want %s: 1", report.KeptDirs, folder)
	}
}

// Grout cannot tell what came out of an unpacked archive without a record,
// and does not offer to delete it.
func TestHasLocalCopy_LegacyUnpackedArchive(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := gbaGame()
	game.Files = []romm.RomFile{{ID: 1, FileName: "Game.zip"}}
	touch(t, filepath.Join(RomDirectory(config, game), "Game.gba"))

	if HasLocalCopy(config, game) {
		t.Error("HasLocalCopy = true for an unpacked archive without a record")
	}
}

// A symlink is removed as a link; what it points at is not touched.
func TestDeletion_SymlinkIsNotFollowed(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := gbaGame()
	romDir := RomDirectory(config, game)

	outside := t.TempDir()
	target := filepath.Join(outside, "keep.txt")
	touch(t, target)
	link := filepath.Join(romDir, "Game.gba")
	if err := os.MkdirAll(romDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	plan := planDeletion(game, romDir, []cache.DownloadedFile{romRow(7, link)}, roots(config, game), noneShared, false)
	report := executeDeletion(plan, (&forgetSpy{}).forget)

	assertExist(t, false, link)
	assertExist(t, true, outside, target)
	if len(report.Deleted) != 1 {
		t.Errorf("report = %+v", report)
	}
}

// A path that reaches outside through a symlinked folder is refused.
func TestDeletion_SymlinkedFolderLeadingOutside(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := gbaGame()
	romDir := RomDirectory(config, game)

	outside := t.TempDir()
	target := filepath.Join(outside, "Game.gba")
	touch(t, target)
	if err := os.MkdirAll(romDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(romDir, "Game")); err != nil {
		t.Fatal(err)
	}

	through := filepath.Join(romDir, "Game", "Game.gba")
	plan := planDeletion(game, romDir, []cache.DownloadedFile{romRow(7, through)}, roots(config, game), noneShared, false)
	if want := []KeptFile{{Path: through, Reason: KeptOutside}}; !slices.Equal(plan.Kept, want) {
		t.Errorf("Kept = %v, want %v", plan.Kept, want)
	}
}

// A file that cannot be removed keeps its row, so the game still offers the
// delete and a retry can finish it.
func TestDeletion_FailureKeepsTheRow(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can remove files from a read-only folder")
	}
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}
	game := psxGame()
	romDir := RomDirectory(config, game)
	folder := filepath.Join(romDir, "Game")
	disc := filepath.Join(folder, "Game (Disc 1).chd")
	cover := filepath.Join(romDir, "images", "Game-image.png")
	touch(t, disc, cover)
	if err := os.Chmod(folder, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(folder, 0755) })

	rows := []cache.DownloadedFile{romRow(9, disc), artRow(9, cover)}
	spy := &forgetSpy{}
	report := executeDeletion(planDeletion(game, romDir, rows, roots(config, game), noneShared, false), spy.forget)

	if report.Failed[disc] == nil {
		t.Errorf("Failed = %v, want %s", report.Failed, disc)
	}
	if want := []cache.DownloadedFile{artRow(9, cover)}; !slices.Equal(spy.rows, want) {
		t.Errorf("forgot %v, want %v", spy.rows, want)
	}
	assertExist(t, true, disc)
}

// muOS moves a multi-file game into _<base> and lifts its playlist; the
// description text is a file of its own.
func TestDeletion_MuOS(t *testing.T) {
	onFirmware(t, cfw.MuOS)
	config := settings.Config{}
	game := psxGame()
	romDir := RomDirectory(config, game)
	folder := filepath.Join(romDir, "_Game")

	disc := filepath.Join(folder, "Game (Disc 1).chd")
	playlist := filepath.Join(romDir, "Game.m3u")
	text := cfw.MetadataLocationFor(gamelist.RomGameEntry{
		Game:     library.Game{Path: playlist, BaseName: "Game"},
		Platform: gamePlatform(game).ToPlatform(),
		RomID:    game.ID,
	}).File
	if text == "" {
		t.Fatal("no description text location on muOS")
	}
	touch(t, disc, playlist, text)

	rows := []cache.DownloadedFile{
		romRow(9, disc), romRow(9, playlist),
		{RomID: 9, Kind: cache.DownloadedMeta, Path: text},
	}
	plan := planDeletion(game, romDir, rows, roots(config, game), noneShared, false)
	report := executeDeletion(plan, (&forgetSpy{}).forget)

	assertExist(t, false, disc, playlist, text, folder)
	if report.EntriesRemoved != 1 || len(report.Failed) != 0 || report.MetadataErr != nil {
		t.Errorf("report = %+v", report)
	}
}

func TestHasLocalCopy(t *testing.T) {
	onFirmware(t, cfw.ROCKNIX)
	config := settings.Config{}

	single := gbaGame()
	versions := gbaGame()
	versions.ID = 8
	versions.HasNestedSingleFile = true
	versions.Files = []romm.RomFile{{ID: 1, FileName: "Game (USA).gba"}, {ID: 2, FileName: "Game (Europe).gba"}}
	multi := psxGame()
	noPlatform := gbaGame()
	noPlatform.PlatformFSSlug = ""

	gbaDir, psxDir := RomDirectory(config, single), RomDirectory(config, multi)
	recorded := filepath.Join(gbaDir, "Recorded.gba")

	tests := []struct {
		name  string
		game  romm.Rom
		files []string
		rows  []cache.DownloadedFile
		want  bool
	}{
		{"nothing on the card", single, nil, nil, false},
		{"the rom file", single, []string{filepath.Join(gbaDir, "Game.gba")}, nil, true},
		{"a recorded rom file", single, []string{recorded}, []cache.DownloadedFile{romRow(7, recorded)}, true},
		{"a recorded rom file that is gone", single, nil, []cache.DownloadedFile{romRow(7, recorded)}, false},
		{"recorded art only", single, []string{recorded}, []cache.DownloadedFile{artRow(7, recorded)}, false},
		{"the second version", versions, []string{filepath.Join(gbaDir, "Game (Europe).gba")}, nil, true},
		{"a disc inside the game folder", multi, []string{filepath.Join(psxDir, "Game", "Game (Disc 2).chd")}, nil, true},
		{"the lifted playlist", multi, []string{filepath.Join(psxDir, "Game.m3u")}, nil, true},
		{"a disc in the rom folder itself", multi, []string{filepath.Join(psxDir, "Game (Disc 1).chd")}, nil, false},
		{"no platform", noPlatform, []string{filepath.Join(gbaDir, "Game.gba")}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			touch(t, tt.files...)
			t.Cleanup(func() {
				for _, p := range tt.files {
					os.Remove(p)
				}
			})
			if got := hasLocalCopy(config, tt.game, tt.rows); got != tt.want {
				t.Errorf("hasLocalCopy = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMultiFileCandidates(t *testing.T) {
	game := psxGame()
	game.Files = append(game.Files,
		romm.RomFile{FileName: "elsewhere.bin", FilePath: "other/place"},
		romm.RomFile{FileName: "escape.bin", FilePath: "psx/Game/../../etc"},
	)
	got := multiFileCandidates(game, "/roms/psx")
	want := []string{
		"/roms/psx/Game.m3u",
		"/roms/psx/Game/Game (Disc 1).chd",
		"/roms/psx/Game/Game (Disc 2).chd",
		"/roms/psx/Game/Game.m3u",
		"/roms/psx/Game/elsewhere.bin",
		"/roms/psx/Game/escape.bin",
		"/roms/psx/_Game/Game (Disc 1).chd",
		"/roms/psx/_Game/Game (Disc 2).chd",
		"/roms/psx/_Game/Game.m3u",
		"/roms/psx/_Game/elsewhere.bin",
		"/roms/psx/_Game/escape.bin",
	}
	if !slices.Equal(got, want) {
		t.Errorf("multiFileCandidates =\n%v\nwant\n%v", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
