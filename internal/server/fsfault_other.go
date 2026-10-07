// SPDX-License-Identifier: MIT

//go:build !linux

package server

// damagedFS has no error to recognise here: EUCLEAN, the answer a damaged
// XFS gives, is Linux's, and no other platform's syscall package defines it.
func damagedFS(error) bool { return false }
