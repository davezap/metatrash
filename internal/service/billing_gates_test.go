package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"metatrash.com/metatrash/extension"
)

func TestActorOf(t *testing.T) {
	ctx := context.Background()
	if actorOf(ctx) != "" {
		t.Fatal("no identity, no actor")
	}
	signedIn := context.WithValue(ctx, mcpIdentityKey{}, mcpIdentity{oauth: &oauthIdentity{UserID: "person"}})
	if actorOf(signedIn) != "person" {
		t.Fatal("signed-in identity is the actor")
	}
	if actorOf(context.WithValue(ctx, mcpIdentityKey{}, mcpIdentity{key: "k"})) != "" {
		t.Fatal("a space key has no actor")
	}
	if actorOf(withActor(signedIn, "payer")) != "payer" {
		t.Fatal("withActor wins")
	}
}

func TestOnlyRemoves(t *testing.T) {
	del := []commitChange{{"a", "delete", "a.md"}, {"b", "delete", "b/AGENTS.md"}}
	if !onlyRemoves(nil, nil, del) {
		t.Fatal("deletes only")
	}
	if onlyRemoves(map[string]string{"a.md": "x"}, nil, del) || onlyRemoves(nil, map[string][]byte{"x": nil}, del) || onlyRemoves(nil, nil, nil) {
		t.Fatal("new content or no change is not only removing")
	}
	if onlyRemoves(nil, nil, append(del, commitChange{"c", "move", "c.md"})) {
		t.Fatal("a move is not a removal")
	}
}

func TestGateAnswer(t *testing.T) {
	refused := problem(402, "subscription_required", "Start a membership to write here.")
	var p *Error
	if err := gateAnswer("g", "x", refused); !errors.As(err, &p) || p.Code != "subscription_required" {
		t.Fatalf("refusal shown as it is: %v", err)
	}
	if err := gateAnswer("g", "x", errors.New("db down")); !errors.As(err, &p) || p.Code != "unavailable" || p.Status != 503 {
		t.Fatalf("failure refuses as unavailable: %v", err)
	}
	if gateAnswer("g", "x", nil) != nil {
		t.Fatal("nil passes")
	}
	s := &Service{extensions: []extension.Hooks{{Name: "none"}}}
	s.gates = s.collectBillingGates()
	if s.admitAccountFunc() != nil || s.checkSpaceCreate(context.Background(), "x") != nil {
		t.Fatal("no hooks, nothing asked")
	}
}

