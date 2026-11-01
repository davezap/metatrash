package service

import (
	"net/url"
	"testing"
)

// The old account pages and their forms are gone (0.82.0): every section is
// in the site app's Settings dialog, and the old addresses only redirect
// there (TestAccountPagesRedirectToSettings).
func TestAccountSectionsAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	session := w.session(w.owner)
	if res := oauthCall(w.h, "GET", "/account/security", nil, session); res.Code != 303 || res.Header().Get("Location") != "/spaces/#settings/security" {
		t.Fatalf("security: %d %q", res.Code, res.Header().Get("Location"))
	}
	if res := oauthCall(w.h, "GET", "/account/security", nil); res.Code != 303 || res.Header().Get("Location") != "/login" {
		t.Fatalf("security without a session: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", "/assets/account.js", nil); res.Code != 404 {
		t.Fatalf("account.js: %d", res.Code)
	}
	// The old form routes no longer take posts, with or without a token.
	for _, path := range []string{"/account/passkeys/rename", "/account/username", "/account/spaces", "/account/delete", "/account/transfer/offer", "/account/agents", "/account/github/connect"} {
		res := oauthCall(w.h, "POST", path, url.Values{"csrf": {w.s.accounts.mac("logout:" + session.Value)}, "name": {"x"}}, session)
		if res.Code < 400 {
			t.Fatalf("%s: %d", path, res.Code)
		}
	}
	if res := oauthCall(w.h, "GET", "/account/nope", nil, session); res.Code != 404 {
		t.Fatalf("unknown account section: %d", res.Code)
	}
}
