package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// accountPost posts body (JSON) to /api/v1/site/account+path as the browser
// with session, with that session's CSRF token: what the Settings dialog
// sends.
func accountPost(w *oauthWorld, session *http.Cookie, path, body string) *httptest.ResponseRecorder {
	return siteCall(w, "POST", "/api/v1/site/account"+path, body, session, siteCSRF(w, session))
}

// siteCSRF is the CSRF token GET /api/v1/site/session gives session.
func siteCSRF(w *oauthWorld, session *http.Cookie) string {
	var s struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(siteCall(w, "GET", "/api/v1/site/session", "", session, "").Body.Bytes(), &s)
	return s.CSRF
}

// jsonOf encodes v for accountPost.
func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// securityPost sends what one of the old Security forms sent (its
// "/account/<route>" and fields) the way Settings → Security does now: JSON
// to /api/v1/site/account/security/<route>, or /confirm/<how> for Confirm
// it's you. Other cookies (Microsoft's waiting account) go along too.
func securityPost(w *oauthWorld, path string, form url.Values, session *http.Cookie, others ...*http.Cookie) *httptest.ResponseRecorder {
	route := strings.TrimPrefix(path, "/account")
	if !strings.HasPrefix(route, "/confirm/") {
		route = "/security" + route
	}
	body := map[string]string{}
	for name, values := range form {
		if len(values) > 0 && name != "csrf" {
			body[name] = values[0]
		}
	}
	if len(others) == 0 {
		return accountPost(w, session, route, jsonOf(body))
	}
	cookies := []string{session.Name + "=" + session.Value}
	for _, c := range others {
		cookies = append(cookies, c.Name+"="+c.Value)
	}
	return siteCall(w, "POST", "/api/v1/site/account"+route, jsonOf(body), nil, siteCSRF(w, session), "Cookie", strings.Join(cookies, "; "))
}

// securityState is GET /api/v1/site/account/security for session.
func securityState(t *testing.T, w *oauthWorld, session *http.Cookie) map[string]any {
	t.Helper()
	res := siteCall(w, "GET", "/api/v1/site/account/security", "", session, "")
	if res.Code != 200 {
		t.Fatalf("security: %d %s", res.Code, res.Body)
	}
	return siteJSON(t, res)
}

// confirmWays is GET /api/v1/site/account/confirm for session.
func confirmWays(t *testing.T, w *oauthWorld, session *http.Cookie) map[string]any {
	t.Helper()
	res := siteCall(w, "GET", "/api/v1/site/account/confirm", "", session, "")
	if res.Code != 200 {
		t.Fatalf("confirm: %d %s", res.Code, res.Body)
	}
	return siteJSON(t, res)
}

// jsonNotice is the notice a JSON answer carries.
func jsonNotice(t *testing.T, res *httptest.ResponseRecorder) string {
	t.Helper()
	s, _ := siteJSON(t, res)["notice"].(string)
	return s
}

// noticeOf is the accountNotices key of the notice a Settings answer
// carries ("" when it carries none, or one that is not a key's text).
func noticeOf(r *httptest.ResponseRecorder) string {
	var out struct {
		Notice string `json:"notice"`
	}
	if json.Unmarshal(r.Body.Bytes(), &out) != nil || out.Notice == "" {
		return ""
	}
	for key, text := range accountNotices {
		if text == out.Notice {
			return key
		}
	}
	return ""
}
