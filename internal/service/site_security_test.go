package service

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Security in the Settings dialog (0.81.0): the same changes as the form,
// as JSON, with step-ups answered as 403 step_up_needed.
func TestSiteSecurityAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	a := w.s.accounts
	a.totpKey = testTOTPKey(t)
	a.sendNotice = func(context.Context, string, string, string) error { return nil }
	codes := map[string]string{}
	a.sendEmailChange = func(_ context.Context, email, code string) error {
		codes[email] = code
		return nil
	}
	stale := w.session(w.owner)
	staleToken := siteToken(t, w, stale)
	session := w.freshSession(w.owner)
	token := siteToken(t, w, session)
	get := func(s *http.Cookie) map[string]any {
		t.Helper()
		res := siteCall(w, "GET", "/api/v1/site/account/security", "", s, "")
		if res.Code != 200 || res.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET security: %d %q %s", res.Code, res.Header().Get("Cache-Control"), res.Body)
		}
		return siteJSON(t, res)
	}
	post := func(route, body string) map[string]any {
		t.Helper()
		res := siteCall(w, "POST", "/api/v1/site/account/security/"+route, body, session, token)
		if res.Code != 200 {
			t.Fatalf("%s: %d %s", route, res.Code, res.Body)
		}
		return siteJSON(t, res)
	}

	// Signed out, unknown routes, Confirm it's you (its own routes), GET only at the top.
	wantCode(t, siteCall(w, "GET", "/api/v1/site/account/security", "", nil, ""), 401, "unauthorized")
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/security/nothing", `{}`, session, token), 404, "")
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/security/confirm/send", `{}`, session, token), 404, "")
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/security", `{}`, session, token), 405, "")
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/security/totp/start", `{}`, session, ""), 403, "forbidden")

	// A stale session sees its state but cannot change sign-in methods.
	out := get(stale)
	if out["email"] != w.owner.Email || out["fresh"] != nil || out["createOptions"] != nil || out["emailLogin"] != true || out["recovery"] != float64(0) {
		t.Fatalf("stale: %v", out)
	}
	for _, route := range []string{"totp/start", "recovery/create", "email/change"} {
		wantCode(t, siteCall(w, "POST", "/api/v1/site/account/security/"+route, `{"email":"x@example.com"}`, stale, staleToken), 403, "step_up_needed")
	}
	devices := out["sessions"].([]any)
	if len(devices) < 2 || out["otherSessions"].(float64) < 1 {
		t.Fatalf("sessions: %v", out["sessions"])
	}

	// Fresh: passkey options are offered.
	out = get(session)
	if out["fresh"] != true || out["minutes"] == nil || !strings.Contains(out["createOptions"].(string), `"challenge"`) {
		t.Fatalf("fresh: %v", out)
	}
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/security/passkeys/rename", `{"passkey":"`+strings.Repeat("0", 32)+`","name":"x"}`, session, token), 404, "")

	// Authenticator app: the QR code is an image, the key text.
	if n := post("totp/start", `{}`); n["notice"] != nil && n["notice"] == "" {
		t.Fatalf("start: %v", n)
	}
	totp := get(session)["totp"].(map[string]any)
	qr, _ := totp["qr"].(string)
	svg, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(qr, "data:image/svg+xml;base64,"))
	if totp["pending"] != true || !strings.HasPrefix(qr, "data:image/svg+xml;base64,") || err != nil || !strings.HasPrefix(string(svg), `<svg class="qr"`) {
		t.Fatalf("setup: %v", totp)
	}
	secret, err := totpEncoding.DecodeString(strings.ReplaceAll(totp["secret"].(string), " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	step := time.Now().Unix() / totpPeriod
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/security/totp/enable", `{"code":"`+totpCode(secret, step+5)+`"}`, session, token), 400, "")
	if n := post("totp/enable", `{"code":"`+totpCode(secret, step)+`"}`); !strings.Contains(n["notice"].(string), "uthenticator") {
		t.Fatalf("enable notice: %v", n)
	}
	out = get(session)
	if totp := out["totp"].(map[string]any); totp["enabled"] != true || totp["secret"] != nil || totp["qr"] != nil || out["strong"] != true {
		t.Fatalf("after enable: %v", out)
	}

	// Recovery codes are shown once.
	post("recovery/create", `{}`)
	out = get(session)
	if fresh := out["newRecoveryCodes"].([]any); len(fresh) < 8 || out["recovery"].(float64) != float64(len(fresh)) || out["canTurnOffEmail"] != true {
		t.Fatalf("recovery: %v", out)
	}
	if again := get(session); again["newRecoveryCodes"] != nil {
		t.Fatal("recovery codes shown twice")
	}

	// Email sign-in off and on.
	post("email-login", `{"email_login":"off"}`)
	if get(session)["emailLogin"] != nil {
		t.Fatal("email sign-in still on")
	}
	post("email-login", `{"email_login":"on"}`)

	// Email address: a code to the new address, then cancel.
	moved := randomName(t, "moved-") + "@example.com"
	post("email/change", `{"email":"`+moved+`"}`)
	if codes[moved] == "" || get(session)["emailChangeTo"] != moved {
		t.Fatalf("email change: %v", codes)
	}
	post("email/cancel", `{}`)
	if get(session)["emailChangeTo"] != nil {
		t.Fatal("email change not cancelled")
	}

	// Signed-in devices: sign one out, then everyone else.
	phone, laptop := w.session(w.owner), w.session(w.owner)
	var other string
	for _, d := range get(session)["sessions"].([]any) {
		if d := d.(map[string]any); d["current"] != true {
			other = d["id"].(string)
			break
		}
	}
	post("sessions/remove", `{"session":"`+other+`"}`)
	post("sessions/end", `{}`)
	if out := get(session); out["otherSessions"] != float64(0) || len(out["sessions"].([]any)) != 1 {
		t.Fatalf("after sign out everywhere else: %v", out)
	}
	for _, gone := range []*http.Cookie{stale, phone, laptop} {
		wantCode(t, siteCall(w, "GET", "/api/v1/site/account/security", "", gone, ""), 401, "unauthorized")
	}

	// Microsoft is not set up here: its routes do not exist.
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/security/microsoft/link", `{}`, session, token), 404, "")
	if get(session)["microsoft"] != nil {
		t.Fatal("microsoft offered without being set up")
	}
}
