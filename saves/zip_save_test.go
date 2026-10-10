package saves

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"grout/hashing"
)

func TestZipSaveUnit_ShapeAndName(t *testing.T) {
	ls := clockSave(t, "SRAM-BYTES", "CLOCK")
	srmTime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(ls.FilePath, srmTime, srmTime); err != nil {
		t.Fatal(err)
	}

	zipPath, cleanup, err := ZipSaveUnit(ls.FilePath, ls.Companions)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	if filepath.Base(zipPath) != "Crystal.zip" {
		t.Errorf("zip named %q, want Crystal.zip", filepath.Base(zipPath))
	}

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var names []string
	contents := map[string]string{}
	for _, f := range r.File {
		names = append(names, f.Name)
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		contents[f.Name] = string(b)
		if f.Name == "Crystal.srm" && !f.Modified.Equal(srmTime) {
			t.Errorf("Crystal.srm entry time = %v, want %v", f.Modified, srmTime)
		}
	}
	if want := []string{"Crystal.srm", "Crystal.rtc"}; !slices.Equal(names, want) {
		t.Errorf("entries = %v, want %v (primary first)", names, want)
	}
	if contents["Crystal.srm"] != "SRAM-BYTES" || contents["Crystal.rtc"] != "CLOCK" {
		t.Errorf("entry bytes = %v", contents)
	}
}

// What Grout uploads hashes on the server to what Grout reports for the save.
func TestZipSaveUnit_HashEqualsLooseFiles(t *testing.T) {
	ls := clockSave(t, "SRAM-BYTES", "CLOCK")

	zipPath, cleanup, err := ZipSaveUnit(ls.FilePath, ls.Companions)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	zipHash, err := hashing.ComputeCompositeZipHash(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	filesHash, err := hashing.ComputeFilesCompositeHash(append([]string{ls.FilePath}, ls.Companions...))
	if err != nil {
		t.Fatal(err)
	}
	if zipHash != filesHash {
		t.Errorf("zip hash %s != loose files hash %s", zipHash, filesHash)
	}
	if content, _, _ := saveHashes(ls); content != zipHash {
		t.Errorf("reported content %s != uploaded zip hash %s", content, zipHash)
	}
}

func TestZipSaveUnit_CleanupRemovesFolder(t *testing.T) {
	ls := clockSave(t, "SRAM-BYTES", "CLOCK")

	zipPath, cleanup, err := ZipSaveUnit(ls.FilePath, ls.Companions)
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	if _, err := os.Stat(filepath.Dir(zipPath)); !os.IsNotExist(err) {
		t.Errorf("temp folder still there after cleanup (err %v)", err)
	}
	if _, err := os.Stat(ls.FilePath); err != nil {
		t.Errorf("cleanup touched the save: %v", err)
	}
}

func TestZipSaveUnit_MissingCompanionErrors(t *testing.T) {
	ls := clockSave(t, "SRAM-BYTES", "CLOCK")
	if err := os.Remove(ls.Companions[0]); err != nil {
		t.Fatal(err)
	}
	if _, cleanup, err := ZipSaveUnit(ls.FilePath, ls.Companions); err == nil {
		cleanup()
		t.Error("expected an error for a missing companion")
	}
}
