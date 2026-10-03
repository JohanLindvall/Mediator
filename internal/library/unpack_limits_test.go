package library

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zip"
)

func TestUnpackingStopsAtDeclaredSize(t *testing.T) {
	dir := t.TempDir()
	writeZip(t, filepath.Join(dir, "sample.zip"), zipMember{
		name: "clip.mkv", data: bytes.Repeat([]byte("a"), 1<<20), method: zip.Deflate,
	})
	l := quietLib(dir)
	l.Scan(nil)
	it := itemsByName(l)["clip.mkv"]
	// A corrupt directory understates the size of the compressed content.
	it.stored.size, it.Size = 100, 100
	path := filepath.Join(t.TempDir(), "output.part")
	err := unpackTo(context.Background(), it, path)
	if !errors.Is(err, errUnpackedWrong) {
		t.Fatalf("oversized output: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > it.Size {
		t.Fatalf("wrote %d bytes for a member declaring %d", info.Size(), it.Size)
	}
}
