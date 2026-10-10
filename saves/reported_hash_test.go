package saves

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"grout/hashing"
	"grout/romm"
)

// clockSave writes Crystal.srm and Crystal.rtc into a temp folder and returns
// the scanned save with the .rtc as its companion.
func clockSave(t *testing.T, sram, clock string) LocalSave {
	t.Helper()
	dir := t.TempDir()
	srm := filepath.Join(dir, "Crystal.srm")
	rtc := filepath.Join(dir, "Crystal.rtc")
	writeTestFile(t, srm, sram)
	writeTestFile(t, rtc, clock)
	return LocalSave{RomID: 6, FileName: "Crystal.srm", FilePath: srm, EmulatorDir: dir, Companions: []string{rtc}}
}

// syncedRecord is what a sync of ls in its current state records: the server's
// hash of the zip it uploads as, and the .srm's MD5.
func syncedRecord(t *testing.T, ls LocalSave) recordedHash {
	t.Helper()
	content, identity, err := saveHashes(ls)
	if err != nil {
		t.Fatal(err)
	}
	return recordedHash{Content: content, Identity: identity}
}

func mustReportedHash(t *testing.T, ls LocalSave, rec recordedHash) string {
	t.Helper()
	h, err := reportedHash(ls, rec)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestSaveHashes(t *testing.T) {
	ls := clockSave(t, "S1", "R1")
	content, identity, err := saveHashes(ls)
	if err != nil {
		t.Fatal(err)
	}
	wantContent, _ := hashing.ComputeFilesCompositeHash([]string{ls.FilePath, ls.Companions[0]})
	if content != wantContent {
		t.Errorf("content = %s, want the composite %s", content, wantContent)
	}
	if identity != hashing.ComputeBytesMD5([]byte("S1")) {
		t.Errorf("identity = %s, want MD5 of the .srm", identity)
	}

	ls.Companions = nil
	content, identity, err = saveHashes(ls)
	if err != nil {
		t.Fatal(err)
	}
	if content != identity || identity != hashing.ComputeBytesMD5([]byte("S1")) {
		t.Errorf("single file: content %s, identity %s, want both the MD5", content, identity)
	}
}

func TestReportedHash(t *testing.T) {
	synced := clockSave(t, "S1", "R1")
	rec := syncedRecord(t, synced)
	serverRec := recordedHash{Content: "server-hash", Identity: rec.Identity}

	clockOnly := clockSave(t, "S1", "R2")
	clockOnlyContent, _, _ := saveHashes(clockOnly)
	progress := clockSave(t, "S2", "R2")
	progressContent, _, _ := saveHashes(progress)

	tests := []struct {
		name string
		ls   LocalSave
		rec  recordedHash
		want string
	}{
		{"identity unchanged reports the recorded hash", clockOnly, serverRec, "server-hash"},
		{"identity changed reports the current content", progress, serverRec, progressContent},
		{"no record reports the current content", clockOnly, recordedHash{}, clockOnlyContent},
		{"empty recorded identity reports the current content", clockOnly, recordedHash{Content: "server-hash"}, clockOnlyContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mustReportedHash(t, tt.ls, tt.rec); got != tt.want {
				t.Errorf("reportedHash = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestBuildClientSaveStates_WithCompanions(t *testing.T) {
	ls := clockSave(t, "SRAM-BYTES", "CLOCK")
	srmTime := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(ls.FilePath, srmTime, srmTime); err != nil {
		t.Fatal(err)
	}
	// The clock file is newer, as after a session without saving.
	rtcTime := srmTime.Add(time.Hour)
	if err := os.Chtimes(ls.Companions[0], rtcTime, rtcTime); err != nil {
		t.Fatal(err)
	}

	states := buildClientSaveStates([]LocalSave{ls}, nil, nil, nil)
	if len(states) != 1 {
		t.Fatalf("states = %+v", states)
	}
	s := states[0]
	if s.FileName != "Crystal.srm" {
		t.Errorf("file = %s, want Crystal.srm", s.FileName)
	}
	if want := int64(len("SRAM-BYTES") + len("CLOCK")); s.FileSizeBytes != want {
		t.Errorf("size = %d, want %d", s.FileSizeBytes, want)
	}
	if !s.UpdatedAt.Equal(srmTime) {
		t.Errorf("updated_at = %v, want the .srm's %v", s.UpdatedAt, srmTime)
	}
	if content, _, _ := saveHashes(ls); s.ContentHash != content {
		t.Errorf("hash = %s, want the composite %s", s.ContentHash, content)
	}

	rec := map[saveKey]recordedHash{{6, "Crystal.srm"}: {Content: "server-hash", Identity: syncedRecord(t, ls).Identity}}
	states = buildClientSaveStates([]LocalSave{ls}, nil, nil, rec)
	if len(states) != 1 || states[0].ContentHash != "server-hash" {
		t.Errorf("with a matching record: states = %+v, want the recorded hash", states)
	}
}

func TestMapOperationsToItems_ClockOnlyChangeSuppressed(t *testing.T) {
	ls := clockSave(t, "S1", "R1")
	rec := syncedRecord(t, ls)
	recordedSlots := map[saveKey]string{{6, "Crystal.srm"}: "autosave"}
	recordedHashes := map[saveKey]recordedHash{{6, "Crystal.srm"}: rec}
	ops := []romm.SyncOperationSchema{{
		Action:   "upload",
		RomID:    6,
		Slot:     strPtr("autosave"),
		FileName: "Crystal [2026-03-01_12-00-00].zip",
	}}

	writeTestFile(t, ls.Companions[0], "R2")
	items := mapOperationsToItems(ops, []LocalSave{ls}, nil, nil, nil, recordedSlots, recordedHashes)
	if got := countUploads(items); got != 0 {
		t.Errorf("clock-only change: %d uploads, want 0", got)
	}

	writeTestFile(t, ls.FilePath, "S2")
	items = mapOperationsToItems(ops, []LocalSave{ls}, nil, nil, nil, recordedSlots, recordedHashes)
	if got := countUploads(items); got != 1 {
		t.Errorf(".srm changed: %d uploads, want 1", got)
	}
}

// A 409 on a save whose clock alone changed means the server is simply ahead.
func TestUpload409_ClockOnlyChangeDownloads(t *testing.T) {
	ls := clockSave(t, "S1", "R1")
	rec := syncedRecord(t, ls)
	writeTestFile(t, ls.Companions[0], "R2")

	slot := "autosave"
	serverSaves := []romm.Save{{ID: 75, RomID: 6, Slot: &slot}}
	res, save := resolveUpload409(serverSaves, slot, mustReportedHash(t, ls, rec), rec.Content)
	if res != resolve409AsDownload || save == nil || save.ID != 75 {
		t.Errorf("resolution = %d, save = %+v, want a download of save 75", res, save)
	}

	writeTestFile(t, ls.FilePath, "S2")
	if res, _ := resolveUpload409(serverSaves, slot, mustReportedHash(t, ls, rec), rec.Content); res != resolve409AsConflict {
		t.Errorf(".srm changed: resolution = %d, want a conflict", res)
	}
}
