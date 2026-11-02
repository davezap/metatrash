package service

import (
	"context"
	"errors"
	"io"
	"log"
	"math"
	"mime"
	"net/http"
	"strings"
	"time"
)

// The site API's account routes (0.79.0) are what the site app's Settings
// dialog reads and changes, in place of the /account pages:
//
//	GET  /api/v1/site/account                          the account, its spaces, invitations and offers
//	POST /api/v1/site/account/username                 {"username"}
//	POST /api/v1/site/account/spaces                   {"name", "timezone"?} or, to finish an interrupted one, {"name", "slug"}
//	GET  /api/v1/site/account/spaces/{id}/sharing      invitations, members and the ownership offer of an owned space
//	POST /api/v1/site/account/spaces/{id}/{action}     visibility, timezone, invite, cancel, suspend, restore, remove, agent,
//	                                                   transfer-offer, transfer-cancel, delete
//	POST /api/v1/site/account/invitations/accept       {"space", "invitation"}
//	POST /api/v1/site/account/offers/{accept|decline}  {"space"}
//	POST /api/v1/site/account/delete                   {"email"}
//	POST /api/v1/site/account/billing/portal           {} (0.91.0, site_billing.go)
//	POST /api/v1/site/account/billing/storage          {"storageGb", "preview"?} (0.92.0, site_billing.go)
//	GET  /api/v1/site/account/confirm                  how this session can confirm it's you
//	POST /api/v1/site/account/confirm/{send|verify|totp|passkey}
//	     /api/v1/site/account/services/...             connected apps, agents and GitHub (0.80.0, site_services.go)
//	     /api/v1/site/account/security/...             sign-in methods, email address, devices (0.81.0, site_security.go)
//
// They sit behind the site API's checks (site address, Origin, Sec-Fetch-Site,
// and on writes the session's X-Metatrash-CSRF token), and every route needs a
// signed-in session. The actor is always the session's user, never a field.
// The steps a script in a page must not take alone (offering a space to
// someone, deleting it, deleting the account, making a space readable on the
// web) need a sign-in or confirmation within the last 10 minutes, and answer
// 403 step_up_needed otherwise: the dialog then asks the person to confirm
// it's them, which a script can't do for them (a passkey or authenticator
// prompt, or a code from their inbox).
//
// A write answers {"notice": "..."} with the text the account pages showed,
// plus what the dialog needs next (the new space's address).

type siteAccountSpace struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Space    string `json:"space,omitempty"`
	Ready    bool   `json:"ready"`
	Web      bool   `json:"web,omitempty"`
	Timezone string `json:"timezone,omitempty"`
	// Usage is what the space takes up, once it was measured (0.89.0).
	Usage *siteUsage `json:"usage,omitempty"`
}

// siteUsage is storage in bytes: Git is the space's repository (every
// version of every text file), Files its virtual files' content and text in
// the bucket (every version kept), Bytes both. Measured is when the oldest
// number in it was measured (RFC 3339).
type siteUsage struct {
	Bytes    int64  `json:"bytes"`
	Git      int64  `json:"git"`
	Files    int64  `json:"files"`
	Measured string `json:"measured,omitempty"`
}

// siteStorage is the account's storage: the sum over the spaces it owns.
// Deleted is what its deleted spaces take up until they are removed for
// good, 30 days after deletion; it is not in Bytes. Allowance is 0 while
// no limit applies (phase 2 of the payment plan sets it).
type siteStorage struct {
	siteUsage
	Deleted   int64 `json:"deleted,omitempty"`
	Allowance int64 `json:"allowance,omitempty"`
}

type siteJoinedSpace struct {
	Space  string `json:"space"`
	Name   string `json:"name"`
	Owner  string `json:"owner"`
	Status string `json:"status"`
	// Open: the space can be opened now (active membership, space loaded).
	Open bool `json:"open,omitempty"`
}

type siteInvitation struct {
	ID      string `json:"id"`
	SpaceID string `json:"spaceId,omitempty"`
	Name    string `json:"name,omitempty"`
	Owner   string `json:"owner,omitempty"`
	Email   string `json:"email,omitempty"`
	Status  string `json:"status,omitempty"`
	Expires string `json:"expires"`
}

