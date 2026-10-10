package saves

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"grout/cfw"
	"grout/hashing"
	"grout/romm"
	"grout/settings"
)

// testZip builds a zip holding the given name -> content entries.
func testZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range entries {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zipHash is the server's hash of a zip payload.
func zipHash(t *testing.T, data []byte) string {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	h, err := hashing.ComputeZipReaderHash(r)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// downloadItem is a download for a ROM on ROCKNIX, where saves sit beside the
// ROMs in dir. The server's save is named <serverName>.
func downloadItem(dir, romFileName, serverName, serverExt string) SyncItem {
	return SyncItem{
		LocalSave: LocalSave{
			RomID:       1,
			FSSlug:      "gbc",
			RomFileName: romFileName,
			EmulatorDir: dir,
		},
		RemoteSave: &romm.Save{ID: 9, FileName: serverName, FileExtension: serverExt},
		Action:     ActionDownload,
	}
}

// withLocalClockSave makes item's local save dir/Crystal.srm with a Crystal.rtc
// companion, both written to disk.
func withLocalClockSave(t *testing.T, item SyncItem, dir, sram, clock string) SyncItem {
	t.Helper()
	srm := filepath.Join(dir, "Crystal.srm")
	rtc := filepath.Join(dir, "Crystal.rtc")
	writeTestFile(t, srm, sram)
	writeTestFile(t, rtc, clock)
	item.LocalSave.FileName = "Crystal.srm"
	item.LocalSave.FilePath = srm
	item.LocalSave.Companions = []string{rtc}
	return item
}

func TestIsZipPayload(t *testing.T) {
	dirOnly := func() []byte {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		if _, err := w.Create("folder/"); err != nil {
			t.Fatal(err)
		}
		w.Close()
		return buf.Bytes()
	}()

	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"real zip", testZip(t, map[string]string{"Crystal.srm": "sram"}), true},
		{"raw save bytes", []byte("\x00\x01raw sram"), false},
		{"zip signature then garbage", append([]byte("PK\x03\x04"), bytes.Repeat([]byte{0xff}, 64)...), false},
		{"zip with only a directory entry", dirOnly, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isZipPayload(tt.data); got != tt.want {
				t.Errorf("isZipPayload = %v, want %v", got, tt.want)
			}
		})
	}
}

// W14: a save stored on the server as Game.zip for a ROM Game.zip, with no local
// save, would be written over the ROM (saves sit beside ROMs on ROCKNIX).
func TestPlanFileDownload_RefusesNonSaveName(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	rom := filepath.Join(dir, "Game.zip")
	writeTestFile(t, rom, "rom bytes")

	item := downloadItem(dir, "Game.zip", "Game.zip", "zip")
	p, err := planFileDownload(item, []byte("raw save bytes"), &settings.Config{}, true)
	if !errors.Is(err, errNonSaveName) {
		t.Fatalf("err = %v, want errNonSaveName (target %q)", err, p.primary)
	}
	if p.primary != rom {
		t.Errorf("primary = %q, want %q reported for the log", p.primary, rom)
	}
	if got := readTestFile(t, rom); got != "rom bytes" {
		t.Errorf("ROM changed: %q", got)
	}
}

