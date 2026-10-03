package server

import (
	"bytes"
	"errors"
)

var errBufferLimit = errors.New("output exceeds byte limit")

// boundedBuffer stops a child process's output before it can consume
// unbounded memory. Unlike an embedded bytes.Buffer it exposes no ReadFrom
// method that io.Copy could use to bypass Write's limit.
type boundedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n, err := b.buf.Write(p[:min(len(p), max(0, b.max-b.buf.Len()))])
	if err == nil && n < len(p) {
		err = errBufferLimit
	}
	return n, err
}

func (b *boundedBuffer) Bytes() []byte { return b.buf.Bytes() }
func (b *boundedBuffer) Len() int      { return b.buf.Len() }
