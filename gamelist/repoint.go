package gamelist

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/beevik/etree"
)

// RepointRomEntries points the entries whose <path> names one of oldPaths at
// newPath instead, in the gamelist at listFile, and returns how many changed.
//
// Unpacking an archive changes which file a game launches from. Rewriting the
// entry in place keeps what the frontend stored on it (favourite, play count,
// last played), which removing it and adding a new one would lose. A relative
// <path> stays relative.
//
// Should that leave two entries naming newPath, the first is kept and the
// rest removed, so the game is not listed twice. A missing or empty file has
// nothing to change, and the file is rewritten only when something did.
func RepointRomEntries(listFile string, oldPaths []string, newPath string) (int, error) {
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
	changed := gl.repointRomEntries(listDir, oldPaths, newPath)
	if changed == 0 {
		return 0, nil
	}

	if err := gl.Save(listFile); err != nil {
		return 0, fmt.Errorf("saving %s: %w", listFile, err)
	}
	return changed, nil
}

func (gl *GameList) repointRomEntries(listDir string, oldPaths []string, newPath string) int {
	root := gl.root()
	target := resolveEntryPath(listDir, newPath)
	if root == nil || target == "" {
		return 0
	}

	from := make(map[string]bool, len(oldPaths))
	for _, p := range oldPaths {
		if resolved := resolveEntryPath(listDir, p); resolved != "" {
			from[resolved] = true
		}
	}

	changed := 0
	var kept *etree.Element
	for _, game := range root.SelectElements(GameElement) {
		pathElement := game.FindElement(PathElement)
		if pathElement == nil {
			continue
		}
		current := strings.TrimSpace(pathElement.Text())
		resolved := resolveEntryPath(listDir, current)

		if from[resolved] && resolved != target {
			pathElement.SetText(entryPathLike(current, listDir, target))
			resolved = target
			changed++
		}
		if resolved != target {
			continue
		}
		if kept == nil {
			kept = game
			continue
		}
		root.RemoveChild(game)
		changed++
	}
	return changed
}

// entryPathLike writes target the way like was written: "./Game.gbc" beside a
// relative entry, as EmulationStation keeps them, and absolute otherwise.
func entryPathLike(like, listDir, target string) string {
	if filepath.IsAbs(like) {
		return target
	}
	rel, err := filepath.Rel(listDir, target)
	if err != nil || strings.HasPrefix(rel, "..") {
		return target
	}
	return "./" + filepath.ToSlash(rel)
}
