package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// A film in a release folder, and a server to delete it through.
func deleteServer(t *testing.T, allow bool) (string, string, string, func(string, any, map[string]string) *http.Response) {
	t.Helper()
	dir := t.TempDir()
	rel := filepath.Join(dir, "Films", "Pale.Harrow.2019-GRP")
	for name, body := range map[string]string{
		"pale.harrow.2019.mkv": "film",
		"pale.harrow.2019.nfo": "about",
	} {
		if err := os.MkdirAll(rel, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rel, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ts, srv, lib := serverUnderTest(t, dir)
	if allow {
		srv.AllowDeletes()
	}
	var id string
	for _, it := range lib.List(library.Query{Limit: 10}).Items {
		id = it.ID
	}
	post := func(path string, body any, headers map[string]string) *http.Response {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			if v == "" {
				req.Header.Del(k)
			} else {
				req.Header.Set(k, v)
			}
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}
	return ts.URL, id, rel, post
}

// Planned, shown, confirmed: the release goes, and a token deletes once.
func TestDeletingThroughTheServer(t *testing.T) {
	_, id, rel, post := deleteServer(t, true)
	res := post("/api/delete/plan", library.DeleteRequest{Kind: library.DeleteItem, ID: id}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("plan: %d", res.StatusCode)
	}
	var plan DeletePlanResponse
	if err := json.NewDecoder(res.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	if plan.Token == "" || plan.Folders != 1 || plan.Items != 1 || len(plan.Paths) != 1 || !strings.HasSuffix(plan.Paths[0], "Pale.Harrow.2019-GRP/") {
		t.Fatalf("plan: %+v", plan)
	}
	if _, err := os.Stat(rel); err != nil {
		t.Fatal("planning removed something")
	}
	res = post("/api/delete", DeleteConfirm{Token: plan.Token}, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", res.StatusCode)
	}
	var done DeleteResult
	if err := json.NewDecoder(res.Body).Decode(&done); err != nil {
		t.Fatal(err)
	}
	if done.Folders != 1 || len(done.Kept) != 0 {
		t.Errorf("result: %+v", done)
	}
	if _, err := os.Stat(rel); !os.IsNotExist(err) {
		t.Error("the release folder is still there")
	}
	if res := post("/api/delete", DeleteConfirm{Token: plan.Token}, nil); res.StatusCode != http.StatusGone {
		t.Errorf("a token confirmed twice: %d", res.StatusCode)
	}
	if res := post("/api/delete", DeleteConfirm{Token: "made-up"}, nil); res.StatusCode != http.StatusGone {
		t.Errorf("a token nobody was given: %d", res.StatusCode)
	}
}

// Only the whole library deletes: not a face, not a confined caller, not a
// server started with -lock — and not a request that does not say it is
// JSON, which a page on another origin cannot send without asking first.
func TestOnlyTheOwnerDeletes(t *testing.T) {
	url, id, rel, post := deleteServer(t, true)
	req := library.DeleteRequest{Kind: library.DeleteItem, ID: id}
	for name, headers := range map[string]map[string]string{
		"a face":          {ContentHeader: "videos"},
		"a confined view": {PathsHeader: filepath.Dir(rel)},
		"a form post":     {"Content-Type": "application/x-www-form-urlencoded"},
		"no content type": {"Content-Type": ""},
	} {
		res := post("/api/delete/plan", req, headers)
		if res.StatusCode == http.StatusOK {
			t.Errorf("%s was given a plan", name)
		}
	}
	_, id2, _, post2 := deleteServer(t, false)
	if res := post2("/api/delete/plan", library.DeleteRequest{Kind: library.DeleteItem, ID: id2}, nil); res.StatusCode != http.StatusForbidden {
		t.Errorf("-lock planned a deletion: %d", res.StatusCode)
	}
	// And the page is told, so it offers nothing it would be refused.
	info := func(h map[string]string) bool {
		req, _ := http.NewRequest(http.MethodGet, url+"/api/info", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out InfoResponse
		_ = json.NewDecoder(res.Body).Decode(&out)
		return out.Deletable
	}
	if !info(nil) || info(map[string]string{ContentHeader: "music"}) {
		t.Error("/api/info says otherwise than the handlers do")
	}
	if _, err := os.Stat(rel); err != nil {
		t.Fatal("something was deleted")
	}
}

// A plan is kept for a while and then forgotten, and the store is bounded.
func TestAPlanIsForgottenInTime(t *testing.T) {
	var d deletePlans
	now := time.Now()
	token, err := d.put(library.DeletePlan{Title: "x"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.take(token, now.Add(deletePlanTTL+time.Second)); ok {
		t.Error("a plan outlived its time")
	}
	for range deletePlansKept + 10 {
		if _, err := d.put(library.DeletePlan{}, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.plans) > deletePlansKept {
		t.Errorf("%d plans kept, bound is %d", len(d.plans), deletePlansKept)
	}
}
