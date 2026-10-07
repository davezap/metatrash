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
	sections := []struct{ path, want, absent string }{
		{"/account", `class="rows rows-spaces"`, "Confirm it’s you"},
		{"/account/shared", "Joined spaces", `class="rows rows-spaces"`},
		{"/account/security", "Confirm it’s you", `id="connected-apps"`},
		{"/account/services", `id="connected-apps"`, "Confirm it’s you"},
		{"/account/profile", "Public username", `id="connected-apps"`},
	}
	for _, s := range sections {
		res := oauthCall(w.h, "GET", s.path, nil, session)
		body := res.Body.String()
		if res.Code != 200 || !strings.Contains(body, s.want) || strings.Contains(body, s.absent) {
			t.Fatalf("%s: %d want %q absent %q\n%s", s.path, res.Code, s.want, s.absent, body)
		}
		if !strings.Contains(body, `href="`+s.path+`" aria-current="page"`) || strings.Count(body, `aria-current="page"`) != 1 {
			t.Fatalf("%s: navigation does not mark exactly this section", s.path)
		}
		if !strings.Contains(body, `src="/assets/account.js"`) {
			t.Fatalf("%s: account.js not loaded", s.path)
		}
		if res := oauthCall(w.h, "GET", s.path, nil); res.Code != 303 || res.Header().Get("Location") != "/login" {
			t.Fatalf("%s without a session: %d", s.path, res.Code)
		}
	}
	// The sharing page belongs to My spaces.
	res := oauthCall(w.h, "GET", "/account/sharing/"+w.ownedID, nil, session)
	if res.Code != 200 || !strings.Contains(res.Body.String(), `href="/account" aria-current="page"`) || !strings.Contains(res.Body.String(), "Invite someone") {
		t.Fatalf("sharing page: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", "/assets/account.js", nil); res.Code != 200 || !strings.Contains(res.Body.String(), "details.pop") {
		t.Fatalf("account.js: %d", res.Code)
	}
	// Only the listed sections exist.
	if res := oauthCall(w.h, "GET", "/account/nope", nil, session); res.Code == 200 {
		t.Fatal("unknown account section served")
	}
}
