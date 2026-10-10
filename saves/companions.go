package saves

import (
	"path/filepath"
	"sort"
	"strings"
)

// companionExtensions maps a primary save extension to the extensions of files
// that belong with it. Mirrors the libretro default layout row in Sigil, which
// Argosy uses: {stem}.srm primary + {stem}.rtc clock.
var companionExtensions = map[string][]string{
	".srm": {".rtc"},
}

// findCompanions returns the files among names (the entries of the primary's
// folder) that belong with the save at primaryPath, as full paths, sorted. The
// stem must match exactly (RetroArch writes it as it is, and ROCKNIX's ext4 is
// case-sensitive); the extension matches in any case.
func findCompanions(primaryPath string, names []string) []string {
	primary := filepath.Base(primaryPath)
	ext := filepath.Ext(primary)
	exts := companionExtensions[strings.ToLower(ext)]
	if len(exts) == 0 {
		return nil
	}
	stem := strings.TrimSuffix(primary, ext)
	dir := filepath.Dir(primaryPath)

	var out []string
	for _, name := range names {
		nameExt := filepath.Ext(name)
		if strings.TrimSuffix(name, nameExt) != stem {
			continue
		}
		for _, want := range exts {
			if strings.EqualFold(nameExt, want) {
				out = append(out, filepath.Join(dir, name))
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
