package gamelist

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// RemoveRomEntries removes the entries whose <path> names one of entryPaths
// from the gamelist at listFile, and returns how many went.
//
// A missing or empty file has nothing to remove. A file that cannot be parsed
// is left untouched, and the file is rewritten only when an entry was removed.
func RemoveRomEntries(listFile string, entryPaths []string) (int, error) {
	data, err := os.ReadFile(listFile)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", listFile, err)
	}
	if len(data) == 0 {
		return 0, nil
	}

	gl := New()
	if err := gl.Parse(data); err != nil {
		return 0, fmt.Errorf("parsing %s: %w", listFile, err)
	}

	listDir := filepath.Dir(listFile)
	removed := 0
	for _, entryPath := range entryPaths {
		removed += gl.RemoveRomEntry(listDir, entryPath)
	}
	if removed == 0 {
		return 0, nil
	}

	if err := gl.Save(listFile); err != nil {
		return 0, fmt.Errorf("saving %s: %w", listFile, err)
	}
	return removed, nil
}
