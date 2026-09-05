package library

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// External subtitle files: "Movie.mkv" is matched by "Movie.srt",
// "Movie.en.srt", "Movie.eng.forced.srt" and so on, in the same directory.
// They are not indexed as library items — they are attachments discovered
// per directory and offered alongside their video.

// Subtitle is one external subtitle file offered for a video.
type Subtitle struct {
	Index int    `json:"index"` // position in the video's subtitle list
	Label string `json:"label"` // shown in the player menu
	Lang  string `json:"lang,omitempty"`

	path string // absolute path on disk
}

var subExts = map[string]bool{
	".srt": true, ".vtt": true, ".ass": true, ".ssa": true,
}

// SubTrack is a subtitle stream carried inside the video file itself, which
// is how a television release ships its captions: no sidecar, one MKV, the
// text muxed in beside the picture. A browser will not surface these from a
// stream it is playing — and cannot see them at all once playback is a
// conversion — so they are read by the probe that already runs when a video
// is opened, and served extracted.
type SubTrack struct {
	Stream int // ffmpeg's s:<n> ordinal, which is what extraction addresses
	Codec  string
	Lang   string
	Title  string
}

// textSubCodecs are the embedded forms that can become WebVTT. Bitmap
// subtitles — a DVD's, a Blu-ray's — are pictures, and pretending to list
// them would offer a menu entry that can never draw a line of text.
var textSubCodecs = map[string]bool{
	"subrip": true, "ass": true, "ssa": true, "webvtt": true, "mov_text": true, "text": true,
}

// IsSubtitle reports whether path names an external subtitle file.
func IsSubtitle(path string) bool {
	return subExts[strings.ToLower(filepath.Ext(path))]
}

// langNames maps the language token in a subtitle filename to a BCP-47 code
// and a display name. Both the 2- and 3-letter forms and the English name
// are accepted, since all three show up in the wild.
var langNames = map[string][2]string{
	"en": {"en", "English"}, "eng": {"en", "English"}, "english": {"en", "English"},
	"sv": {"sv", "Swedish"}, "swe": {"sv", "Swedish"}, "swedish": {"sv", "Swedish"},
	"no": {"no", "Norwegian"}, "nor": {"no", "Norwegian"}, "norwegian": {"no", "Norwegian"},
	"da": {"da", "Danish"}, "dan": {"da", "Danish"}, "danish": {"da", "Danish"},
	"fi": {"fi", "Finnish"}, "fin": {"fi", "Finnish"}, "finnish": {"fi", "Finnish"},
	"de": {"de", "German"}, "ger": {"de", "German"}, "deu": {"de", "German"}, "german": {"de", "German"},
	"fr": {"fr", "French"}, "fre": {"fr", "French"}, "fra": {"fr", "French"}, "french": {"fr", "French"},
	"es": {"es", "Spanish"}, "spa": {"es", "Spanish"}, "spanish": {"es", "Spanish"},
	"it": {"it", "Italian"}, "ita": {"it", "Italian"}, "italian": {"it", "Italian"},
	"nl": {"nl", "Dutch"}, "dut": {"nl", "Dutch"}, "nld": {"nl", "Dutch"}, "dutch": {"nl", "Dutch"},
	"pt": {"pt", "Portuguese"}, "por": {"pt", "Portuguese"}, "portuguese": {"pt", "Portuguese"},
	"pl": {"pl", "Polish"}, "pol": {"pl", "Polish"}, "polish": {"pl", "Polish"},
	"ru": {"ru", "Russian"}, "rus": {"ru", "Russian"}, "russian": {"ru", "Russian"},
	"ja": {"ja", "Japanese"}, "jpn": {"ja", "Japanese"}, "japanese": {"ja", "Japanese"},
	"zh": {"zh", "Chinese"}, "chi": {"zh", "Chinese"}, "zho": {"zh", "Chinese"}, "chinese": {"zh", "Chinese"},
	"ko": {"ko", "Korean"}, "kor": {"ko", "Korean"}, "korean": {"ko", "Korean"},
	"cs": {"cs", "Czech"}, "cze": {"cs", "Czech"}, "ces": {"cs", "Czech"},
	"hu": {"hu", "Hungarian"}, "hun": {"hu", "Hungarian"},
	"tr": {"tr", "Turkish"}, "tur": {"tr", "Turkish"},
	"ar": {"ar", "Arabic"}, "ara": {"ar", "Arabic"},
	"he": {"he", "Hebrew"}, "heb": {"he", "Hebrew"},
	"el": {"el", "Greek"}, "gre": {"el", "Greek"}, "ell": {"el", "Greek"},
	"ro": {"ro", "Romanian"}, "rum": {"ro", "Romanian"}, "ron": {"ro", "Romanian"},
}

