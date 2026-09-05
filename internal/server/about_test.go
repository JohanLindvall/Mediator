package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The box exists to answer two questions from the screen rather than from a
// log: what is running, and what it turned out to be able to do here.
func TestInfoCarriesBuildAndCapabilities(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := flagServer(t, dir)
	res, err := http.Get(ts.URL + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	var info InfoResponse
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	// Something is always said about what is running, even from a build made
	// where there was no repository and nothing was passed in.
	if info.Build.Version == "" {
		t.Error("no version at all")
	}
	if info.Build.Go == "" || info.Build.OS == "" || info.Build.Arch == "" {
		t.Errorf("the runtime is not described: %+v", info.Build)
	}
	// Capabilities are what this machine can do, so they are only checked
	// for coherence: a server with no database must not claim one.
	if info.Capabilities.Database {
		t.Error("a server built without a store says it has one")
	}
	if info.Capabilities.Hardware == "" && info.Capabilities.Device != "" {
		t.Error("a device without an engine to use it")
	}
}

// A build with nothing stamped into it still names itself. "unknown" is a
// worse answer than a commit and a better one than an empty box.
func TestBuildAlwaysSaysSomething(t *testing.T) {
	b := buildOf()
	if b.Version == "" {
		t.Fatal("the version is empty")
	}
	if b.Commit != "" && len(shortCommit(b.Commit)) > 12 {
		t.Errorf("the commit is not shortened: %q", shortCommit(b.Commit))
	}
	if got := shortCommit("abc"); got != "abc" {
		t.Errorf("a short commit was truncated to %q", got)
	}
}
