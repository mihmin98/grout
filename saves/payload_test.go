package saves

import (
	"archive/zip"
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
)

func zipReader(t *testing.T, data []byte) *zip.Reader {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClassifyZipEntries(t *testing.T) {
	tests := []struct {
		name        string
		entries     map[string]string
		wantRAM     string
		wantRTC     string
		wantIgnored []string
		wantErr     bool
	}{
		{name: "Argosy shape", entries: map[string]string{"Crystal.srm": "S", "Crystal.rtc": "R"},
			wantRAM: "Crystal.srm", wantRTC: "Crystal.rtc"},
		{name: "save data only", entries: map[string]string{"Crystal.srm": "S"}, wantRAM: "Crystal.srm"},
		{name: "Sigil neutral: clock.rtc ignored", entries: map[string]string{"save.sram": "S", "clock.rtc": "N"},
			wantRAM: "save.sram", wantIgnored: []string{"clock.rtc"}},
		{name: ".sav save data", entries: map[string]string{"Game.sav": "S"}, wantRAM: "Game.sav"},
		{name: "upper-case extensions", entries: map[string]string{"Crystal.SRM": "S", "Crystal.RTC": "R"},
			wantRAM: "Crystal.SRM", wantRTC: "Crystal.RTC"},
		{name: "unknown files ignored", entries: map[string]string{"Crystal.srm": "S", "notes.txt": "x"},
			wantRAM: "Crystal.srm", wantIgnored: []string{"notes.txt"}},
		{name: "macOS metadata and folders skipped",
			entries: map[string]string{"Crystal.srm": "S", "__MACOSX/._Crystal.srm": "m", "._Crystal.rtc": "m", "folder/": ""},
			wantRAM: "Crystal.srm"},
		{name: "entry in a folder accepted", entries: map[string]string{"saves/Crystal.srm": "S"}, wantRAM: "saves/Crystal.srm"},
		{name: "no save data", entries: map[string]string{"Crystal.rtc": "R"}, wantErr: true},
		{name: "two save-data entries", entries: map[string]string{"Crystal.srm": "S", "Crystal.sav": "S"}, wantErr: true},
		{name: "two clocks", entries: map[string]string{"Crystal.srm": "S", "Crystal.rtc": "R", "Other.rtc": "R"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ram, rtc, ignored, err := classifyZipEntries(zipReader(t, testZip(t, tt.entries)))
			if tt.wantErr {
				if !errors.Is(err, errZipSaveShape) {
					t.Fatalf("err = %v, want errZipSaveShape", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ram == nil || ram.Name != tt.wantRAM {
				t.Errorf("ram = %v, want %s", ram, tt.wantRAM)
			}
			gotRTC := ""
			if rtc != nil {
				gotRTC = rtc.Name
			}
			if gotRTC != tt.wantRTC {
				t.Errorf("rtc = %q, want %q", gotRTC, tt.wantRTC)
			}
			slices.Sort(ignored)
			if !slices.Equal(ignored, tt.wantIgnored) {
				t.Errorf("ignored = %v, want %v", ignored, tt.wantIgnored)
			}
		})
	}
}

func TestClassifyZipEntries_EntryOverCap(t *testing.T) {
	old := maxZipEntrySize
	maxZipEntrySize = 16
	defer func() { maxZipEntrySize = old }()

	r := zipReader(t, testZip(t, map[string]string{"Crystal.srm": strings.Repeat("s", 17)}))
	if _, _, _, err := classifyZipEntries(r); err == nil {
		t.Error("expected an error for an entry over the cap")
	}
}

// readZipEntry enforces the cap on the bytes it reads, not only on the header.
func TestReadZipEntry_Cap(t *testing.T) {
	old := maxZipEntrySize
	maxZipEntrySize = 16
	defer func() { maxZipEntrySize = old }()

	r := zipReader(t, testZip(t, map[string]string{"small.srm": "1234", "big.srm": strings.Repeat("b", 17)}))
	for _, f := range r.File {
		_, err := readZipEntry(f)
		if f.Name == "small.srm" && err != nil {
			t.Errorf("small entry: %v", err)
		}
		if f.Name == "big.srm" && err == nil {
			t.Error("big entry read past the cap")
		}
	}
}

func TestZipRAMExt(t *testing.T) {
	r := zipReader(t, testZip(t, map[string]string{"A.SRM": "", "B.sav": "", "save.sram": ""}))
	want := map[string]string{"A.SRM": "srm", "B.sav": "sav", "save.sram": "srm"}
	for _, f := range r.File {
		if got := zipRAMExt(f); got != want[f.Name] {
			t.Errorf("zipRAMExt(%s) = %q, want %q", f.Name, got, want[f.Name])
		}
	}
}
