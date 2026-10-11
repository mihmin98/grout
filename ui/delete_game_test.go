package ui

import (
	"errors"
	"testing"

	"grout/catalog"
	"grout/romm"
)

func TestDeleteResultMessage(t *testing.T) {
	tests := []struct {
		name   string
		report catalog.DeleteReport
		want   string
	}{
		{
			name:   "done",
			report: catalog.DeleteReport{Deleted: []string{"/roms/gb/Mario.gb"}},
			want:   "Deleted Mario.",
		},
		{
			// A ROCKNIX multi-disc folder with saves and states RetroArch
			// wrote beside the discs, plus a record row that names a save.
			name: "kept",
			report: catalog.DeleteReport{
				Deleted:  []string{"/roms/psx/Mario/Mario (Disc 1).chd"},
				Kept:     []catalog.KeptFile{{Path: "/roms/psx/Mario/Mario.srm", Reason: catalog.KeptSave}},
				KeptDirs: map[string]int{"/roms/psx/Mario": 4},
			},
			want: "Deleted Mario.\nKept 5 file(s): saves and files Grout didn't download.",
		},
		{
			name: "failed",
			report: catalog.DeleteReport{
				Failed:      map[string]error{"/roms/gb/Mario.gb": errors.New("read-only")},
				MetadataErr: errors.New("gamelist.xml is read-only"),
			},
			want: "Deleted Mario.\n2 could not be deleted; see the log.",
		},
		{
			name: "kept and failed",
			report: catalog.DeleteReport{
				Kept:   []catalog.KeptFile{{Path: "/roms/gb/cover.png", Reason: catalog.KeptShared}},
				Failed: map[string]error{"/roms/gb/Mario.gb": errors.New("busy")},
			},
			want: "Deleted Mario.\nKept 1 file(s): saves and files Grout didn't download.\n1 could not be deleted; see the log.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deleteResultMessage("Mario", tt.report); got != tt.want {
				t.Errorf("message = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDeleteConfirmMessage(t *testing.T) {
	game := romm.Rom{Name: "Mario"}

	got := deleteConfirmMessage(catalog.DeletePlan{Game: game})
	if want := "Delete Mario from this device? Saves are kept."; got != want {
		t.Errorf("recorded: message = %q, want %q", got, want)
	}

	got = deleteConfirmMessage(catalog.DeletePlan{Game: game, Legacy: true})
	want := "Delete Mario from this device? Saves are kept.\n" +
		"It was downloaded before Grout kept a record of its files; some files may stay."
	if got != want {
		t.Errorf("legacy: message = %q, want %q", got, want)
	}
}

func TestDeleteNothingMessage(t *testing.T) {
	if got, want := deleteNothingMessage("Mario"), "Mario has nothing left on this device to delete."; got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}
