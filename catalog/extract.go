package catalog

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"

	"go.uber.org/atomic"

	"grout/cache"
	"grout/cfw"
	"grout/download"
	"grout/files"
	"grout/romm"
	"grout/saves"
	"grout/settings"
)

// ExtractableArchive reports whether a game's downloaded file is an archive
// grout may unpack in place: a single-file game, on a platform that does not
// play from the archive itself. A multi-file game always arrives as an archive
// and is unpacked on its own path.
func ExtractableArchive(game romm.Rom, fsSlug, location string) bool {
	return !game.HasMultipleFiles && download.IsArchive(location) && !download.KeepsArchive(fsSlug, filepath.Dir(location))
}

// CanExtractWith7za reports whether the firmware has a 7za to unpack with.
func CanExtractWith7za() bool {
	return download.CanExtractWith7za()
}

// UnpackDownloaded unpacks a game's archive in place and forgets the record of
// the archive, which is gone. It records nothing new: the caller records what
// came out with the rest of what it wrote.
//
// A file the unpacking would overwrite may go only if the game's own record
// lists it and it is not a save.
func UnpackDownloaded(ctx context.Context, game romm.Rom, archivePath string, progress *atomic.Float64) (download.Unpacked, error) {
	unpacked, err := download.ExtractArchive(ctx, archivePath, ownedBy(game.ID), progress)
	if err != nil {
		return unpacked, err
	}
	forgetRows(game.ID, rowsNaming(game.ID, archivePath))
	return unpacked, nil
}

// ExtractLocalArchive unpacks a game's archive that is already on the card, as
// asked for from Game Options: it unpacks it, points the game's gamelist entry
// at what came out, and records the unpacked files in the archive's place.
func ExtractLocalArchive(ctx context.Context, game romm.Rom, archivePath string, progress *atomic.Float64) (download.Unpacked, error) {
	// Read before unpacking, which forgets the archive's rows.
	var entryFiles []string
	for _, row := range rowsNaming(game.ID, archivePath) {
		if row.Kind == cache.DownloadedEntry && !slices.Contains(entryFiles, row.Path) {
			entryFiles = append(entryFiles, row.Path)
		}
	}

	unpacked, err := UnpackDownloaded(ctx, game, archivePath, progress)
	if err != nil {
		return unpacked, err
	}

	listFile := RepointEntries(filepath.Dir(archivePath), []string{archivePath}, unpacked.Path)
	if listFile != "" && !slices.Contains(entryFiles, listFile) {
		entryFiles = append(entryFiles, listFile)
	}

	records := make([]download.Record, 0, len(unpacked.Written)+len(entryFiles))
	for _, path := range unpacked.Written {
		records = append(records, download.Record{RomID: game.ID, Kind: download.RecordROM, Path: path, Detail: archivePath})
	}
	for _, file := range entryFiles {
		records = append(records, download.Record{RomID: game.ID, Kind: download.RecordEntry, Path: file, Detail: unpacked.Path})
	}
	SaveDownloadRecords(records)

	cfw.RefreshFrontend()
	return unpacked, nil
}

// RepointEntries points a game's gamelist entry at the file it launches from
// now, keeping what the frontend stored on it. It returns the gamelist it
// changed, or "".
func RepointEntries(romDirectory string, from []string, to string) string {
	if len(from) == 0 || to == "" {
		return ""
	}
	listFile, err := cfw.RepointGameMetadata(romDirectory, from, to)
	if err != nil {
		slog.Default().Warn("Could not point the gamelist entry at the unpacked file", "dir", romDirectory, "to", to, "error", err)
	}
	return listFile
}

