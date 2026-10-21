// SPDX-License-Identifier: MIT

package server

import (
	"bytes"
	"context"
	"image"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/JohanLindvall/Mediator/internal/mediatest"
)

// A cover the file carries is not its film. ffmpeg lists a WMV's publisher's
// picture ahead of the film, and everything here that took the first video
// stream took the cover: a conversion failed before writing a byte, the MP4
// muxer refusing the re-encoded cover, and the soundtrack's conversion
// copied the cover where the film belonged.
func TestACoverIsConvertedAsTheFilm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "covered.mp4")
	mediatest.CoverFirst(t, path, 2)
	ts, _ := flagServer(t, dir)
	film := strconv.Itoa(mediatest.FilmWidth) + "," + strconv.Itoa(mediatest.FilmHeight)

	for mode, want := range map[string]string{"": "h264," + film, "audio": "mpeg4," + film} {
		res, err := http.Get(ts.URL + "/api/transcode/" + library.PathID(path) + "?mode=" + mode)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode == http.StatusNotImplemented {
			t.Skip("no ffmpeg")
		}
		if res.StatusCode != http.StatusOK || len(body) == 0 {
			t.Fatalf("mode %q: answered %d with %d bytes: %s", mode, res.StatusCode, len(body), body)
		}
		out := filepath.Join(t.TempDir(), "out.mp4")
		if err := os.WriteFile(out, body, 0o644); err != nil {
			t.Fatal(err)
		}
		probe, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "V:0",
			"-show_entries", "stream=codec_name,width,height", "-of", "csv=p=0", out).Output()
		if err != nil {
			t.Fatalf("mode %q: ffprobe of the conversion: %v", mode, err)
		}
		if got := strings.TrimSpace(string(probe)); got != want {
			t.Errorf("mode %q: the conversion's picture is %q, want the film's: %q", mode, got, want)
		}
	}
}

// A tile is a frame of the film, not its cover — and a file with a cover and
// no film, sound under a video's name, still shows the picture it carries.
func TestACoverIsTheTileOnlyWithoutAFilm(t *testing.T) {
	const width = 160
	th := NewThumbnailer(nil, nil, testLog())
	if th.FFmpegPath() == "" {
		t.Skip("no ffmpeg")
	}
	for _, c := range []struct {
		name  string
		write func(testing.TB, string, int)
		wantH int
	}{
		{"covered.mp4", mediatest.CoverFirst, width * mediatest.FilmHeight / mediatest.FilmWidth},
		{"sound.mp4", mediatest.CoverOnly, width}, // the square cover
	} {
		path := filepath.Join(t.TempDir(), c.name)
		c.write(t, path, 4)
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		it := library.Item{
			ID: library.PathID(path), Name: c.name, Path: path, Kind: library.KindVideo,
			Size: info.Size(), ModTime: info.ModTime().UnixMilli(), Duration: 4000,
		}
		data, err := th.fromVideo(context.Background(), it, width)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if cfg.Width != width || cfg.Height != c.wantH {
			t.Errorf("%s: a %dx%d tile, want %dx%d", c.name, cfg.Width, cfg.Height, width, c.wantH)
		}
	}
}
