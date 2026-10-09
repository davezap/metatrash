package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// transferActions are the POST routes /account/transfer/{action}. Each has
// its own session-bound CSRF token. offer and cancel are the owner's (on the
// sharing page); accept and decline are the member's (on Spaces).
var transferActions = map[string]bool{"offer": true, "cancel": true, "accept": true, "decline": true}

// transferMailsPerOwnerDay caps offer emails per owner across all spaces.
const transferMailsPerOwnerDay = 5

func (h *httpAdapter) transferCSRF(action, session string) string {
	return h.service.accounts.mac("transfer:" + action + ":" + session)
}

// submitSpaceTransfer runs after the account handler's Host, Origin, method,
// form and CSRF checks. Actor IDs never come from submitted data.
func (h *httpAdapter) submitSpaceTransfer(w http.ResponseWriter, r *http.Request, session, action string) {
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
		sendError(w, problem(503, "unavailable", "Spaces are temporarily unavailable."))
		return
	}
	ctx, now := r.Context(), time.Now().UTC()
	notice, target := "", h.basePath+"/account/sharing/"+spaceID
	err = h.service.rates.take(allowance{"transfer:user:" + user.ID, 20, 600})
	if err == nil {
		switch action {
		case "offer":
			// Handing a space over is as sensitive as changing how you sign
			// in: it needs a recent sign-in or confirmation.
			if fresh, _ := a.sessionFresh(session, now); !fresh {
				err = errConfirmFirst
				break
			}
			member := r.PostForm.Get("member")
			if err = db.offerSpaceTransfer(ctx, user.ID, spaceID, member, now); err == nil {
				notice = h.emailTransferOffer(ctx, user, spaceID)
			}
		case "cancel":
			err = db.cancelSpaceTransfer(ctx, user.ID, spaceID)
			notice = "transfer-cancelled"
		case "decline":
			err = db.declineSpaceTransfer(ctx, user.ID, spaceID)
			notice, target = "transfer-declined", h.basePath+"/account"
		case "accept":
			var result acceptedTransfer
			var done bool
			result, done, err = h.service.acceptSpaceTransfer(ctx, user.ID, spaceID)
			if err == nil && done {
				h.notifyTransferAccepted(result)
			}
			notice, target = "transfer-accepted", h.basePath+"/account/mine"
		}
	}
	if err == nil {
		accountCookie(w, h.noticeCookieName(), notice, 60)
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session), Message: "Ownership could not be changed. Reload before retrying."}
	status := http.StatusServiceUnavailable
	var p *Error
	if errors.As(err, &p) {
		status, page.Message = p.Status, p.Message
		if p.RetryAfterSeconds > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
		}
	}
	if err == errConfirmFirst {
		page.Message = "Transferring a space needs a sign-in within the last 10 minutes. Confirm it’s you in Security, then offer it again."
	}
	if action == "offer" || action == "cancel" {
		// Show the owner's sharing page again; a member sees Spaces.
		if page.Sharing, err = h.humanSharing(ctx, user, spaceID); err != nil {
			sendError(w, err)
			return
		}
	}
	h.renderAccount(w, r, status, page)
}

// emailTransferOffer tells the member about the offer and returns a notice
// key. The offer stands whatever happens to the email. SMTP errors may
// contain addresses or credentials; never log them.
func (h *httpAdapter) emailTransferOffer(ctx context.Context, owner userAccount, spaceID string) string {
	a := h.service.accounts
	db := h.service.ownedDB
	if a.sendNotice == nil {
		return "transfer-offered-unsent"
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	var email, username, name, slug string
	var expires int64
	err := db.db.QueryRowContext(queryCtx, `SELECT u.email, u.username, s.name, s.slug, t.expires_at FROM metatrash_space_transfers t JOIN metatrash_spaces s ON s.space_id = t.space_id JOIN metatrash_users u ON u.user_id = t.to_user_id WHERE t.space_id = ? AND t.from_user_id = ? AND s.owner_user_id = ?`, spaceID, owner.ID, owner.ID).Scan(&email, &username, &name, &slug, &expires)
	cancel()
	if err != nil {
		return "transfer-offered-unsent"
	}
	if err := h.service.rates.reserve(
		allowance{"transfer-mail:owner:" + owner.ID, transferMailsPerOwnerDay, 86400},
		allowance{"transfer-mail:" + spaceID + ":" + email, 1, 3600},
		allowance{"mail:global:day", 100, 86400},
	); err != nil {
		return "transfer-offered-unsent"
	}
	select {
	case a.mailSlots <- struct{}{}:
		defer func() { <-a.mailSlots }()
	default:
		return "transfer-offered-unsent"
	}
	body := transferOfferMail(a.config.Origin, owner.Username, name, owner.Username+"/"+slug, username+"/"+slug, time.Unix(expires, 0))
	if err := a.sendNotice(ctx, email, "A Metatrash space has been offered to you", body); err != nil {
		return "transfer-offered-unsent"
	}
	return "transfer-offered"
}

// notifyTransferAccepted emails the previous owner. Best effort.
func (h *httpAdapter) notifyTransferAccepted(t acceptedTransfer) {
	body := transferAcceptedMail(h.service.accounts.config.Origin, t.To.Username, t.Name, t.From.Username+"/"+t.Slug, t.To.Username+"/"+t.Slug, time.Now())
	h.sendSecurityNotice(t.From, "Your Metatrash space has a new owner", body)
}

// transferSection fills the owner's Transfer ownership part of the sharing
// page: the pending offer, and the members it could go to.
func (h *httpAdapter) transferSection(ctx context.Context, page *humanSharingPage, ownerID string) error {
	for _, m := range page.Members {
		if m.Status == "active" && m.Username != "" {
			page.Candidates = append(page.Candidates, m)
		}
	}
	var err error
	page.Transfer, err = h.service.ownedDB.pendingTransfer(ctx, page.ID, ownerID, time.Now())
	return err
}

// parseTransferRoute reports whether path is /account/transfer/{action}.
func parseTransferRoute(path string) (string, bool) {
	action, ok := strings.CutPrefix(path, "/account/transfer/")
	return action, ok && transferActions[action]
}
