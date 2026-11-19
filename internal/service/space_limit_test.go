package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// An owner's limit refuses changes that add to a space at or over it, and
// lets through changes that only remove files (0.99.3).
func TestCheckSpaceLimit(t *testing.T) {
	r := &repository{owned: true}
	if err := checkSpaceLimit(r, 1<<30, false); err != nil {
		t.Fatalf("no limit: %v", err)
	}
	r.storageLimit.Store(20 << 20)
	r.usage.setGit(19<<20, time.Now())
	if err := checkSpaceLimit(r, 512<<10, false); err != nil {
		t.Fatalf("fits: %v", err)
	}
	err := checkSpaceLimit(r, 2<<20, false)
	var p *Error
	if !errors.As(err, &p) || p.Status != 507 || p.Code != "space_limit" || !strings.Contains(p.Message, "limited to 20.0 MB") || !strings.Contains(p.Message, "holds 19.0 MB") {
		t.Fatalf("over: %v", err)
	}
	if err := checkSpaceLimit(r, 2<<20, true); err != nil {
		t.Fatalf("removing files is always allowed: %v", err)
	}
	if got := sizeText(3 << 30); got != "3.0 GB" {
		t.Fatalf("sizeText: %s", got)
	}
}

// Settings sets and removes a space's limit; writes over it are refused with
// the owner's own words, and a limit below 10 MB isn't one.
func TestSpaceStorageLimitAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	if !w.s.spaceLimitsReady(ctx) {
		t.Skip("deploy/account-space-storage-limit.sql not applied to the test database")
	}
	owner := w.session(w.owner)
	token := siteToken(t, w, owner)
	post := func(body string) map[string]any {
		t.Helper()
		res := siteCall(w, "POST", "/api/v1/site/account/spaces/"+w.ownedID+"/storage-limit", body, owner, token)
		if res.Code != 200 {
			t.Fatalf("storage-limit %s: %d %s", body, res.Code, res.Body)
		}
		return siteJSON(t, res)
	}
	overview := func() map[string]any {
		t.Helper()
		out := siteJSON(t, siteCall(w, "GET", "/api/v1/site/account", "", owner, ""))
		if out["spaceLimits"] != true {
			t.Fatalf("spaceLimits: %v", out)
		}
		return out["spaces"].([]any)[0].(map[string]any)
	}
	if limit, ok := overview()["limit"]; ok {
		t.Fatalf("no limit yet, got %v", limit)
	}
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/spaces/"+w.ownedID+"/storage-limit", `{"limit":1000}`, owner, token), 400, "invalid_request")
	if out := post(`{"limit":20971520}`); !strings.Contains(out["notice"].(string), "limit is saved") {
		t.Fatalf("set: %v", out)
	}
	if limit := overview()["limit"]; limit != float64(20<<20) {
		t.Fatalf("limit: %v", limit)
	}
	// Someone else can't set it.
	stranger := w.session(w.user("limit-stranger"))
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/spaces/"+w.ownedID+"/storage-limit", `{"limit":20971520}`, stranger, siteToken(t, w, stranger)), 404, "")

	repo := w.s.ownedRepository(w.ownedID)
	repo.usage.setGit(20<<20, time.Now())
	text := "over the limit"
	head := func() string {
		t.Helper()
		h, err := repo.head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	_, err := w.s.dispatchRepository(ctx, repo, w.ownedName, "write", Input{Path: "limit/over.md", Text: &text, IfInState: head()})
	var p *Error
	if !errors.As(err, &p) || p.Code != "space_limit" {
		t.Fatalf("write over the limit: %v", err)
	}
	if out := post(`{"limit":null}`); !strings.Contains(out["notice"].(string), "no limit of its own") {
		t.Fatalf("remove: %v", out)
	}
	if _, err := w.s.dispatchRepository(ctx, repo, w.ownedName, "write", Input{Path: "limit/over.md", Text: &text, IfInState: head()}); err != nil {
		t.Fatalf("write without a limit: %v", err)
	}
	if _, ok := overview()["limit"]; ok {
		t.Fatal("limit still listed")
	}
}
