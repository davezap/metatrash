// Package extension defines build-time application composition contracts.
// Implementations are trusted code linked by the executable, never loaded at runtime.
package extension

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"time"
)

type User struct {
	Username         string    `json:"-"`
	ID               string    `json:"id"`
	Email            string    `json:"email"`
	CreatedAt        time.Time `json:"createdAt"`
	MaxPrivateSpaces int       `json:"maxPrivateSpaces"`
}
type Host interface {
	Database() *sql.DB
	Account(context.Context, string) (User, bool, error)
	CurrentUser(context.Context, string) (User, bool, error)
	SessionFresh(string, time.Time) (bool, time.Time)
	IssueSession(context.Context, string, string, string) (string, error)
	EndOtherSessions(string, string) (int, error)
	AccountUsage(context.Context, string) (int64, error)
	Rate(string, int, int) error
}

// Mailer is implemented by the Host a module is given when the service can
// send email (Core 0.95.27): a module type-asserts its Host to it. SendMail
// sends one plain-text message through the service's own mail settings
// (accounts.json smtp*), the ones sign-in codes use. subject must be
// printable ASCII; body is UTF-8. An error wrapping ErrMailRejected means
// the address or the message was refused for good (an invalid or reserved
// address, or a 5xx answer from the mail server): don't retry it. Any other
// error may be temporary.
type Mailer interface {
	SendMail(ctx context.Context, to, subject, body string) error
}

// ErrMailRejected marks a send that will never succeed (see Mailer).
var ErrMailRejected = errors.New("mail rejected")

// EmailReceiver is implemented by the Host when the service receives email
// (Core 0.97.0, accounts.json email): a module that takes mail in, such as
// an Amazon SES receiver, type-asserts its Host to it. ReceiveEmail files
// one raw RFC 5322 message sent to recipient (the envelope recipient, a
// folder's address) in that folder. checks are the verdicts the module's
// mail service found, which Core trusts. Errors are *server.Error values:
// not_found for an address no folder has (drop the message), sender_refused
// or refused for one the folder won't take, payload_too_large; any other,
// such as rate_limited or unavailable, may succeed later. EmailDomain is the
// domain addresses use, or "" when receiving is off.
type EmailReceiver interface {
	ReceiveEmail(ctx context.Context, recipient string, raw []byte, checks EmailChecks) (EmailReceipt, error)
	EmailDomain() string
}

// EmailChecks are a relay's verdicts on a message, each "pass", "fail",
// "gray", "none" (and SPF's "softfail", "neutral", ...) or "" when it did not
// check. A Virus or Spam "fail" is refused; a DMARC "fail" counts as forged
// for folders that allow only some senders.
type EmailChecks struct {
	SPF   string `json:"spf,omitempty"`
	DKIM  string `json:"dkim,omitempty"`
	DMARC string `json:"dmarc,omitempty"`
	Spam  string `json:"spam,omitempty"`
	Virus string `json:"virus,omitempty"`
}

// EmailReceipt says where a message was filed. Duplicate is true when the
// space already had it (the same Message-ID): nothing new was filed.
type EmailReceipt struct {
	SpaceID   string `json:"spaceID"`
	Path      string `json:"path"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

type AssetEntry struct {
	Entry string
	CSS   []string
}
type HTTP interface {
	Host
	BasePath() string
	Origin() string
	Client(*http.Request) string
	SessionCookie() string
	NoticeCookie() string
	User() User
	Session() string
	FrontendEntry(string) *AssetEntry
	RedirectToLogin(http.ResponseWriter, *http.Request)
	ContinueTo(http.ResponseWriter, *http.Request, string)
	NotifySignInChange(User, string, string)
	RenderMessage(http.ResponseWriter, int, string, string, string, string)
	DecodeJSON(io.Reader, any) error
}
type Handler func(http.ResponseWriter, *http.Request, HTTP)
type Route struct {
	Path, Method string
	Handle       Handler
}
type Field struct {
	Scope, Key string
	Read       func(context.Context, *http.Request, HTTP) (any, error)
}
type LoginProvider struct {
	Path, Label, Hint, FormOrigin string
	Start                         Handler
	Waiting                       func(*http.Request, HTTP) (label, email, returnPath string)
}
type Hooks struct {
	Name          string
	PublicRoutes  []Route
	AccountRoutes []Route
	Fields        []Field
	Login         *LoginProvider
	Notices       map[string]string
	Start         func(context.Context) error
	Close         func()
	Purge         func(context.Context, *sql.Tx, string) error
	// ActsFor lists the owner accounts (user IDs) an account may act for as
	// their owner, for example organisations it administers. Core treats the
	// account as the owner of those accounts' spaces: it reads, writes and
	// manages them as their owner would. An error grants nothing.
	ActsFor func(ctx context.Context, userID string) ([]string, error)
	// SpaceGate is asked before an account opens a space it doesn't own
	// itself, by the website, apps (OAuth) or Sync: may userID use spaces
	// owned by ownerID right now? nil lets Core's own checks decide. An
	// error refuses: a *server.Error is shown as it is (say what to do),
	// anything else refuses as "unavailable". Spaces readable on the web
	// stay readable by everyone. Company organisations use it to require a
	// recent Microsoft sign-in from their tenants.
	SpaceGate func(ctx context.Context, userID, ownerID string) error
	// WriteGate is asked before a change is committed to an account-owned
	// space, whoever makes it: the website, apps and Sync, folder actions,
	// GitHub folders and refreshed virtual files. nil lets the write go on;
	// an error refuses it, shown as SpaceGate's are. Company billing uses it
	// for memberships and storage allowances.
	WriteGate func(ctx context.Context, w Write) error
	// SpaceCreateGate is asked before userID creates a space of its own.
	// An error refuses, shown as SpaceGate's are.
	SpaceCreateGate func(ctx context.Context, userID string) error
	// AdmitAccount is asked before a new account is made for email (the
	// first sign-in with an emailed code), with the ID it will have. An
	// error refuses the sign-in, shown as SpaceGate's are. Existing accounts
	// never ask. Company billing uses it for the controlled launch.
	AdmitAccount func(ctx context.Context, userID, email string) error
	// Brand names and dresses the service (Core 0.95.28; see Brand). At
	// most one module may set it.
	Brand *Brand
	// Tools are agent tools the module adds (Core 0.98.0; see Tool).
	Tools []Tool
}

// Write describes a change about to be committed to an owned space.
type Write struct {
	// ActorID is the account making the change: the signed-in person, or
	// the account a folder action runs for. Empty for a space key or the
	// service itself.
	ActorID string
	OwnerID string
	SpaceID string
	// Bytes is what the change's new Git objects take up. Even a change
	// that only removes files adds a few hundred bytes (its commit).
	Bytes int64
	// OnlyRemoves is true when the change deletes files and adds nothing.
	OnlyRemoves bool
}
type Module func(Host) (Hooks, error)
