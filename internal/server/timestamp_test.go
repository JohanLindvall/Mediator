// SPDX-License-Identifier: MIT

package server

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// AVI holds decode times, but no presentation times for reordered H.264.
// Even an honest B-pyramid stutters when a copy assigns its decode times to
// the displayed frames. Exercise the browser routes with a silent synthetic
// clip: audio and its clock have nothing to do with this fault.
func TestMissingPresentationTimesPlayback(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil || library.FFprobePath() == "" {
		t.Skip("ffmpeg and ffprobe required")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "reordered.avi")
	cmd := exec.CommandContext(t.Context(), ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=25:duration=4",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-bf", "3", "-x264-params", "b-adapt=0", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not build reordered AVI: %v: %s", err, out)
	}
	id := library.PathID(path)
	for _, route := range []string{"item", "remux", "pipe", "seek", "hls"} {
		t.Run(route, func(t *testing.T) {
			// Each route is the first request: none may rely on an item fetch
			// having probed the codecs or populated the timing verdict first.
			ts, _, _ := serverUnderTest(t, dir)
			get := func(url string, want int) (*http.Response, []byte) {
				t.Helper()
				res, err := http.Get(url)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				body, err := io.ReadAll(res.Body)
				if err != nil {
					t.Fatal(err)
				}
				if res.StatusCode != want {
					t.Fatalf("status %d, want %d: %q", res.StatusCode, want, body[:min(len(body), 200)])
				}
				return res, body
			}
			var body []byte
			ext := ".mp4"
			switch route {
			case "item":
				_, body = get(ts.URL+"/api/item/"+id, http.StatusOK)
				var it library.Item
				if err := json.Unmarshal(body, &it); err != nil {
					t.Fatal(err)
				}
				if !it.Reencode {
					t.Fatal("the player was not told to convert the picture")
				}
				return
			case "remux":
				get(ts.URL+"/api/remux/"+id, http.StatusNotFound)
				return
			case "pipe", "seek":
				query := "?mode=audio"
				if route == "seek" {
					query += "&t=1.2"
				}
				_, body = get(ts.URL+"/api/transcode/"+id+query, http.StatusOK)
			case "hls":
				res, playlist := get(ts.URL+"/api/hls/"+id+"/index.m3u8?mode=audio", http.StatusOK)
				for line := range strings.SplitSeq(string(playlist), "\n") {
					if !strings.HasSuffix(strings.TrimSpace(line), ".ts") {
						continue
					}
					url, err := res.Request.URL.Parse(strings.TrimSpace(line))
					if err != nil {
						t.Fatal(err)
					}
					_, body = get(url.String(), http.StatusOK)
					break
				}
				if len(body) == 0 {
					t.Fatalf("no video segment in playlist: %s", playlist)
				}
				ext = ".ts"
			}
			out := filepath.Join(t.TempDir(), "converted"+ext)
			if err := os.WriteFile(out, body, 0o600); err != nil {
				t.Fatal(err)
			}
			assertFrameCadence(t, out, 0.04)
		})
	}
}

// Read presentation times in decoded frame order, which is the order the
// browser sees. Packet order is decode order and would conceal the fault.
func assertFrameCadence(t *testing.T, path string, interval float64) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), library.FFprobePath(), "-v", "error",
		"-select_streams", pictureStream, "-show_entries", "frame=pts_time",
		"-of", "json", path).Output()
	if err != nil {
		t.Fatalf("probe converted frames: %v", err)
	}
	var doc struct {
		Frames []struct {
			PTS string `json:"pts_time"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Frames) < 25 {
		t.Fatalf("only %d decoded frames", len(doc.Frames))
	}
	var last float64
	for i, f := range doc.Frames {
		pts, err := strconv.ParseFloat(f.PTS, 64)
		if err != nil || math.IsNaN(pts) || math.IsInf(pts, 0) {
			t.Fatalf("frame %d has no usable presentation time: %q", i, f.PTS)
		}
		if i > 0 && math.Abs(pts-last-interval) > 0.00001 {
			t.Fatalf("frame %d: presentation time %.6f follows %.6f, want %.3f s interval", i, pts, last, interval)
		}
		last = pts
	}
}
