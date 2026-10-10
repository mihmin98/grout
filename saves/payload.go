package saves

import (
	"archive/zip"
	"bytes"
	"errors"
	"path/filepath"
	"strings"
)

var (
	// errNonSaveName: a download for a ROM with no local save would be written
	// under a name without a save extension. On firmwares that keep saves beside
	// the ROMs that name can be the ROM itself (<rom>.zip).
	errNonSaveName = errors.New("download target is not named like a save file")
	// errZipSaveUnsupported: a zip payload for a single-file save. Writing it
	// would put zip bytes where the emulator expects save data.
	errZipSaveUnsupported = errors.New("downloaded save is a zip; unpacking zip saves is not supported")
)

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

// hasSaveExtension reports whether path ends in a save extension (any case).
func hasSaveExtension(path string) bool {
	return ValidSaveExtensions[strings.ToLower(filepath.Ext(path))]
}
