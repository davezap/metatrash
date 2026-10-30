package service

import (
	"errors"
	"net/http"
	"strconv"
	"time"
)

// submitSpaceDelete handles POST /account/spaces/delete from the sharing
// page: the owner deletes a space after typing its name. Like offering it to
// someone else, it needs a recent sign-in or confirmation.
func (h *httpAdapter) submitSpaceDelete(w http.ResponseWriter, r *http.Request, session string) {
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
	err = h.service.rates.take(allowance{"space-delete:user:" + user.ID, 10, 600})
	if err == nil {
		if fresh, _ := a.sessionFresh(session, time.Now()); !fresh {
			err = errConfirmFirst
		}
	}
	if err == nil {
		_, err = h.service.deleteOwnedSpace(r.Context(), user, spaceID, r.PostForm.Get("name"))
	}
	if err == nil {
		accountCookie(w, h.noticeCookieName(), "space-deleted", 60)
		http.Redirect(w, r, h.basePath+"/account/mine", http.StatusSeeOther)
		return
	}
	page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session), Message: "The space could not be deleted. Reload before retrying."}
	status := http.StatusServiceUnavailable
	var p *Error
	if errors.As(err, &p) {
		status, page.Message = p.Status, p.Message
		if p.RetryAfterSeconds > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
		}
	}
	if err == errConfirmFirst {
		page.Message = "Deleting a space needs a sign-in within the last 10 minutes. Confirm it’s you in Security, then delete it again."
	}
	if page.Sharing, err = h.humanSharing(r.Context(), user, spaceID); err != nil {
		sendError(w, err)
		return
	}
	h.renderAccount(w, r, status, page)
}
