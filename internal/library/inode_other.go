//go:build !unix

package library

import "io/fs"

// fileID has no portable equivalent outside unix; without it every path is
// treated as its own file and nothing is deduplicated.
func fileID(fs.FileInfo) (fileKey, bool) { return fileKey{}, false }
