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
}
type Module func(Host) (Hooks, error)
