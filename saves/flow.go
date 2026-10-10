package saves

import (
	"archive/zip"
	"errors"
	"fmt"
	"grout/cache"
	"grout/cfw"
	"grout/files"
	"grout/hashing"
	"grout/pspdb"
	"grout/romm"
	"grout/settings"
	"grout/version"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	gaba "github.com/BrandonKowalski/gabagool/v2/pkg/gabagool"
)

// maxConcurrentRequests bounds the per-ROM save fetches in the discovery fallback.
// Kept low: grout typically talks to a RomM instance on the same LAN (often a Pi/NAS)
// over Wi-Fi, where a burst of parallel requests gives little benefit and can trip
// timeouts or rate limits.
const maxConcurrentRequests = 4

func ResolveSaveSync(client *romm.Client, config *settings.Config, deviceID string) (SyncResult, error) {
	logger := gaba.GetLogger()
	logger.Debug("Starting save sync resolve (negotiate)", "deviceID", deviceID)

	localSaves := ScanSaves(config)
	logger.Debug("Scanned local saves", "count", len(localSaves))

	recordedSlots := loadRecordedSlots(deviceID)
	recordedHashes := loadRecordedHashes(deviceID)
	states := buildClientSaveStates(localSaves, config, recordedSlots, recordedHashes)

	// Diagnostic: log exactly what we send the orchestrator (rom/slot/hash).
	for _, s := range states {
		logger.Debug("Negotiate request save",
			"romID", s.RomID, "file", s.FileName, "slot", s.Slot,
			"emulator", s.Emulator, "hasHash", s.ContentHash != "", "size", s.FileSizeBytes)
	}

	resp, err := client.Negotiate(romm.SyncNegotiatePayload{
		DeviceID: deviceID,
		Saves:    states,
	})
	if err != nil {
		return SyncResult{}, fmt.Errorf("negotiate failed: %w", err)
	}
	logger.Debug("Negotiate response",
		"sessionID", resp.SessionID,
		"uploads", resp.TotalUpload,
		"downloads", resp.TotalDownload,
		"conflicts", resp.TotalConflict,
		"no_ops", resp.TotalNoOp,
		"operations", len(resp.Operations),
	)
	// Diagnostic: log every operation the orchestrator returned (action/slot/reason).
	for _, op := range resp.Operations {
		saveID := 0
		if op.SaveID != nil {
			saveID = *op.SaveID
		}
		slot := ""
		if op.Slot != nil {
			slot = *op.Slot
		}
		logger.Debug("Negotiate op",
			"action", op.Action, "romID", op.RomID, "saveID", saveID,
			"file", op.FileName, "slot", slot, "reason", op.Reason)
	}

	scan := cfw.ScanRoms(*config)
	resolvedRoms := ResolveLocalRoms(scan)
	cm := cache.GetCacheManager()

	items := mapOperationsToItems(resp.Operations, localSaves, resolvedRoms, cm, config, recordedSlots, recordedHashes)

	// Discovery fallback: the orchestrator only volunteers downloads for non-null-slot
	// saves the device hasn't already synced, and never surfaces null-slot ("archival" /
	// web-UI) saves at all. So for locally-present ROMs that have no local save and no
	// negotiate op, query the server directly and pull the best server save. Discovery
	// only runs when there is no local file, so it safely restores saves after an SD
	// reflash / fresh install (persistent device_id, lost local files).
	discovered := discoverRemoteOnlySaves(client, config, deviceID, localSaves, items, resolvedRoms)
	if len(discovered) > 0 {
		logger.Debug("Discovery fallback found remote-only saves", "count", len(discovered))
		items = append(items, discovered...)
	}

	logger.Debug("Total sync items resolved", "count", len(items))

	return SyncResult{Items: items, SessionID: resp.SessionID}, nil
}

// buildDiscoveryItems turns server saves for uncovered ROMs into download items.
// Discovery only runs for ROMs that have NO local save, so there is nothing to
// clobber: we pull the best server save regardless of this device's prior sync
// state. This restores saves after an SD reflash / fresh install (where the
// device_id persists but local files are gone), which the orchestrator's
// deletion-propagation model would otherwise suppress. Null-slot ("archival" /
// web-UI) saves are included, since negotiate never surfaces them. Pure function:
// the caller supplies the fetched saves keyed by ROM ID.
func buildDiscoveryItems(uncovered map[int]cfw.LocalRomFile, savesByRom map[int][]romm.Save, config *settings.Config) []SyncItem {
	logger := gaba.GetLogger()
	var items []SyncItem

	for romID, rom := range uncovered {
		saves := savesByRom[romID]
		if len(saves) == 0 {
			continue
		}

		preferredSlot := settings.DefaultSaveSlot
		if config != nil {
			preferredSlot = config.GetSlotPreference(romID)
		}
		best := SelectSaveForSlot(saves, preferredSlot)
		if best == nil {
			continue
		}

		ls := LocalSave{
			RomID:       romID,
			RomName:     rom.RomName,
			FSSlug:      rom.FSSlug,
			RomFileName: rom.FileName,
			EmulatorDir: resolveDiscoveredSaveDirectory(rom, config),
		}
		if IsDirectorySavePlatform(ls.FSSlug) {
			ls.IsDirectorySave = true
		}

		logger.Debug("Discovery: remote-only save for ROM without local save",
			"romID", romID, "romName", rom.RomName, "saveID", best.ID,
			"file", best.FileName, "fetched", len(saves))

		item := SyncItem{
			LocalSave:  ls,
			RemoteSave: best,
			TargetSlot: preferredSlot,
			Action:     ActionDownload,
		}
		// First-time multi-slot pull: offer the slot choice to the UI. Discovery only
		// runs for ROMs with no local save, so every multi-slot case is first-time.
		if slots := distinctSaveSlots(saves); len(slots) > 1 {
			item.AvailableSlots = slots
			item.AllRemoteSaves = saves
		}

		items = append(items, item)
	}

	return items
}

