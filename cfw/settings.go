package cfw

import (
	"path/filepath"
	"slices"

	"grout/cfw/muos"
	"grout/settings"
)

// PlatformRomDirectory returns where a platform's roms live on this device,
// honouring the user's directory mapping and the server's platform binding.
func PlatformRomDirectory(config settings.Config, fsSlug string) string {
	relativePath := fsSlug
	if mapping, ok := config.DirectoryMappings[fsSlug]; ok && mapping.RelativePath != "" {
		relativePath = mapping.RelativePath
	}
	return GetPlatformRomDirectory(relativePath, config.ResolveFSSlug(fsSlug))
}

// PlatformArtDirectory returns where a kind of artwork goes for a platform, or
// "" when the firmware keeps none. Callers treat "" as "skip", not as an error.
func PlatformArtDirectory(config settings.Config, slot ArtSlot, fsSlug, platformName string) string {
	return ArtDirectory(GetCFW(), slot, PlatformRomDirectory(config, fsSlug), fsSlug, platformName)
}

// ManagedDirectories lists the folders on this firmware where downloads for a
// platform put files: its ROM folder, the art and media folders, and muOS's
// description texts. Empty entries are left out and each folder appears once.
func ManagedDirectories(config settings.Config, fsSlug, platformName string) []string {
	romDir := PlatformRomDirectory(config, fsSlug)
	dirs := []string{romDir}
	// ArtCover and ArtManual are the first and last slots.
	for slot := ArtCover; slot <= ArtManual; slot++ {
		dirs = append(dirs, ArtDirectory(GetCFW(), slot, romDir, fsSlug, platformName))
	}
	if ActiveFirmware().Gamelist() == GamelistMuOSText {
		dirs = append(dirs, muos.GetTextDirectory(fsSlug, platformName))
	}

	managed := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if dir == "" || slices.Contains(managed, filepath.Clean(dir)) {
			continue
		}
		managed = append(managed, filepath.Clean(dir))
	}
	return managed
}
