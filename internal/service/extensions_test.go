package service

import (
	"context"
	"errors"
	"metatrash.com/metatrash/extension"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestExtensionStartupRollback(t *testing.T) {
	for _, failure := range []string{"factory", "validation", "start"} {
		t.Run(failure, func(t *testing.T) {
			var events []string
			var worker context.Context
			s := &Service{ownedDB: &accountDatabase{}}
			s.modules = []extension.Module{
				func(extension.Host) (extension.Hooks, error) {
					return extension.Hooks{Name: "first", Start: func(ctx context.Context) error { worker = ctx; events = append(events, "start"); return nil }, Close: func() { events = append(events, "first") }}, nil
				},
				func(extension.Host) (extension.Hooks, error) {
					if failure == "factory" {
						return extension.Hooks{}, errors.New("factory failed")
					}
					name := "second"
					if failure == "validation" {
						name = "first"
					}
					return extension.Hooks{Name: name, Start: func(context.Context) error { return errors.New("start failed") }, Close: func() {
						if worker != nil && worker.Err() == nil {
							t.Error("worker not cancelled before close")
						}
						events = append(events, "second")
					}}, nil
				},
			}
			if err := s.startExtensions(); err == nil {
				t.Fatal("startup accepted failed module")
			}
			want := []string{"second", "first"}
			if failure == "factory" {
				want = []string{"first"}
			}
			if failure == "start" {
				want = []string{"start", "second", "first"}
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("cleanup %v; want %v", events, want)
			}
			s.closeExtensions()
			if len(events) != len(want) || len(s.extensions) != 0 || s.extensionCancel != nil || s.ownedDB.extensionPurge != nil {
				t.Fatal("failed startup left live hooks")
			}
		})
	}
}

func TestExtensionRouteOwnership(t *testing.T) {
	for _, path := range []string{"/", "/api/v1/site/account", "/assets/theme", "/spaces/public", "/docs", "/logout", "/account", "/login/send", "/login/send/callback", "/.well-known/openid", "/llms.txt", "/thing/../api", "/thing//x", "/thing?x=1"} {
		if validExtensionRoute("public", path) {
			t.Errorf("accepted reserved public route %q", path)
		}
	}
	for _, path := range []string{"services/key", "confirm/verify", "spaces/delete", "security/passkeys/remove", "security/totp/remove"} {
		if validExtensionRoute("account", path) {
			t.Errorf("accepted reserved account route %q", path)
		}
	}
	for _, path := range []string{"/vendor/webhook", "/plans", "/login/enterprise/callback"} {
		if !validExtensionRoute("public", path) {
			t.Errorf("rejected extension route %q", path)
		}
	}
}

func TestExtensionAccountRouteGuards(t *testing.T) {
	s, _, store := testAccounts(t)
	s.ownedDB = &accountDatabase{}
	user := userAccount{ID: strings.Repeat("a", 32), Email: "person@example.com"}
	store.users[user.Email] = user
	token := strings.Repeat("b", 64)
	s.accounts.sessions[secretDigest(token)] = accountSession{UserID: user.ID, Expires: time.Now().Add(time.Hour)}
	calls := 0
	s.extensions = []extension.Hooks{{Name: "example", AccountRoutes: []extension.Route{{Path: "example/change", Method: "POST", Handle: func(w http.ResponseWriter, r *http.Request, x extension.HTTP) {
		calls++
		if x.User().ID != user.ID || x.Session() != token {
			t.Error("wrong account context")
		}
		w.WriteHeader(204)
	}}}}}
	h := &httpAdapter{service: s, publicHost: "metatrash.com", publicOrigin: "https://metatrash.com"}
	for _, tc := range []struct {
		name, origin, host, csrf, fetch string
		session                         bool
		want                            int
	}{
		{"valid", "https://metatrash.com", "metatrash.com", h.siteCSRF(token), "same-origin", true, 204},
		{"signed out", "https://metatrash.com", "metatrash.com", h.siteCSRF(token), "same-origin", false, 401},
		{"no csrf", "https://metatrash.com", "metatrash.com", "", "same-origin", true, 403},
		{"cross origin", "https://evil.example", "metatrash.com", h.siteCSRF(token), "same-origin", true, 403},
		{"wrong host", "https://metatrash.com", "evil.example", h.siteCSRF(token), "same-origin", true, 403},
		{"cross site", "https://metatrash.com", "metatrash.com", h.siteCSRF(token), "cross-site", true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://"+tc.host+"/api/v1/site/account/example/change", strings.NewReader("{}"))
			r.Header.Set("Origin", tc.origin)
			r.Header.Set(siteCSRFHeader, tc.csrf)
			r.Header.Set("Sec-Fetch-Site", tc.fetch)
			r.Header.Set("Content-Type", "application/json")
			if tc.session {
				r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
			}
			w := httptest.NewRecorder()
			h.serveSiteAPI(w, r, "127.0.0.1")
			if w.Code != tc.want {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
	if calls != 1 {
		t.Fatalf("extension invoked %d times", calls)
	}
}

func TestExtensionLoginGuards(t *testing.T) {
	s, _, _ := testAccounts(t)
	calls := 0
	s.extensions = []extension.Hooks{{Name: "enterprise", Login: &extension.LoginProvider{
		Path: "/login/enterprise", Label: "Enterprise", Start: func(w http.ResponseWriter, r *http.Request, x extension.HTTP) { calls++; w.WriteHeader(204) },
	}}}
	h := &httpAdapter{service: s, publicHost: "metatrash.com", publicOrigin: "https://metatrash.com"}
	browser := strings.Repeat("c", 64)
	for _, tc := range []struct {
		name, origin, csrf string
		want               int
	}{
		{"valid", "https://metatrash.com", browser, 204},
		{"no csrf", "https://metatrash.com", "", 403},
		{"wrong origin", "https://evil.example", browser, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "https://metatrash.com/login/enterprise", strings.NewReader("csrf="+tc.csrf))
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(&http.Cookie{Name: loginCookie, Value: browser})
			w := httptest.NewRecorder()
			if !h.serveAccounts(w, r, "127.0.0.1") {
				t.Fatal("provider route not recognized")
			}
			if w.Code != tc.want {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
	if calls != 1 {
		t.Fatalf("provider started %d times", calls)
	}
	s.extensions = nil
	r := httptest.NewRequest("POST", "https://metatrash.com/login/enterprise", nil)
	if h.serveAccounts(httptest.NewRecorder(), r, "127.0.0.1") {
		t.Fatal("absent provider exposed")
	}
}

// A provider whose sign-in is waiting to be linked isn't offered again: the
// page asks for an emailed code to its address instead (0.95.23).
func TestExtensionLoginWaitingPage(t *testing.T) {
	s, _, _ := testAccounts(t)
	waiting := false
	s.extensions = []extension.Hooks{{Name: "enterprise", Login: &extension.LoginProvider{
		Path: "/login/enterprise", Label: "Enterprise", Hint: "Work account.", Start: func(http.ResponseWriter, *http.Request, extension.HTTP) {},
		Waiting: func(*http.Request, extension.HTTP) (string, string, string) {
			if !waiting {
				return "", "", ""
			}
			return "person@work.example", "person@work.example", "/spaces/#settings/security/enterprise"
		},
	}}}
	h := &httpAdapter{service: s, publicHost: "metatrash.com", publicOrigin: "https://metatrash.com"}
	page := func() string {
		r := httptest.NewRequest("GET", "https://metatrash.com/login", nil)
		w := httptest.NewRecorder()
		if !h.serveAccounts(w, r, "127.0.0.1") || w.Code != 200 {
			t.Fatalf("login page %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	body := page()
	if !strings.Contains(body, ">Sign in with Enterprise</button>") || strings.Contains(body, "Finish signing in") {
		t.Fatal("provider not offered on the usual page")
	}
	waiting = true
	body = page()
	for _, want := range []string{"<h1>Finish signing in with Enterprise</h1>", "Enterprise says you are", `value="person@work.example"`, "it creates your account", "Not you? Use another Enterprise account"} {
		if !strings.Contains(body, want) {
			t.Errorf("waiting page lacks %q", want)
		}
	}
	if strings.Contains(body, ">Sign in with Enterprise</button>") || strings.Contains(body, "Work account.") {
		t.Fatal("waiting page offers the same provider again")
	}
}

func TestExtensionSessionIssuance(t *testing.T) {
	s, _, store := testAccounts(t)
	host := extensionHost{s}
	user := userAccount{ID: strings.Repeat("a", 32), Email: "person@example.com"}
	store.users[user.Email] = user
	s.accounts.challenges["pending"] = loginChallenge{Email: user.Email}
	token, err := host.IssueSession(context.Background(), user.ID, "192.0.2.1", "test browser")
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := host.CurrentUser(context.Background(), token)
	if err != nil || !ok || got.ID != user.ID {
		t.Fatal("issued session does not identify existing user")
	}
	if fresh, _ := host.SessionFresh(token, time.Now()); !fresh {
		t.Fatal("new session lacks recent authentication")
	}
	if _, ok := s.accounts.challenges["pending"]; ok {
		t.Fatal("pending challenge was not cleared")
	}
	if _, err := host.IssueSession(context.Background(), strings.Repeat("f", 32), "", ""); err == nil {
		t.Fatal("issued session for missing user")
	}
}

func TestExtensionReturnAddresses(t *testing.T) {
	for _, target := range []string{"https://evil.example", "//evil.example", "/\\evil.example", "/path\r\nLocation: https://evil.example"} {
		if localReturnAddress(target) {
			t.Errorf("accepted %q", target)
		}
	}
	if !localReturnAddress("/spaces/#settings/security/enterprise") {
		t.Fatal("rejected local settings")
	}
}

func TestExtensionRegistrationSnapshot(t *testing.T) {
	routes := []extension.Route{{Path: "/plans", Method: "GET", Handle: func(http.ResponseWriter, *http.Request, extension.HTTP) {}}}
	provider := &extension.LoginProvider{Path: "/login/example", Label: "Example", Start: func(http.ResponseWriter, *http.Request, extension.HTTP) {}}
	notices := map[string]string{"example-ready": "Ready"}
	s := &Service{ownedDB: &accountDatabase{}, modules: []extension.Module{func(extension.Host) (extension.Hooks, error) {
		return extension.Hooks{Name: "example", PublicRoutes: routes, Login: provider, Notices: notices}, nil
	}}}
	if err := s.startExtensions(); err != nil {
		t.Fatal(err)
	}
	defer s.closeExtensions()
	routes[0].Path = "/changed"
	provider.Path = "/changed"
	notices["example-ready"] = "Changed"
	got := s.extensions[0]
	if got.PublicRoutes[0].Path != "/plans" || got.Login.Path != "/login/example" || got.Notices["example-ready"] != "Ready" {
		t.Fatal("registration retained mutable aliases")
	}
}

func TestExtensionConfigurationFreeze(t *testing.T) {
	s := &Service{configurationFrozen: true}
	if s.SetModules(nil) == nil {
		t.Fatal("changed modules after handler construction")
	}
	if s.SetFrontend(nil) == nil {
		t.Fatal("changed frontend after handler construction")
	}
	if s.EnableAccounts("unused") == nil {
		t.Fatal("enabled accounts after handler construction")
	}
	for _, path := range []string{"billing/", "billing/%2f", "security/example/", "security/example/%2f"} {
		if validExtensionRoute("account", path) {
			t.Errorf("accepted invalid action %q", path)
		}
	}
}