// distinctSaveSlots returns the sorted distinct slot names across saves (nil/empty slot
// counts as "autosave").
func distinctSaveSlots(saves []romm.Save) []string {
	set := make(map[string]bool)
	for _, s := range saves {
		set[SlotName(s.Slot)] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// distinctOpSlots returns the sorted distinct slot names across download ops (nil/empty
// slot counts as "autosave").
func distinctOpSlots(ops []romm.SyncOperationSchema) []string {
	set := make(map[string]bool)
	for _, op := range ops {
		set[SlotName(op.Slot)] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// opStubsToSaves builds romm.Save stubs from download ops for slot re-selection by the
// multi-slot picker.
func opStubsToSaves(ops []romm.SyncOperationSchema) []romm.Save {
	saves := make([]romm.Save, 0, len(ops))
	for _, op := range ops {
		if stub := buildRemoteSaveStub(op); stub != nil {
			saves = append(saves, *stub)
		}
	}
	return saves
}

// discoverRemoteOnlySaves finds locally-present ROMs that have no local save and were
// not covered by a negotiate operation, fetches their server saves, and builds download
// items for any save this device has never synced.
func discoverRemoteOnlySaves(client *romm.Client, config *settings.Config, deviceID string, localSaves []LocalSave, items []SyncItem, resolvedRoms map[int]cfw.LocalRomFile) []SyncItem {
	logger := gaba.GetLogger()

	covered := make(map[int]bool, len(localSaves)+len(items))
	for _, ls := range localSaves {
		covered[ls.RomID] = true
	}
	for _, it := range items {
		covered[it.LocalSave.RomID] = true
	}

	uncovered := make(map[int]cfw.LocalRomFile)
	for romID, rom := range resolvedRoms {
		if !covered[romID] {
			uncovered[romID] = rom
		}
	}
	if len(uncovered) == 0 {
		return nil
	}

	logger.Debug("Discovery: checking remote saves for ROMs without local saves", "count", len(uncovered))

	savesByRom := fetchSavesForRoms(client, deviceID, uncovered)
	return buildDiscoveryItems(uncovered, savesByRom, config)
}

// fetchSavesForRoms asks for saves scoped to the uncovered ROMs. If that request
// fails, or no device ID is available, it uses bounded per-ROM concurrency.
func fetchSavesForRoms(client *romm.Client, deviceID string, uncovered map[int]cfw.LocalRomFile) map[int][]romm.Save {
	logger := gaba.GetLogger()

	if deviceID != "" {
		romIDs := make([]int, 0, len(uncovered))
		for romID := range uncovered {
			romIDs = append(romIDs, romID)
		}
		bulkSaves, err := client.GetSavesForROMIDs(romm.SaveQuery{DeviceID: deviceID}, romIDs)
		if err == nil {
			out := make(map[int][]romm.Save)
			for _, save := range bulkSaves {
				out[save.RomID] = append(out[save.RomID], save)
			}
			logger.Debug("Discovery: fetched saves in bulk", "roms", len(romIDs), "records", len(bulkSaves))
			return out
		}
		logger.Warn("Discovery: device-scoped bulk save fetch failed; using per-ROM fallback",
			"fallbackRequests", len(uncovered),
			"error", err)
	} else {
		logger.Warn("Discovery: device ID empty; using per-ROM fallback", "requests", len(uncovered))
	}

	return fetchSavesForRomsFallback(client, deviceID, uncovered)
}

// fetchSavesForRomsFallback queries each ROM with bounded concurrency. ROMs whose
// fetch errors are logged and omitted.
func fetchSavesForRomsFallback(client *romm.Client, deviceID string, uncovered map[int]cfw.LocalRomFile) map[int][]romm.Save {
	logger := gaba.GetLogger()

	type result struct {
		romID int
		saves []romm.Save
		err   error
	}

	workerCount := len(uncovered)
	if workerCount > maxConcurrentRequests {
		workerCount = maxConcurrentRequests
	}
	if workerCount == 0 {
		return map[int][]romm.Save{}
	}
	jobs := make(chan int, workerCount)
	results := make(chan result, workerCount)
	var wg sync.WaitGroup

	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				saves, err := client.GetSavesForROMIDs(romm.SaveQuery{RomID: id, DeviceID: deviceID}, []int{id})
				results <- result{romID: id, saves: saves, err: err}
			}
		}()
	}

	go func() {
		for romID := range uncovered {
			jobs <- romID
		}
		close(jobs)
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	out := make(map[int][]romm.Save, len(uncovered))
	for r := range results {
		if r.err != nil {
			logger.Warn("Discovery: failed to fetch saves for ROM", "romID", r.romID, "error", r.err)
			continue
		}
		if len(r.saves) > 0 {
			out[r.romID] = r.saves
		}
	}
	return out
}

// mapOperationsToItems converts negotiate operations into executable SyncItems,
// dropping no_op. Order is preserved. Downloads with no local file are resolved
// from the local ROM scan / cache for path determination.
func mapOperationsToItems(
	ops []romm.SyncOperationSchema,
	localSaves []LocalSave,
	resolvedRoms map[int]cfw.LocalRomFile,
	cm *cache.Manager,
	config *settings.Config,
	recordedSlots map[saveKey]string,
	recordedHashes map[saveKey]recordedHash,
) []SyncItem {
	logger := gaba.GetLogger()

	type romSlotKey struct {
		romID int
		slot  string
	}
	// byRomSlot indexes local saves by (rom_id, reported slot), the same key the RomM
	// orchestrator and Argosy pair on. We must NOT match upload/conflict ops by filename:
	// the server datetime-tags slot saves (e.g. "Name [2026-06-09_14-49-22].srm") while
	// grout's local file keeps the plain name, so a filename match misses every time.
	byRomSlot := make(map[romSlotKey]LocalSave, len(localSaves))
	// localByRom holds one local save per ROM. grout manages a single save (one slot)
	// per ROM, so a ROM that already has a local save only ever syncs that save's slot.
	localByRom := make(map[int]LocalSave, len(localSaves))
	for _, ls := range localSaves {
		rsk := romSlotKey{ls.RomID, resolveReportedSlot(ls, config, recordedSlots)}
		if _, ok := byRomSlot[rsk]; !ok {
			byRomSlot[rsk] = ls
		}
		if _, ok := localByRom[ls.RomID]; !ok {
			localByRom[ls.RomID] = ls
		}
	}

	// A ROM is "installed" if it has a local save or a local ROM file. Downloads are
	// gated on this: negotiate enumerates the whole RomM library and offers downloads
	// for every never-synced save, but grout must only pull saves for games actually
	// present on this device.
	installed := make(map[int]bool, len(localSaves)+len(resolvedRoms))
	for _, ls := range localSaves {
		installed[ls.RomID] = true
	}
	for romID := range resolvedRoms {
		installed[romID] = true
	}

	items := make([]SyncItem, 0, len(ops))
	// Download ops are collected per ROM and resolved to a single item below, so a ROM
	// with saves in multiple slots doesn't download several files to the same path.
	downloadOps := make(map[int][]romm.SyncOperationSchema)

	for _, op := range ops {
		switch op.Action {
		case "upload":
			// Match by (rom_id, slot), the orchestrator's pairing key, not by filename,
			// which diverges once the server datetime-tags the stored save.
			slot := reportedOpSlot(op)
			ls, ok := byRomSlot[romSlotKey{op.RomID, slot}]
			if !ok {
				logger.Warn("Negotiate upload op has no local save in the reported slot",
					"romID", op.RomID, "slot", slot, "file", op.FileName)
				continue
			}
			// Suppress a redundant promotion-upload: when the local save's content is
			// byte-identical to what we previously downloaded from the server (recorded
			// hash matches), the content is already on the server, just under a slot
			// negotiate doesn't pair on (e.g. a null-slot "archival" save). Re-uploading
			// it now would only duplicate it (the "uploads everything I just downloaded"
			// churn). It uploads later once the user actually changes the save and the
			// hash diverges. A clock-only change still counts as unchanged (reportedHash).
			if rec, ok := recordedHashes[saveKey{ls.RomID, ls.FileName}]; ok && rec.Content != "" {
				if localHash, err := reportedHash(ls, rec); err == nil && localHash == rec.Content {
					logger.Debug("Skipping upload: content unchanged since download (already on server)",
						"romID", op.RomID, "slot", slot, "file", ls.FileName)
					continue
				}
			}
			items = append(items, SyncItem{
				LocalSave:  ls,
				RemoteSave: buildRemoteSaveStub(op),
				TargetSlot: slot,
				Action:     ActionUpload,
			})

		case "download":
			if !installed[op.RomID] {
				logger.Debug("Skipping download: ROM not downloaded locally", "romID", op.RomID, "file", op.FileName)
				continue
			}
			if buildRemoteSaveStub(op) == nil {
				logger.Error("Negotiate download op missing save identity (no save_id/server_updated_at)", "romID", op.RomID, "file", op.FileName)
				continue
			}
			// grout keeps one save (one slot) per ROM. If the ROM already has a local
			// save, only its own slot is managed, so a download for any other slot is the
			// orchestrator offering an alternate-slot save we don't use here, and pulling
			// it would clobber the local save and flip-flop on every sync. Skip it.
			if ls, ok := localByRom[op.RomID]; ok {
				managedSlot := resolveReportedSlot(ls, config, recordedSlots)
				opSlot := reportedOpSlot(op)
				if opSlot != managedSlot {
					logger.Debug("Skipping download: ROM's local save is in a different slot",
						"romID", op.RomID, "opSlot", opSlot, "managedSlot", managedSlot, "file", op.FileName)
					continue
				}
			}
			downloadOps[op.RomID] = append(downloadOps[op.RomID], op)

		case "conflict":
			stub := buildRemoteSaveStub(op)
			if stub == nil {
				logger.Error("Negotiate conflict op missing save identity (no save_id/server_updated_at)", "romID", op.RomID, "file", op.FileName)
				continue
			}
			slot := reportedOpSlot(op)
			ls, ok := byRomSlot[romSlotKey{op.RomID, slot}]
			if !ok {
				logger.Warn("Negotiate conflict op has no local save in the reported slot",
					"romID", op.RomID, "slot", slot, "file", op.FileName)
				continue
			}
			items = append(items, SyncItem{
				LocalSave:  ls,
				RemoteSave: stub,
				TargetSlot: slot,
				Action:     ActionConflict,
			})

		case "no_op":
			// nothing to do
		default:
			logger.Warn("Unknown negotiate action", "action", op.Action, "romID", op.RomID)
		}
	}

	// Resolve each installed ROM's download ops to a single item, preferring the slot
	// the ROM is reported under (explicit pref / autosave), else the latest save.
	for romID, dops := range downloadOps {
		preferred := settings.DefaultSaveSlot
		if config != nil {
			preferred = config.GetSlotPreference(romID)
		}
		op := pickDownloadOp(dops, preferred)

		// The server datetime-tags stored save filenames, so a download operation's
		// filename normally differs from the emulator's plain local filename. The slot
		// was already checked above; keep the existing local save so its exact path and
		// emulator directory remain authoritative.
		ls, ok := localByRom[op.RomID]
		if !ok {
			ls = resolveLocalSaveForDownload(op, resolvedRoms, cm)
		}
		item := SyncItem{
			LocalSave:  ls,
			RemoteSave: buildRemoteSaveStub(op),
			TargetSlot: preferred,
			Action:     ActionDownload,
		}

		// First-time multi-slot pull: if this ROM has no local save yet and the server
		// offers it in more than one slot, surface the choice to the UI instead of
		// silently picking. (A ROM that already has a local save was filtered to its
		// managed slot above, so it never reaches here multi-slot.)
		if _, hasLocal := localByRom[romID]; !hasLocal {
			if slots := distinctOpSlots(dops); len(slots) > 1 {
				item.AvailableSlots = slots
				item.AllRemoteSaves = opStubsToSaves(dops)
			}
		}

		items = append(items, item)
	}

	return items
}

// reportedOpSlot returns the slot a negotiate op is paired on, treating a nil/empty slot
// as the canonical "autosave" default (the same slot grout reports its saves under).
func reportedOpSlot(op romm.SyncOperationSchema) string {
	return SlotName(op.Slot)
}

// pickDownloadOp chooses one download op for a ROM that has saves in possibly
// multiple slots: the op matching preferredSlot if present, otherwise the one with
// the latest server_updated_at.
func pickDownloadOp(ops []romm.SyncOperationSchema, preferredSlot string) romm.SyncOperationSchema {
	for _, op := range ops {
		if SlotName(op.Slot) == preferredSlot {
			return op
		}
	}
	best := ops[0]
	for _, op := range ops[1:] {
		if op.ServerUpdatedAt == nil {
			continue
		}
		if best.ServerUpdatedAt == nil || op.ServerUpdatedAt.After(*best.ServerUpdatedAt) {
			best = op
		}
	}
	return best
}

// buildRemoteSaveStub builds a *romm.Save from a negotiate operation for execution.
func buildRemoteSaveStub(op romm.SyncOperationSchema) *romm.Save {
	if op.SaveID == nil && op.ServerUpdatedAt == nil {
		return nil
	}
	save := &romm.Save{
		RomID:    op.RomID,
		FileName: op.FileName,
		Emulator: op.Emulator,
	}
	if op.SaveID != nil {
		save.ID = *op.SaveID
	}
	if op.Slot != nil {
		save.Slot = op.Slot
	}
	if op.ServerUpdatedAt != nil {
		save.UpdatedAt = *op.ServerUpdatedAt
	}
	if ext := filepath.Ext(op.FileName); ext != "" {
		save.FileExtension = strings.TrimPrefix(ext, ".")
	}
	return save
}

// resolveLocalSaveForDownload builds a LocalSave for a download whose file does
// not exist locally yet, resolving ROM metadata for path determination.
func resolveLocalSaveForDownload(op romm.SyncOperationSchema, resolvedRoms map[int]cfw.LocalRomFile, cm *cache.Manager) LocalSave {
	ls := LocalSave{RomID: op.RomID, FileName: op.FileName}

	if rom, ok := resolvedRoms[op.RomID]; ok {
		ls.RomName = rom.RomName
		ls.FSSlug = rom.FSSlug
		ls.RomFileName = rom.FileName
	} else if cm != nil {
		if roms, err := cm.GetGamesByIDs([]int{op.RomID}); err == nil && len(roms) > 0 {
			ls.RomName = roms[0].Name
			ls.FSSlug = roms[0].PlatformFSSlug
		}
	}

	if IsDirectorySavePlatform(ls.FSSlug) {
		ls.IsDirectorySave = true
	}
	return ls
}

func ExecuteSaveSync(client *romm.Client, config *settings.Config, deviceID string, items []SyncItem, sessionID int, progressFn func(current, total int)) SyncReport {
	report := ExecuteActions(client, config, deviceID, items, progressFn)

	cm := cache.GetCacheManager()
	if cm != nil {
		for _, item := range report.Items {
			if item.Action == ActionSkip || item.Action == ActionConflict || !item.Success {
				continue
			}
			fileName := item.LocalSave.FileName
			if fileName == "" && item.RemoteSave != nil {
				fileName = item.RemoteSave.FileName
			}
			record := cache.SaveSyncRecord{
				RomID:    item.LocalSave.RomID,
				RomName:  item.LocalSave.RomName,
				Action:   item.Action.String(),
				DeviceID: deviceID,
				FileName: fileName,
			}
			if item.RemoteSave != nil {
				record.SaveID = item.RemoteSave.ID
			}
			cm.RecordSaveSync(record)
		}
	}

	if sessionID > 0 {
		if err := client.CompleteSession(sessionID, romm.SyncCompletePayload{
			OperationsCompleted: report.Uploaded + report.Downloaded,
			// Count runtime conflicts (e.g. a 409 that turned an upload into a conflict)
			// as failed so the server's session totals reconcile with operations_planned.
			OperationsFailed: report.Errors + report.Conflicts,
		}); err != nil {
			// On-demand client has no retry queue; the server expires stale sessions.
			gaba.GetLogger().Warn("Failed to complete sync session (leaving for server to expire)", "sessionID", sessionID, "error", err)
		}
	}

	return report
}

func RegisterDevice(client *romm.Client, name string) (romm.Device, error) {
	dev, err := client.RegisterDevice(romm.RegisterDeviceRequest{
		Name:          name,
		Platform:      string(cfw.GetCFW()),
		Client:        "grout",
		ClientVersion: version.Get().Version,
		SyncMode:      "api",
	})
	if err != nil {
		return dev, err
	}
	// The server returns an existing matching device without updating it (allow_existing),
	// so refresh client_version to keep the server's record current after an upgrade.
	if dev.ID != "" {
		if _, uerr := client.UpdateDevice(dev.ID, romm.UpdateDeviceRequest{
			ClientVersion: version.Get().Version,
		}); uerr != nil {
			gaba.GetLogger().Warn("Failed to refresh device client_version", "deviceID", dev.ID, "error", uerr)
		}
	}
	return dev, nil
}

// RefreshDeviceVersion updates the server's record of this device's client_version when
// the running grout version differs from lastReported (i.e. the app was upgraded since
// the version was last sent). Returns the version now reported and whether an update was
// sent. Best-effort: a failure is logged and leaves lastReported unchanged.
func RefreshDeviceVersion(client *romm.Client, deviceID, lastReported string) (string, bool) {
	current := version.Get().Version
	if deviceID == "" || current == "" || current == lastReported {
		return lastReported, false
	}
	if _, err := client.UpdateDevice(deviceID, romm.UpdateDeviceRequest{ClientVersion: current}); err != nil {
		gaba.GetLogger().Warn("Failed to refresh device client_version on upgrade",
			"deviceID", deviceID, "from", lastReported, "to", current, "error", err)
		return lastReported, false
	}
	gaba.GetLogger().Debug("Refreshed device client_version after upgrade",
		"deviceID", deviceID, "from", lastReported, "to", current)
	return current, true
}

// saveLookupKeys returns the ROM expected-basename candidates a scanned save file (its
// name with the save extension already stripped) could correspond to. RetroArch-based
// CFWs name a save <rombase>.<saveExt>, so nameNoExt already IS the ROM basename. The
// minarch CFWs (NextUI, MinUI) name it <rombase>.<romExt>.<saveExt>, so after stripping
// the save extension a ROM extension is still attached; stripping that recovers the ROM
// basename (issue #245). The extra candidate is only added when the trailing extension
// looks like a real ROM extension, so a dotted title token (e.g. "(V1.1)") is left alone.
func saveLookupKeys(nameNoExt string) []string {
	keys := []string{nameNoExt}
	if ext := filepath.Ext(nameNoExt); isLikelyRomExt(ext) {
		keys = append(keys, strings.TrimSuffix(nameNoExt, ext))
	}
	return keys
}

// isLikelyRomExt reports whether ext (including the leading dot) looks like a ROM file
// extension: a short, purely alphanumeric suffix such as ".sfc", ".gbc", or ".7z". This
// excludes punctuation-bearing fragments like ".1)" that filepath.Ext can yield from a
// version token in a title, so they are never mistaken for an extension to strip.
func isLikelyRomExt(ext string) bool {
	if len(ext) < 2 || len(ext) > 5 || ext[0] != '.' {
		return false
	}
	for _, r := range ext[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// downloadSaveFileName computes the on-disk filename a downloaded save should be written
// under when the ROM has no existing local save. It pairs the save basename for the ROM
// (keepRomExt retains the ROM extension, minarch-style; otherwise it is stripped,
// RetroArch-style, issue #245) with the server save's extension, falling back to the
// server's filename when the ROM filename is unknown.
func downloadSaveFileName(romFileName, serverFileName, serverExt string, keepRomExt bool) string {
	if romFileName == "" {
		return serverFileName
	}
	return cfw.SaveBasename(keepRomExt, romFileName) + "." + serverExt
}

// downloadSaveName is the name a downloaded save is written under. A firmware
// that says how its emulator names saves, like NextUI's Save format setting,
// decides both the style and the extension. Elsewhere the style is read off the
// saves already in saveDir and the server's extension is kept.
func downloadSaveName(romFileName, serverFileName, serverExt, saveDir string) string {
	if keep, ext, ok := cfw.ActiveFirmware().SaveNaming(); ok {
		return downloadSaveFileName(romFileName, serverFileName, ext, keep)
	}
	return downloadSaveFileName(romFileName, serverFileName, serverExt, detectSaveNameStyle(saveDir))
}

// saveDirKeepsRomExt infers whether the emulator that owns saveDir names saves after the
// full ROM filename (keep) or the bare ROM basename (strip), by inspecting existing save
// filenames: a save whose name (minus its save extension) still ends in a ROM-looking
// extension was written minarch-style. Returns known=false when the directory holds no
// save files to infer from, so the caller can fall back to the CFW default. NextUI exposes
// both styles as a setting, so the convention must be detected per-device (issue #245).
func saveDirKeepsRomExt(saveFileNames []string) (keep bool, known bool) {
	for _, name := range saveFileNames {
		if !ValidSaveExtensions[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		known = true
		nameNoExt := strings.TrimSuffix(name, filepath.Ext(name))
		if isLikelyRomExt(filepath.Ext(nameNoExt)) {
			return true, true
		}
	}
	return false, known
}

// detectSaveNameStyle reads saveDir and infers whether saves there keep the ROM extension,
// falling back to the CFW default when the directory has no saves to learn from.
func detectSaveNameStyle(saveDir string) bool {
	keep := cfw.DefaultKeepsRomExt(cfw.GetCFW())
	entries, err := os.ReadDir(saveDir)
	if err != nil {
		return keep
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if k, known := saveDirKeepsRomExt(names); known {
		return k
	}
	return keep
}

func ScanSaves(config *settings.Config) []LocalSave {
	logger := gaba.GetLogger()
	currentCFW := cfw.GetCFW()

	baseSavePath := cfw.BaseSavePath()
	if baseSavePath == "" {
		logger.Error("No save path for current CFW")
		return nil
	}

	emulatorMap := cfw.EmulatorFolderMap(currentCFW)
	if emulatorMap == nil {
		logger.Error("No emulator folder map for current CFW")
		return nil
	}

	cm := cache.GetCacheManager()
	if cm == nil {
		logger.Error("Cache manager not available for save scan")
		return nil
	}

	var saves []LocalSave

	// Folders that depend on the rom folder, like muOS Pickles', only exist
	// for mapped platforms.
	if config != nil {
		emulatorMap = withMappedSaveFolders(emulatorMap, *config)
	}

	logger.Debug("Starting save scan", "baseSavePath", baseSavePath, "platformCount", len(emulatorMap))

	for fsSlug, emulatorDirs := range emulatorMap {
		rommFSSlug := fsSlug
		if config != nil {
			rommFSSlug = config.ResolveRommFSSlug(fsSlug)
		}

		for _, emuDir := range emulatorDirs {
			saveDir := filepath.Join(baseSavePath, emuDir)
			logger.Debug("Checking save directory", "path", saveDir, "fsSlug", rommFSSlug)

			if _, err := os.Stat(saveDir); os.IsNotExist(err) {
				continue
			}

			entries, err := os.ReadDir(saveDir)
			if err != nil {
				logger.Error("Could not read save directory", "path", saveDir, "error", err)
				continue
			}

			if IsDirectorySavePlatform(fsSlug) {
				// Directory-based saves (e.g., PPSSPP): group all directories that
				// share the same Game ID and title into a single LocalSave, so that
				// DATA00/DATA01/INSDIR etc. are synced together as one zip.
				type pspGroup struct {
					title string
					dirs  []string
				}
				groups := make(map[string]*pspGroup)

				for _, entry := range entries {
					if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
						continue
					}
					gameID := extractPSPGameID(entry.Name())
					dirPath := filepath.Join(saveDir, entry.Name())

					if _, ok := groups[gameID]; !ok {
						groups[gameID] = &pspGroup{}
					}
					groups[gameID].dirs = append(groups[gameID].dirs, dirPath)

					if groups[gameID].title == "" {
						if title, ok := ReadPSPSaveTitle(dirPath); ok {
							groups[gameID].title = title
						}
					}
				}

				for gameID, group := range groups {
					// Normalize the Game ID to match pspdb keys (no hyphens or spaces)
					cleanGameID := strings.NewReplacer("-", "", " ", "").Replace(gameID)

					// Prefer the canonical title from pspdb, fall back to PARAM.SFO
					title, inDB := pspdb.Titles[cleanGameID]
					if !inDB {
						if group.title != "" {
							title = group.title
							logger.Debug("PSP game ID not in pspdb, using PARAM.SFO title", "gameID", gameID, "title", title)
						} else {
							logger.Debug("No title found for PSP game ID, skipping", "gameID", gameID, "fsSlug", rommFSSlug)
							continue
						}
					}

					rom, err := cm.GetRomByNameLookup(rommFSSlug, title)
					if err != nil {
						logger.Debug("No cache match for PSP save group", "gameID", gameID, "title", title, "inDB", inDB, "fsSlug", rommFSSlug)
						continue
					}

					sort.Strings(group.dirs)

					logger.Debug("Matched PSP save group to ROM", "gameID", gameID, "title", group.title, "dirCount", len(group.dirs), "romID", rom.ID, "romName", rom.Name)

					saves = append(saves, LocalSave{
						RomID:           rom.ID,
						RomName:         rom.Name,
						FSSlug:          rommFSSlug,
						FileName:        gameID + ".zip",
						FilePath:        group.dirs[0],
						EmulatorDir:     emuDir,
						IsDirectorySave: true,
						GameID:          gameID,
						RelatedDirs:     group.dirs,
					})
				}
			} else {
				saves = append(saves, scanFileSaves(entries, saveDir, emuDir, rommFSSlug,
					cm.GetRomByFSLookup, cfw.ActiveFirmware().PairsClockFiles())...)
			}
		}
	}

	logger.Debug("Completed save scan", "matched", len(saves))
	return saves
}

// scanFileSaves matches the save files in one folder to ROMs. lookup resolves a
// save's name key to a ROM (cm.GetRomByFSLookup on the device). With pairClocks,
// each matched save carries the folder's files that belong with it (Companions).
func scanFileSaves(entries []os.DirEntry, saveDir, emuDir, fsSlug string,
	lookup func(fsSlug, key string) (romm.Rom, error), pairClocks bool) []LocalSave {
	logger := gaba.GetLogger()

	var names []string
	if pairClocks {
		for _, entry := range entries {
			if !entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
	}

	var saves []LocalSave
	saveFileCount := 0
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !ValidSaveExtensions[ext] {
			continue
		}

		saveFileCount++
		nameNoExt := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))

		var rom romm.Rom
		var matched bool
		for _, key := range saveLookupKeys(nameNoExt) {
			if r, err := lookup(fsSlug, key); err == nil {
				rom, matched = r, true
				break
			}
		}
		if !matched {
			logger.Debug("No cache match for save file", "file", entry.Name(), "fsSlug", fsSlug, "nameNoExt", nameNoExt)
			continue
		}

		path := filepath.Join(saveDir, entry.Name())
		var companions []string
		if pairClocks {
			companions = findCompanions(path, names)
		}

		logger.Debug("Matched save to ROM", "file", entry.Name(), "romID", rom.ID, "romName", rom.Name, "companions", len(companions))

		saves = append(saves, LocalSave{
			RomID:       rom.ID,
			RomName:     rom.Name,
			FSSlug:      fsSlug,
			FileName:    entry.Name(),
			FilePath:    path,
			EmulatorDir: emuDir,
			Companions:  companions,
		})
	}

	if saveFileCount > 0 {
		logger.Debug("Scanned emulator directory", "path", saveDir, "saveFiles", saveFileCount)
	}
	return saves
}

// buildClientSaveStates converts scanned local saves into the negotiate payload,
// computing a content hash per save (composite for directory saves, MD5 for files)
// and the slot from the user's per-ROM preference (default "autosave").
// saveKey identifies a local save for save-state record lookups.
type saveKey struct {
	romID    int
	fileName string
}

// loadRecordedSlots reads the persisted save-sync state for a device into a slot
// lookup keyed by (rom_id, file_name).
func loadRecordedSlots(deviceID string) map[saveKey]string {
	cm := cache.GetCacheManager()
	if cm == nil {
		return nil
	}
	states := cm.GetSaveStates(deviceID)
	if len(states) == 0 {
		return nil
	}
	out := make(map[saveKey]string, len(states))
	for _, s := range states {
		out[saveKey{s.RomID, s.FileName}] = s.Slot
	}
	return out
}

// recordedHash is what was recorded for a save at its last sync: the server's
// content hash and the save's identity hash (its hash ignoring clock files).
type recordedHash struct {
	Content  string
	Identity string
}

// loadRecordedHashes reads the persisted save-sync state for a device into a
// hash lookup keyed by (rom_id, file_name). Used to suppress re-uploading a
// downloaded save whose content the user hasn't changed (it's already on the server).
func loadRecordedHashes(deviceID string) map[saveKey]recordedHash {
	cm := cache.GetCacheManager()
	if cm == nil {
		return nil
	}
	states := cm.GetSaveStates(deviceID)
	if len(states) == 0 {
		return nil
	}
	out := make(map[saveKey]recordedHash, len(states))
	for _, s := range states {
		out[saveKey{s.RomID, s.FileName}] = recordedHash{Content: s.ContentHash, Identity: s.IdentityHash}
	}
	return out
}

// recordSaveState upserts the current synced state for a local save after a
// successful upload or download, so subsequent syncs report the same slot/identity.
func recordSaveState(deviceID string, romID int, fileName, slot string, saveID int, contentHash, identityHash string) {
	cm := cache.GetCacheManager()
	if cm == nil || fileName == "" {
		return
	}
	if err := cm.UpsertSaveState(deviceID, cache.SaveSyncState{
		RomID:        romID,
		FileName:     fileName,
		Slot:         slot,
		SaveID:       saveID,
		ContentHash:  contentHash,
		IdentityHash: identityHash,
	}); err != nil {
		gaba.GetLogger().Warn("Failed to record save state", "romID", romID, "file", fileName, "error", err)
	}
}

// recordedDownloadFileName returns the on-disk basename the next ScanSaves will find
// a freshly downloaded save under, so its save-state record (keyed on file_name) can
// be looked up again. File saves are written under the plain basename of savePath; the
// negotiate op's server filename is datetime-tagged and must NOT be used or the record
// is never found, dropping the save back to the "autosave" fallback slot and triggering
// a spurious re-upload. Directory (PSP) saves are scanned as gameID + ".zip".
func recordedDownloadFileName(item SyncItem, savePath string) string {
	if item.LocalSave.IsDirectorySave {
		if item.LocalSave.GameID != "" {
			return item.LocalSave.GameID + ".zip"
		}
		return item.LocalSave.FileName
	}
	return filepath.Base(savePath)
}

// resolveReportedSlot determines which slot a local save is reported under during
// negotiate. Precedence: explicit user preference > recorded last-synced slot >
// "autosave" default. This gives downloaded saves a stable slot identity so they are
// not spuriously re-uploaded to a different slot on the next sync.
func resolveReportedSlot(ls LocalSave, config *settings.Config, recordedSlots map[saveKey]string) string {
	slot := settings.DefaultSaveSlot
	if rec, ok := recordedSlots[saveKey{ls.RomID, ls.FileName}]; ok && rec != "" {
		slot = rec
	}
	if config != nil {
		if pref, ok := config.SlotPreferenceExplicit(ls.RomID); ok {
			slot = pref
		}
	}
	return slot
}

func buildClientSaveStates(localSaves []LocalSave, config *settings.Config, recordedSlots map[saveKey]string, recordedHashes map[saveKey]recordedHash) []romm.ClientSaveState {
	logger := gaba.GetLogger()
	states := make([]romm.ClientSaveState, 0, len(localSaves))

	for _, ls := range localSaves {
		slot := resolveReportedSlot(ls, config, recordedSlots)

		emulator := filepath.Base(ls.EmulatorDir)
		if emulator == "." || emulator == "" {
			emulator = ""
		} else if emulator == "SAVEDATA" {
			emulator = "PPSSPP"
		}

		var updatedAt time.Time
		var size int64
		var hash string

		if ls.IsDirectorySave {
			dirs := ls.RelatedDirs
			if len(dirs) == 0 {
				dirs = []string{ls.FilePath}
			}
			// One walk yields hash + newest mtime + size for the whole directory save.
			stat, err := hashing.ComputeDirsCompositeHashStat(dirs)
			if err != nil {
				logger.Warn("Failed to hash directory save; skipping from negotiate", "romID", ls.RomID, "path", ls.FilePath, "error", err)
				continue
			}
			updatedAt, size, hash = stat.Newest, stat.Size, stat.Hash
		} else {
			info, err := os.Stat(ls.FilePath)
			if err != nil {
				logger.Warn("Cannot stat local save, skipping from negotiate", "path", ls.FilePath, "error", err)
				continue
			}
			// A clock-only change must not look newer: the primary's mtime stands for
			// the whole save, while the size counts every file.
			updatedAt = info.ModTime().Truncate(time.Second)
			size = info.Size()
			companionSize, serr := filesSize(ls.Companions)
			if serr != nil {
				logger.Warn("Cannot stat save companion, skipping from negotiate", "path", ls.FilePath, "error", serr)
				continue
			}
			size += companionSize
			h, herr := reportedHash(ls, recordedHashes[saveKey{ls.RomID, ls.FileName}])
			if herr != nil {
				logger.Warn("Failed to hash local save; skipping from negotiate", "romID", ls.RomID, "path", ls.FilePath, "error", herr)
				continue
			}
			hash = h
		}

		states = append(states, romm.ClientSaveState{
			RomID:         ls.RomID,
			FileName:      ls.FileName,
			Slot:          slot,
			Emulator:      emulator,
			UpdatedAt:     updatedAt,
			FileSizeBytes: size,
			ContentHash:   hash,
		})
	}

	return states
}

// saveHashes returns a local save's content hash (what the server would compute
// for its upload) and its identity hash (the same save ignoring clock files).
// They differ only for a save with companions: content is then the composite
// hash of the flat zip it uploads as, identity the primary file's MD5.
func saveHashes(ls LocalSave) (content, identity string, err error) {
	if ls.IsDirectorySave {
		dirs := ls.RelatedDirs
		if len(dirs) == 0 {
			dirs = []string{ls.FilePath}
		}
		h, err := hashing.ComputeDirsCompositeHash(dirs)
		return h, h, err
	}
	identity, err = hashing.ComputeMD5(ls.FilePath)
	if err != nil {
		return "", "", err
	}
	if len(ls.Companions) == 0 {
		return identity, identity, nil
	}
	content, err = hashing.ComputeFilesCompositeHash(append([]string{ls.FilePath}, ls.Companions...))
	if err != nil {
		return "", "", err
	}
	return content, identity, nil
}

// reportedHash is the hash a local save is presented under: the recorded
// (server) hash when only its clock changed since the last sync, else its
// current content hash.
func reportedHash(ls LocalSave, rec recordedHash) (string, error) {
	content, identity, err := saveHashes(ls)
	if err != nil {
		return "", err
	}
	if rec.Identity != "" && rec.Content != "" && identity == rec.Identity {
		return rec.Content, nil
	}
	return content, nil
}

// filesSize is the total size of the given files.
func filesSize(paths []string) (int64, error) {
	var total int64
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

// SelectSaveForSlot picks the latest save in preferredSlot, falling back to the
// most recently updated save across all slots. Used by the multi-slot download UI.
func SelectSaveForSlot(saves []romm.Save, preferredSlot string) *romm.Save {
	if len(saves) == 0 {
		return nil
	}
	var best *romm.Save
	for i := range saves {
		if SlotName(saves[i].Slot) != preferredSlot {
			continue
		}
		if best == nil || saves[i].UpdatedAt.After(best.UpdatedAt) {
			best = &saves[i]
		}
	}
	if best != nil {
		return best
	}
	best = &saves[0]
	for i := 1; i < len(saves); i++ {
		if saves[i].UpdatedAt.After(best.UpdatedAt) {
			best = &saves[i]
		}
	}
	return best
}

func ExecuteActions(client *romm.Client, config *settings.Config, deviceID string, items []SyncItem, progressFn func(current, total int)) SyncReport {
	logger := gaba.GetLogger()
	report := SyncReport{}

	actionable := 0
	for _, item := range items {
		if item.Action != ActionSkip && item.Action != ActionConflict {
			actionable++
		}
	}

	logger.Debug("Executing sync actions", "total", len(items), "actionable", actionable)

	current := 0
	for i := range items {
		item := &items[i]

		switch item.Action {
		case ActionUpload:
			current++
			if progressFn != nil {
				progressFn(current, actionable)
			}
			switch upload(client, deviceID, item) {
			case uploadOK:
				item.Success = true
				report.Uploaded++
			case uploadSupersededByDownload:
				// 409: server slot is ahead and local is unmodified, so reconcile by
				// downloading the server save (download() backs up the local first).
				item.Action = ActionDownload
				if download(client, config, deviceID, item) {
					item.Success = true
					report.Downloaded++
				} else {
					report.Errors++
				}
			case uploadConflict:
				item.Action = ActionConflict
				report.Conflicts++
			default:
				report.Errors++
			}

		case ActionDownload:
			current++
			if progressFn != nil {
				progressFn(current, actionable)
			}
			if download(client, config, deviceID, item) {
				item.Success = true
				report.Downloaded++
			} else {
				report.Errors++
			}

		case ActionConflict:
			report.Conflicts++

		default:
			report.Skipped++
		}
	}

	report.Items = items
	logger.Debug("Sync execution complete", "uploaded", report.Uploaded, "downloaded", report.Downloaded, "skipped", report.Skipped, "errors", report.Errors)
	return report
}

type uploadOutcome int

const (
	uploadErr uploadOutcome = iota
	uploadOK
	uploadConflict
	// uploadSupersededByDownload: a 409 revealed the server slot is ahead and the
	// local save is unmodified since its last sync, so the item was converted to a
	// download to reconcile (option b, no user prompt needed).
	uploadSupersededByDownload
)

// conflict409Resolution is how grout reconciles a 409 "slot has a newer save"
// upload rejection after fetching the server's current save for the slot.
type conflict409Resolution int

const (
	// resolve409Unresolved: no server save for the slot, so we can't reconcile;
	// the upload stays a bare (non-resolvable) conflict.
	resolve409Unresolved conflict409Resolution = iota
	// resolve409AsDownload: local is unmodified since its last sync, so the server is
	// simply ahead, so auto-download the server save.
	resolve409AsDownload
	// resolve409AsConflict: local diverged from its last sync, a genuine conflict;
	// surface it with the server save populated so it is resolvable.
	resolve409AsConflict
)

// resolveUpload409 decides how to handle a 409 "slot has a newer save" upload
// rejection. It selects the server's current save for slot and, when the local save
// is provably unmodified since its last recorded sync (localHash == recordedHash),
// chooses an auto-download; a diverged or unknown local state becomes a conflict.
func resolveUpload409(serverSaves []romm.Save, slot, localHash, recordedHash string) (conflict409Resolution, *romm.Save) {
	best := SelectSaveForSlot(serverSaves, slot)
	if best == nil {
		return resolve409Unresolved, nil
	}
	if recordedHash != "" && localHash == recordedHash {
		return resolve409AsDownload, best
	}
	return resolve409AsConflict, best
}

// buildUploadQuery derives the /api/saves upload query for a sync item. Overwrite is set
// ONLY when the user explicitly chose keep-local (ForceOverwrite); a normal upload op goes
// out with overwrite=false so the server's optimistic-concurrency guard can return 409 if
// the slot changed since negotiate (which grout then surfaces as a conflict). This mirrors
// Argosy and the server's intent; forcing on every upload would clobber a concurrent
// write from another device.
func buildUploadQuery(deviceID string, item *SyncItem) romm.UploadSaveQuery {
	slot := settings.DefaultSaveSlot
	if item.TargetSlot != "" {
		slot = item.TargetSlot
	} else if item.RemoteSave != nil {
		slot = SlotName(item.RemoteSave.Slot)
	}

	emulator := filepath.Base(item.LocalSave.EmulatorDir)
	if emulator == "." || emulator == "" {
		emulator = "unknown"
	}
	if emulator == "SAVEDATA" { // PSP folder name varies across CFW
		emulator = "PPSSPP"
	}

	query := romm.UploadSaveQuery{
		RomID:     item.LocalSave.RomID,
		DeviceID:  deviceID,
		Emulator:  emulator,
		Slot:      slot,
		Overwrite: item.ForceOverwrite,
	}
	if slot == settings.DefaultSaveSlot {
		query.Autocleanup = true
		query.AutocleanupLimit = 10
	}
	return query
}

// resolveUploadConflict handles a 409 "slot has a newer save" rejection. It fetches
// the server's current save for the slot and, via resolveUpload409, either converts the
// item to an auto-download (local unmodified since last sync, option b) or surfaces a
// resolvable conflict with the server save populated. A bare conflict remains only if
// the server save can't be fetched.
func resolveUploadConflict(client *romm.Client, deviceID string, item *SyncItem, slot string, cause error) uploadOutcome {
	logger := gaba.GetLogger()

	rec := loadRecordedHashes(deviceID)[saveKey{item.LocalSave.RomID, item.LocalSave.FileName}]
	localHash, _ := reportedHash(item.LocalSave, rec)

	serverSaves, err := client.GetSaves(romm.SaveQuery{RomID: item.LocalSave.RomID, Slot: slot, DeviceID: deviceID})
	if err != nil {
		logger.Warn("Upload 409: failed to fetch server save for the slot; surfacing as conflict",
			"romID", item.LocalSave.RomID, "slot", slot, "error", err)
		return uploadConflict
	}

	resolution, serverSave := resolveUpload409(serverSaves, slot, localHash, rec.Content)
	if serverSave != nil {
		item.RemoteSave = serverSave
	}

	switch resolution {
	case resolve409AsDownload:
		logger.Warn("Upload 409: server slot is ahead and local is unmodified; downloading server save instead",
			"romID", item.LocalSave.RomID, "slot", slot, "serverSaveID", serverSave.ID)
		return uploadSupersededByDownload
	default:
		logger.Warn("Upload rejected with 409; surfacing as conflict",
			"romID", item.LocalSave.RomID, "slot", slot, "resolvable", serverSave != nil, "error", cause)
		return uploadConflict
	}
}

// uploadRecordHash picks the content hash to record after an upload: the
// server's when it sent one, else the locally computed one. mismatch reports a
// server hash that differs from a known local one, which the next negotiate would
// otherwise only show as a spurious upload.
func uploadRecordHash(server *string, local string) (hash string, mismatch bool) {
	if server == nil || *server == "" {
		return local, false
	}
	return *server, local != "" && local != *server
}

func upload(client *romm.Client, deviceID string, item *SyncItem) uploadOutcome {
	logger := gaba.GetLogger()
	logger.Debug("Uploading save", "romID", item.LocalSave.RomID, "romName", item.LocalSave.RomName, "file", item.LocalSave.FilePath)

	query := buildUploadQuery(deviceID, item)

	uploadPath := item.LocalSave.FilePath
	if item.LocalSave.IsDirectorySave {
		dirs := item.LocalSave.RelatedDirs
		if len(dirs) == 0 {
			dirs = []string{item.LocalSave.FilePath}
		}
		zipPath, zipErr := ZipDirectories(dirs)
		if zipErr != nil {
			logger.Error("Failed to zip directory save", "gameID", item.LocalSave.GameID, "error", zipErr)
			return uploadErr
		}
		defer os.Remove(zipPath)
		uploadPath = zipPath
	} else if len(item.LocalSave.Companions) > 0 {
		// A save with companions (.srm + .rtc) goes up as one flat zip, as Argosy sends it.
		zipPath, cleanup, zipErr := ZipSaveUnit(item.LocalSave.FilePath, item.LocalSave.Companions)
		if zipErr != nil {
			logger.Error("Failed to zip save with its companions", "path", item.LocalSave.FilePath, "error", zipErr)
			return uploadErr
		}
		defer cleanup()
		uploadPath = zipPath
	}

	uploadedSave, err := client.UploadSaveWithQuery(query, uploadPath)
	if err != nil {
		if errors.Is(err, romm.ErrConflict) {
			return resolveUploadConflict(client, deviceID, item, query.Slot, err)
		}
		logger.Error("Failed to upload save", "romID", item.LocalSave.RomID, "error", err)
		return uploadErr
	}

	// Match server precision so the next scan doesn't see a spurious change.
	if !item.LocalSave.IsDirectorySave {
		t := uploadedSave.UpdatedAt.Truncate(time.Second)
		if err := os.Chtimes(item.LocalSave.FilePath, t, t); err != nil {
			logger.Warn("Failed to set save mtime after upload", "path", item.LocalSave.FilePath, "error", err)
		}
	}
	// No MarkDeviceSynced: the server upserts last_synced_at automatically on
	// upload because device_id is supplied.

	// Record the synced state so the next scan reports this save under the same slot,
	// with the hash the server computed for it.
	localHash, identity, _ := saveHashes(item.LocalSave)
	hash, mismatch := uploadRecordHash(uploadedSave.ContentHash, localHash)
	if mismatch {
		logger.Warn("Server hash differs from local hash after upload",
			"romID", item.LocalSave.RomID, "file", item.LocalSave.FileName, "server", hash, "local", localHash)
	} else if uploadedSave.ContentHash == nil || *uploadedSave.ContentHash == "" {
		logger.Debug("Server sent no content hash after upload; recording the local hash",
			"romID", item.LocalSave.RomID, "file", item.LocalSave.FileName)
	}
	recordSaveState(deviceID, item.LocalSave.RomID, item.LocalSave.FileName, query.Slot, uploadedSave.ID, hash, identity)

	logger.Debug("Upload successful", "romID", item.LocalSave.RomID)
	return uploadOK
}

func download(client *romm.Client, config *settings.Config, deviceID string, item *SyncItem) bool {
	logger := gaba.GetLogger()

	if item.RemoteSave == nil {
		logger.Error("No remote save to download", "romID", item.LocalSave.RomID)
		return false
	}

	logger.Debug("Downloading save", "romID", item.LocalSave.RomID, "romName", item.LocalSave.RomName, "saveID", item.RemoteSave.ID)

	if !item.LocalSave.IsDirectorySave && IsDirectorySavePlatform(item.LocalSave.FSSlug) {
		item.LocalSave.IsDirectorySave = true
	}

	// Fetch before touching anything on disk, so a failed fetch changes nothing.
	data, err := client.DownloadSaveByID(item.RemoteSave.ID, deviceID, false)
	if err != nil {
		logger.Error("Failed to download save", "romID", item.LocalSave.RomID, "saveID", item.RemoteSave.ID, "error", err)
		return false
	}

	var savePath string
	if item.LocalSave.IsDirectorySave {
		if err := backupDirectorySave(item.LocalSave, config); err != nil {
			logger.Error("Failed to backup save before download, aborting download", "path", item.LocalSave.FilePath, "error", err)
			return false
		}
		savePath = item.LocalSave.FilePath
		if savePath == "" {
			savePath = derivedSavePath(*item, config)
		}
		if savePath == "" {
			logger.Error("Could not determine save path", "romID", item.LocalSave.RomID, "fsSlug", item.LocalSave.FSSlug)
			return false
		}
	} else {
		target, err := fileDownloadTarget(*item, data, config)
		if err != nil {
			logger.Error("Not writing downloaded save",
				"romID", item.LocalSave.RomID, "fsSlug", item.LocalSave.FSSlug, "path", target,
				"serverFile", item.RemoteSave.FileName, "serverExt", item.RemoteSave.FileExtension, "error", err)
			return false
		}
		// Back up whatever sits at the target, whether or not the scan matched it.
		if err := backupFile(target, config); err != nil {
			logger.Error("Failed to backup save before download, aborting download", "path", target, "error", err)
			return false
		}
		savePath = target
	}

	if item.LocalSave.IsDirectorySave {
		// Write zip to temp, then extract to the save directory
		tmpZip, err := os.CreateTemp("", "grout-save-dl-*.zip")
		if err != nil {
			logger.Error("Failed to create temp file for directory save", "error", err)
			return false
		}
		tmpZipPath := tmpZip.Name()
		defer os.Remove(tmpZipPath)

		if _, err := tmpZip.Write(data); err != nil {
			tmpZip.Close()
			logger.Error("Failed to write downloaded save zip", "error", err)
			return false
		}
		tmpZip.Close()

		// Validate the downloaded zip BEFORE deleting any local dirs: a corrupt or
		// empty body (server bug, truncation) must not wipe the live save. The backup
		// already exists, but we'd rather never destroy the original.
		if zr, zerr := zip.OpenReader(tmpZipPath); zerr != nil || len(zr.File) == 0 {
			if zr != nil {
				zr.Close()
			}
			logger.Error("Downloaded directory-save zip is corrupt or empty, aborting", "path", savePath, "error", zerr)
			return false
		} else {
			zr.Close()
		}

		// Remove all existing save directories before extracting.
		// For multi-dir PSP saves (DATA00, DATA01, INSDIR…), RelatedDirs holds
		// all of them; fall back to savePath for single-dir or remote-only cases.
		dirsToRemove := item.LocalSave.RelatedDirs
		if len(dirsToRemove) == 0 {
			dirsToRemove = []string{savePath}
		}
		for _, dir := range dirsToRemove {
			os.RemoveAll(dir)
		}

		if err := UnzipToDirectory(tmpZipPath, filepath.Dir(savePath)); err != nil {
			logger.Error("Failed to extract directory save", "path", savePath, "error", err)
			return false
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(savePath), 0755); err != nil {
			logger.Error("Failed to create save directory", "path", filepath.Dir(savePath), "error", err)
			return false
		}

		// Write atomically (temp file in the same dir + rename) so a power loss or I/O
		// error mid-write can't leave a truncated, corrupt save in place.
		if err := files.WriteFileAtomic(savePath, data, 0644); err != nil {
			logger.Error("Failed to write save file", "path", savePath, "error", err)
			return false
		}
	}

	t := item.RemoteSave.UpdatedAt.Truncate(time.Second)
	if !item.LocalSave.IsDirectorySave {
		if err := os.Chtimes(savePath, t, t); err != nil {
			logger.Warn("Failed to set save file mtime", "path", savePath, "error", err)
		}
	}

	if err := client.ConfirmSaveDownloaded(item.RemoteSave.ID, deviceID); err != nil {
		logger.Warn("Failed to confirm save download", "saveID", item.RemoteSave.ID, "error", err)
	}

	// Record synced state. A null-slot ("archival") save is recorded as "autosave"
	// so the next sync promotes it to the autosave slot (matching Argosy); a named-slot
	// save keeps its slot so it isn't re-uploaded elsewhere. The record is keyed on the
	// on-disk basename (NOT the server's datetime-tagged op.FileName) so the next
	// ScanSaves looks it up correctly and keeps its slot identity.
	recordSlot := settings.DefaultSaveSlot
	if item.RemoteSave.Slot != nil && *item.RemoteSave.Slot != "" {
		recordSlot = *item.RemoteSave.Slot
	}
	recordFileName := recordedDownloadFileName(*item, savePath)
	// The written file alone: its hash is both the content and the identity.
	hash, identity, _ := saveHashes(LocalSave{
		FilePath:        savePath,
		IsDirectorySave: item.LocalSave.IsDirectorySave,
		RelatedDirs:     item.LocalSave.RelatedDirs,
	})
	recordSaveState(deviceID, item.LocalSave.RomID, recordFileName, recordSlot, item.RemoteSave.ID, hash, identity)

	logger.Debug("Download successful", "romID", item.LocalSave.RomID, "romName", item.LocalSave.RomName, "path", savePath, "recordedSlot", recordSlot)
	return true
}

// errNoSavePath: neither a local save nor the ROM tells where a download goes.
var errNoSavePath = errors.New("could not determine save path")

// derivedSavePath is where a download goes for a ROM with no local save: the
// platform's save folder and a name derived from the ROM, or "" when the save
// folder is unknown.
func derivedSavePath(item SyncItem, config *settings.Config) string {
	saveDir := item.LocalSave.EmulatorDir
	if saveDir == "" {
		saveDir = ResolveSaveDirectory(item.LocalSave.FSSlug, config)
	}
	if saveDir == "" {
		return ""
	}
	fileName := downloadSaveName(item.LocalSave.RomFileName, item.RemoteSave.FileName, item.RemoteSave.FileExtension, saveDir)
	return filepath.Join(saveDir, fileName)
}

// fileDownloadTarget returns the path a downloaded file save is written to, or
// why it must not be written. The local save's path wins; a path derived for a
// ROM with no local save must carry a save extension. On error the path, when
// known, is still returned for logging.
func fileDownloadTarget(item SyncItem, data []byte, config *settings.Config) (string, error) {
	path := item.LocalSave.FilePath
	derived := path == ""
	if derived {
		path = derivedSavePath(item, config)
	}
	if isZipPayload(data) {
		return path, errZipSaveUnsupported
	}
	if path == "" {
		return "", errNoSavePath
	}
	if derived && !hasSaveExtension(path) {
		return path, errNonSaveName
	}
	return path, nil
}

// backupFile copies the file at path into <dir>/.backup as "<base> [<mtime>]<ext>"
// before it is overwritten, then prunes old backups. A missing file needs none.
func backupFile(path string, config *settings.Config) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}

	backupDir := filepath.Join(filepath.Dir(path), ".backup")
	name := filepath.Base(path)
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	timestamp := info.ModTime().Format("2006-01-02 15-04-05")
	backupPath := filepath.Join(backupDir, fmt.Sprintf("%s [%s]%s", base, timestamp, ext))

	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	if err := files.CopyFile(path, backupPath); err != nil {
		return err
	}

	gaba.GetLogger().Debug("Backed up save before download", "backup", backupPath)
	if config != nil && config.SaveBackupLimit > 0 {
		cleanupBackups(backupDir, base, config.SaveBackupLimit)
	}
	return nil
}

// backupDirectorySave zips a directory (PSP) save's folders into
// <dir>/.backup as "<base> [<mtime>]<ext>" before a download replaces them.
func backupDirectorySave(ls LocalSave, config *settings.Config) error {
	if ls.FilePath == "" {
		return nil
	}
	info, err := os.Stat(ls.FilePath)
	if err != nil {
		return nil
	}

	backupDir := filepath.Join(filepath.Dir(ls.FilePath), ".backup")
	ext := filepath.Ext(ls.FileName)
	base := strings.TrimSuffix(ls.FileName, ext)
	timestamp := info.ModTime().Format("2006-01-02 15-04-05")
	backupPath := filepath.Join(backupDir, fmt.Sprintf("%s [%s]%s", base, timestamp, ext))

	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}

	dirs := ls.RelatedDirs
	if len(dirs) == 0 {
		dirs = []string{ls.FilePath}
	}
	zipPath, err := ZipDirectories(dirs)
	if err != nil {
		return err
	}
	defer os.Remove(zipPath)
	if err := files.CopyFile(zipPath, backupPath); err != nil {
		return err
	}

	gaba.GetLogger().Debug("Backed up save before download", "backup", backupPath)
	if config != nil && config.SaveBackupLimit > 0 {
		cleanupBackups(backupDir, base, config.SaveBackupLimit)
	}
	return nil
}

func cleanupBackups(backupDir string, baseName string, limit int) {
	if limit <= 0 {
		return
	}

	logger := gaba.GetLogger()
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return
	}

	// Collect backup files for this game (matching base name prefix)
	type backupFile struct {
		name    string
		modTime int64
	}
	var backups []backupFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), baseName+" [") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		backups = append(backups, backupFile{name: e.Name(), modTime: info.ModTime().UnixNano()})
	}

	if len(backups) <= limit {
		return
	}

	// Sort oldest first
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].modTime < backups[j].modTime
	})

	// Remove oldest until we're at the limit
	for i := 0; i < len(backups)-limit; i++ {
		path := filepath.Join(backupDir, backups[i].name)
		if err := os.Remove(path); err != nil {
			logger.Warn("Failed to remove old backup", "path", path, "error", err)
		} else {
			logger.Debug("Removed old backup", "path", path)
		}
	}
}

