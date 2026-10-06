// SPDX-License-Identifier: MIT

package server

import (
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

func TestDTSSupportDoesNotImplyTrueHDSupport(t *testing.T) {
	for _, codec := range []string{"truehd", "mlp"} {
		it := library.Item{Name: "film.mkv", Kind: library.KindVideo, VCodec: "h264", ACodec: codec}
		if _, convert := castSoundKind(it, func(string) bool { return true }); !convert {
			t.Errorf("DTS capability was mistaken for %s support", codec)
		}
	}
}
