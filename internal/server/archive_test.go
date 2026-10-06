// SPDX-License-Identifier: MIT

package server

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/JohanLindvall/Mediator/internal/library"
	"github.com/klauspost/compress/zip"
)

// A member deflated inside a zip is streamed like any file: a range request
// is answered with exactly the bytes it asked for, unpacked on the way.
func TestAZippedMemberStreamsByRange(t *testing.T) {
	dir := t.TempDir()
	payload := make([]byte, 300_000)
	for i := range payload {
		payload[i] = byte(i*31 + i/1000)
	}
	f, err := os.Create(filepath.Join(dir, "clips.zip"))
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	hw, err := w.CreateHeader(&zip.FileHeader{Name: "inside/clip.mp4", Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	hw.Write(payload)
	w.Close()
	f.Close()
	ts, lib := flagServer(t, dir)
	res := lib.List(library.Query{Limit: 10})
	if res.Total != 1 {
		t.Fatalf("indexed %d items, want the clip", res.Total)
	}
	it := res.Items[0]
	for _, rng := range [][2]int{{200_000, 200_999}, {10, 99}, {0, 0}} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/stream/"+it.ID, nil)
		req.Header.Set("Range", "bytes="+itoa(rng[0])+"-"+itoa(rng[1]))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent || !bytes.Equal(body, payload[rng[0]:rng[1]+1]) {
			t.Errorf("range %v: status %d, %d bytes, right bytes %v", rng, resp.StatusCode, len(body), bytes.Equal(body, payload[rng[0]:rng[1]+1]))
		}
	}
}

func itoa(n int) string {
	return string(appendInt(nil, n))
}

func appendInt(b []byte, n int) []byte {
	if n >= 10 {
		b = appendInt(b, n/10)
	}
	return append(b, byte('0'+n%10))
}