// extractPSPGameID extracts the Game ID from a PSP save directory name.
// Two rules are applied in this order:
//  1. If the name contains an underscore, the Game ID is the part before it
//     (e.g. "UCUS98751_DATA00" → "UCUS98751", "UCUS98751_INSDIR" → "UCUS98751").
//  2. Otherwise, try to shrink prefixes from longest to shortest against
//     pspdb.Titles until a known Game ID is found
//     (e.g. "UCUS98653PROFILE00" → "UCUS98653").
//
// Falls back to the full directory name if no match is found.
func extractPSPGameID(dirName string) string {
	if idx := strings.Index(dirName, "_"); idx > 0 {
		return dirName[:idx]
	}
	for l := len(dirName) - 1; l > 0; l-- {
		if _, ok := pspdb.Titles[dirName[:l]]; ok {
			return dirName[:l]
		}
	}
	return dirName
}

func ResolveSaveDirectory(fsSlug string, config *settings.Config) string {
	if dir := preferredSaveDirectory(fsSlug, config); dir != "" {
		return dir
	}
	if config != nil && config.SaveDirectoryMappings != nil {
		if mapped, ok := config.SaveDirectoryMappings[fsSlug]; ok && mapped != "" {
			baseSavePath := cfw.BaseSavePath()
			if baseSavePath != "" {
				return filepath.Join(baseSavePath, mapped)
			}
		}
	}

	effectiveFSSlug := fsSlug
	if config != nil {
		effectiveFSSlug = config.ResolveFSSlug(fsSlug)
	}

	return cfw.GetSaveDirectory(effectiveFSSlug)
}

