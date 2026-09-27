package library

import (
	"errors"
	"io"
	"math"
	"os"
	"testing"
)

func TestStoredSeekErrorsPreservePosition(t *testing.T) {
	r := newStoredReader(&storedEntry{size: 12})
	if _, err := r.Seek(5, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		off    int64
		whence int
	}{{-1, io.SeekStart}, {-6, io.SeekCurrent}, {-13, io.SeekEnd}, {math.MaxInt64, io.SeekCurrent}, {math.MaxInt64, io.SeekEnd}, {1, 99}} {
		if _, err := r.Seek(tt.off, tt.whence); err == nil {
			t.Fatalf("Seek(%d, %d) succeeded", tt.off, tt.whence)
		}
		if got, _ := r.Seek(0, io.SeekCurrent); got != 5 {
			t.Fatalf("failed seek moved position to %d", got)
		}
	}
	if got, err := r.Seek(-2, io.SeekEnd); got != 10 || err != nil {
		t.Fatalf("valid seek = %d, %v", got, err)
	}
	r.Close()
	if _, err := r.Seek(0, io.SeekStart); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed seek = %v", err)
	}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed read = %v", err)
	}
}

func TestRarVintRejectsOverflow(t *testing.T) {
	maxInt := sliceReaderAt{255, 255, 255, 255, 255, 255, 255, 255, 127}
	if val, _, err := rarVint(maxInt, 0); val != math.MaxInt64 || err != nil {
		t.Fatalf("maximum = %d, %v", val, err)
	}
	for _, data := range []sliceReaderAt{{128}, {255, 255, 255, 255, 255, 255, 255, 255, 255, 1}, {128, 128, 128, 128, 128, 128, 128, 128, 128, 2}} {
		if _, _, err := rarVint(data, 0); err == nil {
			t.Fatalf("accepted overflowing or truncated integer %v", data)
		}
	}
	if _, err := maxInt.ReadAt(make([]byte, 1), -1); err == nil {
		t.Fatal("accepted negative offset")
	}
}
