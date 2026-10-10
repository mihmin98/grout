package saves

import "testing"

// After an upload grout records the hash RomM computed for the save, so the next
// negotiate compares like with like. A difference from grout's own hash means the
// two hash the save differently and is reported.
func TestUploadRecordHash(t *testing.T) {
	str := func(s string) *string { return &s }

	tests := []struct {
		name         string
		server       *string
		local        string
		wantHash     string
		wantMismatch bool
	}{
		{"server equals local", str("abc"), "abc", "abc", false},
		{"server differs from local", str("srv"), "loc", "srv", true},
		{"no server hash", nil, "loc", "loc", false},
		{"empty server hash", str(""), "loc", "loc", false},
		{"no local hash", str("srv"), "", "srv", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, mismatch := uploadRecordHash(tt.server, tt.local)
			if hash != tt.wantHash || mismatch != tt.wantMismatch {
				t.Errorf("uploadRecordHash = (%q, %v), want (%q, %v)", hash, mismatch, tt.wantHash, tt.wantMismatch)
			}
		})
	}
}