func resolveDiscoveredSaveDirectory(rom cfw.LocalRomFile, config *settings.Config) string {
	if rom.FilePath == "" {
		return ""
	}
	if dir := preferredSaveDirectory(rom.FSSlug, config); dir != "" {
		return dir
	}

	effectiveFSSlug := rom.FSSlug
	if config != nil {
		effectiveFSSlug = config.ResolveFSSlug(rom.FSSlug)
	}

	return cfw.GetSaveDirectoryForRomPath(effectiveFSSlug, rom.FilePath)
}

// preferredSaveDirectory is where the firmware insists new saves go, such as
// muOS Pickles for a folder it runs, or "" to follow the save mapping.
func preferredSaveDirectory(fsSlug string, config *settings.Config) string {
	if config == nil {
		return ""
	}
	folder := cfw.PreferredSaveFolder(*config, fsSlug)
	base := cfw.BaseSavePath()
	if folder == "" || base == "" {
		return ""
	}
	return filepath.Join(base, folder)
}

// withMappedSaveFolders adds each mapped platform's full list of save folders
// to the firmware's table, without changing the table it was given.
func withMappedSaveFolders(folders map[string][]string, config settings.Config) map[string][]string {
	merged := make(map[string][]string, len(folders))
	for fsSlug, dirs := range folders {
		merged[fsSlug] = dirs
	}
	for rommSlug := range config.DirectoryMappings {
		fsSlug := config.ResolveFSSlug(rommSlug)
		for _, dir := range cfw.SaveFolders(config, rommSlug) {
			if !slices.Contains(merged[fsSlug], dir) {
				merged[fsSlug] = append(slices.Clone(merged[fsSlug]), dir)
			}
		}
	}
	return merged
}