// describeSuffix turns the part of a subtitle filename that follows the
// video's name ("en", "eng.forced", "" …) into a label and language code.
func describeSuffix(suffix string) (label, lang string) {
	if suffix == "" {
		return "Subtitles", ""
	}
	var parts []string
	for _, p := range strings.FieldsFunc(suffix, func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == ' '
	}) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "Subtitles", ""
	}
	var words []string
	for i, p := range parts {
		if l, ok := langNames[strings.ToLower(p)]; ok && lang == "" {
			lang = l[0]
			words = append(words, l[1])
			continue
		}
		// Keep other tokens ("forced", "sdh", "cc") as written, capitalized.
		w := p
		if i == 0 || lang != "" {
			w = strings.ToUpper(p[:1]) + p[1:]
		}
		words = append(words, w)
	}
	return strings.Join(words, " "), lang
}

// addSub records a subtitle file. Caller must hold l.mu.
func (l *Library) addSub(path string) {
	dir := filepath.Dir(path)
	for _, p := range l.subsByDir[dir] {
		if p == path {
			return
		}
	}
	l.subsByDir[dir] = append(l.subsByDir[dir], path)
}

// removeSub drops a subtitle file (or every one under a removed directory).
func (l *Library) removeSub(path string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	dir := filepath.Dir(path)
	subs := l.subsByDir[dir]
	for i, p := range subs {
		if p == path {
			l.subsByDir[dir] = append(subs[:i], subs[i+1:]...)
			if len(l.subsByDir[dir]) == 0 {
				delete(l.subsByDir, dir)
			}
			return true
		}
	}
	// A removed directory takes its subtitles with it.
	prefix := path + string(filepath.Separator)
	removed := false
	for d := range l.subsByDir {
		if d == path || strings.HasPrefix(d, prefix) {
			delete(l.subsByDir, d)
			removed = true
		}
	}
	return removed
}

// Subtitles lists the external subtitle files that belong to an item, in a
// stable order. Only videos have them.
func (l *Library) Subtitles(it Item) []Subtitle {
	if it.Kind != KindVideo {
		return nil
	}
	// An archived video is matched by its member name and by the name of
	// the archive itself, both alongside the rar volumes.
	dir := filepath.Dir(it.Path)
	// The name as the filesystem spells it, not the decoded one: these stems
	// are matched against real directory entries.
	stems := []string{stemOf(filepath.Base(it.Path))}
	if it.Archived() {
		if rarPath, _, ok := strings.Cut(it.Path, "\x00"); ok {
			dir = filepath.Dir(rarPath)
			stems = append(stems, stemOf(filepath.Base(rarPath)))
		}
	}

	l.mu.RLock()
	paths := slices.Clone(l.subsByDir[dir])
	l.mu.RUnlock()
	slices.Sort(paths)

	var out []Subtitle
	for _, p := range paths {
		base := stemOf(filepath.Base(p))
		for _, stem := range stems {
			suffix, ok := matchStem(base, stem)
			if !ok {
				continue
			}
			label, lang := describeSuffix(suffix)
			out = append(out, Subtitle{Index: len(out), Label: label, Lang: lang, path: p})
			break
		}
	}
	// The tracks inside the file follow the sidecars, continuing the same
	// index — one list, one numbering, and the client never needs to know
	// which kind it picked. Order is stable on both halves: the sidecars are
	// sorted, and the streams come in the file's own order.
	for _, t := range it.EmbSubs {
		label := t.Title
		if label == "" {
			if t.Lang != "" {
				label = t.Lang
			} else {
				label = fmt.Sprintf("Track %d", t.Stream+1)
			}
		}
		out = append(out, Subtitle{Index: len(out), Label: label, Lang: t.Lang})
	}
	return out
}

// EmbeddedSubStream resolves a combined subtitle index to the stream ordinal
// inside the file, where the index names an embedded track rather than a
// sidecar. The split point is however many sidecars the listing put first.
func (l *Library) EmbeddedSubStream(it Item, index int) (int, bool) {
	subs := l.Subtitles(it)
	if index < 0 || index >= len(subs) || subs[index].path != "" {
		return 0, false
	}
	k := index - (len(subs) - len(it.EmbSubs))
	if k < 0 || k >= len(it.EmbSubs) {
		return 0, false
	}
	return it.EmbSubs[k].Stream, true
}

// SubtitlePath resolves one of an item's subtitles to a file path — for the
// sidecars, which are the entries that have one. An embedded track answers
// false here and is resolved by EmbeddedSubStream instead; answering true
// with an empty path sent the handler off to read a file called nothing.
func (l *Library) SubtitlePath(it Item, index int) (string, bool) {
	subs := l.Subtitles(it)
	if index < 0 || index >= len(subs) || subs[index].path == "" {
		return "", false
	}
	return subs[index].path, true
}

// matchStem reports whether a subtitle base name belongs to a video stem,
// returning the part that follows it ("" for an exact match).
func matchStem(base, stem string) (suffix string, ok bool) {
	if strings.EqualFold(base, stem) {
		return "", true
	}
	// The stem is compared as a prefix and the separator read after it; both
	// stay on rune boundaries, or a name whose stem ends inside a multibyte
	// letter would have a continuation byte tested as its separator.
	if len(base) > len(stem)+1 && utf8.RuneStart(base[len(stem)]) &&
		strings.EqualFold(base[:len(stem)], stem) &&
		(base[len(stem)] == '.' || base[len(stem)] == '_' || base[len(stem)] == '-') {
		return base[len(stem)+1:], true
	}
	return "", false
}

func stemOf(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}