// The billing hooks against a real account database: every write to an
// owned space asks with the actor, owner, space and size, a refusal stops
// the write and is shown as it is, space creation and new accounts are
// asked, and existing accounts never are.
func TestBillingGatesAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	var mu sync.Mutex
	var writes []extension.Write
	var writeRefusal, createRefusal, admitRefusal error
	var admitted []string
	w.s.gates = billingGates{
		write: []func(context.Context, extension.Write) error{func(_ context.Context, x extension.Write) error {
			mu.Lock()
			defer mu.Unlock()
			writes = append(writes, x)
			return writeRefusal
		}},
		create: []func(context.Context, string) error{func(context.Context, string) error {
			mu.Lock()
			defer mu.Unlock()
			return createRefusal
		}},
		admit: []func(context.Context, string, string) error{func(_ context.Context, userID, email string) error {
			mu.Lock()
			defer mu.Unlock()
			admitted = append(admitted, email)
			return admitRefusal
		}},
	}
	w.db.admit = w.s.admitAccountFunc()
	t.Cleanup(func() { w.s.gates, w.db.admit = billingGates{}, nil })
	set := func(target *error, err error) {
		mu.Lock()
		*target = err
		mu.Unlock()
	}
	last := func() extension.Write {
		mu.Lock()
		defer mu.Unlock()
		if len(writes) == 0 {
			t.Fatal("no write asked")
		}
		return writes[len(writes)-1]
	}
	refused := problem(402, "subscription_required", "Start a membership to write here.")

	// The website: the signed-in owner writes to their own space.
	site := "/api/v1/site/spaces/" + w.ownedName
	session := w.session(w.owner)
	csrf := siteToken(t, w, session)
	state := siteJSON(t, siteCall(w, "GET", site+"/files", "", session, ""))["state"].(string)
	res := siteCall(w, "PUT", site+"/file?path=gate.md", `{"text": "# Gate\n", "ifInState": "`+state+`"}`, session, csrf)
	if res.Code != 201 {
		t.Fatalf("write: %d %s", res.Code, res.Body)
	}
	if x := last(); x.ActorID != w.owner.ID || x.OwnerID != w.owner.ID || x.SpaceID != w.ownedID || x.Bytes <= 0 || x.OnlyRemoves {
		t.Fatalf("site write asked %+v", x)
	}
	set(&writeRefusal, refused)
	state = siteJSON(t, siteCall(w, "GET", site+"/files", "", session, ""))["state"].(string)
	wantCode(t, siteCall(w, "PUT", site+"/file?path=gate2.md", `{"text": "# Gate\n", "ifInState": "`+state+`"}`, session, csrf), 402, "subscription_required")
	if res := siteCall(w, "GET", site+"/file?path=gate2.md", "", session, ""); res.Code != 404 {
		t.Fatalf("refused write was committed: %d", res.Code)
	}
	set(&writeRefusal, errors.New("billing database down"))
	wantCode(t, siteCall(w, "PUT", site+"/file?path=gate2.md", `{"text": "# Gate\n", "ifInState": "`+state+`"}`, session, csrf), 503, "unavailable")

	// An app: the owner, a member of the other space, writes and deletes
	// there; the owner of that space is asked about.
	set(&writeRefusal, nil)
	token := w.connect(w.owner, map[string]string{w.joinID: "read_write"}, "")
	wrote := w.tool(token, "write", map[string]any{"space": w.joinName, "path": "app.md", "text": "hello\n", "ifInState": w.tool(token, "list", map[string]any{"space": w.joinName}, "")["state"]}, "")
	if x := last(); x.ActorID != w.owner.ID || x.OwnerID != w.member.ID || x.SpaceID != w.joinID || x.OnlyRemoves {
		t.Fatalf("app write asked %+v", x)
	}
	w.tool(token, "delete", map[string]any{"space": w.joinName, "path": "app.md", "ifInState": wrote["newState"]}, "")
	if x := last(); !x.OnlyRemoves || x.ActorID != w.owner.ID {
		t.Fatalf("app delete asked %+v", x)
	}
	set(&writeRefusal, problem(507, "storage_full", "Your storage is full."))
	w.tool(token, "write", map[string]any{"space": w.joinName, "path": "app2.md", "text": "hello\n", "ifInState": w.tool(token, "list", map[string]any{"space": w.joinName}, "")["state"]}, "storage_full")
	set(&writeRefusal, nil)

	// The service's own commits with no one signed in have no actor.
	r := w.s.ownedRepository(w.ownedID)
	if _, err := w.s.dispatchRepository(ctx, r, w.ownedID, "append", Input{Path: "service.md", Text: ptr("x\n")}); err != nil {
		t.Fatal(err)
	}
	if x := last(); x.ActorID != "" || x.OwnerID != w.owner.ID {
		t.Fatalf("service write asked %+v", x)
	}
	if _, err := w.s.dispatchRepository(withActor(ctx, w.member.ID), r, w.ownedID, "append", Input{Path: "service.md", Text: ptr("y\n")}); err != nil {
		t.Fatal(err)
	}
	if x := last(); x.ActorID != w.member.ID {
		t.Fatalf("folder action write asked %+v", x)
	}

	// Creating a space.
	set(&createRefusal, refused)
	var p *Error
	if _, err := w.s.createOwnedSpace(ctx, w.owner.ID, "Refused", randomName(t, "refused-")); !errors.As(err, &p) || p.Code != "subscription_required" {
		t.Fatalf("create refused: %v", err)
	}
	set(&createRefusal, nil)

	// New accounts: refused sign-ins make no account and say why; existing
	// accounts are never asked.
	held := problem(403, "signups_on_hold", "New subscriptions are currently on hold.")
	set(&admitRefusal, held)
	email := randomName(t, "waiting") + "@example.com"
	if _, err := w.db.FindOrCreate(ctx, email); !errors.As(err, &p) || p.Code != "signups_on_hold" {
		t.Fatalf("admission refused: %v", err)
	}
	if exists, err := w.db.Exists(ctx, email); err != nil || exists {
		t.Fatalf("refused account was made: %v %v", exists, err)
	}
	mu.Lock()
	admitted = nil
	mu.Unlock()
	if _, err := w.db.FindOrCreate(ctx, w.owner.Email); err != nil {
		t.Fatalf("existing account refused: %v", err)
	}
	mu.Lock()
	if len(admitted) != 0 {
		t.Fatalf("existing account was asked: %v", admitted)
	}
	mu.Unlock()
	// Signing in with an emailed code shows the refusal.
	var code string
	w.s.accounts.send = func(_ context.Context, _, value string) error { code = value; return nil }
	browser := strings.Repeat("b", 64)
	if err := w.s.accounts.issue(ctx, browser, email); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.s.accounts.verify(ctx, browser, code); !errors.As(err, &p) || p.Code != "signups_on_hold" {
		t.Fatalf("sign-in shows the refusal: %v", err)
	}
	set(&admitRefusal, nil)
	if err := w.s.accounts.issue(ctx, browser, email); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := w.s.accounts.verify(ctx, browser, code); err != nil || !created {
		t.Fatalf("admitted sign-in: %v %v", created, err)
	}
}
