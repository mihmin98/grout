package cfw

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"grout/cfw/muos"
	"grout/gamelist"
)

// esRestartFlag is watched by the EmulationStation frontend, which reloads its
// gamelists when it appears. The path is relative to the directory grout was
// launched from, which is where the frontend expects it.
const esRestartFlag = "./es_restart_request"

// scheduleESRestart asks the frontend to reload, so metadata just written shows
// up without the user restarting the device.
func scheduleESRestart() {
	file, err := os.Create(esRestartFlag)
	if err != nil {
		slog.Default().Debug("Unable to schedule ES restart", "error", err)
		return
	}
	file.Close()
}

// RefreshFrontend asks the frontend to reload its game lists, on firmwares
// whose frontend keeps them, so files removed from the card stop showing.
func RefreshFrontend() {
	if ActiveFirmware().Gamelist() == GamelistEmulationStation {
		scheduleESRestart()
	}
}

// AddGroutToGamelist adds grout's launcher entry to the frontend's game list.
// Only the EmulationStation family has somewhere to put it.
func AddGroutToGamelist(c CFW) {
	f := Lookup(c)
	path, launcher := f.GroutGamelist(), f.GroutLauncherPath()
	if path == "" || launcher == "" {
		return
	}

	gamelist.AddGroutEntry(path, launcher, f.GroutLogoPath())
	scheduleESRestart()
}

// FillGamesMetadata writes game metadata in the form the firmware reads.
func FillGamesMetadata(entries []gamelist.RomGameEntry) {
	logger := slog.Default()

	switch ActiveFirmware().Gamelist() {
	case GamelistEmulationStation:
		if err := gamelist.AddRomGamesToGamelist(entries, gamelist.GameListFileName); err != nil {
			logger.Warn("Failed to add games to ES gamelist.xml", "error", err)
		}
		scheduleESRestart()

	case GamelistMiyoo:
		if err := gamelist.AddRomGamesToGamelist(entries, gamelist.MiyooGameListFileName); err != nil {
			logger.Warn("Failed to add games to miyoogamelist.xml", "error", err)
		}

	case GamelistMuOSText:
		for _, entry := range entries {
			muos.AddGameDescription(entry)
		}

	case GamelistNone:
	}
}

// MetadataLocation is where FillGamesMetadata put one game's metadata: an entry
// in a gamelist the platform's games share (File is the gamelist, Entry the
// entry's <path>), or a file of the game's own (Entry is empty). File is empty
// when the firmware keeps no metadata.
type MetadataLocation struct {
	RomID int
	File  string
	Entry string
}

// MetadataLocationFor says where FillGamesMetadata writes an entry on the
// active firmware. The two must agree, or a deletion misses what was written.
func MetadataLocationFor(entry gamelist.RomGameEntry) MetadataLocation {
	switch ActiveFirmware().Gamelist() {
	case GamelistEmulationStation:
		return gamelistLocation(entry, gamelist.GameListFileName)

	case GamelistMiyoo:
		return gamelistLocation(entry, gamelist.MiyooGameListFileName)

	case GamelistMuOSText:
		textDir := muos.GetTextDirectory(entry.Platform.FSSlug, entry.Platform.Name)
		return MetadataLocation{RomID: entry.RomID, File: filepath.Join(textDir, entry.Game.BaseName+".txt")}
	}
	return MetadataLocation{RomID: entry.RomID}
}

// gamelistLocation is an entry's place in the platform's gamelist. An entry
// without a path cannot be found again by it, so it has no location.
func gamelistLocation(entry gamelist.RomGameEntry, name gamelist.FileName) MetadataLocation {
	if entry.Game.Path == "" {
		return MetadataLocation{RomID: entry.RomID}
	}
	return MetadataLocation{
		RomID: entry.RomID,
		File:  filepath.Join(entry.RomDirectory, string(name)),
		Entry: entry.Game.Path,
	}
}

