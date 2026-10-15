package download

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.uber.org/atomic"

	"grout/archive"
	"grout/cfw"
	"grout/files"
	"grout/romm"
)

// IsArchive reports whether a rom arrived as an archive worth unpacking.
func IsArchive(fileName string) bool {
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".zip", ".7z":
		return true
	}
	return false
}

// Unpacked is the outcome of unpacking a game.
type Unpacked struct {
	// Path is what the frontend should point at.
	Path string
	// Written lists the files the unpacking put on the card, taken from the
	// archive's own listing rather than the folder, which may hold saves.
	Written []string
	// Source is the archive the files came out of, when it sat beside them.
	// A later download of that archive replaces exactly these files.
	Source string
}

// ExtractMultiFile unpacks a game that arrived as a single archive holding
// several files.
//
// The archive lands in a temp directory rather than the rom directory, so a
// failure here leaves nothing half-unpacked where the frontend would find it.
func ExtractMultiFile(game romm.Rom, romDirectory string, progress *atomic.Float64) (Unpacked, error) {
	archivePath := MultiFileArchivePath(game)
	extractDir := filepath.Join(romDirectory, game.FsNameNoExt)

	// Listed before unpacking: a re-download unpacks into a folder that may
	// already hold the game's saves, and only the archive knows what is new.
	entries, err := archive.ZipFileNames(archivePath)
	if err != nil {
		os.Remove(archivePath)
		return Unpacked{}, fmt.Errorf("reading %s: %w", game.Name, err)
	}
	playlistExisted := files.FileExists(filepath.Join(romDirectory, game.FsNameNoExt+".m3u"))

	if err := archive.Unzip(archivePath, extractDir, progress); err != nil {
		os.Remove(archivePath)
		return Unpacked{}, fmt.Errorf("unpacking %s: %w", game.Name, err)
	}

	if err := cfw.OrganizeExtractedRom(extractDir, romDirectory, game.FsNameNoExt); err != nil {
		os.Remove(archivePath)
		os.RemoveAll(extractDir)
		return Unpacked{}, fmt.Errorf("arranging %s for this firmware: %w", game.Name, err)
	}

	if err := os.Remove(archivePath); err != nil {
		slog.Default().Warn("Could not remove the downloaded archive", "path", archivePath, "error", err)
	}

	return Unpacked{
		Path:    multiFileGamePath(romDirectory, extractDir, game.FsNameNoExt),
		Written: multiFileWritten(entries, extractDir, romDirectory, game.FsNameNoExt, playlistExisted),
	}, nil
}

// multiFileWritten maps a multi-file archive's entries to where they are after
// unpacking and the firmware's rearranging: in extractDir, or in the
// underscore-prefixed folder muOS renames it to. A playlist the firmware wrote
// into romDir counts when it was not there before.
func multiFileWritten(entries []string, extractDir, romDir, base string, playlistExisted bool) []string {
	movedDir := filepath.Join(romDir, "_"+base)

	written := make([]string, 0, len(entries)+1)
	for _, entry := range entries {
		inPlace, moved := filepath.Join(extractDir, entry), filepath.Join(movedDir, entry)
		switch {
		case files.FileExists(inPlace):
			written = append(written, inPlace)
		case files.FileExists(moved):
			written = append(written, moved)
		default:
			// A playlist the firmware lifted out, or a file it dropped.
			slog.Default().Debug("Unpacked file not found where expected", "entry", entry, "dir", extractDir)
		}
	}

	if !playlistExisted {
		if playlist := filepath.Join(romDir, base+".m3u"); files.FileExists(playlist) {
			written = append(written, playlist)
		}
	}
	return written
}

