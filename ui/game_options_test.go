package ui

import (
	"testing"

	"grout/romm"
	"grout/settings"

	gaba "github.com/BrandonKowalski/gabagool/v2/pkg/gabagool"
)

func clickedValues(items []gaba.ItemWithOptions) []string {
	var values []string
	for _, item := range items {
		if value := clickedValue(item); value != "" {
			values = append(values, value)
		}
	}
	return values
}

// Delete from Device is offered only for a game with files on the device, and
// comes after Show QR Code.
func TestGameOptionsMenu_DeleteOnlyWithLocalCopy(t *testing.T) {
	s := NewGameOptionsScreen()
	config := &settings.Config{}
	game := romm.Rom{ID: 7, Name: "Mario"}

	tests := []struct {
		name             string
		deviceRegistered bool
		hasLocalCopy     bool
		wantRows         int
		wantClicks       []string
	}{
		{"not downloaded", false, false, 1, []string{"show_qr"}},
		{"downloaded", false, true, 2, []string{"show_qr", "delete"}},
		{"downloaded, save sync", true, true, 3, []string{"show_qr", "delete"}},
		{"not downloaded, save sync", true, false, 2, []string{"show_qr"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := s.buildMenuItems(config, game, tt.deviceRegistered, nil, tt.hasLocalCopy)

			if len(items) != tt.wantRows {
				t.Fatalf("rows = %d, want %d", len(items), tt.wantRows)
			}
			got := clickedValues(items)
			if len(got) != len(tt.wantClicks) {
				t.Fatalf("clickable rows = %v, want %v", got, tt.wantClicks)
			}
			for i := range got {
				if got[i] != tt.wantClicks[i] {
					t.Errorf("clickable rows = %v, want %v", got, tt.wantClicks)
				}
			}
			if tt.hasLocalCopy && items[len(items)-1].Item.Text != "Delete from Device" {
				t.Errorf("last row = %q, want Delete from Device", items[len(items)-1].Item.Text)
			}
		})
	}
}

// The slot row's options are not clickable, so selecting it is no action.
func TestClickedValue_IgnoresOtherRows(t *testing.T) {
	slot := gaba.ItemWithOptions{
		Item:           gaba.MenuItem{Text: "Save Slot"},
		Options:        []gaba.Option{{DisplayName: "autosave", Value: "autosave"}},
		SelectedOption: 0,
	}
	if got := clickedValue(slot); got != "" {
		t.Errorf("clickedValue(slot row) = %q, want empty", got)
	}
	if got := clickedValue(gaba.ItemWithOptions{SelectedOption: 3}); got != "" {
		t.Errorf("clickedValue(no options) = %q, want empty", got)
	}
}