func TestPlanFileDownload_LocalPathWins(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	local := filepath.Join(dir, "Crystal.srm")
	writeTestFile(t, local, "local sram")

	// The server's name and extension don't matter once a local save exists.
	item := downloadItem(dir, "Crystal.7z", "Crystal [2026-01-01_00-00-00].sav", "sav")
	item.LocalSave.FileName = "Crystal.srm"
	item.LocalSave.FilePath = local

	p, err := planFileDownload(item, []byte("raw"), &settings.Config{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.primary != local {
		t.Errorf("primary = %q, want %q", p.primary, local)
	}
}

func TestPlanFileDownload_DerivedName(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "Crystal.7z"), "rom bytes")

	item := downloadItem(dir, "Crystal.7z", "Crystal [2026-01-01_00-00-00].srm", "srm")
	p, err := planFileDownload(item, []byte("raw"), &settings.Config{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "Crystal.srm"); p.primary != want {
		t.Errorf("primary = %q, want %q", p.primary, want)
	}
}

func TestPlanFileDownload_NoSaveFolder(t *testing.T) {
	t.Setenv(cfw.EnvVar, "")
	item := downloadItem("", "Crystal.7z", "Crystal.srm", "srm")
	if _, err := planFileDownload(item, []byte("raw"), nil, true); !errors.Is(err, errNoSavePath) {
		t.Fatalf("err = %v, want errNoSavePath", err)
	}
}

// W1: Argosy's zip for a ROM with no local save becomes Crystal.srm + Crystal.rtc,
// never Crystal.zip, and the record gets the server's hash of the zip.
func TestPlanFileDownload_ArgosyZipNoLocalSave(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "Crystal.7z"), "rom bytes")

	payload := testZip(t, map[string]string{"Crystal.srm": "S1", "Crystal.rtc": "R1"})
	item := downloadItem(dir, "Crystal.7z", "Crystal [2026-01-01_00-00-00].zip", "zip")
	p, err := planFileDownload(item, payload, &settings.Config{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "Crystal.srm"); p.primary != want {
		t.Errorf("primary = %q, want %q", p.primary, want)
	}
	if want := filepath.Join(dir, "Crystal.rtc"); p.clock != want {
		t.Errorf("clock = %q, want %q", p.clock, want)
	}
	if string(p.ram) != "S1" || string(p.clockData) != "R1" {
		t.Errorf("ram %q, clock %q", p.ram, p.clockData)
	}
	if p.content != zipHash(t, payload) {
		t.Errorf("content = %s, want the zip's hash", p.content)
	}
}

