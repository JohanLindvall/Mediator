package library

import (
	"path/filepath"
	"slices"
	"testing"
)

// excludeTree lays out a root holding one release, its trailer, an extras
// folder and a private subdirectory.
func excludeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "movie.mkv"), "the film")
	write(t, filepath.Join(root, "movie.trailer.mkv"), "a taste")
	write(t, filepath.Join(root, "Extras", "behind.mkv"), "extra")
	write(t, filepath.Join(root, "shows", "private", "secret.mkv"), "private")
	write(t, filepath.Join(root, "shows", "public", "open.mkv"), "public")
	return root
}

func TestExcludeGlobs(t *testing.T) {
	all := []string{"behind.mkv", "movie.mkv", "movie.trailer.mkv", "open.mkv", "secret.mkv"}
	tests := []struct {
		name    string
		pattern func(root string) string
		want    []string
	}{
		{
			name:    "no patterns",
			pattern: func(string) string { return "" },
			want:    all,
		},
		{
			name:    "base name",
			pattern: func(string) string { return "*.trailer.mkv" },
			want:    []string{"behind.mkv", "movie.mkv", "open.mkv", "secret.mkv"},
		},
		{
			name:    "directory by name",
			pattern: func(string) string { return "Extras" },
			want:    []string{"movie.mkv", "movie.trailer.mkv", "open.mkv", "secret.mkv"},
		},
		{
			name:    "path glob",
			pattern: func(root string) string { return filepath.ToSlash(root) + "/*/private/*" },
			want:    []string{"behind.mkv", "movie.mkv", "movie.trailer.mkv", "open.mkv"},
		},
		{
			// The pattern names the directory, not the files under it: they
			// are excluded because an excluded directory is skipped whole.
			name:    "directory by path",
			pattern: func(root string) string { return filepath.ToSlash(root) + "/shows/pri*" },
			want:    []string{"behind.mkv", "movie.mkv", "movie.trailer.mkv", "open.mkv"},
		},
		{
			name:    "a root is never excluded by its own name",
			pattern: func(root string) string { return filepath.Base(root) },
			want:    all,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := excludeTree(t)
			l := quietLib(root)
			if p := tc.pattern(root); p != "" {
				l.SetExcludes([]string{p})
			}
			l.Scan(nil)
			if got := names(l); !slices.Equal(got, tc.want) {
				t.Fatalf("indexed %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExcludeAppliesToWatcherAdds(t *testing.T) {
	root := excludeTree(t)
	l := quietLib(root)
	l.SetExcludes([]string{"Extras", "*.trailer.mkv"})
	l.Scan(nil)

	// Both paths exist and are new to the index; neither may come in.
	l.AddFile(filepath.Join(root, "movie.trailer.mkv"))
	l.AddFile(filepath.Join(root, "Extras", "behind.mkv"))
	want := []string{"movie.mkv", "open.mkv", "secret.mkv"}
	if got := names(l); !slices.Equal(got, want) {
		t.Fatalf("indexed %v, want %v", got, want)
	}
}

func TestIndexedItemsDroppedWhenTheyStartMatching(t *testing.T) {
	// The files are still on disk, so reconciliation asks stillIndexable
	// whether to keep them; a pattern added since must win there too.
	root := excludeTree(t)
	l := quietLib(root)
	l.Scan(nil)
	if got := len(names(l)); got != 5 {
		t.Fatalf("indexed %d items before excluding, want 5", got)
	}

	l.SetExcludes([]string{"Extras", "*.trailer.mkv", filepath.ToSlash(root) + "/*/private/*"})
	l.Scan(nil)
	want := []string{"movie.mkv", "open.mkv"}
	if got := names(l); !slices.Equal(got, want) {
		t.Fatalf("after the rescan %v, want %v", got, want)
	}
}

func TestValidateExcludes(t *testing.T) {
	if err := ValidateExcludes([]string{"*.mkv", "a/b*/c"}); err != nil {
		t.Fatalf("valid patterns rejected: %v", err)
	}
	if err := ValidateExcludes([]string{"*.mkv", "[bad"}); err == nil {
		t.Fatal("a malformed glob was accepted")
	}
}
