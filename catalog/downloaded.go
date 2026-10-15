package catalog

import (
	"log/slog"
	"path/filepath"

	"grout/cache"
	"grout/cfw"
	"grout/files"
	"grout/romm"
	"grout/settings"
)

// DownloadState says how much of a game is already on the device.
type DownloadState int

const (
	NotDownloaded DownloadState = iota
	// PartlyDownloaded is a game stored as several files where some, but not
	// all of them, have arrived.
	PartlyDownloaded
	FullyDownloaded
)

// RomDirectory is where a game's files belong.
//
// It uses the rom's own platform slug, which differs from the screen's when
// browsing a collection that spans several platforms.
func RomDirectory(config settings.Config, game romm.Rom) string {
	return cfw.PlatformRomDirectory(config, game.PlatformFSSlug)
}

// romLayout describes how a game is stored on disk. Here rather than on
// romm.Rom so the API client stays free of the filesystem.
func romLayout(game romm.Rom) cfw.RomLayout {
	names := make([]string, 0, len(game.Files))
	for _, file := range game.Files {
		names = append(names, file.FileName)
	}
	return cfw.RomLayout{
		BaseName:  game.FsNameNoExt,
		MultiDisc: game.HasMultipleFiles,
		FileNames: names,
	}
}

// IsDownloaded reports whether a game is already on the device.
func IsDownloaded(config settings.Config, game romm.Rom) bool {
	return isDownloaded(config, game, gameRecord(game.ID))
}

func isDownloaded(config settings.Config, game romm.Rom, record downloadRecord) bool {
	if game.PlatformFSSlug == "" {
		return false
	}
	// An archive unpacked on the card no longer goes by the name RomM gives
	// it; what the download recorded still finds it.
	return romLayout(game).IsDownloaded(RomDirectory(config, game)) || record.hasRom(game.ID, "")
}

// IsFileDownloaded reports whether one named version of a game is present.
func IsFileDownloaded(config settings.Config, game romm.Rom, fileName string) bool {
	return isFileDownloaded(config, game, fileName, gameRecord(game.ID))
}

func isFileDownloaded(config settings.Config, game romm.Rom, fileName string, record downloadRecord) bool {
	if game.PlatformFSSlug == "" {
		return false
	}
	romDir := RomDirectory(config, game)
	// A version unpacked from its archive is found by the archive it came out of.
	return cfw.IsFileDownloaded(romDir, fileName) || (fileName != "" && record.hasRom(game.ID, filepath.Join(romDir, fileName)))
}

// DownloadStateOf reports how much of a game is on the device.
//
// Every answer costs a look at the card, which is slow enough on these devices
// to be worth asking for only when something will use it.
func DownloadStateOf(config settings.Config, game romm.Rom) DownloadState {
	return downloadStateOf(config, game, gameRecord(game.ID))
}

func downloadStateOf(config settings.Config, game romm.Rom, record downloadRecord) DownloadState {
	if !game.HasNestedSingleFile {
		if isDownloaded(config, game, record) {
			return FullyDownloaded
		}
		return NotDownloaded
	}

	if len(game.Files) == 0 {
		return NotDownloaded
	}

	present := 0
	for _, file := range game.Files {
		if isFileDownloaded(config, game, file.FileName, record) {
			present++
		}
	}

	switch {
	case present == len(game.Files):
		return FullyDownloaded
	case present > 0:
		return PartlyDownloaded
	default:
		return NotDownloaded
	}
}

// downloadRecord is the rom files downloads recorded, by game. It is read once
// for a whole list rather than once per game.
type downloadRecord map[int][]cache.DownloadedFile

// gameRecord reads one game's recorded rom files. Without a cache there is no
// record, and a game is judged by its file names alone.
func gameRecord(romID int) downloadRecord {
	manager := cache.GetCacheManager()
	if manager == nil {
		return nil
	}
	rows, err := manager.GetDownloadedFiles(romID)
	if err != nil {
		slog.Default().Debug("Could not read the download record", "rom", romID, "error", err)
		return nil
	}
	return downloadRecord{romID: rows}
}

// allRecords reads every game's recorded rom files in one query.
func allRecords() downloadRecord {
	manager := cache.GetCacheManager()
	if manager == nil {
		return nil
	}
	rows, err := manager.GetDownloadedROMs()
	if err != nil {
		slog.Default().Debug("Could not read the download record", "error", err)
		return nil
	}
	record := make(downloadRecord)
	for _, row := range rows {
		record[row.RomID] = append(record[row.RomID], row)
	}
	return record
}

// hasRom reports whether a recorded rom file of the game is still on the
// card. With source set, only files unpacked from that archive count.
func (r downloadRecord) hasRom(romID int, source string) bool {
	for _, row := range r[romID] {
		if row.Kind != cache.DownloadedROM || (source != "" && row.Detail != source) {
			continue
		}
		if files.FileExists(row.Path) {
			return true
		}
	}
	return false
}
