package saves

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestFindCompanions(t *testing.T) {
	dir := filepath.Join("roms", "gbc")
	srm := filepath.Join(dir, "Crystal.srm")

	tests := []struct {
		name    string
		primary string
		names   []string
		want    []string
	}{
		{"srm with rtc", srm, []string{"Crystal.7z", "Crystal.srm", "Crystal.rtc"},
			[]string{filepath.Join(dir, "Crystal.rtc")}},
		{"extension in upper case", srm, []string{"Crystal.srm", "Crystal.RTC"},
			[]string{filepath.Join(dir, "Crystal.RTC")}},
		{"other game's rtc", srm, []string{"Crystal.srm", "Other.rtc"}, nil},
		{"stem case differs", srm, []string{"Crystal.srm", "crystal.rtc"}, nil},
		{"no rtc", srm, []string{"Crystal.srm", "Crystal.7z"}, nil},
		{"upper-case primary extension", filepath.Join(dir, "Crystal.SRM"), []string{"Crystal.SRM", "Crystal.rtc"},
			[]string{filepath.Join(dir, "Crystal.rtc")}},
		{"sav primary has no companions", filepath.Join(dir, "Crystal.sav"), []string{"Crystal.sav", "Crystal.rtc"}, nil},
		{"stem with dots", filepath.Join(dir, "Game v1.1.srm"), []string{"Game v1.1.srm", "Game v1.1.rtc", "Game v1.rtc"},
			[]string{filepath.Join(dir, "Game v1.1.rtc")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findCompanions(tt.primary, tt.names); !slices.Equal(got, tt.want) {
				t.Errorf("findCompanions = %v, want %v", got, tt.want)
			}
		})
	}
}
