package cache

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSaveState_IdentityHashRoundTrip(t *testing.T) {
	cm := newTestManager(t)
	const device = "dev-1"

	state := SaveSyncState{RomID: 6, FileName: "Crystal.srm", Slot: "autosave", SaveID: 75,
		ContentHash: "content-1", IdentityHash: "identity-1"}
	if err := cm.UpsertSaveState(device, state); err != nil {
		t.Fatal(err)
	}
	got := cm.GetSaveStates(device)
	if len(got) != 1 || got[0].ContentHash != "content-1" || got[0].IdentityHash != "identity-1" {
		t.Fatalf("after first upsert: %+v", got)
	}

	state.ContentHash, state.IdentityHash = "content-2", "identity-2"
	if err := cm.UpsertSaveState(device, state); err != nil {
		t.Fatal(err)
	}
	got = cm.GetSaveStates(device)
	if len(got) != 1 || got[0].ContentHash != "content-2" || got[0].IdentityHash != "identity-2" {
		t.Fatalf("after second upsert: %+v", got)
	}
}

// openV14DB opens a database file holding only v14 schema metadata, plus
// whatever the setup statements create.
func openV14DB(t *testing.T, setup ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v14.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	stmts := append([]string{
		`CREATE TABLE cache_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`INSERT INTO cache_metadata (key, value, updated_at) VALUES ('schema_version', '14', '2026-01-01T00:00:00Z')`,
	}, setup...)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return db
}

func hasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func migrateAndCreate(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := migrateIfNeeded(db); err != nil {
		t.Fatalf("migrateIfNeeded: %v", err)
	}
	if err := createTables(db); err != nil {
		t.Fatalf("createTables: %v", err)
	}
}

func TestMigrate_V14AddsIdentityHash(t *testing.T) {
	db := openV14DB(t,
		`CREATE TABLE save_sync_state (
			device_id TEXT NOT NULL,
			rom_id INTEGER NOT NULL,
			file_name TEXT NOT NULL,
			slot TEXT NOT NULL,
			save_id INTEGER,
			content_hash TEXT,
			synced_at TEXT NOT NULL,
			PRIMARY KEY (device_id, rom_id, file_name)
		)`,
		`INSERT INTO save_sync_state (device_id, rom_id, file_name, slot, save_id, content_hash, synced_at)
			VALUES ('dev-1', 6, 'Crystal.srm', 'autosave', 75, 'content-1', '2026-01-01T00:00:00Z')`,
	)

	migrateAndCreate(t, db)

	if !hasColumn(t, db, "save_sync_state", "identity_hash") {
		t.Fatal("identity_hash column missing after migration")
	}
	cm := &Manager{db: db, initialized: true, stats: &Stats{}}
	got := cm.GetSaveStates("dev-1")
	if len(got) != 1 {
		t.Fatalf("states = %+v, want the one row kept", got)
	}
	s := got[0]
	if s.RomID != 6 || s.FileName != "Crystal.srm" || s.Slot != "autosave" || s.SaveID != 75 ||
		s.ContentHash != "content-1" || s.IdentityHash != "" {
		t.Errorf("row after migration = %+v", s)
	}

	var version string
	if err := db.QueryRow(`SELECT value FROM cache_metadata WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "15" {
		t.Errorf("schema_version = %s, want 15", version)
	}
}

func TestMigrate_V14WithoutSaveSyncState(t *testing.T) {
	db := openV14DB(t)

	migrateAndCreate(t, db)

	if !hasColumn(t, db, "save_sync_state", "identity_hash") {
		t.Fatal("save_sync_state created without identity_hash")
	}
}

// Running the v15 step on a table that already has the column changes nothing.
func TestAddColumnIfMissing_AlreadyPresent(t *testing.T) {
	db := openV14DB(t, `CREATE TABLE save_sync_state (rom_id INTEGER, identity_hash TEXT)`)
	if err := addColumnIfMissing(db, "save_sync_state", "identity_hash", "TEXT"); err != nil {
		t.Fatal(err)
	}
	if !hasColumn(t, db, "save_sync_state", "identity_hash") {
		t.Fatal("column lost")
	}
}
