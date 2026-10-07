// SPDX-License-Identifier: MIT

//go:build linux

package server

import (
	"io/fs"
	"syscall"
	"testing"
)

// A filesystem that needs repair is said so, in the viewer's words, rather
// than as the errno: XFS answers EUCLEAN until it is repaired, which is what a
// remount after a crash leaves behind, and it is Linux's alone.
func TestADamagedFilesystemIsSaidSo(t *testing.T) {
	err := &fs.PathError{Op: "open", Path: "/x", Err: syscall.EUCLEAN}
	if got, want := openFault(err), "the filesystem it is on is damaged and needs repair"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
}
