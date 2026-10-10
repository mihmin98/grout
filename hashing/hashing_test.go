package hashing

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestComputeMD5(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.bin")
	if err := os.WriteFile(p, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := ComputeMD5(p)
	if err != nil {
		t.Fatal(err)
	}
	want := "5d41402abc4b2a76b9719d911017c592" // md5("hello")
	if got != want {
		t.Errorf("ComputeMD5 = %s, want %s", got, want)
	}
	// sanity: matches stdlib
	sum := md5.Sum([]byte("hello"))
	if got != hex.EncodeToString(sum[:]) {
		t.Errorf("ComputeMD5 disagrees with stdlib")
	}
}

// helper: reference composite hash from an ordered (name -> content) map,
// computed independently of the zip-reading code path.
func refComposite(t *testing.T, entries map[string]string) string {
	t.Helper()
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	var lines []string
	for _, n := range names {
		sum := md5.Sum([]byte(entries[n]))
		lines = append(lines, n+":"+hex.EncodeToString(sum[:]))
	}
	combined := strings.Join(lines, "\n")
	final := md5.Sum([]byte(combined))
	return hex.EncodeToString(final[:])
}

func TestComputeCompositeZipHash_Structure(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "a.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	// write in non-sorted order to prove sorting happens
	for _, name := range []string{"b.txt", "a.txt"} {
		fw, _ := w.Create(name)
		fw.Write([]byte(name + "-content"))
	}
	// a directory entry must be ignored by the hash
	w.Create("d/")
	w.Close()
	f.Close()

	got, err := ComputeCompositeZipHash(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	want := refComposite(t, map[string]string{
		"a.txt": "a.txt-content",
		"b.txt": "b.txt-content",
	})
	if got != want {
		t.Errorf("ComputeCompositeZipHash = %s, want %s", got, want)
	}
}

func TestDirHashMatchesZipHash(t *testing.T) {
	root := t.TempDir()
	saveDir := filepath.Join(root, "ULUS10064DATA00")
	if err := os.MkdirAll(filepath.Join(saveDir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(saveDir, "SETTINGS.BIN"), []byte("settings"), 0644)
	os.WriteFile(filepath.Join(saveDir, "sub", "DATA.DAT"), []byte("payload"), 0644)
	os.WriteFile(filepath.Join(saveDir, ".hidden"), []byte("nope"), 0644) // must be skipped

	// Build a zip the same way addDirToZip does (folder name as top-level prefix).
	zipPath := filepath.Join(root, "out.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(zf)
	parent := filepath.Dir(saveDir)
	filepath.Walk(saveDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(parent, path)
		if strings.HasPrefix(filepath.Base(rel), ".") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			zw.Create(rel + "/")
			return nil
		}
		w, _ := zw.Create(rel)
		b, _ := os.ReadFile(path)
		w.Write(b)
		return nil
	})
	zw.Close()
	zf.Close()

	zipHash, err := ComputeCompositeZipHash(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	dirHash, err := ComputeDirsCompositeHash([]string{saveDir})
	if err != nil {
		t.Fatal(err)
	}
	if zipHash != dirHash {
		t.Errorf("dir hash %s != zip hash %s", dirHash, zipHash)
	}
}

// writeFiles creates each name -> content file in dir and returns the paths in
// the order given by names.
func writeFiles(t *testing.T, dir string, names []string, contents map[string]string) []string {
	t.Helper()
	paths := make([]string, 0, len(names))
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte(contents[n]), 0644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

// zipBytes builds a flat zip of the given entries in order, plus an optional
// directory entry, and returns its bytes.
func zipBytes(t *testing.T, names []string, contents map[string]string, dirEntry string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, n := range names {
		fw, err := w.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(contents[n])); err != nil {
			t.Fatal(err)
		}
	}
	if dirEntry != "" {
		if _, err := w.Create(dirEntry); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestComputeBytesMD5_MatchesComputeMD5(t *testing.T) {
	data := []byte("raw sram \x00\x01\x02")
	p := filepath.Join(t.TempDir(), "Game.srm")
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
	fromDisk, err := ComputeMD5(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := ComputeBytesMD5(data); got != fromDisk {
		t.Errorf("ComputeBytesMD5 = %s, ComputeMD5 = %s", got, fromDisk)
	}
}

func TestComputeZipReaderHash_MatchesComputeCompositeZipHash(t *testing.T) {
	contents := map[string]string{"Crystal.srm": "sram", "Crystal.rtc": "clock"}
	data := zipBytes(t, []string{"Crystal.srm", "Crystal.rtc"}, contents, "sub/")
	zipPath := filepath.Join(t.TempDir(), "Crystal.zip")
	if err := os.WriteFile(zipPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	fromDisk, err := ComputeCompositeZipHash(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	fromMemory, err := ComputeZipReaderHash(r)
	if err != nil {
		t.Fatal(err)
	}
	if fromMemory != fromDisk {
		t.Errorf("in-memory hash %s != on-disk hash %s", fromMemory, fromDisk)
	}
	if want := refComposite(t, contents); fromDisk != want {
		t.Errorf("zip hash %s, want %s", fromDisk, want)
	}
}

func TestComputeFilesCompositeHash_MatchesServerFormula(t *testing.T) {
	contents := map[string]string{"Crystal.srm": "save-ram-bytes", "Crystal.rtc": "clock-bytes"}
	paths := writeFiles(t, t.TempDir(), []string{"Crystal.srm", "Crystal.rtc"}, contents)

	got, err := ComputeFilesCompositeHash(paths)
	if err != nil {
		t.Fatal(err)
	}

	md5hex := func(s string) string {
		sum := md5.Sum([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	want := md5hex("Crystal.rtc:" + md5hex("clock-bytes") + "\nCrystal.srm:" + md5hex("save-ram-bytes"))
	if got != want {
		t.Errorf("ComputeFilesCompositeHash = %s, want %s", got, want)
	}
}

func TestComputeFilesCompositeHash_EqualsZipOfSameFiles(t *testing.T) {
	dir := t.TempDir()
	names := []string{"Crystal.srm", "Crystal.rtc"}
	contents := map[string]string{"Crystal.srm": "save-ram-bytes", "Crystal.rtc": "clock-bytes"}
	paths := writeFiles(t, dir, names, contents)

	zipPath := filepath.Join(dir, "Crystal.zip")
	if err := os.WriteFile(zipPath, zipBytes(t, names, contents, ""), 0644); err != nil {
		t.Fatal(err)
	}

	zipHash, err := ComputeCompositeZipHash(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	filesHash, err := ComputeFilesCompositeHash(paths)
	if err != nil {
		t.Fatal(err)
	}
	if filesHash != zipHash {
		t.Errorf("files hash %s != zip hash %s", filesHash, zipHash)
	}
}

func TestComputeFilesCompositeHash_DuplicateBaseNameErrors(t *testing.T) {
	root := t.TempDir()
	var paths []string
	for _, sub := range []string{"a", "b"} {
		dir := filepath.Join(root, sub)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, writeFiles(t, dir, []string{"Crystal.srm"}, map[string]string{"Crystal.srm": sub})...)
	}

	if _, err := ComputeFilesCompositeHash(paths); err == nil {
		t.Error("expected an error for two files with the same base name")
	}
}
