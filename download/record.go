package download

import (
	"slices"

	"grout/cfw"
	"grout/files"
)

// Kinds of Record. The values are what the cache stores. A rom unpacked from an
// archive beside it carries that archive as its Detail.
const (
	RecordROM   = "rom"   // a rom file: the download itself, an unpacked entry, a disc or playlist
	RecordArt   = "art"   // an art or media file
	RecordMeta  = "meta"  // a metadata file of the game's own, such as a muOS description text
	RecordEntry = "entry" // an entry in a shared gamelist: Path is the gamelist, Detail the entry's <path>
)

// Record is one thing a download created for a game, kept so a later delete
// can remove exactly that. This package stays free of the cache; the caller
// stores the records.
type Record struct {
	RomID  int
	Kind   string
	Path   string
	Detail string
}

// Records lists what a finished download wrote, for the delete action: each
// game's ROM files, the art that arrived, and its firmware metadata.
//
// written maps each game that landed to the rom files it left on the card. Art
// and metadata are recorded only for those games, and art only when its file
// exists, since a failed fetch writes nothing. Files unpacked from an archive
// beside them carry that archive as their Detail, so downloading it again can
// find and replace them.
func Records(written map[int]Unpacked, art []Item, entries []cfw.MetadataLocation) []Record {
	ids := make([]int, 0, len(written))
	for id := range written {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	var records []Record
	for _, id := range ids {
		for _, path := range written[id].Written {
			records = append(records, Record{RomID: id, Kind: RecordROM, Path: path, Detail: written[id].Source})
		}
	}

	for _, item := range art {
		if _, landed := written[item.RomID]; !landed || !files.FileExists(item.Location) {
			continue
		}
		records = append(records, Record{RomID: item.RomID, Kind: RecordArt, Path: item.Location})
	}

	for _, loc := range entries {
		if _, landed := written[loc.RomID]; !landed || loc.File == "" {
			continue
		}
		if loc.Entry == "" {
			records = append(records, Record{RomID: loc.RomID, Kind: RecordMeta, Path: loc.File})
		} else {
			records = append(records, Record{RomID: loc.RomID, Kind: RecordEntry, Path: loc.File, Detail: loc.Entry})
		}
	}

	return records
}
