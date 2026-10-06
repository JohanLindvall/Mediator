// SPDX-License-Identifier: MIT

//go:build !linux

package server

// bytesRead cannot be answered here, so a read is reported as under way
// without saying how far.
func bytesRead(int) (int64, bool) { return 0, false }
