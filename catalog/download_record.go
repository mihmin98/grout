package catalog

import (
	"log/slog"

	"grout/cache"
	"grout/download"
)

// SaveDownloadRecords stores what a download wrote, so the game can later be
// deleted precisely. A failure is logged and never fails the download: without
// a record, a delete falls back to what it can infer.
func SaveDownloadRecords(records []download.Record) {
	if len(records) == 0 {
		return
	}

	manager := cache.GetCacheManager()
	if manager == nil {
		slog.Default().Warn("No cache to record the download in", "files", len(records))
		return
	}

	if err := manager.RecordDownloadedFiles(downloadedFiles(records)); err != nil {
		slog.Default().Warn("Could not record the downloaded files", "files", len(records), "error", err)
	}
}

// downloadedFiles converts records to the cache's rows. The kinds share their
// values, so they pass through unchanged.
func downloadedFiles(records []download.Record) []cache.DownloadedFile {
	rows := make([]cache.DownloadedFile, 0, len(records))
	for _, r := range records {
		rows = append(rows, cache.DownloadedFile{RomID: r.RomID, Kind: r.Kind, Path: r.Path, Detail: r.Detail})
	}
	return rows
}
