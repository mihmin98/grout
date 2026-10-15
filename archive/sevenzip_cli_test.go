package archive

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"go.uber.org/atomic"
)

const sevenZipListing = `
7-Zip (a) 26.04 (x64) : Copyright (c) 1999-2026 Igor Pavlov : 2026-10-05

Listing archive: Game.7z

--
Path = Game.7z
Type = 7z
Method = LZMA2:24
Solid = +

----------
Path = sub
Size = 0
Attributes = D drwxr-xr-x
CRC =
Encrypted = -
Method =

Path = Game.gbc
Size = 3000000
Attributes = A -rw-r--r--
Encrypted = -
Method = BCJ LZMA2:24

Path = sub/readme.txt
Size = 3
Attributes =  -rw-r--r--
Folder = -
Encrypted = +
Method = Store
`

const zipListing = `
Listing archive: Game.zip

--
Path = Game.zip
Type = zip

----------
Path = Disc
Folder = +
Size = 0

Path = Disc/Game.cue
Folder = -
Size = 120
Method = Deflate
`

func TestParseSevenZipListing(t *testing.T) {
	tests := []struct {
		name    string
		listing string
		want    []Entry
	}{
		{"7z", sevenZipListing, []Entry{
			{Name: "sub", IsDir: true},
			{Name: "Game.gbc", Size: 3000000, Method: "BCJ LZMA2:24"},
			{Name: "sub/readme.txt", Size: 3, Method: "Store", Encrypted: true},
		}},
		{"zip", zipListing, []Entry{
			{Name: "Disc", IsDir: true},
			{Name: "Disc/Game.cue", Size: 120, Method: "Deflate"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSevenZipListing(strings.NewReader(tt.listing))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

// The archive's own properties come before the dashes; without them there is
// nothing to trust.
func TestParseSevenZipListing_NoSeparator(t *testing.T) {
	if _, err := parseSevenZipListing(strings.NewReader("Path = Game.7z\nType = 7z\n")); err == nil {
		t.Error("expected an error for a listing without entries")
	}
}

func TestProgressParser(t *testing.T) {
	progress := &atomic.Float64{}
	p := &progressParser{progress: progress}

	// What 7za writes when its output is a pipe, split mid-token on purpose.
	chunks := []string{
		"  0M Scan\b\b\b\b\b\b\b\b\b         \b\b\b\b\b\b\b\b\b  0%\b\b\b\b    \b\b\b\b 6",
		"7% - big.bin\b\b\b\b\b\b\b\b\b\b\b\b\b\b               \b\b",
	}
	for _, c := range chunks {
		if _, err := p.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if got := progress.Load(); got != 0.67 {
		t.Errorf("progress = %v, want 0.67", got)
	}

	// Progress never runs backwards, whatever 7za prints next.
	_, _ = p.Write([]byte(" 12%\b\b\b\b"))
	if got := progress.Load(); got != 0.67 {
		t.Errorf("progress = %v after a lower value, want 0.67", got)
	}
}

func TestParsePercent(t *testing.T) {
	tests := map[string]float64{" 45% 3 - Game.gbc": 45, "100%": 100, "%": -1, "abc": -1, "250%": -1}
	for in, want := range tests {
		got, ok := parsePercent([]byte(in))
		if want < 0 {
			if ok {
				t.Errorf("parsePercent(%q) = %v, want no value", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("parsePercent(%q) = %v, %v; want %v", in, got, ok, want)
		}
	}
}

func TestDecoderMemory(t *testing.T) {
	tests := map[string]uint64{
		"LZMA2:24":              16 << 20,
		"LZMA2:3m":              3 << 20,
		"LZMA:1536m":            1536 << 20,
		"BCJ LZMA2:64m":         64 << 20,
		"BCJ2 LZMA2:24 LZMA:20": 16 << 20,
		"PPMD:o6:mem24":         16 << 20,
		"PPMD:o32:mem192m":      192 << 20,
		"Deflate":               0,
		"Store":                 0,
		"":                      0,
		"LZMA2:junk":            0,
	}
	for method, want := range tests {
		if got := DecoderMemory(method); got != want {
			t.Errorf("DecoderMemory(%q) = %d, want %d", method, got, want)
		}
	}
}

func TestTailBuffer(t *testing.T) {
	b := &tailBuffer{max: 8}
	_, _ = b.Write([]byte("0123456789"))
	_, _ = b.Write([]byte("ab"))
	if got := b.String(); got != "456789ab" {
		t.Errorf("tail = %q, want %q", got, "456789ab")
	}
}

// fakeSevenZip writes a script standing in for 7za. It exits with the code
// given for a command line that asks for one thread, mimicking an old 7za that
// does not know the switch, and otherwise writes a file into the -o folder.
func fakeSevenZip(t *testing.T, mmtExit int) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "7za")
	body := `#!/bin/sh
for a in "$@"; do
  case "$a" in
    -mmt1) exit ` + strconv.Itoa(mmtExit) + ` ;;
    -o*) out="${a#-o}" ;;
  esac
done
mkdir -p "$out" && echo rom > "$out/Game.gbc"
printf ' 50%%\b\b\b\b'
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestSevenZipExtract_RetriesWithoutTheThreadSwitch(t *testing.T) {
	bin := fakeSevenZip(t, sevenZipCommandLine)
	dest := t.TempDir()

	if err := SevenZipExtract(context.Background(), bin, "Game.7z", dest, &atomic.Float64{}); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Game.gbc")); err != nil {
		t.Errorf("the retry did not unpack: %v", err)
	}
}

func TestSevenZipExtract_OutOfMemory(t *testing.T) {
	bin := fakeSevenZip(t, sevenZipOutOfMemory)

	err := SevenZipExtract(context.Background(), bin, "Game.7z", t.TempDir(), &atomic.Float64{})
	if !errors.Is(err, ErrOutOfMemory) {
		t.Errorf("err = %v, want ErrOutOfMemory", err)
	}
}

func TestSevenZipExtract_WarningIsNotAFailure(t *testing.T) {
	bin := fakeSevenZip(t, sevenZipWarning)

	if err := SevenZipExtract(context.Background(), bin, "Game.7z", t.TempDir(), &atomic.Float64{}); err != nil {
		t.Errorf("a warning exit must not fail: %v", err)
	}
}

func TestSevenZipExtract_Cancelled(t *testing.T) {
	bin := fakeSevenZip(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := SevenZipExtract(ctx, bin, "Game.7z", t.TempDir(), &atomic.Float64{}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// Runs the real 7za when the machine has one.
func TestSevenZip_RealBinary(t *testing.T) {
	bin, ok := SevenZipPath()
	if !ok {
		t.Skip("7za is not installed")
	}

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "Game.zip")
	writeZip(t, archivePath, "Game.gbc", "rom bytes", "Extras/readme.txt", "hi")

	entries, err := SevenZipList(context.Background(), bin, archivePath)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir {
			names = append(names, e.Name)
		}
	}
	if !reflect.DeepEqual(names, []string{"Game.gbc", "Extras/readme.txt"}) {
		t.Errorf("names = %q", names)
	}

	dest := filepath.Join(dir, "out")
	progress := &atomic.Float64{}
	if err := SevenZipExtract(context.Background(), bin, archivePath, dest, progress); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "Extras", "readme.txt")); err != nil || string(got) != "hi" {
		t.Errorf("readme = %q, %v", got, err)
	}
	if progress.Load() != 1 {
		t.Errorf("progress = %v, want 1 once done", progress.Load())
	}

	if _, err := SevenZipList(context.Background(), bin, filepath.Join(dir, "missing.7z")); err == nil {
		t.Error("expected listing a missing archive to fail")
	}
}

// writeZip writes a zip of name, content pairs, in order.
func writeZip(t *testing.T, path string, nameContent ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for i := 0; i+1 < len(nameContent); i += 2 {
		entry, err := w.Create(nameContent[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(nameContent[i+1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}
