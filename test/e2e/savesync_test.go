//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Knulli keeps saves in one folder per platform, which makes it the clearest
// firmware to write a save into. muOS nests them under the emulator that
// wrote them, which save_mapping.go is about and which is covered by its own
// unit tests.
const knulliSave = "saves/snes/Test Game (USA).srm"

// A save sitting on the card is uploaded to the server, which is the whole
// point of sync.
func TestSaveSyncUploadsALocalSave(t *testing.T) {
	server := romm(t)
	s := start(t, options{
		CFW: "KNULLI", Server: server, Platforms: []string{"snes"},
		Existing: []string{"roms/tools"},
	})
	s.awaitLog("Starting Grout")

	// A save only counts if grout can match it to a game it knows about, so
	// its name is the rom's.
	s.putSave(knulliSave, "a saved game")

	s.press("y") // the sync menu, from the platform list
	s.screenshot("sync-menu")
	s.press("a") // Sync Now

	s.awaitLog("Starting save scan")
	s.screenshot("sync-result")

	saves := serverSaves(t, server)
	if len(saves) == 0 {
		t.Fatalf("the server holds no saves after a sync\n\nlog:\n%s", s.logTail())
	}
	if got := saves[0].FileName; !strings.Contains(got, "Test Game") {
		t.Errorf("the server holds %q, want the save that was on the card", got)
	}
}

// Nothing to sync is not a failure, and must not look like one.
func TestSaveSyncWithNothingToDo(t *testing.T) {
	s := start(t, options{
		CFW: "KNULLI", Server: romm(t), Platforms: []string{"snes"},
		Existing: []string{"roms/tools"},
	})
	s.awaitLog("Starting Grout")

	s.press("y") // the sync menu
	s.press("a") // Sync Now

	s.awaitLog("Starting save scan")
	s.screenshot("nothing-to-sync")
}

// serverSave is the part of RomM's save record the tests look at.
type serverSave struct {
	ID       int    `json:"id"`
	FileName string `json:"file_name"`
}

// serverSaves asks RomM what saves it holds, so a test can check what arrived
// rather than trusting grout's own account of it.
func serverSaves(t *testing.T, s *server) []serverSave {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, s.URL+"/api/saves", nil)
	if err != nil {
		t.Fatalf("asking the server for its saves: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+s.Token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("asking the server for its saves: %v", err)
	}
	defer response.Body.Close()

	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("the server answered %d for its saves: %s", response.StatusCode, body)
	}

	var saves []serverSave
	if err := json.Unmarshal(body, &saves); err != nil {
		t.Fatalf("reading the server's saves: %v\n%s", err, body)
	}
	return saves
}

// History lists what was synced, with an icon for which way it went.
func TestSyncHistoryShowsWhatSynced(t *testing.T) {
	s := start(t, options{
		CFW: "KNULLI", Server: romm(t), Platforms: []string{"snes"},
		Existing: []string{"roms/tools"},
	})
	s.awaitLog("Starting Grout")
	// The server is shared with the other tests, which upload a save for the
	// same game. Contents of its own make this one an upload, which is what
	// puts it in the history.
	s.putSave(knulliSave, fmt.Sprintf("a saved game at %d", time.Now().UnixNano()))

	s.press("y") // the sync menu
	s.press("a") // Sync Now
	// The result only takes a press once the sync is done. Starting the scan
	// is too early: a slow run was still scanning when the press landed.
	s.awaitLog("Sync execution complete")
	s.awaitStill()
	s.press("a") // dismiss the result, back to the sync menu

	s.press("Down", "Down", "a") // View History
	s.awaitLog("Showing sync history")
	s.screenshot("history")
}
