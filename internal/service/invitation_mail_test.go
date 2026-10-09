package service

import (
	"context"
	"errors"
	"io"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestInvitationMailMessage(t *testing.T) {
	m := invitationMail{Owner: "dave-zap", SpaceName: "Ōtautahi\r\nBcc: victim@example.com", SpaceAddress: "dave-zap/otautahi", Email: "guest@example.com", Expires: time.Date(2026, 10, 10, 1, 2, 0, 0, time.UTC)}
	body := m.body("https://metatrash.com")
	if strings.ContainsAny(body[:strings.Index(body, "\n")], "\r") || strings.Contains(body, "\nBcc:") {
		t.Fatalf("control characters survived: %q", body)
	}
	for _, want := range []string{`dave-zap invited you to their Metatrash space "ŌtautahiBcc: victim@example.com" (dave-zap/otautahi).`, "https://metatrash.com/login", "(guest@example.com)", "10 October 2026 01:02 UTC", "https://metatrash.com/account and choose Accept invitation"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	if !strings.HasPrefix(invitationMail{}.body("https://x.test"), "A Metatrash user invited you") {
		t.Fatal("empty owner not replaced")
	}
	cfg := accountConfig{SMTPFrom: "noreply@metatrash.com", SMTPHost: "smtp.example.com"}
	raw, err := mailMessage(cfg, "guest@example.com", "You're invited to a Metatrash space", body)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("Subject") != "You're invited to a Metatrash space" || msg.Header.Get("To") != "<guest@example.com>" || msg.Header.Get("Bcc") != "" || msg.Header.Get("Content-Transfer-Encoding") != "quoted-printable" {
		t.Fatalf("headers: %v", msg.Header)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 78 {
			t.Fatalf("line too long: %q", line)
		}
	}
	decoded, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoded), "Ōtautahi") || !strings.Contains(string(decoded), "\r\n") {
		t.Fatalf("decoded body: %q", decoded)
	}
	if _, err := mailMessage(cfg, "a@example.com", "Your Metatrash login code", "Your Metatrash login code is: 123456\n"); err != nil {
		t.Fatal(err)
	}
}

func TestInvitationEmailAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	guest := w.user("guest")
	other := w.user("other")
	type sent struct {
		to string
		m  invitationMail
	}
	var sends []sent
	var fail error
	w.s.accounts.sendInvite = func(_ context.Context, to string, m invitationMail) error {
		if fail != nil {
			return fail
		}
		sends = append(sends, sent{to, m})
		return nil
	}
	session := w.session(w.member)
	csrf := w.s.accounts.mac("membership:invite:" + session.Value)
	invite := func(email string) string {
		t.Helper()
		res := oauthCall(w.h, "POST", "/account/membership/invite", url.Values{"csrf": {csrf}, "space": {w.joinID}, "email": {strings.ToUpper(email)}}, session)
		if res.Code != 303 || res.Header().Get("Location") != "/account/sharing/"+w.joinID {
			t.Fatalf("invite: %d %s %s", res.Code, res.Header().Get("Location"), res.Body)
		}
		cookie := responseCookie(res, w.h.noticeCookieName())
		if cookie == nil {
			t.Fatal("no notice cookie")
		}
		page := oauthCall(w.h, "GET", "/account/sharing/"+w.joinID, nil, session, cookie)
		if page.Code != 200 || !strings.Contains(page.Body.String(), accountNotices[cookie.Value]) || !strings.Contains(page.Body.String(), email) {
			t.Fatalf("sharing page after invite (%s): %d", cookie.Value, page.Code)
		}
		if cleared := responseCookie(page, w.h.noticeCookieName()); cleared == nil || cleared.MaxAge >= 0 {
			t.Fatal("notice cookie not cleared")
		}
		return cookie.Value
	}
	if got := invite(guest.Email); got != "invite-sent" {
		t.Fatalf("first invite: %s", got)
	}
	if len(sends) != 1 || sends[0].to != guest.Email || sends[0].m.SpaceName != "Shared plans" || sends[0].m.SpaceAddress != w.joinName || sends[0].m.Owner != w.member.Username || time.Until(sends[0].m.Expires) < 6*24*time.Hour {
		t.Fatalf("sent: %+v", sends)
	}
	// Inviting the same pending address again is limited to one email an hour.
	if got := invite(guest.Email); got != "invite-limited" || len(sends) != 1 {
		t.Fatalf("resend: %s %d", got, len(sends))
	}
	// A failed email keeps the invitation, and the page says so.
	fail = errors.New("smtp: 550 secret detail")
	if got := invite(other.Email); got != "invite-failed" {
		t.Fatalf("failed send: %s", got)
	}
	page := oauthCall(w.h, "GET", "/account/sharing/"+w.joinID, nil, session)
	if strings.Contains(page.Body.String(), "secret detail") || !strings.Contains(page.Body.String(), other.Email) || strings.Contains(page.Body.String(), `class="notice"`) {
		t.Fatalf("sharing page after failure: %s", page.Body)
	}
	// Five emails per owner per day (one failed send above counts). Three more
	// addresses fill the allowance; the next invitation is saved but not emailed.
	fail = nil
	for i := 0; i < 3; i++ {
		if got := invite(w.user("extra").Email); got != "invite-sent" {
			t.Fatalf("extra invite %d: %s", i, got)
		}
	}
	last := w.user("last")
	if got := invite(last.Email); got != "invite-daily" || len(sends) != 4 {
		t.Fatalf("daily cap: %s %d", got, len(sends))
	}
	// An unknown notice value shows nothing.
	page = oauthCall(w.h, "GET", "/account/sharing/"+w.joinID, nil, session, &http.Cookie{Name: w.h.noticeCookieName(), Value: "<script>"})
	if page.Code != 200 || strings.Contains(page.Body.String(), `class="notice"`) {
		t.Fatalf("unknown notice shown: %d", page.Code)
	}
	// The invited person can still accept by signing in with that address.
	if got := oauthCall(w.h, "GET", "/account/shared", nil, w.session(guest)); !strings.Contains(got.Body.String(), "Shared plans") {
		t.Fatal("guest does not see the invitation")
	}
	// Waiting invitations lead Spaces under All and Shared with me, not Mine.
	guestSession := w.session(guest)
	accept := `action="/account/membership/accept"`
	for path, want := range map[string]bool{"/account": true, "/account/shared": true, "/account/mine": false} {
		if got := strings.Contains(oauthCall(w.h, "GET", path, nil, guestSession).Body.String(), accept); got != want {
			t.Fatalf("guest %s shows the invitation: %v", path, got)
		}
	}
	if strings.Contains(oauthCall(w.h, "GET", "/account", nil, session).Body.String(), `id="invites-heading"`) {
		t.Fatal("invitations section shown without invitations")
	}
}
