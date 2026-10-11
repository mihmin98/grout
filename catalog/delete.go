package catalog

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"grout/cache"
	"grout/cfw"
	"grout/download"
	"grout/files"
	"grout/gamelist"
	"grout/library"
	"grout/romm"
	"grout/saves"
	"grout/settings"
)

// Why a file a deletion would touch is left in place.
const (
	KeptSave      = "save"      // it looks like a save, a state or a backup
	KeptShared    = "shared"    // another game's record lists it too
	KeptOutside   = "outside"   // not inside the firmware's folders for the platform
	KeptDirectory = "directory" // a folder, not a file
)

// DeletePlan is what deleting a game from the device will do.
type DeletePlan struct {
	Game romm.Rom
	// Files are removed one by one.
	Files []string
	// Dirs are removed afterwards if empty, deepest first.
	Dirs []string
	// Meta are the gamelist entries and description texts to take out.
	Meta []cfw.MetadataLocation
	// Kept are recorded or inferred files left alone, and why.
	Kept []KeptFile
	// Records are the game's rows in the download record, forgotten once
	// their files are gone.
	Records []cache.DownloadedFile
	// Legacy means the game has no record, so what to remove was inferred
	// from its layout and the current art settings.
	Legacy bool
}

// KeptFile is a file deletion leaves in place.
type KeptFile struct {
	Path   string
	Reason string
}

// DeleteReport is what a deletion did.
type DeleteReport struct {
	Deleted []string
	// Missing were planned but already gone.
	Missing []string
	Failed  map[string]error
	Kept    []KeptFile
	// KeptDirs are folders left because something is still in them, with
	// how many entries remain.
	KeptDirs       map[string]int
	EntriesRemoved int
	// MetadataErr is why some metadata could not be taken out.
	MetadataErr error
}

// HasLocalCopy reports whether a game has anything on the device that
// deleting it would remove.
func HasLocalCopy(config settings.Config, game romm.Rom) bool {
	rows, err := recordedFiles(game.ID)
	if err != nil {
		slog.Default().Warn("Could not read the download record", "rom", game.ID, "error", err)
	}
	return hasLocalCopy(config, game, rows)
}

func hasLocalCopy(config settings.Config, game romm.Rom, rows []cache.DownloadedFile) bool {
	for _, row := range rows {
		if row.Kind == cache.DownloadedROM && files.FileExists(row.Path) {
			return true
		}
	}

	if game.PlatformFSSlug == "" {
		return false
	}
	if IsDownloaded(config, game) {
		return true
	}

	romDir := RomDirectory(config, game)
	if game.HasMultipleFiles {
		return slices.ContainsFunc(multiFileCandidates(game, romDir), files.FileExists)
	}
	for _, file := range game.Files {
		if IsFileDownloaded(config, game, file.FileName) {
			return true
		}
	}
	return false
}

// PlanDeletion works out what deleting a game removes: what its downloads
// recorded or, for a game downloaded before the record, what can be inferred.
// It changes nothing.
func PlanDeletion(config settings.Config, game romm.Rom) (DeletePlan, error) {
	if game.PlatformFSSlug == "" {
		return DeletePlan{Game: game}, fmt.Errorf("%s has no platform", game.Name)
	}

	rows, err := recordedFiles(game.ID)
	if err != nil {
		return DeletePlan{Game: game}, err
	}

	platform := gamePlatform(game)
	romDir := RomDirectory(config, game)
	roots := cfw.ManagedDirectories(config, platform.FSSlug, platform.Name)

	legacy := len(rows) == 0
	if legacy {
		rows = inferredFiles(config, game, platform, romDir)
	}

	plan := planDeletion(game, romDir, rows, roots, sharedWithOtherGame(game.ID), legacy)
	slog.Default().Info("Planned deleting a game",
		"rom", game.ID, "legacy", plan.Legacy, "files", plan.Files, "dirs", plan.Dirs,
		"meta", len(plan.Meta), "kept", plan.Kept)
	return plan, nil
}

// recordedFiles reads a game's rows of the download record. Without a cache
// there is no record, which is not an error.
func recordedFiles(romID int) ([]cache.DownloadedFile, error) {
	manager := cache.GetCacheManager()
	if manager == nil {
		return nil, nil
	}
	return manager.GetDownloadedFiles(romID)
}

// sharedWithOtherGame reports whether another game's record lists a path. When
// the record cannot be read the answer is yes, so the file is kept.
func sharedWithOtherGame(romID int) func(string) bool {
	manager := cache.GetCacheManager()
	return func(path string) bool {
		if manager == nil {
			return false
		}
		ids, err := manager.RomIDsForPath(path)
		if err != nil {
			slog.Default().Warn("Could not check who else uses a file, keeping it", "path", path, "error", err)
			return true
		}
		return slices.ContainsFunc(ids, func(id int) bool { return id != romID })
	}
}

