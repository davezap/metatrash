package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"
)

type humanInvitation struct {
	ID, SpaceID, Name, Owner, Email, Status, Expires string
}
type joinedHumanSpace struct {
	Name, Owner, URL, Status string
}
type humanMember struct {
	ID, Email, Username, Status, AgentPermission string
}
type humanSharingPage struct {
	ID, Name, URL string
	Invitations   []humanInvitation
	Members       []humanMember
}

// Query deadlines and row limits bound dashboard work. Lists are deliberately
// capped for this first human flow; the page labels the limit explicitly.
func (h *httpAdapter) accountMemberships(ctx context.Context, user userAccount) ([]humanInvitation, []joinedHumanSpace, error) {
	if h.service.ownedDB == nil {
		return nil, nil, problem(503, "unavailable", "Sharing is temporarily unavailable.")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := h.service.ownedDB.db.QueryContext(ctx, `SELECT i.invitation_id, s.space_id, s.name, u.username, i.expires_at FROM metatrash_invitations i JOIN metatrash_spaces s ON s.space_id = i.space_id JOIN metatrash_users u ON u.user_id = s.owner_user_id WHERE i.email = ? AND i.status = 'pending' AND i.expires_at > ? AND s.provisioning_state = 'ready' ORDER BY i.expires_at, i.invitation_id LIMIT 200`, user.Email, time.Now().UTC().Unix())
	if err != nil {
		return nil, nil, err
	}
	invitations := []humanInvitation{}
	for rows.Next() {
		var item humanInvitation
		var expires int64
		if err := rows.Scan(&item.ID, &item.SpaceID, &item.Name, &item.Owner, &expires); err != nil {
			rows.Close()
			return nil, nil, err
		}
		item.Expires = time.Unix(expires, 0).UTC().Format("2006-01-02 15:04 UTC")
		invitations = append(invitations, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	rows, err = h.service.ownedDB.db.QueryContext(ctx, `SELECT s.space_id, s.name, s.slug, u.username, m.status FROM metatrash_memberships m JOIN metatrash_spaces s ON s.space_id = m.space_id JOIN metatrash_users u ON u.user_id = s.owner_user_id WHERE m.user_id = ? AND s.provisioning_state = 'ready' ORDER BY m.joined_at, s.space_id LIMIT 200`, user.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	joined := []joinedHumanSpace{}
	for rows.Next() {
		var item joinedHumanSpace
		var id, slug string
		if err := rows.Scan(&id, &item.Name, &slug, &item.Owner, &item.Status); err != nil {
			return nil, nil, err
		}
		if item.Status == "active" && h.service.ownedRepository(id) != nil {
			item.URL = h.ownedSpaceURL(item.Owner, slug)
		}
		joined = append(joined, item)
	}
	return invitations, joined, rows.Err()
}

func (h *httpAdapter) humanSharing(ctx context.Context, user userAccount, spaceID string) (*humanSharingPage, error) {
	if h.service.ownedDB == nil {
		return nil, problem(503, "unavailable", "Sharing is temporarily unavailable.")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	space, err := scanOwnedSpace(h.service.ownedDB.db.QueryRowContext(ctx, "SELECT "+ownedSpaceColumns+" FROM metatrash_spaces WHERE space_id = ? AND owner_user_id = ? AND provisioning_state = 'ready'", spaceID, user.ID))
	if err == sql.ErrNoRows {
		return nil, missing()
	}
	if err != nil {
		return nil, err
	}
	page := &humanSharingPage{ID: space.ID, Name: space.Name, URL: h.ownedSpaceURL(user.Username, space.Slug)}
	rows, err := h.service.ownedDB.db.QueryContext(ctx, `SELECT invitation_id, email, expires_at FROM metatrash_invitations WHERE space_id = ? AND status = 'pending' ORDER BY expires_at, invitation_id LIMIT 200`, space.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Unix()
	for rows.Next() {
		var item humanInvitation
		var expires int64
		if err := rows.Scan(&item.ID, &item.Email, &expires); err != nil {
			rows.Close()
			return nil, err
		}
		item.Status = "pending"
		if expires <= now {
			item.Status = "expired"
		}
		item.Expires = time.Unix(expires, 0).UTC().Format("2006-01-02 15:04 UTC")
		page.Invitations = append(page.Invitations, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = h.service.ownedDB.db.QueryContext(ctx, `SELECT m.user_id, u.email, COALESCE(u.username, ''), m.status, m.agent_permission FROM metatrash_memberships m JOIN metatrash_users u ON u.user_id = m.user_id WHERE m.space_id = ? ORDER BY m.joined_at, m.user_id LIMIT 200`, space.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item humanMember
		if err := rows.Scan(&item.ID, &item.Email, &item.Username, &item.Status, &item.AgentPermission); err != nil {
			return nil, err
		}
		page.Members = append(page.Members, item)
	}
	return page, rows.Err()
}

// Called only after the account handler's Host, Origin, method, form and
// action-specific session CSRF checks. Actor IDs never come from submitted data.
func (h *httpAdapter) submitHumanMembership(w http.ResponseWriter, r *http.Request, session, action string) {
	a := h.service.accounts
	user, signedIn, err := a.currentUser(r.Context(), session)
	if err != nil {
		sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable."))
		return
	}
	if !signedIn {
		http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
		return
	}
	spaceID := r.PostForm.Get("space")
	if !idPattern.MatchString(spaceID) {
		sendError(w, missing())
		return
	}
	db := h.service.ownedDB
	if db == nil {
		sendError(w, problem(503, "unavailable", "Sharing is temporarily unavailable."))
		return
	}
	notice := ""
	err = h.service.rates.take(allowance{"membership:user:" + user.ID, 30, 600})
	if err == nil {
		switch action {
		case "invite":
			var invitationID string
			invitationID, err = db.inviteHuman(r.Context(), user.ID, spaceID, r.PostForm.Get("email"))
			if err == nil {
				notice = h.emailInvitation(r.Context(), user, spaceID, invitationID)
			}
		case "cancel":
			err = db.cancelHumanInvitation(r.Context(), user.ID, spaceID, r.PostForm.Get("invitation"))
		case "accept":
			err = db.acceptHumanInvitation(r.Context(), user.ID, spaceID, r.PostForm.Get("invitation"))
		case "agent":
			err = db.setMemberAgentPermission(r.Context(), user.ID, spaceID, r.PostForm.Get("member"), r.PostForm.Get("permission"))
		default:
			err = db.manageHumanMember(r.Context(), user.ID, spaceID, r.PostForm.Get("member"), action)
		}
	}
	if err != nil {
		page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session), Message: "Sharing could not be updated. Reload before retrying."}
		status := http.StatusServiceUnavailable
		var p *Error
		if errors.As(err, &p) {
			status, page.Message = p.Status, p.Message
			if p.RetryAfterSeconds > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
			}
		}
		if action != "accept" {
			sharing, loadErr := h.humanSharing(r.Context(), user, spaceID)
			if loadErr != nil {
				sendError(w, loadErr)
				return
			}
			page.Sharing = sharing
		}
		h.renderAccount(w, r, status, page)
		return
	}
	if notice != "" {
		accountCookie(w, h.noticeCookieName(), notice, 60)
	}
	target := h.basePath + "/account/shared"
	if action != "accept" {
		target = h.basePath + "/account/sharing/" + spaceID
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// Notices are fixed keys carried across the post-redirect in a short-lived cookie;
// account routes take no query parameters.
var accountNotices = map[string]string{
	"invite-sent":            "Invitation emailed. They sign in with that address and accept from Your account.",
	"invite-limited":         "Invitation saved, but not emailed again because this address was emailed recently. Try again later, or ask them to sign in with that address and accept from Your account.",
	"invite-failed":          "Invitation saved, but the email could not be sent. Ask them to sign in with that address and accept from Your account.",
	"invite-daily":           "Invitation saved, but not emailed: you have used your five invitation emails for today. Ask them to sign in with that address and accept from Your account, or invite again tomorrow to send the email.",
	"space-web":              "The space is now readable by anyone on the web, read-only. Agent access has not changed.",
	"space-private":          "The space is private again: only you and its members can browse it.",
	"signin-confirmed":       "Confirmed. For the next 10 minutes you can change your email address and sign-in methods.",
	"passkey-added":          "Passkey added. Next time, choose Sign in with a passkey. We’ve emailed you a notice of this change.",
	"totp-added":             "Authenticator app added. Next time, choose Use an authenticator app when you sign in. We’ve emailed you a notice of this change.",
	"totp-removed":           "Authenticator app removed. Also delete Metatrash from the app. We’ve emailed you a notice of this change.",
	"totp-cancelled":         "Authenticator app setup cancelled.",
	"recovery-created":       "New recovery codes created. Save them now: they are shown only once. We’ve emailed you a notice of this change.",
	"email-login-off":        "Email sign-in is off. Sign in with a passkey, your authenticator app or a recovery code. We’ve emailed you a notice of this change.",
	"email-login-on":         "Email sign-in is back on. We’ve emailed you a notice of this change.",
	"email-change-sent":      "Code sent. Check the new address and enter the code under Email address.",
	"email-changed":          "Email address changed. Sign in with the new address from now on. We’ve emailed a notice to both addresses.",
	"email-change-cancelled": "Email address change cancelled. Your address is unchanged.",
	"passkey-renamed":        "Passkey renamed.",
	"passkey-removed":        "Passkey removed. We’ve emailed you a notice of this change. Also delete it from the device or password manager that holds it, or it may keep offering it here.",
}

// inviteMailsPerOwnerDay caps invitation emails per owner across all their spaces.
const inviteMailsPerOwnerDay = 5

func (h *httpAdapter) noticeCookieName() string {
	if h.basePath == "" {
		return noticeCookie
	}
	return noticeCookie + "-" + secretDigest(h.basePath)[:16]
}

// takeNotice reads and clears the notice cookie; unknown values are ignored.
func (h *httpAdapter) takeNotice(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie(h.noticeCookieName())
	if err != nil {
		return ""
	}
	accountCookie(w, h.noticeCookieName(), "", -1)
	return accountNotices[cookie.Value]
}

// emailInvitation runs after the invitation is committed and returns a notice
// key. The invitation stands whatever happens to the email. Limits: one email
// per space and address an hour, a few per address a day across all spaces, and
// five a day per owner, so the form cannot be used to flood an inbox. SMTP
// errors may contain addresses or credentials; never log them.
func (h *httpAdapter) emailInvitation(ctx context.Context, owner userAccount, spaceID, invitationID string) string {
	a := h.service.accounts
	db := h.service.ownedDB
	if a == nil || a.sendInvite == nil || db == nil {
		return "invite-failed"
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	var email, name, slug string
	var expires int64
	err := db.db.QueryRowContext(queryCtx, `SELECT i.email, s.name, s.slug, i.expires_at FROM metatrash_invitations i JOIN metatrash_spaces s ON s.space_id = i.space_id WHERE i.invitation_id = ? AND i.space_id = ? AND i.status = 'pending' AND s.owner_user_id = ?`, invitationID, spaceID, owner.ID).Scan(&email, &name, &slug, &expires)
	cancel()
	if err != nil {
		return "invite-failed"
	}
	ownerDaily := allowance{"invite-mail:owner:" + owner.ID, inviteMailsPerOwnerDay, 86400}
	if err := h.service.rates.reserve(
		allowance{"invite-mail:" + spaceID + ":" + email, 1, 3600},
		allowance{"invite-mail:to:" + email, 5, 86400},
		ownerDaily,
	); err != nil {
		if h.service.rates.full(ownerDaily) {
			return "invite-daily"
		}
		return "invite-limited"
	}
	select {
	case a.mailSlots <- struct{}{}:
		defer func() { <-a.mailSlots }()
	default:
		return "invite-failed"
	}
	m := invitationMail{Owner: owner.Username, SpaceName: name, SpaceAddress: owner.Username + "/" + slug, Email: email, Expires: time.Unix(expires, 0)}
	if err := a.sendInvite(ctx, email, m); err != nil {
		return "invite-failed"
	}
	return "invite-sent"
}