// ExtractArchive unpacks a single-file game's .zip or .7z into the folder it
// sits in, then removes the archive. It uses 7za when the firmware has one and
// the built-in readers otherwise.
//
// Nothing lands in the rom folder until the whole archive has unpacked: it is
// unpacked into a hidden staging folder beside the archive, on the same card
// so moving each file out is a rename. A failure, including running out of
// space or memory or being cancelled, leaves the archive and the folder as
// they were.
//
// mayReplace says whether a file already in the way may be overwritten: the
// caller allows the game's own earlier files and never a save. Any other
// file in the way stops the unpacking with a ConflictError.
func ExtractArchive(ctx context.Context, archivePath string, mayReplace func(string) bool, progress *atomic.Float64) (Unpacked, error) {
	return extractArchive(ctx, chooseUnpacker(archivePath), archivePath, mayReplace, progress)
}

// CanExtractWith7za reports whether the firmware has a 7za to unpack with.
func CanExtractWith7za() bool {
	_, ok := archive.SevenZipPath()
	return ok
}

// stagingPrefix names the hidden folders archives unpack into. The dot keeps
// them out of the frontend's lists and grout's own scans.
const stagingPrefix = ".grout-extract-"

// Headroom on top of what the archive itself says it needs.
const (
	spaceHeadroom  = 16 << 20
	memoryHeadroom = 32 << 20
)

// NoSpaceError is an archive that would not fit on the card.
type NoSpaceError struct{ Needed, Free uint64 }

func (e *NoSpaceError) Error() string {
	return fmt.Sprintf("not enough free space: needs %d bytes, %d free", e.Needed, e.Free)
}

// NoMemoryError is an archive whose dictionary needs more memory than the
// device has free.
type NoMemoryError struct{ Needed, Available uint64 }

func (e *NoMemoryError) Error() string {
	if e.Available == 0 {
		return fmt.Sprintf("not enough memory to unpack: needs about %d bytes", e.Needed)
	}
	return fmt.Sprintf("not enough memory to unpack: needs about %d bytes, %d available", e.Needed, e.Available)
}

// ConflictError is a file from the archive that would overwrite one that is
// not the game's own.
type ConflictError struct{ Path string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s is already on the card", e.Path)
}

// unpacker lists and unpacks one kind of archive.
type unpacker interface {
	list(ctx context.Context, archivePath string) ([]archive.Entry, error)
	unpack(ctx context.Context, archivePath, destDir string, progress *atomic.Float64) error
}

func chooseUnpacker(archivePath string) unpacker {
	if bin, ok := archive.SevenZipPath(); ok {
		return sevenZipCLI{bin: bin}
	}
	return builtinUnpacker{sevenZip: strings.EqualFold(filepath.Ext(archivePath), ".7z")}
}

// sevenZipCLI unpacks with the firmware's 7za.
type sevenZipCLI struct{ bin string }

func (u sevenZipCLI) list(ctx context.Context, archivePath string) ([]archive.Entry, error) {
	return archive.SevenZipList(ctx, u.bin, archivePath)
}

func (u sevenZipCLI) unpack(ctx context.Context, archivePath, destDir string, progress *atomic.Float64) error {
	return archive.SevenZipExtract(ctx, u.bin, archivePath, destDir, progress)
}

// builtinUnpacker unpacks with grout's own readers. It cannot be stopped part
// way, so a cancel takes effect once it finishes.
type builtinUnpacker struct{ sevenZip bool }

func (u builtinUnpacker) list(_ context.Context, archivePath string) ([]archive.Entry, error) {
	if u.sevenZip {
		return archive.List7z(archivePath)
	}
	return archive.ListZip(archivePath)
}

func (u builtinUnpacker) unpack(ctx context.Context, archivePath, destDir string, progress *atomic.Float64) error {
	var err error
	if u.sevenZip {
		err = archive.Un7zip(archivePath, destDir, progress)
	} else {
		err = archive.Unzip(archivePath, destDir, progress)
	}
	if err == nil {
		err = ctx.Err()
	}
	return err
}