// ReplaceExtraction removes what an earlier unpacking of an archive left, now
// that a fresh copy of the archive has arrived, and returns the files it
// removed. Only called once the download has landed, so a failed or cancelled
// download leaves the game playable.
//
// Saves, files another game claims and anything outside the platform's folders
// stay. The game's gamelist entry is left for the caller to point at whatever
// the game launches from next.
func ReplaceExtraction(config settings.Config, game romm.Rom, archivePath string) []string {
	logger := slog.Default()

	rows, err := recordedFiles(game.ID)
	if err != nil {
		logger.Warn("Could not read the download record", "rom", game.ID, "error", err)
		return nil
	}
	stale := staleExtraction(game, archivePath, rows)
	if len(stale) == 0 {
		return nil
	}

	platform := gamePlatform(game)
	romDir := RomDirectory(config, game)
	roots := cfw.ManagedDirectories(config, platform.FSSlug, platform.Name)

	plan := planDeletion(game, romDir, stale, roots, sharedWithOtherGame(game.ID), false)
	report := ExecuteDeletion(plan)
	logger.Info("Replaced an unpacked copy with the downloaded archive",
		"rom", game.ID, "archive", archivePath, "deleted", report.Deleted, "kept", report.Kept)

	removed := slices.Concat(report.Deleted, report.Missing)
	// The entry rows naming those files go too; the caller records the entry
	// afresh once it knows where it points.
	var entries []cache.DownloadedFile
	for _, row := range rows {
		if row.Kind == cache.DownloadedEntry && slices.Contains(removed, row.Detail) {
			entries = append(entries, row)
		}
	}
	forgetRows(game.ID, entries)
	return removed
}

// staleExtraction picks the rom rows an earlier unpacking of archivePath
// recorded: those naming it as their source. Files unpacked before sources
// were recorded carry none; they are taken too when the game has a single
// version, so its archive is the only thing they can have come from, and they
// sit beside it.
func staleExtraction(game romm.Rom, archivePath string, rows []cache.DownloadedFile) []cache.DownloadedFile {
	singleVersion := len(game.PlayableFiles()) <= 1
	dir := filepath.Dir(archivePath)

	var stale []cache.DownloadedFile
	for _, row := range rows {
		if row.Kind != cache.DownloadedROM || row.Path == archivePath {
			continue
		}
		switch {
		case row.Detail == archivePath:
		case row.Detail == "" && singleVersion && isInside(dir, row.Path) && !download.IsArchive(row.Path):
		default:
			continue
		}
		stale = append(stale, row)
	}
	return stale
}

// LocalArchive finds a game's archive on the card that could be unpacked: the
// one its download recorded, or failing that one of its RomM files that is an
// archive and is in the rom folder.
func LocalArchive(config settings.Config, game romm.Rom) (string, bool) {
	if game.PlatformFSSlug == "" || game.HasMultipleFiles {
		return "", false
	}
	romDir := RomDirectory(config, game)

	var candidates []string
	if rows, err := recordedFiles(game.ID); err == nil {
		for _, row := range rows {
			// A row with a source was unpacked from it, not downloaded.
			if row.Kind == cache.DownloadedROM && row.Detail == "" {
				candidates = append(candidates, row.Path)
			}
		}
	}
	for _, file := range game.Files {
		candidates = append(candidates, filepath.Join(romDir, file.FileName))
	}

	for _, path := range candidates {
		if ExtractableArchive(game, game.PlatformFSSlug, path) && files.FileExists(path) {
			return path, true
		}
	}
	return "", false
}

// ownedBy allows unpacking to overwrite a file only when the game's own
// record lists it and it is not a save.
func ownedBy(romID int) func(string) bool {
	return func(path string) bool {
		if saves.IsSaveLike(path) {
			return false
		}
		manager := cache.GetCacheManager()
		if manager == nil {
			return false
		}
		ids, err := manager.RomIDsForPath(path)
		if err != nil {
			slog.Default().Warn("Could not check who owns a file, keeping it", "path", path, "error", err)
			return false
		}
		return slices.Contains(ids, romID)
	}
}

// rowsNaming is the game's rows about one file: the file itself, and gamelist
// entries pointing at it.
func rowsNaming(romID int, path string) []cache.DownloadedFile {
	rows, err := recordedFiles(romID)
	if err != nil {
		slog.Default().Warn("Could not read the download record", "rom", romID, "error", err)
		return nil
	}
	var naming []cache.DownloadedFile
	for _, row := range rows {
		if (row.Kind == cache.DownloadedROM && row.Path == path) || (row.Kind == cache.DownloadedEntry && row.Detail == path) {
			naming = append(naming, row)
		}
	}
	return naming
}

func forgetRows(romID int, rows []cache.DownloadedFile) {
	if len(rows) == 0 {
		return
	}
	if err := forgetDownloadedFiles(romID, rows); err != nil {
		slog.Default().Warn("Could not update the download record", "rom", romID, "error", err)
	}
}
