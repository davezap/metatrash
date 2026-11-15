package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"metatrash.com/metatrash/extension"
)

// The mail drop (0.97.0): a relay that holds accounts.json email's secret
// posts each message it receives for the email domain.
//
//	POST /email/inbound
//	Authorization: Bearer <secret>
//	X-Metatrash-Recipient: <the envelope recipient, a folder's address>
//	X-Metatrash-Checks: spf=pass; dkim=pass; dmarc=pass; spam=pass; virus=pass   (optional)
//	Content-Type: message/rfc822
//	<the raw message>
//
// 201 {"spaceID", "path"} filed; 200 with "duplicate": true when the space
// already has it; 404 no folder has the address; 403 sender_refused; 422
// refused (virus or spam); 413 too large; 429 and 503 may succeed later.
// A Postfix pipe transport or a fetchmail mda can run curl with these.
//
// The owner of a space reads a folder's address from the REST resource
// email: GET .../spaces/{space}/email?folder=inbox/ answers {"address"}.

func (h *httpAdapter) serveEmailInbound(w http.ResponseWriter, r *http.Request, client string) bool {
	if r.URL.Path != "/email/inbound" {
		return false
	}
	e := h.service.emailIn
	if e == nil || e.secret == nil {
		sendError(w, missing())
		return true
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		sendError(w, problem(405, "invalid_request", "Use POST."))
		return true
	}
	if err := h.service.rates.take(allowance{"email:inbound:" + client, 600, 60}); err != nil {
		sendError(w, err)
		return true
	}
	auth := strings.Fields(r.Header.Get("Authorization"))
	given := ""
	if len(auth) == 2 && strings.EqualFold(auth[0], "Bearer") {
		given = auth[1]
	}
	want := sha256.Sum256(e.secret)
	got := sha256.Sum256([]byte(given))
	if given == "" || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
		_ = h.service.rates.take(allowance{"email:inbound:bad:" + client, 20, 600})
		sendError(w, problem(401, "unauthorized", "The mail drop needs its secret as a bearer token."))
		return true
	}
	recipient := r.Header.Get("X-Metatrash-Recipient")
	if recipient == "" || len(recipient) > 320 {
		sendError(w, invalid("X-Metatrash-Recipient must name the envelope recipient."))
		return true
	}
	allowLong(w, 3*time.Minute)
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEmailBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			sendError(w, problem(413, "payload_too_large", "The message is larger than a folder takes."))
		} else {
			sendError(w, invalid("Could not read the message."))
		}
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	receipt, err := h.service.receiveEmail(ctx, recipient, raw, parseChecks(r.Header.Get("X-Metatrash-Checks")))
	if err != nil {
		sendError(w, err)
		return true
	}
	status := 201
	if receipt.Duplicate {
		status = 200
	}
	sendJSON(w, status, receipt)
	return true
}

// parseChecks reads "spf=pass; dkim=fail; ..." (Authentication-Results
// style, in any order; unknown names are ignored).
func parseChecks(header string) extension.EmailChecks {
	var c extension.EmailChecks
	for _, item := range strings.FieldsFunc(header, func(r rune) bool { return r == ';' || r == ',' }) {
		k, v, ok := strings.Cut(strings.TrimSpace(item), "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "spf":
			c.SPF = v
		case "dkim":
			c.DKIM = v
		case "dmarc":
			c.DMARC = v
		case "spam":
			c.Spam = v
		case "virus":
			c.Virus = v
		}
	}
	return c
}

// serveEmailAddress answers GET .../email?folder=<folder/>: the folder's
// address, for the space's owner (and those who act as its owner) only,
// since anyone who has it can file messages there.
func (h *httpAdapter) serveEmailAddress(w http.ResponseWriter, r *http.Request, repo *repository) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) > 1 || len(q) == 1 && len(q["folder"]) != 1 {
		sendError(w, invalid("Only ?folder=<folder/> is accepted (empty for the space root)."))
		return
	}
	identity, _ := r.Context().Value(mcpIdentityKey{}).(mcpIdentity)
	owner := h.service.repositoryOwner(repo)
	if identity.oauth == nil || identity.oauth.UserID == "" || owner == "" ||
		identity.oauth.UserID != owner && (h.service.ownedDB == nil || !h.service.ownedDB.actsFor(r.Context(), identity.oauth.UserID, owner)) {
		sendError(w, problem(403, "forbidden", "Only the space's owner sees its email addresses."))
		return
	}
	folder := q.Get("folder")
	address, err := h.service.folderEmailAddress(r.Context(), repo, folder)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, 200, map[string]string{"folder": folder, "address": address})
}
