// Package extension defines build-time application composition contracts.
// Implementations are trusted code linked by the executable, never loaded at runtime.
package extension

import (
	"context"
	"database/sql"
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
