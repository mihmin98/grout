package cache

// Kinds of downloaded_files rows. A ROM row unpacked from an archive beside it
// carries that archive's path as its detail.
const (
	DownloadedROM   = "rom"   // a ROM file: the download itself, an unpacked entry, a disc or playlist
	DownloadedArt   = "art"   // an art or media file
	DownloadedMeta  = "meta"  // a metadata file of its own, such as a muOS description text
	DownloadedEntry = "entry" // an entry in a shared gamelist file: Path is the file, Detail the entry's <path>
)

// DownloadedFile is one thing a download created for a ROM. Path is absolute.
type DownloadedFile struct {
	RomID  int
	Kind   string
	Path   string
	Detail string
}

// RecordDownloadedFiles stores rows in one transaction. A row already recorded is kept
// once. Rows of an earlier download of the same ROM stay: its files may still be on the card.
func (cm *Manager) RecordDownloadedFiles(rows []DownloadedFile) error {
	if cm == nil || !cm.initialized {
		return ErrNotInitialized
	}
	if len(rows) == 0 {
		return nil
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	tx, err := cm.db.Begin()
	if err != nil {
		return newCacheError("save", "downloaded_files", "", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO downloaded_files (rom_id, kind, path, detail, recorded_at)
		VALUES (?, ?, ?, ?, ?)
	`)
	if err != nil {
		return newCacheError("save", "downloaded_files", "", err)
	}
	defer stmt.Close()

	now := nowUTC()
	for _, r := range rows {
		if _, err := stmt.Exec(r.RomID, r.Kind, r.Path, r.Detail, now); err != nil {
			return newCacheError("save", "downloaded_files", r.Path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return newCacheError("save", "downloaded_files", "", err)
	}
	return nil
}

// GetDownloadedFiles returns what downloads recorded for a ROM, ordered by kind, path
// and detail. No rows means nothing was recorded (a download from before the record).
func (cm *Manager) GetDownloadedFiles(romID int) ([]DownloadedFile, error) {
	if cm == nil || !cm.initialized {
		return nil, ErrNotInitialized
	}

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	rows, err := cm.db.Query(`
		SELECT rom_id, kind, path, detail FROM downloaded_files
		WHERE rom_id = ?
		ORDER BY kind, path, detail
	`, romID)
	if err != nil {
		return nil, newCacheError("get", "downloaded_files", "", err)
	}
	defer rows.Close()

	var files []DownloadedFile
	for rows.Next() {
		var f DownloadedFile
		if err := rows.Scan(&f.RomID, &f.Kind, &f.Path, &f.Detail); err != nil {
			return nil, newCacheError("get", "downloaded_files", "", err)
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, newCacheError("get", "downloaded_files", "", err)
	}
	return files, nil
}

// GetDownloadedROMs returns every ROM row of the record, for every ROM, ordered by ROM
// and path, so a list of games can be checked against it with one query.
func (cm *Manager) GetDownloadedROMs() ([]DownloadedFile, error) {
	if cm == nil || !cm.initialized {
		return nil, ErrNotInitialized
	}

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	rows, err := cm.db.Query(`
		SELECT rom_id, kind, path, detail FROM downloaded_files
		WHERE kind = ?
		ORDER BY rom_id, path, detail
	`, DownloadedROM)
	if err != nil {
		return nil, newCacheError("get", "downloaded_files", "", err)
	}
	defer rows.Close()

	var files []DownloadedFile
	for rows.Next() {
		var f DownloadedFile
		if err := rows.Scan(&f.RomID, &f.Kind, &f.Path, &f.Detail); err != nil {
			return nil, newCacheError("get", "downloaded_files", "", err)
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, newCacheError("get", "downloaded_files", "", err)
	}
	return files, nil
}

// RomIDsForPath returns, in ascending order, the ROMs whose record lists path (in any
// kind), so a delete can leave alone a file another game also claims.
func (cm *Manager) RomIDsForPath(path string) ([]int, error) {
	if cm == nil || !cm.initialized {
		return nil, ErrNotInitialized
	}

	cm.mu.RLock()
	defer cm.mu.RUnlock()

	rows, err := cm.db.Query(`
		SELECT DISTINCT rom_id FROM downloaded_files
		WHERE path = ?
		ORDER BY rom_id
	`, path)
	if err != nil {
		return nil, newCacheError("get", "downloaded_files", path, err)
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, newCacheError("get", "downloaded_files", path, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, newCacheError("get", "downloaded_files", path, err)
	}
	return ids, nil
}

// ForgetDownloadedFiles removes the given rows of romID's record (matched on kind, path
// and detail; each row's own RomID is ignored). Other rows stay.
func (cm *Manager) ForgetDownloadedFiles(romID int, rows []DownloadedFile) error {
	if cm == nil || !cm.initialized {
		return ErrNotInitialized
	}
	if len(rows) == 0 {
		return nil
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	tx, err := cm.db.Begin()
	if err != nil {
		return newCacheError("delete", "downloaded_files", "", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		DELETE FROM downloaded_files
		WHERE rom_id = ? AND kind = ? AND path = ? AND detail = ?
	`)
	if err != nil {
		return newCacheError("delete", "downloaded_files", "", err)
	}
	defer stmt.Close()

	for _, r := range rows {
		if _, err := stmt.Exec(romID, r.Kind, r.Path, r.Detail); err != nil {
			return newCacheError("delete", "downloaded_files", r.Path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return newCacheError("delete", "downloaded_files", "", err)
	}
	return nil
}
