// SPDX-License-Identifier: MIT

package server

import (
	"math"
	"strconv"
)

const maxMediaSeconds = 1e7

// All playback routes use the same finite, bounded clock. ParseFloat also
// accepts NaN and infinities, which cannot be sent as JSON or to ffmpeg.
func mediaSeconds(value string) float64 {
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(n) || n < 0 || n > maxMediaSeconds {
		return 0
	}
	return n
}
