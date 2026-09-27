package server

import (
	"math"
	"strconv"
)

// All playback routes use the same finite, bounded clock. ParseFloat also
// accepts NaN and infinities, which cannot be sent as JSON or to ffmpeg.
func mediaSeconds(value string) float64 {
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(n) || n < 0 || n > 1e7 {
		return 0
	}
	return n
}
