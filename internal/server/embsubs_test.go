package server

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// writeSubbedMKV writes a clip with a SubRip stream muxed inside it — the
// shape a television release ships its captions in: no sidecar, one file.
func writeSubbedMKV(t *testing.T, path string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if library.FFprobePath() == "" {
		t.Skip("ffprobe not installed")
	}
	srt := filepath.Join(t.TempDir(), "cues.srt")
	if err := os.WriteFile(srt, []byte(
		"1\n00:00:00,500 --> 00:00:01,500\nA line somebody says\n\n"+
			"2\n00:00:01,600 --> 00:00:02,000\nAnd the reply\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=2",
		"-i", srt,
		"-map", "0:v", "-map", "1:s",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:s", "srt", "-metadata:s:s:0", "language=eng",
		"-metadata:s:s:0", "title=English (CC)", "-y", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not mux subtitles: %v: %s", err, out)
	}
}

// The captions a television release carries are inside the file, and until
// this they were simply not offered: the listing looked only at sidecars,
// and the file in front of the report had a subtitle stream and no sidecar
// anywhere on the disk.
func TestEmbeddedSubtitlesAreOfferedAndServed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "An Episode.mkv")
	writeSubbedMKV(t, path)
	ts, lib := flagServer(t, dir)
	id := library.PathID(path)
	lib.EnsureCodecs(t.Context(), id)

	get := func(u string) (int, string) {
		res, err := http.Get(ts.URL + u)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, string(body)
	}

	code, body := get("/api/subs/" + id)
	if code != http.StatusOK || !strings.Contains(body, "English (CC)") {
		t.Fatalf("listing answered %d %q; want the embedded track offered", code, body)
	}

	// Served as WebVTT with the cues intact, exactly as a sidecar would be.
	code, body = get("/api/subs/" + id + "/0")
	if code != http.StatusOK {
		t.Fatalf("the track answered %d", code)
	}
	if !strings.HasPrefix(body, "WEBVTT") || !strings.Contains(body, "A line somebody says") {
		t.Errorf("the track came back as %q; want WebVTT with the cues", body[:min(len(body), 80)])
	}

	// The same conversions a sidecar gets: SubRip for a television, and the
	// shift that rebases cues onto a converted stream's clock.
	if _, body = get("/api/subs/" + id + "/0?format=srt"); !strings.Contains(body, "00:00:00,500") {
		t.Errorf("srt came back as %q", body[:min(len(body), 80)])
	}
	// The shift rebases cues onto a converted stream's clock, which starts
	// at the keyframe: it subtracts. A cue at 0.5 s under a 0.4 s shift
	// lands at 0.1 — and ffmpeg writes its timestamps without the hour
	// field, which the parser has to take as readily as a sidecar's.
	if _, body = get("/api/subs/" + id + "/0?shift=0.4"); !strings.Contains(body, "00:00:00.100") {
		t.Errorf("shifted cues came back as %q", body[:min(len(body), 120)])
	}

	// The whole-file read happens once: the second ask is the cache.
	if code, _ = get("/api/subs/" + id + "/0"); code != http.StatusOK {
		t.Errorf("the cached track answered %d", code)
	}
}

// A sidecar and an embedded track share one numbering, sidecars first — the
// client picks an index and never needs to know which kind it chose.
func TestSidecarAndEmbeddedShareOneNumbering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "An Episode.mkv")
	writeSubbedMKV(t, path)
	if err := os.WriteFile(filepath.Join(dir, "An Episode.swe.srt"), []byte(
		"1\n00:00:00,500 --> 00:00:01,500\nEn rad någon säger\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, lib := flagServer(t, dir)
	id := library.PathID(path)
	lib.EnsureCodecs(t.Context(), id)

	res, err := http.Get(ts.URL + "/api/subs/" + id)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	listing := string(body)
	if !strings.Contains(listing, "Swedish") || !strings.Contains(listing, "English (CC)") {
		t.Fatalf("listing %q; want the sidecar and the embedded track together", listing)
	}

	// Index 0 is the sidecar; index 1 reaches into the file.
	res, err = http.Get(ts.URL + "/api/subs/" + id + "/1")
	if err != nil {
		t.Fatal(err)
	}
	vtt, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(vtt), "A line somebody says") {
		t.Errorf("index 1 served %q; want the embedded cues", string(vtt[:min(len(vtt), 80)]))
	}
}

