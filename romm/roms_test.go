package romm

import (
	"encoding/json"
	"grout/library"
	"grout/settings"
	"testing"
)

func TestCanonicalLocalBasename(t *testing.T) {
	tests := []struct {
		name string
		rom  Rom
		want string
	}{
		{
			name: "simple single file uses the file basename",
			rom: Rom{
				HasMultipleFiles: false,
				FsName:           "Metroid_ Scrolls 6 (1.1).gba",
				FsNameNoExt:      "Metroid_ Scrolls 6 (1.1)",
				Files:            []RomFile{{FileName: "Metroid_ Scrolls 6 (1.1).gba"}},
			},
			want: "Metroid_ Scrolls 6 (1.1)",
		},
		{
			// The issue #242 case: RomM stores the ROM as a folder (fs_name is the
			// clean folder name) containing one tagged file. The save and the
			// downloaded ROM on disk are named after the *file*, not the folder.
			name: "nested single file uses the inner file basename not the folder",
			rom: Rom{
				HasMultipleFiles: false,
				FsName:           "Kingdom Hearts - Chain of Memories",
				FsNameNoExt:      "Kingdom Hearts - Chain of Memories",
				Files:            []RomFile{{FileName: "Kingdom Hearts - Chain of Memories (Europe) (En,Fr,De,Es,It).gba"}},
			},
			want: "Kingdom Hearts - Chain of Memories (Europe) (En,Fr,De,Es,It)",
		},
		{
			name: "multi file uses the m3u basename (fs_name_no_ext)",
			rom: Rom{
				HasMultipleFiles: true,
				FsName:           "Final Fantasy VII",
				FsNameNoExt:      "Final Fantasy VII",
				Files: []RomFile{
					{FileName: "Final Fantasy VII (Disc 1).bin"},
					{FileName: "Final Fantasy VII (Disc 2).bin"},
				},
			},
			want: "Final Fantasy VII",
		},
		{
			name: "no file metadata falls back to fs_name_no_ext",
			rom: Rom{
				HasMultipleFiles: false,
				FsNameNoExt:      "Some Game (USA)",
				Files:            nil,
			},
			want: "Some Game (USA)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rom.CanonicalLocalBasename(); got != tt.want {
				t.Errorf("CanonicalLocalBasename() = %q, want %q", got, tt.want)
			}
		})
	}
}

// LocalBasenames must return EVERY on-disk basename a ROM can occupy, so a save/ROM for
// any of a multi-file game's alternative versions resolves, not just Files[0] (issue #242).
func TestLocalBasenames(t *testing.T) {
	tests := []struct {
		name string
		rom  Rom
		want []string
	}{
		{
			name: "simple single file yields the one file basename",
			rom: Rom{
				HasMultipleFiles: false,
				FsNameNoExt:      "Metroid_ Scrolls 6 (1.1)",
				Files:            []RomFile{{FileName: "Metroid_ Scrolls 6 (1.1).gba"}},
			},
			want: []string{"Metroid_ Scrolls 6 (1.1)"},
		},
		{
			name: "multiple alternative files each yield a basename",
			rom: Rom{
				HasMultipleFiles: false,
				FsName:           "Kingdom Hearts - Chain of Memories",
				FsNameNoExt:      "Kingdom Hearts - Chain of Memories",
				Files: []RomFile{
					{FileName: "Kingdom Hearts - Chain of Memories (Europe) (En,Fr,De,Es,It).gba"},
					{FileName: "Kingdom Hearts - Chain of Memories (USA).gba"},
				},
			},
			want: []string{
				"Kingdom Hearts - Chain of Memories (Europe) (En,Fr,De,Es,It)",
				"Kingdom Hearts - Chain of Memories (USA)",
			},
		},
		{
			name: "multi-disc uses only the m3u basename (fs_name_no_ext)",
			rom: Rom{
				HasMultipleFiles: true,
				FsNameNoExt:      "Final Fantasy VII",
				Files: []RomFile{
					{FileName: "Final Fantasy VII (Disc 1).bin"},
					{FileName: "Final Fantasy VII (Disc 2).bin"},
				},
			},
			want: []string{"Final Fantasy VII"},
		},
		{
			name: "no file metadata falls back to fs_name_no_ext",
			rom: Rom{
				HasMultipleFiles: false,
				FsNameNoExt:      "Some Game (USA)",
				Files:            nil,
			},
			want: []string{"Some Game (USA)"},
		},
		{
			name: "duplicate file basenames are de-duplicated",
			rom: Rom{
				HasMultipleFiles: false,
				FsNameNoExt:      "Doom",
				Files: []RomFile{
					{FileName: "Doom.gb"},
					{FileName: "Doom.gb"},
				},
			},
			want: []string{"Doom"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.rom.LocalBasenames()
			if len(got) != len(tt.want) {
				t.Fatalf("LocalBasenames() = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("LocalBasenames() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// The miximage v2 kind must decode its own ss_metadata fields and resolve them
// like the other RomM-hosted art: path over URL, asset prefix added only when
// missing, and the regular cover when RomM has no v2 artwork.
func TestArtworkURLMixImageV2(t *testing.T) {
	host := settings.Host{RootURI: "http://romm.local", Port: 8080}

	tests := []struct {
		name string
		json string
		want string
	}{
		{
			name: "relative path gets the asset prefix",
			json: `{"ss_metadata":{"miximage_v2_path":"roms/1/miximage_v2.png"}}`,
			want: "http://romm.local:8080/assets/romm/resources/roms/1/miximage_v2.png",
		},
		{
			name: "path already under /assets is not prefixed twice",
			json: `{"ss_metadata":{"miximage_v2_path":"/assets/romm/resources/roms/1/miximage_v2.png?ts=42"}}`,
			want: "http://romm.local:8080/assets/romm/resources/roms/1/miximage_v2.png?ts=42",
		},
		{
			name: "path wins over URL",
			json: `{"ss_metadata":{"miximage_v2_path":"roms/1/miximage_v2.png","miximage_v2_url":"https://ss.example/v2.png"}}`,
			want: "http://romm.local:8080/assets/romm/resources/roms/1/miximage_v2.png",
		},
		{
			name: "URL only is used as is",
			json: `{"ss_metadata":{"miximage_v2_url":"https://ss.example/v2 image.png"}}`,
			want: "https://ss.example/v2%20image.png",
		},
		{
			name: "v1 miximage is not used for v2",
			json: `{"path_cover_small":"/assets/cover_small.png","ss_metadata":{"miximage_path":"roms/1/miximage.png","miximage_url":"https://ss.example/v1.png"}}`,
			want: "http://romm.local:8080/assets/cover_small.png",
		},
		{
			name: "no v2 artwork falls back to the URL cover",
			json: `{"url_cover":"https://igdb.example/cover.png","ss_metadata":{}}`,
			want: "https://igdb.example/cover.png",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rom Rom
			if err := json.Unmarshal([]byte(tt.json), &rom); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := rom.GetArtworkURL(library.ArtKindMixImageV2, host); got != tt.want {
				t.Errorf("GetArtworkURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
