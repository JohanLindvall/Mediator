//go:build unix

package library

import (
	"io/fs"
	"syscall"
)

// fileID returns the identity of the underlying file. Inode numbers are only
// unique within a filesystem, so the device is part of the key — two media
// volumes will happily use the same inode number for unrelated files.
func fileID(info fs.FileInfo) (fileKey, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return fileKey{}, false
	}
	return fileKey{dev: uint64(st.Dev), ino: uint64(st.Ino)}, true
}