// AirPlay hands a receiver a URL and nothing else, so the only way subtitles
// reach one is inside what the URL describes: a master playlist naming them
// as renditions. The session and its segments are untouched — one conversion
// serves every choice, and the choice marks a rendition DEFAULT.
func TestHLSMasterCarriesSubtitleRenditions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "An Episode.mkv")
	writeSubbedMKV(t, path)
	ts, lib := flagServer(t, dir)
	id := library.PathID(path)
	lib.EnsureCodecs(t.Context(), id)

	get := func(u string) (int, string) {
		res, err := http.Get(ts.URL + u)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, string(body)
	}

	code, master := get("/api/hls/" + id + "/index.m3u8?mode=audio&sub=0")
	if code != http.StatusOK {
		t.Fatalf("the playlist answered %d: %s", code, master)
	}
	if !strings.Contains(master, "#EXT-X-MEDIA:TYPE=SUBTITLES") ||
		!strings.Contains(master, `NAME="English (CC)"`) {
		t.Fatalf("no subtitle rendition in the master:\n%s", master)
	}
	if !strings.Contains(master, "DEFAULT=YES") {
		t.Errorf("the chosen subtitle is not marked DEFAULT:\n%s", master)
	}
	if !strings.Contains(master, `SUBTITLES="text"`) || !strings.Contains(master, "BANDWIDTH=") {
		t.Errorf("the stream entry does not join the subtitle group:\n%s", master)
	}

	// The names the master uses resolve under the session path, exactly as
	// segments do — which is what carries the signed prefix for a receiver.
	sid := ""
	for _, line := range strings.Split(master, "\n") {
		if strings.HasSuffix(strings.TrimSpace(line), "/media.m3u8") {
			sid = strings.TrimSuffix(strings.TrimSpace(line), "/media.m3u8")
		}
	}
	if sid == "" {
		t.Fatalf("no media playlist named in the master:\n%s", master)
	}
	code, media := get("/api/hls/" + id + "/" + sid + "/media.m3u8")
	if code != http.StatusOK || !strings.Contains(media, "#EXTINF") {
		t.Fatalf("the media playlist answered %d:\n%s", code, media)
	}
	if strings.Contains(media, sid+"/") {
		t.Errorf("segment names are qualified inside the session path, so they resolve twice:\n%s", media)
	}
	code, subpl := get("/api/hls/" + id + "/" + sid + "/sub0.m3u8")
	if code != http.StatusOK || !strings.Contains(subpl, "sub0.vtt") ||
		!strings.Contains(subpl, "#EXT-X-ENDLIST") {
		t.Fatalf("the rendition playlist answered %d:\n%s", code, subpl)
	}
	code, vtt := get("/api/hls/" + id + "/" + sid + "/sub0.vtt")
	if code != http.StatusOK || !strings.HasPrefix(vtt, "WEBVTT") ||
		!strings.Contains(vtt, "A line somebody says") {
		t.Fatalf("the rendition answered %d: %q", code, vtt[:min(len(vtt), 80)])
	}

	// Without a choice the renditions are offered and none is default: the
	// menu is the offer, and AUTOSELECT on everything would put subtitles on
	// screen by system language that nobody asked for.
	_, master = get("/api/hls/" + id + "/index.m3u8?mode=audio")
	if strings.Contains(master, "DEFAULT=YES") {
		t.Errorf("a subtitle was defaulted with nothing chosen:\n%s", master)
	}

	// A film with no subtitles keeps yesterday's shape: the media playlist
	// itself, no master in the way.
	plain := filepath.Join(dir, "plain.mkv")
	writeMKV(t, plain, 2)
	lib.Scan(nil)
	pid := library.PathID(plain)
	lib.EnsureCodecs(t.Context(), pid)
	_, direct := get("/api/hls/" + pid + "/index.m3u8?mode=audio")
	if strings.Contains(direct, "TYPE=SUBTITLES") || !strings.Contains(direct, "#EXTINF") {
		t.Errorf("a film without subtitles was given a master:\n%s", direct)
	}
}
