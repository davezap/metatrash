package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"metatrash.com/metatrash/extension"
)

type extensionHost struct{ s *Service }

func (h extensionHost) Database() *sql.DB {
	if h.s.ownedDB == nil {
		return nil
	}
	return h.s.ownedDB.db
}
func (h extensionHost) Account(ctx context.Context, id string) (extension.User, bool, error) {
	return h.s.accounts.store.ByID(ctx, id)
}
func (h extensionHost) CurrentUser(ctx context.Context, token string) (extension.User, bool, error) {
	return h.s.accounts.currentUser(ctx, token)
}
func (h extensionHost) SessionFresh(token string, now time.Time) (bool, time.Time) {
	return h.s.accounts.sessionFresh(token, now)
}
func (h extensionHost) EndOtherSessions(id, token string) (int, error) {
	return h.s.accounts.endOtherSessions(id, token)
}
func (h extensionHost) AccountUsage(ctx context.Context, id string) (int64, error) {
	u, e := h.s.accountStorage(ctx, id)
	return u.Total, e
}
func (h extensionHost) Rate(key string, n, seconds int) error {
	return h.s.rates.take(allowance{key, n, seconds})
}
func (h extensionHost) IssueSession(ctx context.Context, id, ip, ua string) (string, error) {
	a := h.s.accounts
	user, ok, err := a.store.ByID(ctx, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", missing()
	}
	ctx = withSessionOrigin(ctx, sessionOrigin{Device: deviceLabel(ua), IP: ip})
	a.mu.Lock()
	defer a.mu.Unlock()
	token, err := a.startSessionLocked(ctx, user.ID, time.Now())
	if err == nil {
		for k, pending := range a.challenges {
			if pending.Email == user.Email {
				delete(a.challenges, k)
			}
		}
	}
	return token, err
}

type extensionHTTP struct {
	extensionHost
	h       *httpAdapter
	user    extension.User
	session string
}

func (x extensionHTTP) BasePath() string              { return x.h.basePath }
func (x extensionHTTP) Origin() string                { return x.h.publicOrigin }
func (x extensionHTTP) Client(r *http.Request) string { return x.h.client(r) }
func (x extensionHTTP) User() extension.User          { return x.user }
func (x extensionHTTP) Session() string               { return x.session }
func (x extensionHTTP) SessionCookie() string         { return x.h.sessionCookieName() }
func (x extensionHTTP) NoticeCookie() string          { return x.h.noticeCookieName() }
func (x extensionHTTP) FrontendEntry(name string) *extension.AssetEntry {
	b := x.h.frontendEntry(name)
	if b == nil {
		return nil
	}
	return &extension.AssetEntry{Entry: b.Entry, CSS: append([]string{}, b.CSS...)}
}
func (x extensionHTTP) RedirectToLogin(w http.ResponseWriter, r *http.Request) {
	x.h.redirectToLogin(w, r)
}
func (x extensionHTTP) NotifySignInChange(u extension.User, change, what string) {
	x.h.notifySignInChange(u, change, what)
}
func (x extensionHTTP) ContinueTo(w http.ResponseWriter, r *http.Request, target string) {
	if !localReturnAddress(target) {
		sendError(w, invalid("Invalid return address."))
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	x.h.renderLogin(w, r, 200, loginView{ContinueURL: target})
}
func (x extensionHTTP) RenderMessage(w http.ResponseWriter, status int, label, title, message, back string) {
	x.h.renderGitHub(w, status, githubPage{Eyebrow: label, Title: title, Message: message, ContinueURL: back, Button: "Continue →"})
}
func (x extensionHTTP) DecodeJSON(reader io.Reader, out any) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	if !validUnicodeJSON(data) || noDuplicateKeys(data) != nil {
		return invalid("Invalid JSON object.")
	}
	return strictJSON(data, out)
}
func (h *httpAdapter) extensionContext(user userAccount, session string) extensionHTTP {
	return extensionHTTP{extensionHost: extensionHost{h.service}, h: h, user: user, session: session}
}

// SetModules is only valid before enabling accounts or constructing a handler.
func (s *Service) SetModules(modules []extension.Module) error {
	if s.configurationFrozen || s.accounts != nil || s.modules != nil {
		return fmt.Errorf("modules already initialized")
	}
	s.modules = append([]extension.Module{}, modules...)
	return nil
}

