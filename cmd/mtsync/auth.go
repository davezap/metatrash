package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"metatrash.com/metatrash"
)

// credentials are the tokens for one server, kept in the user's config
// directory (never in the synced folder).
type credentials struct {
	Access  string    `json:"accessToken"`
	Refresh string    `json:"refreshToken"`
	Expires time.Time `json:"expires"`
	Scope   string    `json:"scope"`
}

func credentialsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "metatrash", "sync-credentials.json"), nil
}

func loadAllCredentials() (map[string]credentials, error) {
	all := map[string]credentials{}
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("%s is damaged; delete it and run mtsync login", path)
	}
	return all, nil
}

func saveCredentials(server string, c *credentials) error {
	all, err := loadAllCredentials()
	if err != nil {
		return err
	}
	if c == nil {
		delete(all, server)
	} else {
		all[server] = *c
	}
	path, _ := credentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(all, "", "  ")
	return writeFileAtomic(path, b, 0600)
}

func clientID(server string) string { return server + "/oauth/clients/sync" }
func resource(server string) string { return server + "/api/v1/account" }

func randomURLString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// login runs the authorization code flow with PKCE, receiving the code on a
// loopback port (RFC 8252). The user picks spaces and access in the browser.
func login(ctx context.Context, server string, out io.Writer) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)
	verifier, state := randomURLString(32), randomURLString(16)
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID(server)},
		"redirect_uri":          {redirect},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"resource":              {resource(server)},
		"scope":                 {"spaces:read spaces:write"},
	}
	authorize := server + "/oauth/authorize?" + query.Encode()
	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	var once sync.Once
	finish := func(r result) { once.Do(func() { done <- r }) }
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		page := func(ok bool, message string) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
			fmt.Fprint(w, callbackPage(ok, server, message))
		}
		switch {
		case q.Get("state") != state:
			page(false, "This sign-in response does not match. Run mtsync login again.")
			finish(result{err: fmt.Errorf("sign-in response did not match the request")})
		case q.Get("error") != "":
			page(false, "Sign-in was not completed: "+q.Get("error_description"))
			finish(result{err: fmt.Errorf("sign-in failed: %s %s", q.Get("error"), q.Get("error_description"))})
		case q.Get("iss") != "" && q.Get("iss") != server:
			page(false, "This sign-in came from another server. Run mtsync login again.")
			finish(result{err: fmt.Errorf("sign-in came from %s, not %s", q.Get("iss"), server)})
		default:
			page(true, "")
			finish(result{code: q.Get("code")})
		}
	})}
	go func() { _ = srv.Serve(listener) }()
	defer srv.Close()
	fmt.Fprintf(out, "Opening your browser to connect Metatrash Sync. If it does not open, visit:\n\n  %s\n\n", authorize)
	openBrowser(authorize)
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Minute):
		return fmt.Errorf("timed out waiting for the browser sign-in")
	}
	if r.err != nil {
		return r.err
	}
	creds, err := tokenRequest(ctx, server, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {r.code},
		"redirect_uri":  {redirect},
		"client_id":     {clientID(server)},
		"code_verifier": {verifier},
		"resource":      {resource(server)},
	})
	if err != nil {
		return err
	}
	return saveCredentials(server, creds)
}

