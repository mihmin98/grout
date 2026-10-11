package cache

import (
	"reflect"
	"strconv"
	"testing"
)

func TestDownloadedFiles_RoundTrip(t *testing.T) {
	cm := newTestManager(t)

	rows := []DownloadedFile{
		{RomID: 7, Kind: DownloadedROM, Path: "/roms/psx/Game/Game (Disc 2).chd"},
		{RomID: 7, Kind: DownloadedEntry, Path: "/roms/psx/gamelist.xml", Detail: "./Game/Game.m3u"},
		{RomID: 7, Kind: DownloadedArt, Path: "/roms/psx/images/Game.png"},
		{RomID: 7, Kind: DownloadedROM, Path: "/roms/psx/Game/Game (Disc 1).chd"},
		{RomID: 8, Kind: DownloadedROM, Path: "/roms/psx/Other.chd"},
	}
	if err := cm.RecordDownloadedFiles(rows); err != nil {
		t.Fatal(err)
	}

	got, err := cm.GetDownloadedFiles(7)
	if err != nil {
		t.Fatal(err)
	}
	want := []DownloadedFile{
		{RomID: 7, Kind: DownloadedArt, Path: "/roms/psx/images/Game.png"},
		{RomID: 7, Kind: DownloadedEntry, Path: "/roms/psx/gamelist.xml", Detail: "./Game/Game.m3u"},
		{RomID: 7, Kind: DownloadedROM, Path: "/roms/psx/Game/Game (Disc 1).chd"},
		{RomID: 7, Kind: DownloadedROM, Path: "/roms/psx/Game/Game (Disc 2).chd"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetDownloadedFiles(7) =\n%+v\nwant\n%+v", got, want)
	}

	got, err = cm.GetDownloadedFiles(99)
	if err != nil || len(got) != 0 {
		t.Errorf("GetDownloadedFiles(99) = %+v, %v; want nothing", got, err)
	}
}

func TestDownloadedFiles_SecondRecordAddsAndDeduplicates(t *testing.T) {
	cm := newTestManager(t)

	first := DownloadedFile{RomID: 7, Kind: DownloadedROM, Path: "/roms/gba/Game (USA).gba"}
	if err := cm.RecordDownloadedFiles([]DownloadedFile{first}); err != nil {
		t.Fatal(err)
	}
	// A re-download of another version, plus the first file again.
	second := DownloadedFile{RomID: 7, Kind: DownloadedROM, Path: "/roms/gba/Game (Europe).gba"}
	if err := cm.RecordDownloadedFiles([]DownloadedFile{first, second}); err != nil {
		t.Fatal(err)
	}

	got, err := cm.GetDownloadedFiles(7)
	if err != nil {
		t.Fatal(err)
	}
	want := []DownloadedFile{second, first}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetDownloadedFiles(7) = %+v, want %+v", got, want)
	}
}

func TestDownloadedFiles_RomIDsForPath(t *testing.T) {
	cm := newTestManager(t)

	shared := "/roms/gba/images/Game.png"
	rows := []DownloadedFile{
		{RomID: 12, Kind: DownloadedArt, Path: shared},
		{RomID: 5, Kind: DownloadedArt, Path: shared},
		{RomID: 5, Kind: DownloadedROM, Path: "/roms/gba/Game.zip"},
		{RomID: 12, Kind: DownloadedROM, Path: "/roms/gba/Game.7z"},
	}
	if err := cm.RecordDownloadedFiles(rows); err != nil {
		t.Fatal(err)
	}

	got, err := cm.RomIDsForPath(shared)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{5, 12}) {
		t.Errorf("RomIDsForPath(shared) = %v, want [5 12]", got)
	}

	got, err = cm.RomIDsForPath("/roms/gba/Game.zip")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{5}) {
		t.Errorf("RomIDsForPath(Game.zip) = %v, want [5]", got)
	}

	got, err = cm.RomIDsForPath("/roms/gba/Nothing.gba")
	if err != nil || len(got) != 0 {
		t.Errorf("RomIDsForPath(unknown) = %v, %v; want nothing", got, err)
	}
}

