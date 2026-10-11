package download

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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

// ExtractArchive unpacks a rom that arrived as a .zip or .7z straight into its
// rom directory.
//
// The archive is removed once it is unpacked. A failure leaves it alone, so
// the game is still playable from the archive if the frontend can read one.
func ExtractArchive(archivePath, romDirectory string, progress *atomic.Float64) (Unpacked, error) {
	sevenZip := strings.EqualFold(filepath.Ext(archivePath), ".7z")

	var contents []string
	var err error
	if sevenZip {
		contents, err = archive.SevenZipFileNames(archivePath)
	} else {
		contents, err = archive.ZipFileNames(archivePath)
	}
	if err != nil {
		return Unpacked{}, fmt.Errorf("reading %s: %w", filepath.Base(archivePath), err)
	}

	if sevenZip {
		err = archive.Un7zip(archivePath, romDirectory, progress)
	} else {
		err = archive.Unzip(archivePath, romDirectory, progress)
	}
	if err != nil {
		return Unpacked{}, fmt.Errorf("unpacking %s: %w", filepath.Base(archivePath), err)
	}

	if err := os.Remove(archivePath); err != nil {
		slog.Default().Warn("Could not remove the archive after unpacking", "path", archivePath, "error", err)
	}

	written := make([]string, 0, len(contents))
	for _, name := range contents {
		written = append(written, filepath.Join(romDirectory, name))
	}
	return Unpacked{Path: filepath.Join(romDirectory, playablePath(contents)), Written: written}, nil
}

// playablePath picks what to launch out of an archive's contents: the playlist
// if there is one, since it is what ties several discs together, and otherwise
// the first file.
func playablePath(contents []string) string {
	if len(contents) == 0 {
		return ""
	}
	for _, name := range contents {
		if strings.EqualFold(filepath.Ext(name), ".m3u") {
			return name
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