type siteOffer struct {
	SpaceID string `json:"spaceId"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	From    string `json:"from"`
	Expires string `json:"expires"`
}

type siteAccount struct {
	Email           string             `json:"email"`
	Username        string             `json:"username,omitempty"`
	MaxSpaces       int                `json:"maxSpaces"`
	CanCreate       bool               `json:"canCreate"`
	DefaultTimezone string             `json:"defaultTimezone"`
	Apps            bool               `json:"apps,omitempty"`
	Fresh           bool               `json:"fresh,omitempty"`
	Spaces          []siteAccountSpace `json:"spaces"`
	Joined          []siteJoinedSpace  `json:"joined"`
	Invitations     []siteInvitation   `json:"invitations"`
	Offers          []siteOffer        `json:"offers"`
	// Storage is the account's storage (0.89.0); absent when it could not
	// be read.
	Storage *siteStorage `json:"storage,omitempty"`
	// Billing is the account's plan (0.91.0, site_billing.go); absent when
	// the server sells no plans.
	Billing *siteBilling `json:"billing,omitempty"`
	// Unavailable names the parts that could not be read just now.
	Unavailable []string `json:"unavailable,omitempty"`
}

type siteMember struct {
	ID              string `json:"id"`
	Email           string `json:"email"`
	Username        string `json:"username,omitempty"`
	Status          string `json:"status"`
	AgentPermission string `json:"agentPermission"`
}

type siteTransfer struct {
	ToEmail    string `json:"toEmail"`
	ToUsername string `json:"toUsername,omitempty"`
	Expires    string `json:"expires"`
	Expired    bool   `json:"expired,omitempty"`
}

type siteSharing struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Slug        string           `json:"slug"`
	Space       string           `json:"space"`
	Invitations []siteInvitation `json:"invitations"`
	Members     []siteMember     `json:"members"`
	Transfer    *siteTransfer    `json:"transfer,omitempty"`
	Candidates  []siteMember     `json:"candidates"`
	Fresh       bool             `json:"fresh,omitempty"`
	Apps        bool             `json:"apps,omitempty"`
}

type siteConfirm struct {
	Fresh bool `json:"fresh,omitempty"`
	// Minutes left of the window, while fresh.
	Minutes int `json:"minutes,omitempty"`
	// The ways this account can confirm: Email (a code to Address), TOTP, and
	// Passkey, the WebAuthn request options when it has passkeys.
	Email   bool   `json:"email,omitempty"`
	Address string `json:"address"`
	TOTP    bool   `json:"totp,omitempty"`
	Passkey string `json:"passkey,omitempty"`
	// Sent: a code is waiting for this session.
	Sent bool `json:"sent,omitempty"`
}

// siteAccountBody is every field an account write may send; each route reads
// its own and ignores the rest being empty.
type siteAccountBody struct {
	Username          string  `json:"username"`
	Name              string  `json:"name"`
	Slug              *string `json:"slug"`
	Timezone          string  `json:"timezone"`
	Visibility        string  `json:"visibility"`
	Email             string  `json:"email"`
	Space             string  `json:"space"`
	Invitation        string  `json:"invitation"`
	Member            string  `json:"member"`
	Permission        string  `json:"permission"`
	Code              string  `json:"code"`
	Credential        string  `json:"credential"`
	ClientData        string  `json:"client_data"`
	AuthenticatorData string  `json:"authenticator_data"`
	Signature         string  `json:"signature"`
	UserHandle        string  `json:"user_handle"`
	// Services (0.80.0, site_services.go).
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Key      string            `json:"key"`
	Endpoint string            `json:"endpoint"`
	Cap      string            `json:"cap"`
	Agent    string            `json:"agent"`
	Spaces   map[string]string `json:"spaces"`
	// Security (0.81.0, site_security.go).
	Attestation string `json:"attestation"`
	Transports  string `json:"transports"`
	Passkey     string `json:"passkey"`
	EmailLogin  string `json:"email_login"`
	Session     string `json:"session"`
	Identity    string `json:"identity"`
	// Billing (0.92.0, site_billing.go).
	StorageGB *int `json:"storageGb"`
	Preview   bool `json:"preview"`
}

type siteNotice struct {
	Notice string `json:"notice,omitempty"`
	// Space is the owner/slug a write created or handed over, to open.
	Space string `json:"space,omitempty"`
	// SignedOut: the session ended (the account was marked for deletion).
	SignedOut bool `json:"signedOut,omitempty"`
	// Failed: the notice says something did not work (an agent's test).
	Failed bool `json:"failed,omitempty"`
	// Redirect is where to go next (GitHub, to connect it).
	Redirect string `json:"redirect,omitempty"`
	// Preview is what a change of extra storage would cost (0.92.0).
	Preview *siteStoragePreview `json:"preview,omitempty"`
}

// siteSpaceActions are the POST routes /account/spaces/{id}/{action}.
var siteSpaceActions = map[string]bool{
	"visibility": true, "timezone": true, "invite": true, "cancel": true, "suspend": true, "restore": true,
	"remove": true, "agent": true, "transfer-offer": true, "transfer-cancel": true, "delete": true,
}

// stepUpMessages say why a route needed a recent confirmation.
var stepUpMessages = map[string]string{
	"visibility":      "Making a space readable on the web needs a sign-in within the last 10 minutes. Confirm it’s you first.",
	"transfer-offer":  "Offering a space to someone needs a sign-in within the last 10 minutes. Confirm it’s you first.",
	"delete":          "Deleting a space needs a sign-in within the last 10 minutes. Confirm it’s you first.",
	"billing-portal":  "Managing your subscription needs a sign-in within the last 10 minutes. Confirm it’s you first.",
	"billing-storage": "Changing your storage needs a sign-in within the last 10 minutes, as it changes what you pay. Confirm it’s you first.",
	"account-delete":  "Deleting your account needs a sign-in within the last 10 minutes. Confirm it’s you first.",
	"app-spaces":      "Giving an app more of your spaces needs a sign-in within the last 10 minutes. Confirm it’s you first.",
	"security":        "Changing how you sign in needs a sign-in within the last 10 minutes. Confirm it’s you first.",
	"agent-add":       "Adding an agent needs a sign-in within the last 10 minutes, because its endpoint receives the files your folder actions read. Confirm it’s you first.",
}

// sendSiteAccountError answers a failed account route. A missing step-up is
// 403 step_up_needed, which the dialog answers with Confirm it's you.
func sendSiteAccountError(w http.ResponseWriter, err error, route string) {
	if errors.Is(err, errConfirmFirst) {
		message := stepUpMessages[route]
		if message == "" {
			message = "Confirm it’s you first: this needs a sign-in within the last 10 minutes."
		}
		sendError(w, problem(403, "step_up_needed", message))
		return
	}
	var p *Error
	if errors.As(err, &p) && p.Code == "confirm_required" {
		// Another step-up with its own words (changing the email address).
		sendError(w, problem(403, "step_up_needed", p.Message))
		return
	}
	if p == nil {
		log.Printf("site account %s failed: %v", route, err)
		err = problem(503, "unavailable", "That could not be saved just now. Try again shortly.")
	}
	sendError(w, err)
}

func sendSiteNotice(w http.ResponseWriter, value siteNotice) {
	if text, ok := accountNotices[value.Notice]; ok {
		value.Notice = text
	}
	sendJSON(w, 200, value)
}

// serveSiteAccount runs after serveSiteAPI's checks; rest is the path after
// /api/v1/site/account ("" or "/...").
func (h *httpAdapter) serveSiteAccount(w http.ResponseWriter, r *http.Request, user userAccount, signedIn bool, session, client, rest string) {
	if !signedIn {
		sendError(w, problem(401, "unauthorized", "Sign in to see your account."))
		return
	}
	if r.URL.RawQuery != "" {
		sendError(w, invalid("Unknown query parameter."))
		return
	}
	parts := strings.Split(strings.TrimPrefix(rest, "/"), "/")
	if rest == "" {
		parts = nil
	}
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	get := func() bool {
		if !read {
			w.Header().Set("Allow", "GET")
			sendError(w, problem(405, "invalid_request", "Use GET."))
		}
		return read
	}
	route := strings.Join(parts, "/")
	if len(parts) > 0 && parts[0] == "services" {
		h.serveSiteServices(w, r, user, session, parts[1:])
		return
	}
	if len(parts) > 0 && parts[0] == "security" {
		h.serveSiteSecurity(w, r, user, session, client, parts[1:])
		return
	}
	switch {
	case len(parts) == 0:
		if get() {
			sendJSON(w, 200, h.siteAccountOverview(r.Context(), user, session))
		}
		return
	case route == "confirm":
		if get() {
			value, err := h.siteConfirmState(r.Context(), user, session)
			if err != nil {
				sendSiteAccountError(w, err, route)
				return
			}
			sendJSON(w, 200, value)
		}
		return
	case len(parts) == 3 && parts[0] == "spaces" && parts[2] == "sharing" && idPattern.MatchString(parts[1]):
		if get() {
			value, err := h.siteSharingState(r.Context(), user, session, parts[1])
			if err != nil {
				sendSiteAccountError(w, err, route)
				return
			}
			sendJSON(w, 200, value)
		}
		return
	}
	known := route == "username" || route == "spaces" || route == "invitations/accept" || route == "offers/accept" ||
		route == "offers/decline" || route == "delete" || route == "confirm/send" || route == "confirm/verify" ||
		route == "confirm/totp" || route == "confirm/passkey" || (route == "billing/portal" || route == "billing/storage") && h.service.accounts.paddle != nil ||
		len(parts) == 3 && parts[0] == "spaces" && idPattern.MatchString(parts[1]) && siteSpaceActions[parts[2]]
	if !known {
		sendError(w, missing())
		return
	}
	body, ok := h.readSiteAccountBody(w, r, 16*1024)
	if !ok {
		return
	}
	ctx := r.Context()
	var value siteNotice
	var err error
	switch {
	case route == "username":
		err = h.service.rates.take(allowance{"username:user:" + user.ID, 20, 600})
		if err == nil {
			err = h.service.accounts.store.ChooseUsername(ctx, user.ID, body.Username)
		}
	case route == "spaces":
		value, err = h.siteCreateSpace(ctx, user, body)
	case route == "invitations/accept":
		err = h.service.rates.take(allowance{"membership:user:" + user.ID, 30, 600})
		if err == nil {
			err = h.service.ownedDB.acceptHumanInvitation(ctx, user.ID, body.Space, body.Invitation)
		}
	case route == "offers/accept" || route == "offers/decline":
		value, err = h.siteTransfer(ctx, user, session, strings.TrimPrefix(route, "offers/"), body.Space, "")
	case route == "delete":
		value, err = h.siteDeleteAccount(w, ctx, user, session, body.Email)
		route = "account-delete"
	case route == "billing/portal":
		value, err = h.siteBillingPortal(ctx, user, session)
		route = "billing-portal"
	case route == "billing/storage":
		value, err = h.siteBillingStorage(ctx, user, session, body)
		route = "billing-storage"
	case strings.HasPrefix(route, "confirm/"):
		value, err = h.siteConfirm(ctx, user, session, client, strings.TrimPrefix(route, "confirm/"), body)
	default:
		value, err = h.siteSpaceAction(ctx, user, session, parts[1], parts[2], body)
		route = parts[2]
	}
	if err != nil {
		sendSiteAccountError(w, err, route)
		return
	}
	sendSiteNotice(w, value)
}

// readSiteAccountBody checks an account write is a POST of strict JSON of at
// most limit bytes, and reads it; it answers the problem itself otherwise.
func (h *httpAdapter) readSiteAccountBody(w http.ResponseWriter, r *http.Request, limit int64) (siteAccountBody, bool) {
	var body siteAccountBody
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		sendError(w, problem(405, "invalid_request", "Use POST."))
		return body, false
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		sendError(w, problem(415, "invalid_request", "Use application/json."))
		return body, false
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		sendError(w, problem(413, "payload_too_large", "Request exceeds the byte limit."))
		return body, false
	}
	if !validUnicodeJSON(b) || noDuplicateKeys(b) != nil || strictJSON(b, &body) != nil {
		sendError(w, invalid("Send a JSON object with the fields this route takes."))
		return body, false
	}
	if h.service.ownedDB == nil {
		sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable."))
		return body, false
	}
	return body, true
}

// siteAccountOverview is GET /account. A part that can't be read is named
// in Unavailable and left empty, as the Spaces page showed it.
func (h *httpAdapter) siteAccountOverview(ctx context.Context, user userAccount, session string) siteAccount {
	out := siteAccount{
		Email: user.Email, Username: user.Username, MaxSpaces: user.MaxPrivateSpaces, DefaultTimezone: defaultAgentTimezone,
		Apps: h.oauth != nil, Spaces: []siteAccountSpace{}, Joined: []siteJoinedSpace{}, Invitations: []siteInvitation{}, Offers: []siteOffer{},
	}
	out.Fresh, _ = h.service.accounts.sessionFresh(session, time.Now())
	storage, storageErr := h.service.accountStorage(ctx, user.ID)
	if storageErr != nil {
		out.Unavailable = append(out.Unavailable, "storage")
	} else {
		out.Storage = &siteStorage{siteUsage: siteUsage{Bytes: storage.Total, Git: storage.Git, Files: storage.Files, Measured: siteTime(storage.Oldest)}, Deleted: storage.Deleted}
	}
	var used *int64
	if storageErr == nil {
		used = &storage.Total
	}
	if billing, err := h.siteBillingState(ctx, user, used); err != nil {
		log.Printf("site account: billing for %s: %v", user.ID, err)
		out.Unavailable = append(out.Unavailable, "billing")
	} else {
		out.Billing = billing
	}
	if spaces, err := h.accountSpaces(ctx, user); err != nil {
		out.Unavailable = append(out.Unavailable, "spaces")
	} else {
		for _, s := range spaces {
			item := siteAccountSpace{ID: s.ID, Name: s.Name, Slug: s.Slug, Ready: s.Ready, Web: s.Web, Timezone: s.Timezone}
			if s.Ready && user.Username != "" {
				item.Space = user.Username + "/" + s.Slug
			}
			if u, ok := storage.Spaces[s.ID]; ok && storageErr == nil {
				item.Usage = &siteUsage{Bytes: u.Total(), Git: u.GitBytes, Files: u.FileBytes, Measured: siteTime(u.oldest())}
			}
			out.Spaces = append(out.Spaces, item)
		}
		out.CanCreate = user.Username != "" && len(spaces) < user.MaxPrivateSpaces
	}
	if invitations, joined, err := h.accountMemberships(ctx, user); err != nil {
		out.Unavailable = append(out.Unavailable, "memberships")
	} else {
		for _, i := range invitations {
			out.Invitations = append(out.Invitations, siteInvitation{ID: i.ID, SpaceID: i.SpaceID, Name: i.Name, Owner: i.Owner, Expires: i.Expires})
		}
		for _, j := range joined {
			out.Joined = append(out.Joined, siteJoinedSpace{Space: j.Owner + "/" + j.Slug, Name: j.Name, Owner: j.Owner, Status: j.Status, Open: j.URL != ""})
		}
	}
	if offers, err := h.service.ownedDB.transferOffers(ctx, user.ID, time.Now()); err != nil {
		out.Unavailable = append(out.Unavailable, "offers")
	} else {
		for _, o := range offers {
			out.Offers = append(out.Offers, siteOffer{SpaceID: o.SpaceID, Name: o.Name, Slug: o.Slug, From: o.From, Expires: o.Expires})
		}
	}
	return out
}

func (h *httpAdapter) siteSharingState(ctx context.Context, user userAccount, session, spaceID string) (siteSharing, error) {
	page, err := h.humanSharing(ctx, user, spaceID)
	if err != nil {
		return siteSharing{}, err
	}
	out := siteSharing{ID: page.ID, Name: page.Name, Slug: page.Slug, Space: user.Username + "/" + page.Slug, Apps: h.oauth != nil,
		Invitations: []siteInvitation{}, Members: []siteMember{}, Candidates: []siteMember{}}
	out.Fresh, _ = h.service.accounts.sessionFresh(session, time.Now())
	for _, i := range page.Invitations {
		out.Invitations = append(out.Invitations, siteInvitation{ID: i.ID, Email: i.Email, Status: i.Status, Expires: i.Expires})
	}
	member := func(m humanMember) siteMember {
		return siteMember{ID: m.ID, Email: m.Email, Username: m.Username, Status: m.Status, AgentPermission: m.AgentPermission}
	}
	for _, m := range page.Members {
		out.Members = append(out.Members, member(m))
	}
	for _, m := range page.Candidates {
		out.Candidates = append(out.Candidates, member(m))
	}
	if t := page.Transfer; t != nil {
		out.Transfer = &siteTransfer{ToEmail: t.ToEmail, ToUsername: t.ToUsername, Expires: t.Expires, Expired: t.Expired}
	}
	return out, nil
}

// siteCreateSpace is POST /account/spaces. Without a slug the address is
// made from the name; with one, an interrupted space finishes at the address
// it reserved.
func (h *httpAdapter) siteCreateSpace(ctx context.Context, user userAccount, body siteAccountBody) (siteNotice, error) {
	err := h.service.rates.take(allowance{"space-create:user:" + user.ID, 10, 600})
	if err != nil {
		return siteNotice{}, err
	}
	slug := ""
	if body.Slug != nil {
		slug = *body.Slug
	} else if slug, err = h.deriveNewSpaceSlug(ctx, user, body.Name); err != nil {
		var p *Error
		if errors.As(err, &p) && p.Code == "conflict" {
			return siteNotice{}, problem(409, "conflict", "You already have a space at "+user.Username+"/"+slug+". Choose a different name.")
		}
		return siteNotice{}, err
	}
	space, err := h.service.createOwnedSpace(ctx, user.ID, body.Name, slug)
	if err != nil {
		var p *Error
		if !errors.As(err, &p) {
			log.Printf("site account: space not created: %v", err)
			return siteNotice{}, problem(503, "unavailable", "Your space is not ready yet. Try again shortly; Retry finishes it at the same address.")
		}
		return siteNotice{}, err
	}
	if zone := body.Timezone; zone != "" && validTimezone(zone) {
		if err := h.service.ownedDB.setSpaceTimezone(ctx, user.ID, space.ID, zone); err != nil {
			log.Printf("space %s: time zone not saved: %v", space.ID, err)
		}
	}
	return siteNotice{Space: user.Username + "/" + space.Slug}, nil
}

// siteSpaceAction is POST /account/spaces/{id}/{action}, on a space the
// session's user owns (the database calls check that).
func (h *httpAdapter) siteSpaceAction(ctx context.Context, user userAccount, session, spaceID, action string, body siteAccountBody) (siteNotice, error) {
	db := h.service.ownedDB
	switch action {
	case "visibility":
		notice, err := h.changeSpaceVisibility(ctx, user, session, spaceID, body.Visibility)
		return siteNotice{Notice: notice}, err
	case "timezone":
		zone := strings.TrimSpace(body.Timezone)
		err := h.service.rates.take(allowance{"space-timezone:user:" + user.ID, 30, 600})
		if err == nil && zone != "" && !validTimezone(zone) {
			err = invalid(zone + " is not a time zone. Use a name such as Pacific/Auckland or Australia/Sydney, or leave it empty for the server's (" + defaultAgentTimezone + ").")
		}
		if err == nil {
			err = db.setSpaceTimezone(ctx, user.ID, spaceID, zone)
		}
		if err != nil {
			return siteNotice{}, err
		}
		h.service.spaceZoneChanged(spaceID)
		return siteNotice{Notice: "space-timezone"}, nil
	case "transfer-offer", "transfer-cancel":
		return h.siteTransfer(ctx, user, session, strings.TrimPrefix(action, "transfer-"), spaceID, body.Member)
	case "delete":
		err := h.service.rates.take(allowance{"space-delete:user:" + user.ID, 10, 600})
		if err == nil {
			if fresh, _ := h.service.accounts.sessionFresh(session, time.Now()); !fresh {
				err = errConfirmFirst
			}
		}
		if err == nil {
			_, err = h.service.deleteOwnedSpace(ctx, user, spaceID, body.Name)
		}
		return siteNotice{Notice: "space-deleted"}, err
	}
	// Sharing: invite, cancel, suspend, restore, remove, agent.
	if err := h.service.rates.take(allowance{"membership:user:" + user.ID, 30, 600}); err != nil {
		return siteNotice{}, err
	}
	switch action {
	case "invite":
		invitationID, err := db.inviteHuman(ctx, user.ID, spaceID, body.Email)
		if err != nil {
			return siteNotice{}, err
		}
		return siteNotice{Notice: h.emailInvitation(ctx, user, spaceID, invitationID)}, nil
	case "cancel":
		return siteNotice{}, db.cancelHumanInvitation(ctx, user.ID, spaceID, body.Invitation)
	case "agent":
		return siteNotice{}, db.setMemberAgentPermission(ctx, user.ID, spaceID, body.Member, body.Permission)
	default:
		return siteNotice{}, db.manageHumanMember(ctx, user.ID, spaceID, body.Member, action)
	}
}

// siteTransfer offers a space (owner, step-up), cancels the offer (owner),
// or accepts or declines it (the member offered it).
func (h *httpAdapter) siteTransfer(ctx context.Context, user userAccount, session, action, spaceID, member string) (siteNotice, error) {
	db, now := h.service.ownedDB, time.Now().UTC()
	if !idPattern.MatchString(spaceID) {
		return siteNotice{}, missing()
	}
	if err := h.service.rates.take(allowance{"transfer:user:" + user.ID, 20, 600}); err != nil {
		return siteNotice{}, err
	}
	switch action {
	case "offer":
		if fresh, _ := h.service.accounts.sessionFresh(session, now); !fresh {
			return siteNotice{}, errConfirmFirst
		}
		if err := db.offerSpaceTransfer(ctx, user.ID, spaceID, member, now); err != nil {
			return siteNotice{}, err
		}
		return siteNotice{Notice: h.emailTransferOffer(ctx, user, spaceID)}, nil
	case "cancel":
		return siteNotice{Notice: "transfer-cancelled"}, db.cancelSpaceTransfer(ctx, user.ID, spaceID)
	case "decline":
		return siteNotice{Notice: "transfer-declined"}, db.declineSpaceTransfer(ctx, user.ID, spaceID)
	}
	result, done, err := h.service.acceptSpaceTransfer(ctx, user.ID, spaceID)
	if err != nil {
		return siteNotice{}, err
	}
	if done {
		h.notifyTransferAccepted(result)
	}
	value := siteNotice{Notice: "transfer-accepted"}
	if user.Username != "" && result.Slug != "" {
		value.Space = user.Username + "/" + result.Slug
	}
	return value, nil
}

// siteDeleteAccount is POST /account/delete: after a recent confirmation the
// person types their email address; the account is marked for deletion and
// signed out everywhere, this browser included.
func (h *httpAdapter) siteDeleteAccount(w http.ResponseWriter, ctx context.Context, user userAccount, session, email string) (siteNotice, error) {
	a := h.service.accounts
	db := a.sessionDB()
	err := h.service.rates.take(allowance{"account-delete:user:" + user.ID, 10, 600})
	if err == nil && db == nil {
		err = problem(503, "unavailable", "Deleting an account is not available on this server.")
	}
	if err == nil {
		if fresh, _ := a.sessionFresh(session, time.Now()); !fresh {
			err = errConfirmFirst
		}
	}
	if err == nil && !strings.EqualFold(strings.TrimSpace(email), user.Email) {
		err = problem(400, "invalid_request", "That is not your email address. Type it exactly as shown under Email.")
	}
	now := time.Now()
	if err == nil {
		err = db.requestAccountDeletion(ctx, user.ID, now)
	}
	if err != nil {
		return siteNotice{}, err
	}
	log.Printf("account deletion requested user=%s", user.ID)
	if endErr := a.endUserSessions(user.ID); endErr != nil {
		log.Printf("account deletion: sessions of %s not all ended in the database", user.ID)
	}
	h.sendSecurityNotice(user, "Your Metatrash account will be deleted", accountDeletionRequestedMail(a.config.Origin, now, now.Add(AccountDeletionGrace)))
	accountCookie(w, h.sessionCookieName(), "", -1)
	// The sign-in page the dialog goes to next shows it.
	accountCookie(w, h.noticeCookieName(), "account-deletion-requested", 60)
	return siteNotice{Notice: "account-deletion-requested", SignedOut: true}, nil
}

// siteConfirmState is GET /account/confirm: the ways this session can
// confirm it's the account's owner. Passkey options carry a fresh challenge.
func (h *httpAdapter) siteConfirmState(ctx context.Context, user userAccount, session string) (siteConfirm, error) {
	a, db, now := h.service.accounts, h.service.ownedDB, time.Now()
	if db == nil {
		return siteConfirm{}, problem(503, "unavailable", "Confirming is temporarily unavailable.")
	}
	out := siteConfirm{Address: user.Email}
	var until time.Time
	if out.Fresh, until = a.sessionFresh(session, now); out.Fresh {
		out.Minutes = int(math.Ceil(until.Sub(now).Minutes()))
	}
	state, err := db.signInState(ctx, user.ID)
	if err != nil {
		return siteConfirm{}, err
	}
	out.Email, out.TOTP = state.EmailLogin, state.TOTP && a.totpKey != nil
	if state.Passkeys > 0 {
		list, err := db.passkeys(ctx, user.ID)
		if err != nil {
			return siteConfirm{}, err
		}
		if len(list) > 0 {
			out.Passkey = a.requestOptions("confirm", session, list, now)
		}
	}
	a.mu.Lock()
	c, waiting := a.challenges[confirmKey(session)]
	out.Sent = waiting && c.Ready && now.Before(c.Expires)
	a.mu.Unlock()
	return out, nil
}

// siteConfirm is POST /account/confirm/{send|verify|totp|passkey}, the same
// checks and limits as Confirm it's you on Security.
func (h *httpAdapter) siteConfirm(ctx context.Context, user userAccount, session, client, how string, body siteAccountBody) (siteNotice, error) {
	a, db, now := h.service.accounts, h.service.ownedDB, time.Now()
	if err := h.service.rates.take(allowance{"signin:user:" + user.ID, 30, 600}); err != nil {
		return siteNotice{}, err
	}
	if how == "send" || how == "verify" {
		state, err := db.signInState(ctx, user.ID)
		if err != nil {
			return siteNotice{}, err
		}
		if !state.EmailLogin {
			return siteNotice{}, problem(403, "email_login_off", "Email sign-in is turned off for this account, so emailed codes cannot confirm it’s you. Confirm with a passkey or your authenticator app.")
		}
	}
	switch how {
	case "send":
		emailKey := a.mac("email:" + user.Email)
		err := h.service.rates.reserve(codeMailBudget, allowance{"mail:ip:" + client, 10, 3600}, allowance{"mail:email:minute:" + emailKey, 1, 60}, allowance{"mail:email:hour:" + emailKey, 3, 3600})
		h.logMailBudget()
		if err == nil {
			err = a.issueCode(ctx, confirmKey(session), user.Email, a.sendConfirm)
		}
		return siteNotice{}, err
	case "verify":
		a.mu.Lock()
		a.cleanup(now)
		email, err := a.checkCodeLocked(confirmKey(session), strings.TrimSpace(body.Code))
		a.mu.Unlock()
		if err == nil && email != user.Email {
			err = problem(400, "invalid_code", "That code is invalid or expired. Try again, or request a new code.")
		}
		if err != nil {
			return siteNotice{}, err
		}
		a.markConfirmed(session, user.ID, now)
	case "totp":
		if _, err := h.submitTOTP(ctx, "/account/confirm/totp", session, user, body.Code, now); err != nil {
			return siteNotice{}, err
		}
	case "passkey":
		fields := map[string]string{"credential": body.Credential, "client_data": body.ClientData, "authenticator_data": body.AuthenticatorData, "signature": body.Signature, "user_handle": body.UserHandle}
		in, err := passkeyAssertionFrom(func(name string) string { return fields[name] })
		if err == nil {
			err = h.confirmWithPasskey(ctx, session, user, in, now)
		}
		if err != nil {
			return siteNotice{}, err
		}
	}
	return siteNotice{Notice: "Confirmed. For the next 10 minutes you can make the changes that need it."}, nil
}
