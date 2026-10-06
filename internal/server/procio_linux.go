// SPDX-License-Identifier: MIT

//go:build linux

package server

import (
	"bytes"
	"os"
	"strconv"
)

// bytesRead is how much a process has read, by the kernel's account: rchar
// in /proc/<pid>/io counts every byte its reads returned, from a file, a pipe
// or a socket alike — and a read of archived content over the loopback stream
// is a socket.
func bytesRead(pid int) (int64, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/io")
	if err != nil {
		return 0, false
	}
	for line := range bytes.SplitSeq(b, []byte("\n")) {
		if v, ok := bytes.CutPrefix(line, []byte("rchar:")); ok {
			n, err := strconv.ParseInt(string(bytes.TrimSpace(v)), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}