// gamePlatform is the game's own platform, as a download from a collection
// screen would use it.
func gamePlatform(game romm.Rom) romm.Platform {
	return romm.Platform{ID: game.PlatformID, FSSlug: game.PlatformFSSlug, Name: game.PlatformDisplayName}
}

// inferredFiles stands in for the record of a game downloaded before there was
// one: the RomM-listed files where a download would have put them, the art the
// current settings would fetch, and the metadata that names those files. Only
// files that exist are listed.
func inferredFiles(config settings.Config, game romm.Rom, platform romm.Platform, romDir string) []cache.DownloadedFile {
	var romPaths []string
	if game.HasMultipleFiles {
		romPaths = multiFileCandidates(game, romDir)
	} else {
		for _, file := range game.Files {
			romPaths = append(romPaths, filepath.Join(romDir, file.FileName))
		}
	}

	var rows []cache.DownloadedFile
	add := func(kind, path, detail string) {
		row := cache.DownloadedFile{RomID: game.ID, Kind: kind, Path: path, Detail: detail}
		if !slices.Contains(rows, row) {
			rows = append(rows, row)
		}
	}

	for _, p := range romPaths {
		if exists(p) {
			add(cache.DownloadedROM, p, "")
		}
	}
	for _, p := range download.ArtLocations(config, game, platform) {
		if exists(p) {
			add(cache.DownloadedArt, p, "")
		}
	}

	for _, row := range slices.Clone(rows) {
		if row.Kind != cache.DownloadedROM {
			continue
		}
		loc := cfw.MetadataLocationFor(gamelist.RomGameEntry{
			Game:         library.Game{Path: row.Path, BaseName: game.FsNameNoExt},
			Platform:     platform.ToPlatform(),
			RomDirectory: romDir,
			RomID:        game.ID,
		})
		switch {
		case loc.File == "":
		case loc.Entry != "":
			add(cache.DownloadedEntry, loc.File, loc.Entry)
		default:
			add(cache.DownloadedMeta, loc.File, "")
		}
	}
	return rows
}