var extensionName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func (s *Service) startExtensions() (err error) {
	if s.extensionCancel != nil || len(s.extensions) != 0 {
		return fmt.Errorf("extensions already started")
	}
	defer func() {
		if err != nil {
			s.closeExtensions()
		}
	}()
	paths := map[string]bool{}
	names := map[string]bool{}
	fields := map[string]bool{}
	notices := map[string]bool{}
	for _, factory := range s.modules {
		if factory == nil {
			return fmt.Errorf("nil extension factory")
		}
		hooks, err := factory(extensionHost{s})
		if err != nil {
			return err
		}
		hooks = snapshotHooks(hooks)
		valid := false
		cleanup := func() {
			if !valid && hooks.Close != nil {
				hooks.Close()
			}
		}
		if !extensionName.MatchString(hooks.Name) || names[hooks.Name] {
			cleanup()
			return fmt.Errorf("invalid or duplicate extension name")
		}
		names[hooks.Name] = true
		for _, routes := range []struct {
			scope string
			list  []extension.Route
		}{{"public", hooks.PublicRoutes}, {"account", hooks.AccountRoutes}} {
			for _, route := range routes.list {
				key := routes.scope + ":" + route.Path
				if route.Handle == nil || paths[key] || !validExtensionRoute(routes.scope, route.Path) || route.Method != "GET" && route.Method != "POST" {
					cleanup()
					return fmt.Errorf("invalid or duplicate extension route %q", key)
				}
				paths[key] = true
			}
		}
		for _, field := range hooks.Fields {
			key := field.Scope + ":" + field.Key
			if (field.Scope != "account" && field.Scope != "security") || !extensionName.MatchString(field.Key) || fields[key] || field.Read == nil || coreAccountFields[field.Key] {
				cleanup()
				return fmt.Errorf("invalid extension field %q", key)
			}
			fields[key] = true
		}
		if p := hooks.Login; p != nil {
			if p.Path != "/login/"+hooks.Name || p.Label == "" || p.Start == nil || loginPosts[p.Path] || paths["public:"+p.Path] {
				cleanup()
				return fmt.Errorf("invalid login provider")
			}
			if p.FormOrigin != "" {
				u, err := url.Parse(p.FormOrigin)
				if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
					cleanup()
					return fmt.Errorf("invalid login provider origin")
				}
			}
		}
		if hooks.Login != nil {
			paths["public:"+hooks.Login.Path] = true
		}
		for name := range hooks.Notices {
			if accountNotices[name] != "" || notices[name] || !strings.HasPrefix(name, hooks.Name+"-") {
				cleanup()
				return fmt.Errorf("duplicate or invalid notice")
			}
			notices[name] = true
		}
		valid = true
		s.extensions = append(s.extensions, hooks)
	}
	if s.ownedDB != nil && s.ownedDB.db != nil {
		rows, e := s.ownedDB.db.Query("SELECT component FROM metatrash_schema_components WHERE component <> 'core'")
		if e != nil {
			return fmt.Errorf("cannot read installed extension ownership: %w", e)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if e := rows.Scan(&name); e != nil {
				return e
			}
			if !names[name] {
				return fmt.Errorf("database requires the %s extension; refusing to start without it", name)
			}
		}
		if e := rows.Err(); e != nil {
			return e
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.extensionCancel = cancel
	for _, hooks := range s.extensions {
		if hooks.Start != nil {
			if err := hooks.Start(ctx); err != nil {
				return fmt.Errorf("start %s: %w", hooks.Name, err)
			}
		}
	}
	if s.ownedDB == nil {
		if len(s.extensions) != 0 {
			return fmt.Errorf("extensions require an account database")
		}
		return nil
	}
	s.ownedDB.extensionPurge = func(ctx context.Context, tx *sql.Tx, id string) error {
		for _, hooks := range s.extensions {
			if hooks.Purge != nil {
				if err := hooks.Purge(ctx, tx, id); err != nil {
					return fmt.Errorf("purge %s: %w", hooks.Name, err)
				}
			}
		}
		return nil
	}
	s.ownedDB.actingFor = s.actingOwnersFunc()
	s.ownedDB.gate = s.spaceGateFunc()
	s.gates = s.collectBillingGates()
	s.ownedDB.admit = s.admitAccountFunc()
	return nil
}

// Cancel workers before closing resources, including when startup fails.
func (s *Service) closeExtensions() {
	if s.extensionCancel != nil {
		s.extensionCancel()
		s.extensionCancel = nil
	}
	for i := len(s.extensions) - 1; i >= 0; i-- {
		if s.extensions[i].Close != nil {
			s.extensions[i].Close()
		}
	}
	s.extensions = nil
	if s.ownedDB != nil {
		s.ownedDB.extensionPurge = nil
		s.ownedDB.actingFor = nil
	}
}

var coreAccountFields = map[string]bool{"email": true, "username": true, "maxSpaces": true, "canCreate": true, "defaultTimezone": true, "apps": true, "fresh": true, "spaces": true, "joined": true, "invitations": true, "offers": true, "storage": true, "unavailable": true, "passkeys": true, "totp": true, "sessions": true, "notice": true, "recovery": true, "newRecoveryCodes": true, "strong": true, "emailLogin": true, "canTurnOffEmail": true, "createOptions": true, "maxPasskeys": true, "minutes": true, "recoveryCreated": true, "emailChangeTo": true, "otherSessions": true}

func validExtensionRoute(scope, path string) bool {
	if path == "" || strings.ContainsAny(path, "?#\\") || strings.Contains(path, "..") || strings.Contains(path, "//") {
		return false
	}
	if scope == "account" {
		parts := strings.Split(path, "/")
		if parts[0] == "security" {
			return len(parts) == 3 && extensionName.MatchString(parts[1]) && extensionName.MatchString(parts[2]) && parts[1] != "sessions" && parts[1] != "passkeys" && parts[1] != "totp" && parts[1] != "email" && parts[1] != "recovery"
		}
		return len(parts) == 2 && extensionName.MatchString(parts[0]) && extensionName.MatchString(parts[1]) && parts[0] != "services" && parts[0] != "spaces" && parts[0] != "confirm" && parts[0] != "offers" && parts[0] != "invitations"
	}
	if !strings.HasPrefix(path, "/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, part := range parts {
		if !extensionName.MatchString(part) {
			return false
		}
	}
	// Reserve entire core namespaces, including routes introduced below the
	// public dispatcher. An extension cannot replace their security checks.
	for _, name := range []string{"metatrash", "api", "mcp", "spaces", "docs", "app", "assets", "oauth", "github", "account", "healthz", "logout", "llms", "robots", "favicon"} {
		if parts[0] == name {
			return false
		}
	}
	if parts[0] == "login" {
		return len(parts) == 3 && parts[2] == "callback" && !loginPosts["/login/"+parts[1]]
	}
	return true
}
func (h *httpAdapter) serveExtensionRoute(w http.ResponseWriter, r *http.Request, scope, path string, user userAccount, session string) bool {
	for _, hooks := range h.service.extensions {
		routes := hooks.PublicRoutes
		if scope == "account" {
			routes = hooks.AccountRoutes
		}
		for _, route := range routes {
			if route.Path != path {
				continue
			}
			if r.Method != route.Method && !(route.Method == "GET" && r.Method == "HEAD") {
				w.Header().Set("Allow", route.Method)
				sendError(w, problem(405, "invalid_request", "Unsupported method."))
				return true
			}
			if !strings.EqualFold(r.Host, h.publicHost) {
				sendError(w, problem(403, "forbidden", "Use the configured site address."))
				return true
			}
			if scope == "account" && r.Method == http.MethodPost {
				kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil || kind != "application/json" {
					sendError(w, problem(415, "invalid_request", "Use application/json."))
					return true
				}
				r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
			}
			route.Handle(w, r, h.extensionContext(user, session))
			return true
		}
	}
	return false
}
func (h *httpAdapter) extensionJSON(w http.ResponseWriter, r *http.Request, scope string, base any, user userAccount, session string) {
	data, err := json.Marshal(base)
	if err != nil {
		sendError(w, err)
		return
	}
	out := map[string]any{}
	if json.Unmarshal(data, &out) != nil {
		sendError(w, fmt.Errorf("invalid account response"))
		return
	}
	for _, hooks := range h.service.extensions {
		for _, field := range hooks.Fields {
			if field.Scope != scope {
				continue
			}
			value, err := field.Read(r.Context(), r, h.extensionContext(user, session))
			if err != nil {
				log.Printf("extension %s summary: %v", hooks.Name, err)
				sendError(w, problem(503, "unavailable", "Account settings are temporarily unavailable."))
				return
			}
			if value != nil {
				out[field.Key] = value
			}
		}
	}
	sendJSON(w, 200, out)
}

func localReturnAddress(target string) bool {
	return strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "//") && !strings.ContainsAny(target, "\\\r\n")
}

func (h *httpAdapter) extensionLogin(path string) *extension.LoginProvider {
	for _, hooks := range h.service.extensions {
		if hooks.Login != nil && hooks.Login.Path == path {
			return hooks.Login
		}
	}
	return nil
}

// Factories retain no mutable aliases into the published registration.
func snapshotHooks(h extension.Hooks) extension.Hooks {
	h.PublicRoutes = append([]extension.Route(nil), h.PublicRoutes...)
	h.AccountRoutes = append([]extension.Route(nil), h.AccountRoutes...)
	h.Fields = append([]extension.Field(nil), h.Fields...)
	if h.Login != nil {
		p := *h.Login
		h.Login = &p
	}
	notices := make(map[string]string, len(h.Notices))
	for k, v := range h.Notices {
		notices[k] = v
	}
	h.Notices = notices
	return h
}