func TestDownloadedFiles_ForgetRemovesOnlyGivenRows(t *testing.T) {
	cm := newTestManager(t)

	rom := DownloadedFile{RomID: 7, Kind: DownloadedROM, Path: "/roms/gba/Game.gba"}
	art := DownloadedFile{RomID: 7, Kind: DownloadedArt, Path: "/roms/gba/images/Game.png"}
	entry := DownloadedFile{RomID: 7, Kind: DownloadedEntry, Path: "/roms/gba/gamelist.xml", Detail: "./Game.gba"}
	otherRom := DownloadedFile{RomID: 8, Kind: DownloadedArt, Path: "/roms/gba/images/Game.png"}
	if err := cm.RecordDownloadedFiles([]DownloadedFile{rom, art, entry, otherRom}); err != nil {
		t.Fatal(err)
	}

	// The entry is matched on its detail too: a different <path> in the same file stays.
	if err := cm.ForgetDownloadedFiles(7, []DownloadedFile{
		rom,
		art,
		{Kind: DownloadedEntry, Path: "/roms/gba/gamelist.xml", Detail: "./Other.gba"},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := cm.GetDownloadedFiles(7)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []DownloadedFile{entry}) {
		t.Errorf("ROM 7 after forget = %+v, want only the entry", got)
	}

	got, err = cm.GetDownloadedFiles(8)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []DownloadedFile{otherRom}) {
		t.Errorf("ROM 8 after forgetting ROM 7's rows = %+v, want its art kept", got)
	}
}

// "Rebuild Cache" clears the metadata tables; the record of files on the card stays.
func TestDownloadedFiles_SurviveClearMetadata(t *testing.T) {
	cm := newTestManager(t)

	row := DownloadedFile{RomID: 7, Kind: DownloadedROM, Path: "/roms/gba/Game.gba"}
	if err := cm.RecordDownloadedFiles([]DownloadedFile{row}); err != nil {
		t.Fatal(err)
	}
	if err := cm.ClearMetadata(); err != nil {
		t.Fatal(err)
	}

	got, err := cm.GetDownloadedFiles(7)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []DownloadedFile{row}) {
		t.Errorf("after ClearMetadata = %+v, want the row kept", got)
	}
}

func TestDownloadedFiles_NotInitialized(t *testing.T) {
	var cm *Manager
	if err := cm.RecordDownloadedFiles([]DownloadedFile{{RomID: 1}}); err != ErrNotInitialized {
		t.Errorf("RecordDownloadedFiles = %v, want ErrNotInitialized", err)
	}
	if _, err := cm.GetDownloadedFiles(1); err != ErrNotInitialized {
		t.Errorf("GetDownloadedFiles = %v, want ErrNotInitialized", err)
	}
	if _, err := cm.RomIDsForPath("/x"); err != ErrNotInitialized {
		t.Errorf("RomIDsForPath = %v, want ErrNotInitialized", err)
	}
	if err := cm.ForgetDownloadedFiles(1, []DownloadedFile{{RomID: 1}}); err != ErrNotInitialized {
		t.Errorf("ForgetDownloadedFiles = %v, want ErrNotInitialized", err)
	}
}

func TestMigrate_V15AddsDownloadedFiles(t *testing.T) {
	db := openV14DB(t,
		`UPDATE cache_metadata SET value = '15' WHERE key = 'schema_version'`,
		`CREATE TABLE save_sync_state (
			device_id TEXT NOT NULL,
			rom_id INTEGER NOT NULL,
			file_name TEXT NOT NULL,
			slot TEXT NOT NULL,
			save_id INTEGER,
			content_hash TEXT,
			identity_hash TEXT,
			synced_at TEXT NOT NULL,
			PRIMARY KEY (device_id, rom_id, file_name)
		)`,
		`INSERT INTO save_sync_state (device_id, rom_id, file_name, slot, save_id, content_hash, identity_hash, synced_at)
			VALUES ('dev-1', 6, 'Crystal.srm', 'autosave', 75, 'content-1', 'identity-1', '2026-01-01T00:00:00Z')`,
	)

	migrateAndCreate(t, db)

	for _, col := range []string{"rom_id", "kind", "path", "detail", "recorded_at"} {
		if !hasColumn(t, db, "downloaded_files", col) {
			t.Fatalf("downloaded_files.%s missing after migration", col)
		}
	}

	cm := &Manager{db: db, initialized: true, stats: &Stats{}}
	states := cm.GetSaveStates("dev-1")
	if len(states) != 1 {
		t.Fatalf("states = %+v, want the one row kept", states)
	}
	s := states[0]
	if s.RomID != 6 || s.FileName != "Crystal.srm" || s.Slot != "autosave" || s.SaveID != 75 ||
		s.ContentHash != "content-1" || s.IdentityHash != "identity-1" {
		t.Errorf("save state after migration = %+v", s)
	}

	row := DownloadedFile{RomID: 6, Kind: DownloadedROM, Path: "/roms/gbc/Crystal.gbc"}
	if err := cm.RecordDownloadedFiles([]DownloadedFile{row}); err != nil {
		t.Fatalf("recording into the migrated table: %v", err)
	}

	var version string
	if err := db.QueryRow(`SELECT value FROM cache_metadata WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != strconv.Itoa(schemaVersion) {
		t.Errorf("schema_version = %s, want %d", version, schemaVersion)
	}
}
