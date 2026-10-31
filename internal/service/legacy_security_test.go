package service

import (
	"net/http"
	"net/http/httptest"
)

// legacySecurity renders the old /account/security page, which now redirects
// to the Settings dialog (0.81.0). The form routes still post there until the
// clean-up release, so their tests read the page this way.
func legacySecurity(h *httpAdapter, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/account/security", nil)
	req.Host = "metatrash.com"
	for _, cookie := range cookies {
		if cookie != nil {
			req.AddCookie(cookie)
		}
	}
	rec := httptest.NewRecorder()
	a := h.service.accounts
	session := cookieToken(req, sessionCookie)
	user, signedIn, err := a.currentUser(req.Context(), session)
	if err != nil {
		h.renderAccount(rec, req, 503, accountPage{Message: "Sign-in is temporarily unavailable."})
		return rec
	}
	if !signedIn {
		h.redirectToLogin(rec, req)
		return rec
	}
	page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session)}
	page.Notice = h.takeNotice(rec, req)
	h.renderAccount(rec, req, 200, page)
	return rec
}
