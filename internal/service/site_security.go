package service

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// Security in the site app's Settings dialog (0.81.0), in place of
// /account/security:
//
//	GET  /api/v1/site/account/security            sign-in methods, recovery codes, email address, devices
//	POST /api/v1/site/account/security/{route}    the Security form's routes, with the same fields as JSON:
//	     passkeys/add {"name", "client_data", "attestation", "transports"}, passkeys/rename {"passkey", "name"},
//	     passkeys/remove {"passkey"}, totp/start, totp/enable {"code"}, totp/remove, recovery/create,
//	     email-login {"email_login": "on|off"}, email/change {"email"}, email/verify {"code"}, email/cancel,
//	     sessions/end, sessions/remove {"session"}, microsoft/link (answers {"redirect"}), microsoft/confirm,
//	     microsoft/forget, microsoft/remove {"identity"}
//
// Confirming it’s you is /api/v1/site/account/confirm/... (site_account.go). Every
// change runs signInChange, the same code as the form, with its rate limits,
// emails and step-ups: adding or removing a sign-in method, recovery codes,
// email sign-in and the email address need a sign-in within the last 10
// minutes, and answer 403 step_up_needed otherwise.

type sitePasskey struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Created  string `json:"created"`
	LastUsed string `json:"lastUsed"`
	Synced   bool   `json:"synced,omitempty"`
}

type siteTOTP struct {
	Enabled  bool   `json:"enabled,omitempty"`
	Added    string `json:"added,omitempty"`
	LastUsed string `json:"lastUsed,omitempty"`
	// Pending: set up but not turned on; QR (a data: URL of an SVG image)
	// and Secret are for the app.
	Pending bool   `json:"pending,omitempty"`
	QR      string `json:"qr,omitempty"`
	Secret  string `json:"secret,omitempty"`
}

type siteDevice struct {
	ID       string `json:"id"`
	Device   string `json:"device"`
	IP       string `json:"ip,omitempty"`
	SignedIn string `json:"signedIn"`
	LastUsed string `json:"lastUsed"`
	Current  bool   `json:"current,omitempty"`
}

type siteIdentity struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Created  string `json:"created"`
	LastUsed string `json:"lastUsed"`
}

type siteMicrosoft struct {
	Accounts []siteIdentity `json:"accounts"`
	Waiting  string         `json:"waiting,omitempty"`
	Max      bool           `json:"max,omitempty"`
}

type siteSecurity struct {
	Email   string `json:"email"`
	Fresh   bool   `json:"fresh,omitempty"`
	Minutes int    `json:"minutes,omitempty"`
	// Notice is a change finished on another page (Microsoft's return).
	Notice      string        `json:"notice,omitempty"`
	Passkeys    []sitePasskey `json:"passkeys"`
	MaxPasskeys bool          `json:"maxPasskeys,omitempty"`
	// CreateOptions are WebAuthn creation options (JSON), while fresh.
	CreateOptions   string         `json:"createOptions,omitempty"`
	TOTP            *siteTOTP      `json:"totp,omitempty"`
	EmailLogin      bool           `json:"emailLogin,omitempty"`
	CanTurnOffEmail bool           `json:"canTurnOffEmail,omitempty"`
	Strong          bool           `json:"strong,omitempty"`
	Recovery        int            `json:"recovery"`
	RecoveryCreated string         `json:"recoveryCreated,omitempty"`
	NewRecovery     []string       `json:"newRecoveryCodes,omitempty"`
	EmailChangeTo   string         `json:"emailChangeTo,omitempty"`
	Sessions        []siteDevice   `json:"sessions"`
	OtherSessions   int            `json:"otherSessions"`
	Microsoft       *siteMicrosoft `json:"microsoft,omitempty"`
}

// serveSiteSecurity serves /account/security/...; parts is the path after
// "security". The session is signed in (serveSiteAccount checked).
func (h *httpAdapter) serveSiteSecurity(w http.ResponseWriter, r *http.Request, user userAccount, session, client string, parts []string) {
	route := strings.Join(parts, "/")
	if route == "" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET")
			sendError(w, problem(405, "invalid_request", "Use GET."))
			return
		}
		page, ok := h.signInSection(r, user)
		if !ok {
			sendError(w, problem(503, "unavailable", "Sign-in methods are temporarily unavailable. Reload shortly."))
			return
		}
		out := siteSecurityOf(user, page)
		out.Notice = h.takeNotice(w, r)
		sendJSON(w, 200, out)
		return
	}
	path := "/account/" + route
	if !signInRoutes[path] {
		sendError(w, missing())
		return
	}
	body, ok := h.readSiteAccountBody(w, r, 32*1024)
	if !ok {
		return
	}
	fields := map[string]string{
		"name": body.Name, "client_data": body.ClientData, "attestation": body.Attestation, "transports": body.Transports,
		"passkey": body.Passkey, "code": body.Code, "email_login": body.EmailLogin, "email": body.Email,
		"session": body.Session, "identity": body.Identity,
	}
	notice, redirect, err := h.signInChange(w, r, session, client, path, user, func(name string) string { return fields[name] })
	if err != nil {
		sendSiteAccountError(w, err, "security")
		return
	}
	sendSiteNotice(w, siteNotice{Notice: notice, Redirect: redirect})
}

func siteSecurityOf(user userAccount, page *signInPage) siteSecurity {
	out := siteSecurity{Email: user.Email, Fresh: page.Fresh, Minutes: page.FreshMinutes, Passkeys: []sitePasskey{}, MaxPasskeys: page.MaxPasskeys,
		CreateOptions: page.CreateOptions, EmailLogin: page.EmailLogin, CanTurnOffEmail: page.CanTurnOffEmail, Strong: page.Strong,
		Recovery: page.Recovery, RecoveryCreated: page.RecoveryCreated, NewRecovery: page.NewRecoveryCodes, EmailChangeTo: page.EmailChangeTo,
		Sessions: []siteDevice{}, OtherSessions: page.OtherSessions}
	for _, p := range page.Passkeys {
		out.Passkeys = append(out.Passkeys, sitePasskey{ID: p.ID, Name: p.Name, Created: p.Created, LastUsed: p.LastUsed, Synced: p.Synced})
	}
	if t := page.TOTP; t != nil {
		out.TOTP = &siteTOTP{Enabled: t.Enabled, Added: t.Added, LastUsed: t.LastUsed, Pending: t.Pending, Secret: t.Secret}
		if t.QR != "" {
			// An image, not markup: the dialog shows it with <img>, so it
			// never puts server HTML into the page.
			out.TOTP.QR = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(t.QR))
		}
	}
	for _, s := range page.Sessions {
		out.Sessions = append(out.Sessions, siteDevice{ID: s.ID, Device: s.Device, IP: s.IP, SignedIn: s.SignedIn, LastUsed: s.LastUsed, Current: s.Current})
	}
	if m := page.Microsoft; m != nil {
		out.Microsoft = &siteMicrosoft{Accounts: []siteIdentity{}, Waiting: m.Waiting, Max: m.Max}
		for _, id := range m.Accounts {
			out.Microsoft.Accounts = append(out.Microsoft.Accounts, siteIdentity{ID: id.ID, Label: id.Label, Created: id.Created, LastUsed: id.LastUsed})
		}
	}
	return out
}
