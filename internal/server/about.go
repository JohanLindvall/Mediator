package server

import (
	"runtime"
	"runtime/debug"
	"sync"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// What is running, and what it turned out to be able to do.
//
// Two questions that are easy to answer wrongly from the outside. A binary
// that has been rebuilt and not restarted looks exactly like one that has;
// and hardware conversion either happens or does not, silently, with the
// difference between a film that plays and one that stalls for ever hanging
// on a group membership and a driver. Both are worth being able to read off
// the screen rather than out of a log.

// Build is what was built, filled in at link time where it can be.
type Build struct {
	// Version is what the build was called — a tag, or a commit, or the
	// word for neither.
	Version string `json:"version"`
	// Commit and Time come from the version control stamp Go embeds when it
	// can see a repository, or from the linker when the build was made
	// somewhere that could not.
	Commit string `json:"commit,omitempty"`
	Time   string `json:"time,omitempty"`
	// Modified says the tree had uncommitted changes in it. Worth saying:
	// it is the difference between a build somebody can go and read and one
	// that existed on one machine for one afternoon.
	Modified bool   `json:"modified,omitempty"`
	Go       string `json:"go"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

// Stamped by the linker, for builds made where there is no repository to read
// — which is every build here, the sources being copied into an image.
var (
	buildVersion = ""
	buildCommit  = ""
	buildTime    = ""
)

var (
	buildOnce sync.Once
	buildInfo Build
)

// BuildInfo is what this binary knows about itself: for -version, which
// answers before a server exists, and for /api/info, which answers after.
func BuildInfo() Build { return buildOf() }

// buildOf reads what this binary knows about itself, once.
func buildOf() Build {
	buildOnce.Do(func() {
		b := Build{
			Version: buildVersion,
			Commit:  buildCommit,
			Time:    buildTime,
			Go:      runtime.Version(),
			OS:      runtime.GOOS,
			Arch:    runtime.GOARCH,
		}
		// A build made from a checkout carries its own stamp, which is
		// better than anything passed in: it cannot be stale.
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, s := range info.Settings {
				switch s.Key {
				case "vcs.revision":
					b.Commit = s.Value
				case "vcs.time":
					b.Time = s.Value
				case "vcs.modified":
					b.Modified = s.Value == "true"
				}
			}
		}
		if b.Version == "" {
			if b.Commit != "" {
				b.Version = shortCommit(b.Commit)
			} else {
				b.Version = "unknown"
			}
		}
		buildInfo = b
	})
	return buildInfo
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// Capabilities is what this server found it could do here — the answers that
// depend on the machine rather than on the code.
type Capabilities struct {
	// FFmpeg and FFprobe are what everything about video needs. Without
	// them a picture is served as it is or not at all.
	FFmpeg  bool `json:"ffmpeg"`
	FFprobe bool `json:"ffprobe"`
	// Hardware is the engine conversions run on where the processor cannot
	// keep up, and Device is where it was found. Empty means everything is
	// converted on the processor — see hwaccel.go for what that costs.
	Hardware string `json:"hardware,omitempty"`
	Device   string `json:"device,omitempty"`
	// Database says whether anything is remembered between runs: thumbnails,
	// metadata, positions, play counts, the signing key. With `-db off` the
	// server works and forgets.
	Database bool `json:"database"`
	// Loopback is this server's own address, which is how ffmpeg reads
	// content that has no path — inside an archive or a disc image. Without
	// it those are piped, and a pipe cannot seek.
	Loopback bool `json:"loopback"`
}

// capabilities gathers what has actually been established. The hardware
// answer is whatever the search has settled on; main asks for that search at
// startup so this is not the first thing to pay for it.
func (s *Server) capabilities() Capabilities {
	ffmpeg := s.thumbs.FFmpegPath()
	c := Capabilities{
		FFmpeg:   ffmpeg != "",
		FFprobe:  library.FFprobePath() != "",
		Database: s.thumbs.store != nil,
		Loopback: library.LoopbackBase() != "",
	}
	if engine, device := hw.chosen(); engine != nil {
		c.Hardware = engine.name
		c.Device = device
	}
	return c
}
