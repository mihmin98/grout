package saves

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	gaba "github.com/BrandonKowalski/gabagool/v2/pkg/gabagool"
)

var (
	// errNonSaveName: a download for a ROM with no local save would be written
	// under a name without a save extension. On firmwares that keep saves beside
	// the ROMs that name can be the ROM itself (<rom>.zip).
	errNonSaveName = errors.New("download target is not named like a save file")
	// errZipSaveShape: a zip save without exactly one save-data entry, or with
	// more than one clock entry. Nothing in it can be placed safely.
	errZipSaveShape = errors.New("zip save must hold exactly one save-data entry and at most one clock entry")
)

// maxZipEntrySize caps how much of one zip entry is read into memory, far above
// any save RAM, so a misbehaving server can't send a zip bomb. A var for tests.
var maxZipEntrySize int64 = 64 << 20

// zipMagic is the signature a zip's first local file header starts with.
var zipMagic = []byte("PK\x03\x04")

// isZipPayload reports whether a downloaded save is a zip: it starts with the
// zip signature, opens as a zip, and holds at least one file. Raw save data that
// merely starts with "PK" is not a zip.
func isZipPayload(data []byte) bool {
	if !bytes.HasPrefix(data, zipMagic) {
		return false
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	for _, f := range r.File {
		if !f.FileInfo().IsDir() {
			return true
		}
	}
	return false
}

// classifyZipEntries finds the save-data entry and the clock entry in a
// downloaded zip save, by role rather than exact name. Save data is a .srm or
// .sav entry, or Sigil's save.sram; the clock is a .rtc entry. Sigil's neutral
// clock.rtc is not a RetroArch clock and is ignored, as is anything else. Folders,
// macOS metadata (__MACOSX/, ._*) and entries in subfolders are tolerated.
func classifyZipEntries(r *zip.Reader) (ram, rtc *zip.File, ignored []string, err error) {
	logger := gaba.GetLogger()
	var rams, rtcs []*zip.File
	for _, f := range r.File {
		if f.FileInfo().IsDir() || strings.HasPrefix(f.Name, "__MACOSX/") || strings.Contains(f.Name, "/__MACOSX/") {
			continue
		}
		base := path.Base(f.Name)
		if strings.HasPrefix(base, "._") {
			continue
		}
		if base != f.Name {
			logger.Warn("Zip save entry is in a folder; using its file name", "entry", f.Name)
		}

		ext := strings.ToLower(path.Ext(base))
		switch {
		case ext == ".srm" || ext == ".sav" || strings.EqualFold(base, "save.sram"):
			rams = append(rams, f)
		case strings.EqualFold(base, "clock.rtc"):
			logger.Warn("Ignoring a neutral-format clock file in a zip save; it is not a RetroArch clock", "entry", f.Name)
			ignored = append(ignored, f.Name)
		case ext == ".rtc":
			rtcs = append(rtcs, f)
		default:
			ignored = append(ignored, f.Name)
		}
	}

	if len(rams) != 1 || len(rtcs) > 1 {
		return nil, nil, ignored, fmt.Errorf("%w: %d save-data and %d clock entries", errZipSaveShape, len(rams), len(rtcs))
	}
	ram = rams[0]
	if len(rtcs) == 1 {
		rtc = rtcs[0]
	}
	for _, f := range []*zip.File{ram, rtc} {
		if f != nil && f.UncompressedSize64 > uint64(maxZipEntrySize) {
			return nil, nil, ignored, fmt.Errorf("zip entry %s is larger than %d bytes", f.Name, maxZipEntrySize)
		}
	}
	return ram, rtc, ignored, nil
}

// zipRAMExt is the save extension, without the dot, a zip's save-data entry is
// written with: its own (.srm or .sav), with Sigil's save.sram as .srm.
func zipRAMExt(f *zip.File) string {
	base := path.Base(f.Name)
	if strings.EqualFold(base, "save.sram") {
		return "srm"
	}
	return strings.TrimPrefix(strings.ToLower(path.Ext(base)), ".")
}

// readZipEntry reads one entry into memory, at most maxZipEntrySize bytes,
// whatever size its header claims.
func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxZipEntrySize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxZipEntrySize {
		return nil, fmt.Errorf("zip entry %s is larger than %d bytes", f.Name, maxZipEntrySize)
	}
	return data, nil
}

// zipEntryNames lists a zip's entries, for logs.
func zipEntryNames(r *zip.Reader) []string {
	names := make([]string, 0, len(r.File))
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	return names
}
