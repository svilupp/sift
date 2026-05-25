package index

import "testing"

func TestFolderIndex_IsZero(t *testing.T) {
	t.Helper()
	cases := []struct {
		name string
		idx  *FolderIndex
		want bool
	}{
		{"nil", nil, true},
		{"empty struct", &FolderIndex{}, true},
		{"only purpose", &FolderIndex{Purpose: "x"}, false},
		{"only files", &FolderIndex{Files: map[string]FileEntry{"a": {}}}, false},
		{"with refresh", &FolderIndex{Refresh: RefreshStats{FileCount: 1}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.idx.IsZero(); got != c.want {
				t.Fatalf("IsZero(%s) = %v want %v", c.name, got, c.want)
			}
		})
	}
}

func TestSchemaConstants(t *testing.T) {
	if SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", SchemaVersion)
	}
	if FilenameSiftToml != "sift.toml" {
		t.Fatalf("FilenameSiftToml = %q, want sift.toml", FilenameSiftToml)
	}
}