// W2: Argosy's zip onto an existing .srm + .rtc replaces both.
func TestPlanFileDownload_ArgosyZipOntoLocalSave(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	item := withLocalClockSave(t, downloadItem(dir, "Crystal.7z", "Crystal.zip", "zip"), dir, "S0", "R0")

	payload := testZip(t, map[string]string{"Crystal.srm": "S1", "Crystal.rtc": "R1"})
	p, err := planFileDownload(item, payload, &settings.Config{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.primary != item.LocalSave.FilePath || p.clock != item.LocalSave.Companions[0] {
		t.Errorf("primary %q, clock %q, want the local .srm and .rtc", p.primary, p.clock)
	}
	if p.keptLocalClock {
		t.Error("same-size clock should be replaced, not kept")
	}
}

// The clock is written into the local save's own .rtc, whatever its case.
func TestPlanFileDownload_ClockIntoLocalCompanion(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	item := withLocalClockSave(t, downloadItem(dir, "Crystal.7z", "Crystal.zip", "zip"), dir, "S0", "R0")
	upper := filepath.Join(dir, "Crystal.RTC")
	if err := os.Rename(item.LocalSave.Companions[0], upper); err != nil {
		t.Fatal(err)
	}
	item.LocalSave.Companions = []string{upper}

	payload := testZip(t, map[string]string{"Crystal.srm": "S1", "Crystal.rtc": "R1"})
	p, err := planFileDownload(item, payload, &settings.Config{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.clock != upper {
		t.Errorf("clock = %q, want %q", p.clock, upper)
	}
}

// A local .rtc of a different size is another clock format: keep it.
func TestPlanFileDownload_ClockSizeGuard(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	local32 := strings.Repeat("c", 32)
	item := withLocalClockSave(t, downloadItem(dir, "Crystal.7z", "Crystal.zip", "zip"), dir, "S0", local32)

	payload := testZip(t, map[string]string{"Crystal.srm": "S1", "Crystal.rtc": "8-bytes!"})
	p, err := planFileDownload(item, payload, &settings.Config{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !p.keptLocalClock || p.clock != "" {
		t.Errorf("kept %v, clock %q, want the local clock kept", p.keptLocalClock, p.clock)
	}
	if string(p.ram) != "S1" || p.content != zipHash(t, payload) {
		t.Errorf("ram %q, content %s: the save data must still be written", p.ram, p.content)
	}
}

// Off ROCKNIX the clock entry is ignored and no .rtc is created.
func TestPlanFileDownload_NoClockPairing(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.Knulli))
	dir := t.TempDir()
	payload := testZip(t, map[string]string{"Crystal.srm": "S1", "Crystal.rtc": "R1"})
	item := downloadItem(dir, "Crystal.7z", "Crystal.zip", "zip")
	p, err := planFileDownload(item, payload, &settings.Config{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.clock != "" || p.clockData != nil {
		t.Errorf("clock %q planned without clock pairing", p.clock)
	}
	if want := filepath.Join(dir, "Crystal.srm"); p.primary != want || string(p.ram) != "S1" {
		t.Errorf("primary %q ram %q", p.primary, p.ram)
	}
}

// W9: Sigil's neutral zip: save.sram becomes the .srm; its clock.rtc is ignored.
func TestPlanFileDownload_SigilNeutralZip(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	payload := testZip(t, map[string]string{"save.sram": "S1", "clock.rtc": strings.Repeat("n", 48)})
	item := downloadItem(dir, "Crystal.7z", "Crystal.zip", "zip")
	p, err := planFileDownload(item, payload, &settings.Config{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "Crystal.srm"); p.primary != want {
		t.Errorf("primary = %q, want %q", p.primary, want)
	}
	if p.clock != "" {
		t.Errorf("clock = %q, want none", p.clock)
	}
}

// W7: a raw .srm onto a .srm + .rtc save leaves the .rtc alone.
func TestPlanFileDownload_RawOntoClockSave(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	item := withLocalClockSave(t, downloadItem(dir, "Crystal.7z", "Crystal.srm", "srm"), dir, "S0", "R0")

	p, err := planFileDownload(item, []byte("S1"), &settings.Config{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.primary != item.LocalSave.FilePath || p.clock != "" {
		t.Errorf("primary %q clock %q, want the .srm only", p.primary, p.clock)
	}
	if p.content != hashing.ComputeBytesMD5([]byte("S1")) {
		t.Errorf("content = %s, want the payload's MD5", p.content)
	}
}

// W8: a zip with no usable save data writes nothing.
func TestPlanFileDownload_InvalidZipShapes(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	shapes := map[string]map[string]string{
		"no save data":       {"Crystal.rtc": "R1", "readme.txt": "x"},
		"two save-data":      {"Crystal.srm": "S1", "Crystal.sav": "S2"},
		"two clocks":         {"Crystal.srm": "S1", "Crystal.rtc": "R1", "Other.rtc": "R2"},
		"only unknown files": {"notes.txt": "x"},
	}
	for name, entries := range shapes {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			item := withLocalClockSave(t, downloadItem(dir, "Crystal.7z", "Crystal.zip", "zip"), dir, "S0", "R0")
			if _, err := planFileDownload(item, testZip(t, entries), &settings.Config{}, true); !errors.Is(err, errZipSaveShape) {
				t.Errorf("err = %v, want errZipSaveShape", err)
			}
			if readTestFile(t, item.LocalSave.FilePath) != "S0" || readTestFile(t, item.LocalSave.Companions[0]) != "R0" {
				t.Error("local save changed")
			}
		})
	}
}

// Writing a plan puts the bytes in place, and what the download records makes
// the next sync see no change.
func TestWritePlacement_ArgosyZipRoundTrip(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	payload := testZip(t, map[string]string{"Crystal.srm": "S1", "Crystal.rtc": "R1"})
	item := downloadItem(dir, "Crystal.7z", "Crystal.zip", "zip")
	p, err := planFileDownload(item, payload, &settings.Config{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePlacement(p); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, p.primary) != "S1" || readTestFile(t, p.clock) != "R1" {
		t.Fatal("written bytes differ from the zip entries")
	}
	if _, err := os.Stat(filepath.Join(dir, "Crystal.zip")); !os.IsNotExist(err) {
		t.Error("a Crystal.zip was written")
	}

	// What download records, and the save as the next scan sees it.
	identity, err := hashing.ComputeMD5(p.primary)
	if err != nil {
		t.Fatal(err)
	}
	rec := recordedHash{Content: p.content, Identity: identity}
	scanned := LocalSave{RomID: 1, FileName: "Crystal.srm", FilePath: p.primary, Companions: []string{p.clock}}
	if got := mustReportedHash(t, scanned, rec); got != p.content {
		t.Errorf("next sync reports %s, want the recorded %s", got, p.content)
	}
	// Grout's own hash of the files equals the server's: even without the record.
	if got := mustReportedHash(t, scanned, recordedHash{}); got != p.content {
		t.Errorf("composite of the written files %s != zip hash %s", got, p.content)
	}
}

func TestWritePlacement_RawLeavesClockAlone(t *testing.T) {
	dir := t.TempDir()
	srm := filepath.Join(dir, "Crystal.srm")
	rtc := filepath.Join(dir, "Crystal.rtc")
	writeTestFile(t, srm, "S0")
	writeTestFile(t, rtc, "R0")

	if err := writePlacement(downloadPlacement{primary: srm, ram: []byte("S1")}); err != nil {
		t.Fatal(err)
	}
	if readTestFile(t, srm) != "S1" || readTestFile(t, rtc) != "R0" {
		t.Errorf("srm %q rtc %q, want S1 and the untouched R0", readTestFile(t, srm), readTestFile(t, rtc))
	}
}

// A Game.srm the scan didn't match to the ROM still sits at the derived target,
// so it must be backed up before the download replaces it.
func TestBackupFile_UnmatchedFileAtTarget(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	existing := filepath.Join(dir, "Game.srm")
	writeTestFile(t, existing, "unmatched progress")

	item := downloadItem(dir, "Game.gbc", "Game.srm", "srm")
	p, err := planFileDownload(item, []byte("server sram"), &settings.Config{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.primary != existing {
		t.Fatalf("primary = %q, want %q", p.primary, existing)
	}

	if err := backupFile(p.primary, &settings.Config{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".backup"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("backup folder holds %d files, want one", len(entries))
	}
	name := entries[0].Name()
	if !strings.HasPrefix(name, "Game [") || !strings.HasSuffix(name, "].srm") {
		t.Errorf("backup named %q, want Game [<mtime>].srm", name)
	}
	if got := readTestFile(t, filepath.Join(dir, ".backup", name)); got != "unmatched progress" {
		t.Errorf("backup holds %q", got)
	}
}

func TestBackupFile_MissingFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := backupFile(filepath.Join(dir, "Game.srm"), &settings.Config{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backup")); !os.IsNotExist(err) {
		t.Errorf("backup folder created for a missing file (err %v)", err)
	}
}

// W11: with the option set, a new NDS save from Argosy (Game.srm) lands as the
// Game.sav DSperate reads, raw or zipped.
func TestPlanFileDownload_ExtOverrideForNewSave(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	cfg := &settings.Config{}
	cfg.SetSaveExtension("nds", "sav")

	for name, payload := range map[string][]byte{
		"raw": []byte("nds save"),
		"zip": testZip(t, map[string]string{"Game.srm": "nds save"}),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			item := downloadItem(dir, "Game.nds", "Game [2026].srm", "srm")
			item.LocalSave.FSSlug = "nds"
			p, err := planFileDownload(item, payload, cfg, true)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(dir, "Game.sav"); p.primary != want {
				t.Errorf("primary = %q, want %q", p.primary, want)
			}
		})
	}
}

// W12: the option only names new saves; an existing local save is written in place.
func TestPlanFileDownload_OverrideIgnoredForLocalSave(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	local := filepath.Join(dir, "Game.sav")
	writeTestFile(t, local, "old")
	cfg := &settings.Config{}
	cfg.SetSaveExtension("nds", "srm")

	item := downloadItem(dir, "Game.nds", "Game [2026].srm", "srm")
	item.LocalSave.FSSlug = "nds"
	item.LocalSave.FileName = "Game.sav"
	item.LocalSave.FilePath = local
	p, err := planFileDownload(item, []byte("new"), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.primary != local {
		t.Errorf("primary = %q, want the local %q", p.primary, local)
	}
}
