package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
	"github.com/JohanLindvall/Mediator/internal/library"
)

// Exercise the real startup path: the lock must take effect before loading
// saved roots or publishing the HTTP listener, and cancellation must drain it.
func TestRunHonorsLockedRoots(t *testing.T) {
	for _, locked := range []bool{false, true} {
		name := "saved preferences"
		if locked {
			name = "locked command line"
		}
		t.Run(name, func(t *testing.T) {
			commandRoot, savedRoot, data, scratch := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			dbPath := filepath.Join(data, "media.db")
			db, err := blob.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			err = db.SetRoots([]string{savedRoot})
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			log := &startupLog{listening: make(chan string, 1)}
			go func() {
				done <- run(ctx, config{
					roots: []string{commandRoot}, dataDir: data, dbPath: dbPath,
					listen: "127.0.0.1:0", lock: locked, tmpDir: scratch,
					archiveMax: library.DefaultArchiveMax,
				}, slog.New(slog.NewJSONHandler(log, nil)))
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("shutdown: %v", err)
					}
				case <-time.After(15 * time.Second):
					t.Error("server did not stop after cancellation")
				}
			})
			var base string
			select {
			case base = <-log.listening:
			case err := <-done:
				t.Fatalf("startup: %v", err)
			case <-time.After(15 * time.Second):
				t.Fatal("server did not listen")
			}
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Get(base + "/api/prefs")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var prefs struct {
				Roots    []string `json:"roots"`
				Editable bool     `json:"editable"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&prefs); err != nil {
				t.Fatal(err)
			}
			want := savedRoot
			if locked {
				want = commandRoot
			}
			if !slices.Equal(prefs.Roots, []string{want}) || prefs.Editable == locked {
				t.Errorf("prefs = %+v, want roots [%s], editable %v", prefs, want, !locked)
			}
		})
	}
}

type startupLog struct{ listening chan string }

func (l *startupLog) Write(p []byte) (int, error) {
	var line struct {
		Msg string `json:"msg"`
		URL string `json:"url"`
	}
	if json.Unmarshal(p, &line) == nil && line.Msg == "listening" {
		select {
		case l.listening <- line.URL:
		default:
		}
	}
	return len(p), nil
}
