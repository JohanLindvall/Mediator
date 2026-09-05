package server

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Browsers only take WebVTT in <track>, so external .srt/.ass files are
// converted on the way out. Subtitle files in the wild are frequently not
// UTF-8 either, so the text is transcoded first.

// decodeText returns data as UTF-8, honouring a BOM and falling back to
// Latin-1 for bytes that are not valid UTF-8 (the common case for old .srt).
func decodeText(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return string(data[3:])
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return decodeUTF16(data[2:], false)
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return decodeUTF16(data[2:], true)
	}
	if utf8.Valid(data) {
		return string(data)
	}
	// Latin-1: every byte is its own code point.
	var b strings.Builder
	b.Grow(len(data) * 2)
	for _, c := range data {
		b.WriteRune(rune(c))
	}
	return b.String()
}

func decodeUTF16(data []byte, bigEndian bool) string {
	u := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if bigEndian {
			u = append(u, uint16(data[i])<<8|uint16(data[i+1]))
		} else {
			u = append(u, uint16(data[i+1])<<8|uint16(data[i]))
		}
	}
	return string(utf16.Decode(u))
}

// srtTime matches an SRT timestamp, whose fractional separator is a comma.
var srtTime = regexp.MustCompile(`(\d{1,2}):(\d{2}):(\d{2})[,.](\d{1,3})`)

// assTag matches ASS inline override blocks, e.g. {\an8\i1}.
var assTag = regexp.MustCompile(`\{[^}]*\}`)

// ToVTT converts a subtitle file to WebVTT. name selects the parser by
// extension; .vtt passes through (with a header added if missing).
func ToVTT(name string, data []byte) ([]byte, error) {
	text := strings.ReplaceAll(decodeText(data), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	switch strings.ToLower(filepath.Ext(name)) {
	case ".vtt":
		if !strings.HasPrefix(strings.TrimLeft(text, "\ufeff \t\n"), "WEBVTT") {
			text = "WEBVTT\n\n" + text
		}
		return []byte(text), nil
	case ".srt":
		return []byte(srtToVTT(text)), nil
	case ".ass", ".ssa":
		return []byte(assToVTT(text)), nil
	}
	return nil, fmt.Errorf("unsupported subtitle format %q", filepath.Ext(name))
}

// vttTime matches a WebVTT timestamp, whose hours are optional.
var vttTime = regexp.MustCompile(`(?:(\d+):)?(\d{1,2}):(\d{2})\.(\d{1,3})`)

// shiftVTT moves every cue earlier by shift seconds, dropping the ones that
// end before the new zero and clamping one that straddles it.
//
// Cue times are absolute in the subtitle file, but a live transcode restarts
// the media clock at the keyframe it seeks to. The browser matches cues
// against that clock, so without rebasing them onto the same origin the
// subtitles of a transcoded video are simply wrong — by minutes, once the
// viewer has seeked.
func shiftVTT(data []byte, shift float64) []byte {
	if shift <= 0 {
		return data
	}
	var out strings.Builder
	out.Grow(len(data))

	// Cues are separated by blank lines, and one that falls off the front has
	// to go as a whole — its identifier and text with it.
	var block []string
	flush := func() {
		keep := true
		for i, line := range block {
			if !strings.Contains(line, "-->") {
				continue
			}
			shifted, ok := shiftCueTiming(line, shift)
			if !ok {
				keep = false
				break
			}
			block[i] = shifted
		}
		if keep {
			for _, line := range block {
				out.WriteString(line)
				out.WriteByte('\n')
			}
		}
		block = block[:0]
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			out.WriteByte('\n')
			continue
		}
		block = append(block, line)
	}
	flush()
	return []byte(out.String())
}

// shiftCueTiming rewrites the two timestamps of a cue timing line, reporting
// false when the whole cue has passed before the new origin. Anything else on
// the line — cue settings such as line: or align: — is left alone.
func shiftCueTiming(line string, shift float64) (string, bool) {
	at := vttTime.FindAllStringIndex(line, -1)
	if len(at) < 2 {
		return line, true // not a timing line after all
	}
	start, ok1 := parseVTTTime(line[at[0][0]:at[0][1]])
	end, ok2 := parseVTTTime(line[at[1][0]:at[1][1]])
	if !ok1 || !ok2 {
		return line, true
	}
	if end-shift <= 0 {
		return "", false
	}
	return line[:at[0][0]] + formatVTTTime(max(start-shift, 0)) +
		line[at[0][1]:at[1][0]] + formatVTTTime(end-shift) +
		line[at[1][1]:], true
}

func parseVTTTime(s string) (float64, bool) {
	m := vttTime.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	hours, _ := strconv.Atoi(m[1]) // empty when the timestamp has no hours
	minutes, _ := strconv.Atoi(m[2])
	seconds, _ := strconv.Atoi(m[3])
	frac := m[4]
	for len(frac) < 3 {
		frac += "0"
	}
	millis, _ := strconv.Atoi(frac)
	return float64(hours*3600+minutes*60+seconds) + float64(millis)/1000, true
}

func formatVTTTime(t float64) string {
	millis := int(t*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d:%02d.%03d",
		millis/3_600_000, millis/60_000%60, millis/1000%60, millis%1000)
}

