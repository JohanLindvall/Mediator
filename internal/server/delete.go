package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JohanLindvall/Mediator/internal/library"
)

// Deleting things from the disk (POST /api/delete/plan, POST /api/delete).
//
// Two requests, and the split is the safety: the first works out what would
// go and changes nothing, the owner is shown exactly that, and the second
// deletes exactly that — named by a token the first handed out, and nothing
// the library holds by then. What goes, and the rules for when a folder goes
// with it, are the library's (library/delete.go).
//
// **Only the whole library deletes.** There is no authentication in front of
// any of this, so what a caller is shown is the only thing that says who it
// is: a face restricted to one kind of media, or to part of the disk, is a
// view somebody was given, and a view somebody was given does not remove the
// owner's files. Refused outright, before anything is looked at, as the
// preferences refuse a confined caller; and -lock, which already says the
// library may not be altered from here, refuses it too. /api/info tells the
// page whether to offer it, and that is a courtesy — these checks are the
// guarantee.
//
// The token also stands between a page elsewhere and the disk: a request
// another site sends to this address can arrive, but it cannot read the
// plan's answer, so it cannot learn the token the deletion needs — and both
// requests insist on a JSON body, which a page on another origin cannot send
// without asking first, and is refused when it asks.

// deletePlanTTL is how long a confirmation may take. A plan older than that
// is forgotten, and confirming it deletes nothing: the disk may have moved on.
const deletePlanTTL = 10 * time.Minute

// deletePlansKept bounds the plans waiting to be confirmed.
const deletePlansKept = 64

// deletePlanShown is how many paths the confirmation lists.
const deletePlanShown = 12

// deletePlans are the plans handed out and not yet confirmed.
type deletePlans struct {
	mu    sync.Mutex
	plans map[string]pendingDelete
}

type pendingDelete struct {
	plan    library.DeletePlan
	expires time.Time
}

// put keeps a plan and names it.
func (d *deletePlans) put(p library.DeletePlan, now time.Time) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b[:])
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.plans == nil {
		d.plans = map[string]pendingDelete{}
	}
	for k, v := range d.plans {
		if now.After(v.expires) {
			delete(d.plans, k)
		}
	}
	for len(d.plans) >= deletePlansKept {
		var oldest string
		for k, v := range d.plans {
			if oldest == "" || v.expires.Before(d.plans[oldest].expires) {
				oldest = k
			}
		}
		delete(d.plans, oldest)
	}
	d.plans[token] = pendingDelete{plan: p, expires: now.Add(deletePlanTTL)}
	return token, nil
}

// take hands a plan over once and forgets it: confirming twice deletes once.
func (d *deletePlans) take(token string, now time.Time) (library.DeletePlan, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.plans[token]
	delete(d.plans, token)
	if !ok || now.After(p.expires) {
		return library.DeletePlan{}, false
	}
	return p.plan, true
}

// AllowDeletes lets the owner delete things from the disk. main calls it
// unless the server was started with -lock.
func (s *Server) AllowDeletes() { s.deletes.Store(true) }

// mayDelete says whether this caller may delete, and if not, why.
func (s *Server) mayDelete(r *http.Request) (bool, string) {
	switch {
	case pathsOf(r).Restricted():
		return false, "this view is confined to part of the library"
	case !contentOf(r).unrestricted():
		return false, "this view shows part of the library"
	case !s.deletes.Load():
		return false, "this server was started with -lock"
	}
	return true, ""
}

// jsonBody says the request says it carries JSON.
func jsonBody(r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mt == "application/json"
}

// handleDeletePlan works out what deleting something would remove.
func (s *Server) handleDeletePlan(w http.ResponseWriter, r *http.Request) {
	if ok, why := s.mayDelete(r); !ok {
		http.Error(w, why, http.StatusForbidden)
		return
	}
	if !jsonBody(r) {
		http.Error(w, "a JSON body is expected", http.StatusUnsupportedMediaType)
		return
	}
	var req library.DeleteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	plan, err := s.lib.PlanDelete(req)
	if errors.Is(err, library.ErrNothingToDelete) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	token, err := s.pendingDeletes.put(plan, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.log.Debug("delete planned", "what", plan.Title, "files", len(plan.Files), "folders", len(plan.Folders), "bytes", plan.Bytes)
	writeJSON(w, s.describePlan(token, plan))
}

// describePlan is a plan as the confirmation shows it: places as the listing
// names them, the folders first, then the files that go on their own.
func (s *Server) describePlan(token string, p library.DeletePlan) DeletePlanResponse {
	out := DeletePlanResponse{
		Token: token, Title: p.Title, Files: len(p.Files), Folders: len(p.Folders),
		Items: len(p.Items), Bytes: p.Bytes, Others: p.Others,
	}
	paths := make([]string, 0, len(p.Folders)+len(p.Files))
	for _, dir := range p.Folders {
		paths = append(paths, s.lib.DisplayPath(dir)+"/")
	}
	for _, f := range p.Files {
		if !f.InFolder {
			paths = append(paths, s.lib.DisplayPath(f.Path))
		}
	}
	if len(paths) > deletePlanShown {
		out.More = len(paths) - deletePlanShown
		paths = paths[:deletePlanShown]
	}
	out.Paths = paths
	return out
}

// handleDelete carries out a plan the owner has confirmed.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if ok, why := s.mayDelete(r); !ok {
		http.Error(w, why, http.StatusForbidden)
		return
	}
	if !jsonBody(r) {
		http.Error(w, "a JSON body is expected", http.StatusUnsupportedMediaType)
		return
	}
	var c DeleteConfirm
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&c); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	plan, ok := s.pendingDeletes.take(strings.TrimSpace(c.Token), time.Now())
	if !ok {
		// Unknown, used, or kept too long: nothing is deleted on the
		// strength of a plan that may no longer describe the disk.
		http.Error(w, "that deletion is no longer on offer; ask again", http.StatusGone)
		return
	}
	out := s.lib.DeleteNow(plan)
	kept := make([]string, 0, len(out.Kept))
	for _, k := range out.Kept {
		kept = append(kept, s.lib.DisplayPath(k.Path)+": "+k.Why)
	}
	// Said at Info, whatever -debug says: files removed from the disk are
	// the one thing this server does that cannot be undone.
	s.log.Info("deleted", "what", plan.Title, "files", out.Files, "folders", out.Folders,
		"bytes", out.Bytes, "kept", len(kept), "from", r.RemoteAddr)
	for _, k := range kept {
		s.log.Info("left in place", "path", k)
	}
	writeJSON(w, DeleteResult{Files: out.Files, Folders: out.Folders, Bytes: out.Bytes, Kept: kept})
}