func extractArchive(ctx context.Context, u unpacker, archivePath string, mayReplace func(string) bool, progress *atomic.Float64) (Unpacked, error) {
	logger := slog.Default()
	name := filepath.Base(archivePath)
	romDirectory := filepath.Dir(archivePath)

	entries, err := u.list(ctx, archivePath)
	if err != nil {
		return Unpacked{}, fmt.Errorf("reading %s: %w", name, err)
	}
	contents, tops, err := archiveLayout(entries)
	if err != nil {
		return Unpacked{}, fmt.Errorf("reading %s: %w", name, err)
	}
	if err := checkResources(entries, romDirectory); err != nil {
		return Unpacked{}, err
	}

	staging := filepath.Join(romDirectory, stagingPrefix+strings.TrimSuffix(name, filepath.Ext(name)))
	// A staging folder left by a run that was killed holds nothing worth keeping.
	if err := os.RemoveAll(staging); err != nil {
		return Unpacked{}, fmt.Errorf("clearing %s: %w", staging, err)
	}
	if err := os.MkdirAll(staging, 0755); err != nil {
		return Unpacked{}, fmt.Errorf("creating %s: %w", staging, err)
	}
	defer func() {
		if err := os.RemoveAll(staging); err != nil {
			logger.Warn("Could not remove the staging folder", "path", staging, "error", err)
		}
	}()

	if err := u.unpack(ctx, archivePath, staging, progress); err != nil {
		if errors.Is(err, archive.ErrOutOfMemory) {
			return Unpacked{}, &NoMemoryError{Needed: decoderMemory(entries)}
		}
		return Unpacked{}, fmt.Errorf("unpacking %s: %w", name, err)
	}

	if err := checkConflicts(tops, staging, romDirectory, archivePath, mayReplace); err != nil {
		return Unpacked{}, err
	}
	if err := moveOut(tops, staging, romDirectory); err != nil {
		return Unpacked{}, fmt.Errorf("moving %s into place: %w", name, err)
	}

	if err := os.Remove(archivePath); err != nil {
		logger.Warn("Could not remove the archive after unpacking", "path", archivePath, "error", err)
	}

	written := make([]string, 0, len(contents))
	for _, entry := range contents {
		written = append(written, filepath.Join(romDirectory, entry))
	}
	return Unpacked{
		Path:    filepath.Join(romDirectory, playablePath(contents)),
		Written: written,
		Source:  archivePath,
	}, nil
}

// archiveLayout returns the files an archive holds and the top-level names it
// would create, refusing one that is empty, encrypted, or reaches outside the
// folder it is unpacked into.
func archiveLayout(entries []archive.Entry) (contents, tops []string, err error) {
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.Encrypted {
			return nil, nil, fmt.Errorf("%s is password protected", entry.Name)
		}
		rel := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(entry.Name, "\\", "/")))
		if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, nil, fmt.Errorf("%q would land outside the rom folder", entry.Name)
		}
		top, _, _ := strings.Cut(rel, string(filepath.Separator))
		if !seen[top] {
			seen[top] = true
			tops = append(tops, top)
		}
		if !entry.IsDir {
			contents = append(contents, rel)
		}
	}
	if len(contents) == 0 {
		return nil, nil, errors.New("the archive holds no files")
	}
	return contents, tops, nil
}

// checkResources refuses an archive the card has no room for, or one whose
// dictionary needs more memory than is free: on a device with 1 GB, running
// out part way would take the whole app down with it.
func checkResources(entries []archive.Entry, romDirectory string) error {
	var needed uint64
	for _, entry := range entries {
		needed += entry.Size
	}
	if free, ok := archive.FreeBytes(romDirectory); ok && needed+spaceHeadroom > free {
		return &NoSpaceError{Needed: needed, Free: free}
	}

	memory := decoderMemory(entries)
	if memory == 0 {
		return nil
	}
	if available, ok := archive.AvailableMemory(); ok && memory+memoryHeadroom > available {
		return &NoMemoryError{Needed: memory, Available: available}
	}
	return nil
}

func decoderMemory(entries []archive.Entry) uint64 {
	var largest uint64
	for _, entry := range entries {
		largest = max(largest, archive.DecoderMemory(entry.Method))
	}
	return largest
}