// RemoveGamesMetadata takes out what FillGamesMetadata wrote at these
// locations on the active firmware, and returns how many entries and files
// went. Already gone counts as done, not as an error.
//
// It touches only the firmware's own kind of metadata: an entry is removed
// only from a file named as the firmware's gamelist, and a text file only from
// muOS's catalogue text folders. A location that fails these is an error.
func RemoveGamesMetadata(locations []MetadataLocation) (int, error) {
	switch format := ActiveFirmware().Gamelist(); format {
	case GamelistEmulationStation:
		removed, err := removeGamelistEntries(locations, gamelist.GameListFileName)
		if removed > 0 {
			scheduleESRestart()
		}
		return removed, err

	case GamelistMiyoo:
		return removeGamelistEntries(locations, gamelist.MiyooGameListFileName)

	case GamelistMuOSText:
		return removeMuOSTexts(locations, filepath.Join(muos.GetInfoDirectory(), "catalogue"))
	}
	return 0, nil
}

// RepointGameMetadata points the active firmware's gamelist entries for a
// game at the file it launches from now, after unpacking moved it. from are
// the paths the entries may name: the archive, or an earlier unpacked copy.
// It returns the gamelist it changed, or "" when it changed none.
//
// Only gamelists keep the path; muOS names its texts after the game, which
// unpacking does not change.
func RepointGameMetadata(romDirectory string, from []string, to string) (string, error) {
	var name gamelist.FileName
	switch ActiveFirmware().Gamelist() {
	case GamelistEmulationStation:
		name = gamelist.GameListFileName
	case GamelistMiyoo:
		name = gamelist.MiyooGameListFileName
	default:
		return "", nil
	}

	listFile := filepath.Join(romDirectory, string(name))
	changed, err := gamelist.RepointRomEntries(listFile, from, to)
	if err != nil || changed == 0 {
		return "", err
	}
	if name == gamelist.GameListFileName {
		scheduleESRestart()
	}
	return listFile, nil
}

// removeGamelistEntries removes the entries of locations that name a gamelist,
// editing each file once.
func removeGamelistEntries(locations []MetadataLocation, name gamelist.FileName) (int, error) {
	var errs []error
	var listFiles []string
	entries := make(map[string][]string)
	for _, loc := range locations {
		if loc.File == "" || loc.Entry == "" {
			continue
		}
		if filepath.Base(loc.File) != string(name) {
			errs = append(errs, fmt.Errorf("%s is not a %s", loc.File, name))
			continue
		}
		if _, seen := entries[loc.File]; !seen {
			listFiles = append(listFiles, loc.File)
		}
		entries[loc.File] = append(entries[loc.File], loc.Entry)
	}

	removed := 0
	for _, listFile := range listFiles {
		n, err := gamelist.RemoveRomEntries(listFile, entries[listFile])
		if err != nil {
			errs = append(errs, err)
		}
		removed += n
	}
	return removed, errors.Join(errs...)
}

// removeMuOSTexts removes the description texts of locations that name a file
// of the game's own.
func removeMuOSTexts(locations []MetadataLocation, catalogue string) (int, error) {
	var errs []error
	removed := 0
	for _, loc := range locations {
		if loc.File == "" || loc.Entry != "" {
			continue
		}
		if !isMuOSText(catalogue, loc.File) {
			errs = append(errs, fmt.Errorf("%s is not a muOS description text", loc.File))
			continue
		}
		switch err := os.Remove(loc.File); {
		case err == nil:
			removed++
		case errors.Is(err, fs.ErrNotExist):
		default:
			errs = append(errs, err)
		}
	}
	return removed, errors.Join(errs...)
}

// isMuOSText reports whether path is a description text where muOS keeps
// them: <catalogue>/<system>/text/<name>.txt.
func isMuOSText(catalogue, path string) bool {
	if !filepath.IsAbs(path) || filepath.Ext(path) != ".txt" {
		return false
	}
	rel, err := filepath.Rel(catalogue, filepath.Clean(path))
	if err != nil {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	return len(parts) == 3 && parts[0] != ".." && parts[1] == "text"
}
