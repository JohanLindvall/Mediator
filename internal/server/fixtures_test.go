// SPDX-License-Identifier: MIT

package server

import "bytes"

// silentMP3 is a track a probe takes for one: twelve MPEG-1 Layer III frames
// of silence at 128 kbit/s and 44.1 kHz, a third of a second. A fixture that
// is a name and a few letters is not media, and the library leaves a track
// like that out of every listing, count and release (library.Item.listed) —
// so a test that wants a song in a release has to write one.
func silentMP3() string {
	frame := append([]byte{0xFF, 0xFB, 0x90, 0x64}, make([]byte, 413)...)
	return string(bytes.Repeat(frame, 12))
}