// checkConflicts looks at every name the archive would create before any is
// moved, so a clash leaves the rom folder untouched. A folder in the way is
// never merged into, and the archive itself can never be replaced by
// something inside it.
func checkConflicts(tops []string, staging, romDirectory, archivePath string, mayReplace func(string) bool) error {
	for _, top := range tops {
		target := filepath.Join(romDirectory, top)
		info, err := os.Lstat(target)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("checking %s: %w", target, err)
		}

		staged, err := os.Lstat(filepath.Join(staging, top))
		if err != nil {
			return fmt.Errorf("checking what was unpacked: %w", err)
		}
		if info.IsDir() || staged.IsDir() || target == archivePath || mayReplace == nil || !mayReplace(target) {
			return &ConflictError{Path: target}
		}
	}
	return nil
}

// moveOut renames each unpacked top-level name into the rom folder. Should one
// fail, what was already moved is taken back out, so the folder is left as
// it was apart from files the game itself was allowed to replace.
func moveOut(tops []string, staging, romDirectory string) error {
	var moved []string
	for _, top := range tops {
		target := filepath.Join(romDirectory, top)
		_, statErr := os.Lstat(target)
		existed := statErr == nil

		if err := os.Rename(filepath.Join(staging, top), target); err != nil {
			for _, path := range moved {
				_ = os.RemoveAll(path)
			}
			return err
		}
		if !existed {
			moved = append(moved, target)
		}
	}
	return nil
}

// KeepsArchive reports whether a platform plays its games from the archive
// itself, so unpacking one would break it: arcade sets, Neo Geo, DOS. Both the
// RomM platform and the folder it is mapped to are checked, since a user can
// map either to the other.
func KeepsArchive(fsSlug, romDirectory string) bool {
	if archivePlatforms[strings.ToLower(fsSlug)] {
		return true
	}
	return romDirectory != "" && archiveFolders[strings.ToLower(filepath.Base(romDirectory))]
}

var archivePlatforms = map[string]bool{
	"arcade":     true,
	"neogeoaes":  true,
	"neogeomvs":  true,
	"cps1":       true,
	"cps2":       true,
	"cps3":       true,
	"naomi":      true,
	"naomi2":     true,
	"atomiswave": true,
	"dos":        true,
}

var archiveFolders = map[string]bool{
	"arcade":     true,
	"mame":       true,
	"fbneo":      true,
	"neogeo":     true,
	"cps1":       true,
	"cps2":       true,
	"cps3":       true,
	"naomi":      true,
	"atomiswave": true,
	"pc":         true,
	"dos":        true,
}

// playablePath picks what to launch out of an archive's contents: the playlist
// if there is one, since it is what ties several discs together, then a disc
// sheet, which names the tracks beside it, and otherwise the first file.
func playablePath(contents []string) string {
	if len(contents) == 0 {
		return ""
	}
	for _, exts := range [][]string{{".m3u"}, {".cue", ".gdi", ".ccd"}} {
		for _, name := range contents {
			if slices.ContainsFunc(exts, func(ext string) bool { return strings.EqualFold(filepath.Ext(name), ext) }) {
				return name
			}
		}
	}
	return contents[0]
}

// multiFileGamePath finds what to launch after a multi-file game is unpacked.
//
// A firmware that rearranges the files may have lifted a playlist up into the
// rom directory, so that is looked for first. Failing everything the extract
// directory itself is named, which at least points somewhere real.
func multiFileGamePath(romDirectory, extractDir, baseName string) string {
	logger := slog.Default()

	if playlist := filepath.Join(romDirectory, baseName+".m3u"); files.FileExists(playlist) {
		return playlist
	}

	entries, err := os.ReadDir(extractDir)
	if err != nil {
		logger.Debug("Cannot read the extract directory, naming it instead", "path", extractDir, "error", err)
		return extractDir
	}

	firstFile := ""
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".m3u") {
			return filepath.Join(extractDir, entry.Name())
		}
		if firstFile == "" {
			firstFile = filepath.Join(extractDir, entry.Name())
		}
	}

	if firstFile == "" {
		logger.Debug("Nothing was unpacked, naming the directory instead", "path", extractDir)
		return extractDir
	}
	return firstFile
}
