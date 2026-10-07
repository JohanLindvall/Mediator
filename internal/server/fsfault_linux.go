// SPDX-License-Identifier: MIT

//go:build linux

package server

import (
	"errors"
	"syscall"
)

// damagedFS reports a filesystem whose on-disk structure is damaged: XFS
// answers EUCLEAN, "structure needs cleaning", until it is repaired, which is
// what a remount after a crash leaves behind.
func damagedFS(err error) bool { return errors.Is(err, syscall.EUCLEAN) }
