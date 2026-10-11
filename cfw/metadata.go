package cfw

import (
	"log/slog"
	"os"
	"path/filepath"

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

// AddGroutToGamelist adds grout's launcher entry to the frontend's game list.
// Only the EmulationStation family has somewhere to put it.
func AddGroutToGamelist(c CFW) {
	f := Lookup(c)
	path, launcher := f.GroutGamelist(), f.GroutLauncherPath()
	if path == "" || launcher == "" {
		return
	}

	gamelist.AddGroutEntry(path, launcher)
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
