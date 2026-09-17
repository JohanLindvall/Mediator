package server

import (
	"strings"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// Go's built-in mime table calls a WebM file `audio/webm` — which is what one
// holding only sound is, and not what a video downloaded from a video site is.
// A television decides from that name alone, so it declined a film for being
// a soundtrack.
func TestVideoContainersAreNamedAsVideo(t *testing.T) {
	for _, ext := range []string{
		".mp4", ".mkv", ".webm", ".mov", ".avi", ".m4v", ".mpg", ".mpeg",
		".wmv", ".flv", ".3gp", ".vob", ".ts", ".mts", ".m2ts", ".divx", ".f4v", ".ogv",
	} {
		it := library.Item{Name: "a clip" + ext, Kind: library.KindVideo}
		if got := mimeFor(it); !strings.HasPrefix(got, "video/") {
			t.Errorf("%s is offered as %q; a set decides from that name alone", ext, got)
		}
	}
}

// A container has more than one name in circulation, and a set knows the ones
// its makers chose. Refusing a file it can demux perfectly well, over what it
// was called, is the one failure worth taking a second look at before giving
// up — measured against a real set, which lists video/x-matroska and neither
// spelling of WebM.
func TestCastFindsAnotherNameForTheSameBytes(t *testing.T) {
	takes := func(types ...string) func(string) bool {
		return func(t string) bool {
			for _, ok := range types {
				if ok == t {
					return true
				}
			}
			return false
		}
	}
	for _, c := range []struct {
		why    string
		typ    string
		accept []string
		want   string
	}{
		{"a WebM is a Matroska file, and this set says so in the other name",
			"video/webm", []string{"video/x-matroska", "video/mp4"}, "video/x-matroska"},
		{"one container, another spelling",
			"video/x-msvideo", []string{"video/avi"}, "video/avi"},
		{"and the third spelling of it",
			"video/x-msvideo", []string{"video/msvideo"}, "video/msvideo"},
		{"a transport stream is an MPEG stream",
			"video/mp2t", []string{"video/mpeg"}, "video/mpeg"},
		{"a set that lists none of them leaves nothing to try",
			"video/webm", []string{"video/mp4"}, ""},
		// A Matroska file is not necessarily a WebM one, so that alias runs
		// one way: naming an arbitrary MKV as WebM would promise a set VP8 or
		// VP9 and hand it anything at all.
		{"Matroska is never offered as WebM",
			"video/x-matroska", []string{"video/webm"}, ""},
	} {
		got, ok := castAlias(c.typ, takes(c.accept...))
		if !ok {
			got = ""
		}
		if got != c.want {
			t.Errorf("%s: found %q; want %q", c.why, got, c.want)
		}
	}
}

// A video that ships automatic dubs arrives as Matroska carrying one Opus
// track per language, and the only way to tell a television which language is
// wanted is to hand it a file holding just that one. The copy keeps the
// container: an MP4 of VP9 and Opus is a container neither the set nor the
// codecs asked for, where a copy into its own kind is lossless.
func TestCastChoosesTheSoundtrackByContainerCopy(t *testing.T) {
	dubbed := library.Item{
		ID: "abc", Name: "a talk.webm", Kind: library.KindVideo,
		VCodec: "vp9", ACodec: "opus",
		Tracks: []library.AudioTrack{{Index: 0, Lang: "ara"}, {Index: 1, Lang: "eng"}},
	}
	if kind, ok := castTrackKind(dubbed, "1"); !ok || kind != remuxTrack {
		t.Fatalf("a dubbed WebM asked for kind %q (%v); want the container copy", kind, ok)
	}
	if got := remuxMime(dubbed, remuxTrack); got != "video/webm" {
		t.Errorf("the copy is offered as %q; it is still a WebM", got)
	}
	// Nothing to choose between, so nothing to copy.
	one := dubbed
	one.Tracks = one.Tracks[:1]
	if _, ok := castTrackKind(one, "0"); ok {
		t.Error("a file with one soundtrack was copied to choose it")
	}
	// And a viewer who has chosen nothing is handed the file as it is.
	if _, ok := castTrackKind(dubbed, ""); ok {
		t.Error("a copy was made for a choice nobody expressed")
	}
	// An MP4-shaped release still takes the MP4 rewrap, which is what it has
	// always done and what its streams belong in.
	mp4ish := library.Item{
		ID: "def", Name: "a film.mkv", Kind: library.KindVideo,
		VCodec: "h264", ACodec: "aac",
		Tracks: []library.AudioTrack{{Index: 0}, {Index: 1}},
	}
	// Matroska is copyable in its own right, so it takes that path first —
	// lossless either way, and it keeps a container this set already listed.
	if kind, _ := castTrackKind(mp4ish, "1"); kind != remuxTrack {
		t.Errorf("an MKV asked for kind %q; want the container copy", kind)
	}
	avi := library.Item{
		ID: "ghi", Name: "a film.avi", Kind: library.KindVideo,
		VCodec: "h264", ACodec: "aac",
		Tracks: []library.AudioTrack{{Index: 0}, {Index: 1}},
	}
	if kind, ok := castTrackKind(avi, "1"); !ok || kind != remuxCopy {
		t.Errorf("an AVI asked for kind %q (%v); want the MP4 rewrap", kind, ok)
	}
}

// The two copies of one film are two files, and a third kind is a third file:
// serving one under another's name hands a viewer the wrong soundtrack with
// nothing on screen to say why.
func TestRemuxTrackIsItsOwnFile(t *testing.T) {
	it := library.Item{ID: "abc", ModTime: 17, Size: 42, Name: "a talk.webm"}
	name := remuxName(it, 1, remuxTrack)
	if !strings.HasSuffix(name, ".webm") {
		t.Errorf("a soundtrack copy of a WebM is called %q; it is still a WebM", name)
	}
	key, ok := remuxKeyFromName(name)
	if !ok {
		t.Fatalf("remuxKeyFromName(%q) did not parse", name)
	}
	if got := key[strings.LastIndex(key, "|")+1:]; got != string(remuxTrack) {
		t.Errorf("read back kind %q; want %q", got, remuxTrack)
	}
	for _, other := range []remuxKind{remuxCopy, remuxSound} {
		if remuxName(it, 1, other) == name {
			t.Errorf("kind %q is written to the same file as %q", other, remuxTrack)
		}
	}
	// A run interrupted mid-copy is still not ours to adopt.
	if _, ok := remuxKeyFromName(name + ".part"); ok {
		t.Error("a part-written copy parsed as a finished one")
	}
}

// A television's fetch is marked as one, because the endpoint refuses a copy
// for a reason that is a browser's alone: a picture that reorders further
// than it declares plays correctly only where something re-encodes it, which
// a browser needs and a set does not. Unmarked, that refusal reached a
// television as "716 Resource not found" with the copy sitting ready on disk.
func TestARewrapForASetSaysSo(t *testing.T) {
	for _, c := range []struct {
		kind remuxKind
		want string
	}{
		{remuxCopy, "&tv=1"},
		{remuxSound, "&tv=1"},
		{remuxTrack, "&tv=1&mode=track"},
	} {
		if got := remuxQuery(c.kind); got != c.want {
			t.Errorf("kind %q asks %q, want %q", c.kind, got, c.want)
		}
	}
}

// A soundtrack no television decodes is converted before the set is given
// it, because that failure is silent: the film plays, there is no error, and
// a set has no menu to put it right. The picture is the other way round — it
// fails visibly — which is why only the sound is decided here.
func TestASoundtrackNoSetDecodesIsConvertedFirst(t *testing.T) {
	takesAnything := func(string) bool { return true }
	takesNoDTS := func(mime string) bool { return !strings.Contains(mime, "dts") }

	cinema := library.Item{Name: "a film.mkv", Kind: library.KindVideo, VCodec: "h264", ACodec: "dts"}
	kind, ok := castSoundKind(cinema, takesNoDTS)
	if !ok || kind != remuxSound {
		t.Errorf("a DTS film asked for %q (%v); want the sound copy", kind, ok)
	}
	// A set that says it decodes DTS is taken at its word and handed the file.
	if _, ok := castSoundKind(cinema, takesAnything); ok {
		t.Error("a set that lists DTS was made to wait for a copy anyway")
	}
	// Everything a television does decode is left alone.
	for _, codec := range []string{"aac", "ac3", "eac3", "mp3", "opus", "flac", ""} {
		it := cinema
		it.ACodec = codec
		if _, ok := castSoundKind(it, takesNoDTS); ok {
			t.Errorf("a %q soundtrack was converted for no reason", codec)
		}
	}
	// And a film whose picture cannot be copied into an MP4 is left alone
	// too: the copy would not be a copy.
	odd := library.Item{Name: "a film.mkv", Kind: library.KindVideo, VCodec: "mpeg2video", ACodec: "dts"}
	if _, ok := castSoundKind(odd, takesNoDTS); ok {
		t.Error("a picture no MP4 can hold was sent through the sound copy")
	}
}
