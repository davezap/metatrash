package service

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Checks for the fixes from the 2026-10-31 security review
// (docs/security-review-2026-10-31.md).

// blockingStore holds ByID until release is closed, like a slow database.
type blockingStore struct {
	*memoryAccountStore
	entered, release chan struct{}
}

func (s *blockingStore) ByID(ctx context.Context, id string) (userAccount, bool, error) {
	s.entered <- struct{}{}
	<-s.release
	return s.memoryAccountStore.ByID(ctx, id)
}

// F4: looking up a session's account does not hold the accounts lock, so a
// slow database does not stall every other request.
func TestCurrentUserReleasesLockDuringLookup(t *testing.T) {
	s, code, store := testAccounts(t)
	a := s.accounts
	browser := strings.Repeat("b", 64)
	if err := a.issue(context.Background(), browser, "slow@example.com"); err != nil {
		t.Fatal(err)
	}
	token, _, _, err := a.verify(context.Background(), browser, *code)
	if err != nil {
		t.Fatal(err)
	}
	slow := &blockingStore{memoryAccountStore: store, entered: make(chan struct{}), release: make(chan struct{})}
	a.store = slow
	done := make(chan userAccount)
	go func() {
		user, _, _ := a.currentUser(context.Background(), token)
		done <- user
	}()
	<-slow.entered
	// While the lookup waits, another request can still use the lock.
	other := make(chan int)
	go func() { other <- a.otherSessions("nobody", token, time.Now()) }()
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("accounts lock held during the account lookup")
	}
	close(slow.release)
	if user := <-done; user.Email != "slow@example.com" {
		t.Fatalf("lookup returned %+v", user)
	}

	// An expired session is refused even before the next sweep.
	a.store = store
	a.mu.Lock()
	key := secretDigest(token)
	session := a.sessions[key]
	session.Expires = time.Now().Add(-time.Second)
	a.sessions[key] = session
	a.swept = time.Now()
	a.mu.Unlock()
	if _, ok, _ := a.currentUser(context.Background(), token); ok {
		t.Fatal("expired session accepted between sweeps")
	}
}

// F2: security notices have their own budget, so spending the day's code
// budget does not silence them.
func TestSecurityNoticesHaveOwnBudget(t *testing.T) {
	s, _, _ := testAccounts(t)
	h := &httpAdapter{service: s}
	sent := make(chan string, 1)
	s.accounts.sendNotice = func(_ context.Context, email, subject, _ string) error {
		sent <- subject
		return nil
	}
	for range codeMailBudget.limit {
		if err := s.rates.reserve(codeMailBudget); err != nil {
			t.Fatal(err)
		}
	}
	if !s.rates.full(codeMailBudget) {
		t.Fatal("code budget not used up")
	}
	h.logMailBudget()
	h.sendSecurityNotice(userAccount{ID: strings.Repeat("1", 32), Email: "owner@example.com"}, "Test notice", "body")
	select {
	case subject := <-sent:
		if subject != "Test notice" {
			t.Fatal(subject)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notice skipped after the code budget was spent")
	}
}

// F1: a day's cap on authenticator codes per address, and one notice to the
// owner when wrong codes use it up.
func TestAuthenticatorAppDailyCap(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	a := w.s.accounts
	a.totpKey = testTOTPKey(t)
	notices := make(chan string, 8)
	a.sendNotice = func(_ context.Context, email, subject, body string) error {
		notices <- email + "|" + subject + "|" + body
		return nil
	}
	secret := []byte("12345678901234567890")
	sealed, err := sealTOTP(a.totpKey, w.owner.ID, secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := w.db.startTOTP(ctx, w.owner.ID, sealed, now); err != nil {
		t.Fatal(err)
	}
	if err := w.db.enableTOTP(ctx, w.owner.ID, now.Unix()/totpPeriod-10, now); err != nil {
		t.Fatal(err)
	}
	login := oauthCall(w.h, "GET", "/login", nil)
	browser := responseCookie(login, loginCookie)
	try := func(code string) (int, string) {
		r := oauthCall(w.h, "POST", "/login/totp", url.Values{"csrf": {browser.Value}, "email": {w.owner.Email}, "code": {code}}, browser)
		return r.Code, r.Body.String()
	}
	wrong := totpCode(secret, now.Unix()/totpPeriod+7)
	quarter := totpAttempts("email:" + a.mac("email:"+w.owner.Email)).key
	for i := range 10 {
		if i == 5 {
			// Past the 15-minute budget, as if a quarter hour went by.
			w.s.rates.mu.Lock()
			delete(w.s.rates.buckets, quarter)
			w.s.rates.mu.Unlock()
		}
		if status, _ := try(wrong); status != 400 {
			t.Fatalf("wrong code %d: %d", i+1, status)
		}
	}
	select {
	case n := <-notices:
		if !strings.HasPrefix(n, w.owner.Email+"|Wrong authenticator codes tried") || !strings.Contains(n, "paused for this address until 00:00 UTC") {
			t.Fatalf("notice %q", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no notice after the day's codes were used up")
	}
	// Even the right code waits for tomorrow now, with a message that says so.
	w.s.rates.mu.Lock()
	delete(w.s.rates.buckets, quarter)
	w.s.rates.mu.Unlock()
	status, body := try(totpCode(secret, time.Now().Unix()/totpPeriod))
	if status != http.StatusTooManyRequests || !strings.Contains(body, "Too many authenticator codes were tried for this address today") {
		t.Fatalf("right code after the cap: %d", status)
	}
	select {
	case n := <-notices:
		t.Fatalf("second notice: %q", n)
	case <-time.After(200 * time.Millisecond):
	}
}

// F5: connecting an app and making a space readable on the web email the
// owner.
func TestLastingAccessNotices(t *testing.T) {
	w := newOAuthWorld(t)
	a := w.s.accounts
	notices := make(chan string, 8)
	a.sendNotice = func(_ context.Context, email, subject, body string) error {
		notices <- email + "|" + subject + "|" + body
		return nil
	}
	expect := func(subject, text string) {
		t.Helper()
		select {
		case n := <-notices:
			if !strings.HasPrefix(n, w.owner.Email+"|"+subject+"|") || !strings.Contains(n, text) {
				t.Fatalf("notice %q", n)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no %q notice", subject)
		}
	}
	session := w.session(w.owner)
	res := oauthCall(w.h, "POST", "/account/spaces/visibility", url.Values{"csrf": {a.mac("space-visibility:" + session.Value)}, "space": {w.ownedID}, "visibility": {spaceWeb}}, session)
	if res.Code != 303 {
		t.Fatalf("make readable: %d %s", res.Code, res.Body)
	}
	expect("A Metatrash space is now readable on the web", "was made readable on the web")
	res = oauthCall(w.h, "POST", "/account/spaces/visibility", url.Values{"csrf": {a.mac("space-visibility:" + session.Value)}, "space": {w.ownedID}, "visibility": {spacePrivate}}, session)
	if res.Code != 303 {
		t.Fatalf("make private: %d", res.Code)
	}
	select {
	case n := <-notices:
		t.Fatalf("notice for making a space private: %q", n)
	case <-time.After(200 * time.Millisecond):
	}

	w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "")
	expect("An app was connected to your Metatrash account", "can read and change 1 of your spaces")
}
