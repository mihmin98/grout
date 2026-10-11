package catalog

import (
	"reflect"
	"testing"

	"grout/cache"
	"grout/download"
)

// The record crosses from download to the cache as plain strings, so the two
// sets of kinds must agree.
func TestDownloadRecordKindsMatchTheCache(t *testing.T) {
	pairs := map[string]string{
		download.RecordROM:   cache.DownloadedROM,
		download.RecordArt:   cache.DownloadedArt,
		download.RecordMeta:  cache.DownloadedMeta,
		download.RecordEntry: cache.DownloadedEntry,
	}
	for got, want := range pairs {
		if got != want {
			t.Errorf("download kind %q, cache kind %q", got, want)
		}
	}
}

func TestDownloadedFiles(t *testing.T) {
	records := []download.Record{
		{RomID: 7, Kind: download.RecordROM, Path: "/roms/gba/Game.gba"},
		{RomID: 7, Kind: download.RecordEntry, Path: "/roms/gba/gamelist.xml", Detail: "/roms/gba/Game.gba"},
	}
	want := []cache.DownloadedFile{
		{RomID: 7, Kind: cache.DownloadedROM, Path: "/roms/gba/Game.gba"},
		{RomID: 7, Kind: cache.DownloadedEntry, Path: "/roms/gba/gamelist.xml", Detail: "/roms/gba/Game.gba"},
	}
	if got := downloadedFiles(records); !reflect.DeepEqual(got, want) {
		t.Errorf("downloadedFiles = %+v, want %+v", got, want)
	}
}