func tokenRequest(ctx context.Context, server string, form url.Values) (*credentials, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", server+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Access      string `json:"access_token"`
		Refresh     string `json:"refresh_token"`
		ExpiresIn   int    `json:"expires_in"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("token endpoint: %s", resp.Status)
	}
	if resp.StatusCode != 200 || body.Access == "" {
		if body.Error == "invalid_grant" {
			return nil, errSignedOut
		}
		return nil, fmt.Errorf("token endpoint: %s %s", body.Error, body.Description)
	}
	return &credentials{Access: body.Access, Refresh: body.Refresh, Scope: body.Scope, Expires: time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)}, nil
}

var errSignedOut = errors.New("this computer is signed out of metatrash (the connection expired or was removed); run mtsync login")

// tokenSource returns access tokens for server, refreshing them before they
// expire or when the server refuses one. Refresh tokens rotate on every use,
// so each new pair is saved at once, under a lock file that keeps two mtsync
// processes from spending the same refresh token.
func tokenSource(server string) func(ctx context.Context, refresh bool) (string, error) {
	var mu sync.Mutex
	last := ""
	return func(ctx context.Context, refresh bool) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		unlock, err := lockCredentials(ctx)
		if err != nil {
			return "", err
		}
		defer unlock()
		all, err := loadAllCredentials()
		if err != nil {
			return "", err
		}
		creds, ok := all[server]
		if !ok {
			return "", errSignedOut
		}
		// Another process may already have refreshed since our token was refused.
		fresh := time.Until(creds.Expires) > time.Minute
		if fresh && (!refresh || creds.Access != last) {
			last = creds.Access
			return creds.Access, nil
		}
		next, err := tokenRequest(ctx, server, url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {creds.Refresh},
			"client_id":     {clientID(server)},
			"resource":      {resource(server)},
		})
		if err != nil {
			return "", err
		}
		if err := saveCredentials(server, next); err != nil {
			return "", err
		}
		last = next.Access
		return next.Access, nil
	}
}

// lockCredentials takes the credentials lock file, breaking one left behind
// by a process that stopped while holding it.
func lockCredentials(ctx context.Context) (func(), error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock := path + ".lock"
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			f.Close()
			return func() { os.Remove(lock) }, nil
		}
		if info, statErr := os.Stat(lock); statErr == nil && time.Since(info.ModTime()) > time.Minute {
			os.Remove(lock)
			continue
		}
		if err := sleepContext(ctx, 100*time.Millisecond); err != nil {
			return nil, err
		}
	}
}

// openBrowser opens target in the user's browser; tests replace it.
var openBrowser = func(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	_ = cmd.Start()
}

// callbackPage is what the browser shows when sign-in comes back to mtsync.
// It is self-contained (the loopback server serves nothing else) and uses
// the site's colours.
func callbackPage(ok bool, server, message string) string {
	host := server
	if u, err := url.Parse(server); err == nil && u.Host != "" {
		host = u.Host
	}
	folder := "~/spaces/space"
	if runtime.GOOS == "windows" {
		folder = `C:\spaces\space`
	}
	body := `<p class="eyebrow">Metatrash Sync</p>
<div class="mark ok" aria-hidden="true"><svg viewBox="0 0 52 52"><circle cx="26" cy="26" r="24"/><path d="M15 27l7 7 15-16"/></svg></div>
<h1>This computer is connected.</h1>
<p class="lede">Agents write to your spaces and the files show up in your folders. You edit the folders and the spaces keep up.</p>
<ol class="steps">
<li><span>See the spaces you chose</span><code>mtsync spaces</code></li>
<li><span>Pick a space and a folder</span><code>mtsync add owner/space ` + folder + `</code></li>
<li><span>Keep it syncing at every login</span><code>mtsync install</code></li>
</ol>
<p class="hint">You can close this tab and go back to the terminal. To change the spaces or revoke access, see <a href="` + html.EscapeString(server) + `/account/services">Services</a> in your account on ` + html.EscapeString(host) + `.</p>`
	title := "Connected"
	if !ok {
		title = "Not connected"
		body = `<p class="eyebrow">Metatrash Sync</p>
<div class="mark bad" aria-hidden="true"><svg viewBox="0 0 52 52"><circle cx="26" cy="26" r="24"/><path d="M18 18l16 16M34 18L18 34"/></svg></div>
<h1>That didn't work.</h1>
<p class="lede">` + html.EscapeString(message) + `</p>
<p class="hint">Close this tab and run <code>mtsync login</code> again.</p>`
	}
	return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>` + title + ` · Metatrash Sync</title>
<style>
:root{color-scheme:dark;--bg:#141817;--panel:#1b211f;--line:#343d38;--muted:#a8b4aa;--text:#edf2e9;--accent:#d5f297;--bad:#e5a78e;font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:var(--text);background:var(--bg)}
*{box-sizing:border-box}body{margin:0;min-height:100vh;display:flex;flex-direction:column}
header{padding:22px 5%;border-bottom:1px solid var(--line)}.brand{font-size:24px;font-weight:750;letter-spacing:-1.3px}.brand span{color:var(--accent)}
main{flex:1;width:100%;max-width:620px;margin:auto;padding:56px 24px}
.eyebrow{font-size:11px;font-weight:650;text-transform:uppercase;letter-spacing:2px;color:var(--accent);margin:0 0 22px}
h1{font-size:clamp(32px,6vw,46px);line-height:1.08;letter-spacing:-1.8px;font-weight:550;margin:18px 0 18px}
.lede{font-size:18px;line-height:1.6;color:var(--muted);margin:0 0 30px}
.mark svg{width:56px;height:56px;fill:none;stroke-width:3;stroke-linecap:round;stroke-linejoin:round}
.mark circle{stroke:var(--line)}.ok path{stroke:var(--accent)}.bad path{stroke:var(--bad)}
.ok circle{stroke:var(--accent);stroke-dasharray:151;stroke-dashoffset:151;animation:draw .6s ease-out forwards}
.ok path{stroke-dasharray:40;stroke-dashoffset:40;animation:draw .35s .5s ease-out forwards}
@keyframes draw{to{stroke-dashoffset:0}}
.steps{list-style:none;counter-reset:s;margin:0;padding:0;border-top:1px solid var(--line)}
.steps li{counter-increment:s;display:grid;grid-template-columns:28px 1fr;gap:6px 10px;padding:14px 0;border-bottom:1px solid var(--line)}
.steps li::before{content:counter(s);grid-row:1/3;color:var(--accent);font:600 13px ui-monospace,Consolas,monospace;padding-top:2px}
.steps span{font-size:14px;color:var(--muted)}
code{font:13px ui-monospace,Consolas,monospace;color:var(--text);overflow-wrap:anywhere}
.steps code{display:block;padding:9px 12px;background:var(--panel);border:1px solid var(--line);border-radius:4px;user-select:all}
.hint{font-size:13px;line-height:1.7;color:var(--muted);margin-top:26px}a{color:var(--accent)}
footer{padding:18px 5%;border-top:1px solid var(--line);color:var(--muted);font:11px ui-monospace,Consolas,monospace}
@media (prefers-reduced-motion:reduce){.ok circle,.ok path{animation:none;stroke-dashoffset:0}}
</style></head>
<body><header><span class="brand">metatrash<span>.</span></span></header>
<main>` + body + `</main>
<footer>mtsync ` + html.EscapeString(metatrash.Version) + ` · ` + html.EscapeString(host) + `</footer>
</body></html>`
}