// srtToVTT rewrites SRT cue timings (comma decimals) as WebVTT ones and
// prepends the required header. Cue text is carried over as-is.
func srtToVTT(text string) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "-->") {
			// Also drop the optional trailing position coordinates.
			if i := strings.Index(line, "X1:"); i >= 0 {
				line = line[:i]
			}
			line = srtTime.ReplaceAllString(strings.TrimSpace(line), "$1:$2:$3.$4")
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// assToVTT extracts the dialogue lines of an ASS/SSA script. Styling,
// positioning and karaoke tags are dropped — the text and timings survive.
func assToVTT(text string) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	startCol, endCol, textCol, cols := 1, 2, 9, 10
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(trimmed), "format:") && textCol == 9 {
			fields := strings.Split(trimmed[len("format:"):], ",")
			for i, f := range fields {
				switch strings.ToLower(strings.TrimSpace(f)) {
				case "start":
					startCol = i
				case "end":
					endCol = i
				case "text":
					textCol = i
				}
			}
			cols = len(fields)
			continue
		}
		if !strings.HasPrefix(strings.ToLower(trimmed), "dialogue:") {
			continue
		}
		// Text is the last field and may itself contain commas.
		fields := strings.SplitN(strings.TrimSpace(trimmed[len("dialogue:"):]), ",", cols)
		if len(fields) <= textCol || textCol >= cols {
			continue
		}
		start, ok1 := assTime(fields[startCol])
		end, ok2 := assTime(fields[endCol])
		if !ok1 || !ok2 {
			continue
		}
		cue := assTag.ReplaceAllString(fields[textCol], "")
		cue = strings.ReplaceAll(cue, `\N`, "\n")
		cue = strings.ReplaceAll(cue, `\n`, "\n")
		cue = strings.TrimSpace(strings.ReplaceAll(cue, `\h`, " "))
		if cue == "" {
			continue
		}
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", start, end, cue)
	}
	return b.String()
}

// assTime converts an ASS timestamp (h:mm:ss.cc) to WebVTT (hh:mm:ss.mmm).
func assTime(s string) (string, bool) {
	m := srtTime.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", false
	}
	frac := m[4]
	for len(frac) < 3 { // centiseconds in ASS, milliseconds in WebVTT
		frac += "0"
	}
	return fmt.Sprintf("%02s:%s:%s.%s", m[1], m[2], m[3], frac[:3]), true
}

// ToSRT converts a subtitle file to SubRip, which is what a television
// wants: a DLNA renderer is pointed at a sidecar file by URL and reads it
// itself, and SRT is the one format they all read. WebVTT — the format the
// browser insists on — is the one they mostly do not.
//
// It goes through ToVTT rather than parsing each input format again: that is
// where the encoding is settled (BOMs, UTF-16, the Latin-1 files that
// dominate in the wild) and where ASS override tags are stripped. What is
// left is mechanical.
func ToSRT(name string, data []byte) ([]byte, error) {
	vtt, err := ToVTT(name, data)
	if err != nil {
		return nil, err
	}
	return vttToSRT(string(vtt)), nil
}

// vttToSRT rewrites cues into SubRip: numbered, with a comma before the
// milliseconds and hours always written out. Everything WebVTT has that
// SubRip does not — the header, notes, styles, regions, and the cue settings
// after the timing — is dropped rather than passed through, a player that
// meets one commonly showing it as a line of text on the screen.
func vttToSRT(text string) []byte {
	var out strings.Builder
	n := 0
	blocks := strings.Split(strings.TrimSpace(text), "\n\n")
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) == 0 {
			continue
		}
		head := strings.TrimSpace(lines[0])
		if strings.HasPrefix(head, "WEBVTT") || strings.HasPrefix(head, "NOTE") ||
			strings.HasPrefix(head, "STYLE") || strings.HasPrefix(head, "REGION") {
			continue
		}
		// The timing is the first line holding an arrow; anything before it
		// is the cue's identifier, which SubRip numbers itself.
		timing := -1
		for i, line := range lines {
			if strings.Contains(line, "-->") {
				timing = i
				break
			}
		}
		if timing < 0 {
			continue
		}
		body := strings.TrimSpace(strings.Join(lines[timing+1:], "\n"))
		if body == "" {
			continue
		}
		n++
		fmt.Fprintf(&out, "%d\n%s\n%s\n\n", n, srtTiming(lines[timing]), body)
	}
	return []byte(out.String())
}

// srtTiming turns one WebVTT timing line into a SubRip one.
func srtTiming(line string) string {
	start, end := "00:00:00,000", "00:00:00,000"
	if times := vttTime.FindAllString(line, 2); len(times) == 2 {
		start, end = srtStamp(times[0]), srtStamp(times[1])
	}
	return start + " --> " + end
}

// srtStamp normalises one timestamp: WebVTT may leave the hours off and may
// write fewer than three digits of milliseconds; SubRip may not.
func srtStamp(s string) string {
	m := vttTime.FindStringSubmatch(s)
	if m == nil {
		return "00:00:00,000"
	}
	h, _ := strconv.Atoi(m[1])
	mm, _ := strconv.Atoi(m[2])
	ss, _ := strconv.Atoi(m[3])
	ms := (m[4] + "000")[:3]
	return fmt.Sprintf("%02d:%02d:%02d,%s", h, mm, ss, ms)
}
