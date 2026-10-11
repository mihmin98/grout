package saves

import "testing"

func TestIsSaveLike(t *testing.T) {
	tests := map[string]bool{
		"/roms/gba/Game.srm":                  true,
		"/roms/gba/Game.SAV":                  true,
		"/roms/gba/Game.rtc":                  true,
		"/roms/gba/Game.ldci":                 true,
		"/roms/gba/Game.state":                true,
		"/roms/gba/Game.state12":              true,
		"/roms/gba/Game.state.auto":           true,
		"/roms/gba/Game.state1.png":           true,
		"/roms/psx/.backup/x.chd":             true,
		"/roms/psx/Game/.backup/Game.srm.bak": true,
		"/roms/psx/Game/Game (Disc 1).chd":    false,
		"/roms/gba/Game.gba":                  false,
		"/roms/psx/Game.m3u":                  false,
		"/roms/gba/images/Game-image.png":     false,
		"/roms/gba/statement.txt":             false,
		"/roms/gba/Game.stateful.gba":         false,
		"/roms/gba/Game.backup.gba":           false,
	}
	for path, want := range tests {
		if got := IsSaveLike(path); got != want {
			t.Errorf("IsSaveLike(%q) = %v, want %v", path, got, want)
		}
	}
}