// exists reports whether something is at path, without following a symlink.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// multiFileCandidates lists where a multi-file game's RomM-listed files and
// playlist would be after a download: in <base>/, or in _<base>/ where muOS
// moves them, plus the playlist muOS lifts into the ROM folder.
//
// RomM gives each file's folder as file_path, which starts with the game's
// own folder (fs_path/fs_name); the rest is the file's place inside it. A file
// whose folder does not fit, including one that climbs out with "..", is
// looked for at the top of the game folder.
func multiFileCandidates(game romm.Rom, romDir string) []string {
	base := game.FsNameNoExt
	if base == "" {
		return nil
	}
	gameFolder := path.Join(game.FsPath, game.FsName)

	var inside []string
	for _, file := range game.Files {
		if file.FileName == "" || strings.ContainsAny(file.FileName, `/\`) {
			continue
		}
		sub := ""
		switch folder := path.Clean(filepath.ToSlash(file.FilePath)); {
		case folder == gameFolder:
		case strings.HasPrefix(folder, gameFolder+"/"):
			sub = strings.TrimPrefix(folder, gameFolder+"/")
		default:
			slog.Default().Debug("RomM file outside the game's folder, looking at the top",
				"file", file.FileName, "file_path", file.FilePath, "game_folder", gameFolder)
		}
		inside = append(inside, path.Join(sub, file.FileName))
	}
	inside = append(inside, base+".m3u")

	var candidates []string
	for _, folder := range []string{base, "_" + base} {
		for _, rel := range inside {
			candidates = append(candidates, filepath.Join(romDir, folder, filepath.FromSlash(rel)))
		}
	}
	candidates = append(candidates, filepath.Join(romDir, base+".m3u"))
	slices.Sort(candidates)
	return slices.Compact(candidates)
}

// planDeletion decides what to remove from the rows of a game's record, or of
// what was inferred for it. Every file passes the guards in keepReason; one
// that fails is kept, with the reason. shared reports whether another game
// also lists a path.
func planDeletion(game romm.Rom, romDir string, rows []cache.DownloadedFile, roots []string,
	shared func(string) bool, legacy bool) DeletePlan {

	plan := DeletePlan{Game: game, Legacy: legacy}
	if !legacy {
		plan.Records = rows
	}

	romDir = filepath.Clean(romDir)
	seen := make(map[string]bool)
	dirs := make(map[string]bool)

	for _, row := range rows {
		switch row.Kind {
		case cache.DownloadedEntry:
			plan.Meta = append(plan.Meta, cfw.MetadataLocation{RomID: game.ID, File: row.Path, Entry: row.Detail})
			continue
		case cache.DownloadedMeta:
			plan.Meta = append(plan.Meta, cfw.MetadataLocation{RomID: game.ID, File: row.Path})
			continue
		case cache.DownloadedROM, cache.DownloadedArt:
		default:
			slog.Default().Warn("Unknown kind in the download record, ignoring it", "kind", row.Kind, "path", row.Path)
			continue
		}

		if seen[row.Path] {
			continue
		}
		seen[row.Path] = true

		if reason := keepReason(row.Path, roots, shared); reason != "" {
			plan.Kept = append(plan.Kept, KeptFile{Path: row.Path, Reason: reason})
			continue
		}
		plan.Files = append(plan.Files, row.Path)

		if row.Kind != cache.DownloadedROM {
			continue
		}
		for dir := filepath.Dir(row.Path); isInside(romDir, dir) && !slices.Contains(roots, dir); dir = filepath.Dir(dir) {
			dirs[dir] = true
		}
	}

	for dir := range dirs {
		plan.Dirs = append(plan.Dirs, dir)
	}
	slices.SortFunc(plan.Dirs, func(a, b string) int {
		return cmp.Or(cmp.Compare(depth(b), depth(a)), cmp.Compare(a, b))
	})
	return plan
}

// keepReason says why a file must be left in place, or "" when it may go.
func keepReason(p string, roots []string, shared func(string) bool) string {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return KeptOutside
	}
	if !slices.ContainsFunc(roots, func(root string) bool { return isInside(root, p) || root == p }) {
		return KeptOutside
	}
	if slices.Contains(roots, p) {
		return KeptDirectory
	}
	// A folder on the way there that is a symlink could lead anywhere.
	if !parentStaysInside(p, roots) {
		return KeptOutside
	}
	if info, err := os.Lstat(p); err == nil && info.IsDir() {
		return KeptDirectory
	}
	if saves.IsSaveLike(p) {
		return KeptSave
	}
	if shared(p) {
		return KeptShared
	}
	return ""
}

// parentStaysInside reports whether p's folder, with symlinks resolved, is
// still inside one of the roots (also resolved). A folder that does not exist
// holds nothing to remove, so it passes.
func parentStaysInside(p string, roots []string) bool {
	parent, err := filepath.EvalSymlinks(filepath.Dir(p))
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	for _, root := range roots {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if resolved == parent || isInside(resolved, parent) {
			return true
		}
	}
	return false
}

// isInside reports whether p is strictly below dir. Both must be clean.
func isInside(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func depth(p string) int {
	return strings.Count(p, string(filepath.Separator))
}

// ExecuteDeletion carries out a plan: the files, then the metadata, then the
// folders left empty, and finally the record of what is gone.
func ExecuteDeletion(plan DeletePlan) DeleteReport {
	return executeDeletion(plan, forgetDownloadedFiles)
}

func forgetDownloadedFiles(romID int, rows []cache.DownloadedFile) error {
	manager := cache.GetCacheManager()
	if manager == nil {
		return nil
	}
	return manager.ForgetDownloadedFiles(romID, rows)
}

func executeDeletion(plan DeletePlan, forget func(int, []cache.DownloadedFile) error) DeleteReport {
	logger := slog.Default()
	report := DeleteReport{
		Kept:     plan.Kept,
		Failed:   make(map[string]error),
		KeptDirs: make(map[string]int),
	}

	settled := make(map[string]bool) // gone, or kept on purpose: its row can go
	for _, kept := range plan.Kept {
		settled[kept.Path] = true
	}

	for _, p := range plan.Files {
		switch err := os.Remove(p); {
		case err == nil:
			report.Deleted = append(report.Deleted, p)
			settled[p] = true
		case errors.Is(err, fs.ErrNotExist):
			report.Missing = append(report.Missing, p)
			settled[p] = true
		default:
			report.Failed[p] = err
		}
	}
	logger.Info("Deleted a game's files", "rom", plan.Game.ID,
		"deleted", report.Deleted, "missing", report.Missing, "failed", len(report.Failed), "kept", plan.Kept)

	report.EntriesRemoved, report.MetadataErr = cfw.RemoveGamesMetadata(plan.Meta)
	if report.MetadataErr != nil {
		logger.Warn("Could not remove all of a game's metadata", "rom", plan.Game.ID, "error", report.MetadataErr)
	}
	logger.Info("Removed a game's metadata", "rom", plan.Game.ID, "removed", report.EntriesRemoved)

	for _, dir := range plan.Dirs {
		err := os.Remove(dir)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) > 0 {
			report.KeptDirs[dir] = len(entries)
			continue
		}
		report.Failed[dir] = err
	}
	logger.Info("Removed a game's empty folders", "rom", plan.Game.ID, "dirs", plan.Dirs, "kept", report.KeptDirs)

	if len(report.Deleted) > 0 {
		cfw.RefreshFrontend()
	}

	var forgotten []cache.DownloadedFile
	for _, row := range plan.Records {
		switch row.Kind {
		case cache.DownloadedEntry, cache.DownloadedMeta:
			if report.MetadataErr == nil {
				forgotten = append(forgotten, row)
			}
		default:
			if settled[row.Path] {
				forgotten = append(forgotten, row)
			}
		}
	}
	if err := forget(plan.Game.ID, forgotten); err != nil {
		logger.Warn("Could not update the download record", "rom", plan.Game.ID, "error", err)
	}

	return report
}
