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
func TestFileDownloadTarget_RefusesNonSaveName(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	rom := filepath.Join(dir, "Game.zip")
	writeTestFile(t, rom, "rom bytes")

	item := downloadItem(dir, "Game.zip", "Game.zip", "zip")
	target, err := fileDownloadTarget(item, []byte("raw save bytes"), &settings.Config{})
	if !errors.Is(err, errNonSaveName) {
		t.Fatalf("err = %v, want errNonSaveName (target %q)", err, target)
	}
	if target != rom {
		t.Errorf("target = %q, want %q reported for the log", target, rom)
	}
	if got := readTestFile(t, rom); got != "rom bytes" {
		t.Errorf("ROM changed: %q", got)
	}
}

// W2: a zip payload must never be written into a single-file save.
func TestFileDownloadTarget_RefusesZipPayload(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	local := filepath.Join(dir, "Crystal.srm")
	writeTestFile(t, local, "local sram")

	item := downloadItem(dir, "Crystal.7z", "Crystal.zip", "zip")
	item.LocalSave.FileName = "Crystal.srm"
	item.LocalSave.FilePath = local

	payload := testZip(t, map[string]string{"Crystal.srm": "sram", "Crystal.rtc": "clock"})
	if _, err := fileDownloadTarget(item, payload, &settings.Config{}); !errors.Is(err, errZipSaveUnsupported) {
		t.Fatalf("err = %v, want errZipSaveUnsupported", err)
	}
	if got := readTestFile(t, local); got != "local sram" {
		t.Errorf("local save changed: %q", got)
	}
}

func TestFileDownloadTarget_LocalPathWins(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	local := filepath.Join(dir, "Crystal.srm")
	writeTestFile(t, local, "local sram")

	// The server's name and extension don't matter once a local save exists.
	item := downloadItem(dir, "Crystal.7z", "Crystal [2026-01-01_00-00-00].sav", "sav")
	item.LocalSave.FileName = "Crystal.srm"
	item.LocalSave.FilePath = local

	target, err := fileDownloadTarget(item, []byte("raw"), &settings.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if target != local {
		t.Errorf("target = %q, want %q", target, local)
	}
}

func TestFileDownloadTarget_DerivedName(t *testing.T) {
	t.Setenv(cfw.EnvVar, string(cfw.ROCKNIX))
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "Crystal.7z"), "rom bytes")

	item := downloadItem(dir, "Crystal.7z", "Crystal [2026-01-01_00-00-00].srm", "srm")
	target, err := fileDownloadTarget(item, []byte("raw"), &settings.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "Crystal.srm"); target != want {
		t.Errorf("target = %q, want %q", target, want)
	}
}

func TestFileDownloadTarget_NoSaveFolder(t *testing.T) {
	t.Setenv(cfw.EnvVar, "")
	item := downloadItem("", "Crystal.7z", "Crystal.srm", "srm")
	if _, err := fileDownloadTarget(item, []byte("raw"), nil); !errors.Is(err, errNoSavePath) {
		t.Fatalf("err = %v, want errNoSavePath", err)
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
	target, err := fileDownloadTarget(item, []byte("server sram"), &settings.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if target != existing {
		t.Fatalf("target = %q, want %q", target, existing)
	}

	if err := backupFile(target, &settings.Config{}); err != nil {
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
