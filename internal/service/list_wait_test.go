package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"metatrash.com/metatrash"
)

// A list with after= waits while the space is still at that state and
// answers as soon as a write moves it.
func TestListWaitsForChange(t *testing.T) {
	dir := t.TempDir()
	b, err := os.ReadFile("../../config/spaces.example.json")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "spaces.json")
	if err = os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), configPath, "", filepath.Join(dir, "data"), metatrash.SpaceREADME)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h, err := s.Handler(metatrash.ToolSchema, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, url string, body any) (int, ListResult) {
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, url, bytes.NewReader(payload))
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out ListResult
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	_, first := call("GET", "/api/v1/spaces/public/files?limit=1", nil)

	// Nothing changes: the wait ends with the same state.
	s.listWait = 200 * time.Millisecond
	start := time.Now()
	if code, out := call("GET", "/api/v1/spaces/public/files?limit=1&after="+first.State, nil); code != 200 || out.State != first.State || time.Since(start) < 150*time.Millisecond {
		t.Fatalf("idle wait: %d %s after %s", code, out.State, time.Since(start))
	}

	// A write during the wait answers at once with the new state.
	s.listWait = 20 * time.Second
	done := make(chan ListResult, 1)
	go func() {
		_, out := call("GET", "/api/v1/spaces/public/files?blobs=true&after="+first.State, nil)
		done <- out
	}()
	time.Sleep(200 * time.Millisecond)
	if code, _ := call("PUT", "/api/v1/spaces/public/file?path=wake.txt", map[string]any{"text": "now", "ifInState": first.State}); code != 201 {
		t.Fatalf("write: %d", code)
	}
	select {
	case out := <-done:
		found := false
		for _, f := range out.Files {
			found = found || (f.Path == "wake.txt" && f.Blob != "")
		}
		if out.State == first.State || !found {
			t.Fatalf("woken list: %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait not woken by the write")
	}

	// An older state answers at once.
	start = time.Now()
	if _, out := call("GET", "/api/v1/spaces/public/files?limit=1&after="+first.State, nil); out.State == first.State || time.Since(start) > 2*time.Second {
		t.Fatal("stale after= waited")
	}
	// since= lists only what changed: wake.txt added, then changed, then
	// another file removed.
	_, changes := call("GET", "/api/v1/spaces/public/files?since="+first.State, nil)
	if len(changes.Files) != 1 || changes.Files[0].Path != "wake.txt" || changes.Files[0].Blob == "" || len(changes.Removed) != 0 {
		t.Fatalf("changes: %+v", changes)
	}
	mark := changes.State
	if code, _ := call("PUT", "/api/v1/spaces/public/file?path=wake.txt", map[string]any{"text": "later", "ifInState": mark}); code != 200 {
		t.Fatal("rewrite")
	}
	_, now := call("GET", "/api/v1/spaces/public/files?limit=1", nil)
	r := httptest.NewRequest("DELETE", "/api/v1/spaces/public/file?path=wake.txt", bytes.NewReader([]byte(`{"ifInState":"`+now.State+`"}`)))
	w := httptest.NewRecorder()
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	_, changes = call("GET", "/api/v1/spaces/public/files?since="+mark, nil)
	if len(changes.Files) != 0 || len(changes.Removed) != 1 || changes.Removed[0] != "wake.txt" {
		t.Fatalf("removed: %+v", changes)
	}
	// Nothing changed: an empty answer at the same state; with a prefix
	// outside the change, empty too.
	_, latest := call("GET", "/api/v1/spaces/public/files?since="+mark, nil)
	if _, same := call("GET", "/api/v1/spaces/public/files?since="+latest.State, nil); same.State != latest.State || len(same.Files)+len(same.Removed) != 0 {
		t.Fatalf("no changes: %+v", same)
	}
	if _, other := call("GET", "/api/v1/spaces/public/files?prefix=inbox/&since="+first.State, nil); len(other.Files)+len(other.Removed) != 0 {
		t.Fatalf("prefix: %+v", other)
	}
	// after= and since= together: wait, then the changes.
	go func() {
		_, out := call("GET", "/api/v1/spaces/public/files?after="+latest.State+"&since="+latest.State, nil)
		done <- out
	}()
	time.Sleep(200 * time.Millisecond)
	call("PUT", "/api/v1/spaces/public/file?path=again.txt", map[string]any{"text": "x", "ifInState": latest.State})
	select {
	case out := <-done:
		if len(out.Files) != 1 || out.Files[0].Path != "again.txt" {
			t.Fatalf("wait and changes: %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait with since not woken")
	}
	if code, _ := call("GET", "/api/v1/spaces/public/files?since="+strings.Repeat("ab", 20), nil); code != 404 {
		t.Fatalf("unknown since: %d", code)
	}
	for _, bad := range []string{"after=abc", "after=" + first.State + "&cursor=x", "since=abc", "since=" + first.State + "&cursor=x"} {
		if code, _ := call("GET", "/api/v1/spaces/public/files?"+bad, nil); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
}
