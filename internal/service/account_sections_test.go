package service

import (
	"strings"
	"testing"
)

// Your account is one page per section, with the sections down the left like
// a space's explorer. Each section loads only what it shows.
func TestAccountSectionsAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	session := w.session(w.owner)
	// Every section moved to the site app's Settings dialog: Spaces, sharing
	// and Profile in 0.79.0, Services in 0.80.0, Security in 0.81.0
	// (TestAccountPagesRedirectToSettings). The old Security page still
	// renders until the clean-up release; its form routes post there.
	res := legacySecurity(w.h, session)
	body := res.Body.String()
	if res.Code != 200 || !strings.Contains(body, "Confirm it’s you") || strings.Contains(body, `id="connected-apps"`) {
		t.Fatalf("security: %d\n%s", res.Code, body)
	}
	if !strings.Contains(body, `href="/account/security" aria-current="page"`) || strings.Count(body, `aria-current="page"`) != 1 {
		t.Fatal("navigation does not mark exactly Security")
	}
	if !strings.Contains(body, `src="/assets/account.js"`) {
		t.Fatal("account.js not loaded")
	}
	if res := oauthCall(w.h, "GET", "/account/security", nil); res.Code != 303 || res.Header().Get("Location") != "/login" {
		t.Fatalf("security without a session: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", "/assets/account.js", nil); res.Code != 200 || !strings.Contains(res.Body.String(), "details.pop") {
		t.Fatalf("account.js: %d", res.Code)
	}
	// Only the listed sections exist.
	if res := oauthCall(w.h, "GET", "/account/nope", nil, session); res.Code == 200 {
		t.Fatal("unknown account section served")
	}
}
