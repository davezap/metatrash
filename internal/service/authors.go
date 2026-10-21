package service

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Every change records who made it, as trailers at the end of its commit
// message, and history returns them as by:
//
//	Metatrash-By: dave          the account's username, or its email address
//	                            before the @ when it has none; empty for keys
//	Metatrash-App: Claude       the app the user connected (Metatrash Sync for
//	                            mtsync and Slate Viewer), or "space key"
//	Metatrash-Kind: agent       person (a Metatrash Sync write) or agent
//
// Usernames are public, so these show wherever history does. Commits made by
// the service itself (new spaces, added folder files) carry none.
type changeAuthor struct {
	Name string `json:"name,omitempty"`
	App  string `json:"app,omitempty"`
	Kind string `json:"kind"`
}

type authorKey struct{}

// authorSource resolves the author once, on the first commit that needs it,
// so reads never pay for the account lookup.
type authorSource struct {
	once    sync.Once
	resolve func() changeAuthor
	author  changeAuthor
}

func withAuthor(ctx context.Context, resolve func() changeAuthor) context.Context {
	return context.WithValue(ctx, authorKey{}, &authorSource{resolve: resolve})
}

func authorOf(ctx context.Context) (changeAuthor, bool) {
	src, ok := ctx.Value(authorKey{}).(*authorSource)
	if !ok {
		return changeAuthor{}, false
	}
	src.once.Do(func() { src.author = src.resolve() })
	return src.author, true
}

// trailers are the author lines a commit message ends with.
func (a changeAuthor) trailers() string {
	lines := "\n"
	if a.Name != "" {
		lines += "Metatrash-By: " + trailerValue(a.Name) + "\n"
	}
	if a.App != "" {
		lines += "Metatrash-App: " + trailerValue(a.App) + "\n"
	}
	return lines + "Metatrash-Kind: " + trailerValue(a.Kind) + "\n"
}

// parseAuthor reads the author trailers from a commit message, or nil. The
// trailers come last, so later lines win.
func parseAuthor(body string) *changeAuthor {
	var a changeAuthor
	for _, line := range strings.Split(body, "\n") {
		key, value, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		switch key {
		case "Metatrash-By":
			a.Name = value
		case "Metatrash-App":
			a.App = value
		case "Metatrash-Kind":
			a.Kind = value
		}
	}
	if a.Kind == "" {
		return nil
	}
	return &a
}

// emailName is an email address before the @.
func emailName(email string) string {
	name, _, _ := strings.Cut(email, "@")
	return name
}

// oauthAuthor looks up the user and app behind a signed-in connection. A
// failed lookup still records the kind.
func (s *Service) oauthAuthor(ctx context.Context, id oauthIdentity, person bool) changeAuthor {
	a := changeAuthor{Kind: "agent"}
	if person {
		a.Kind = "person"
	}
	if s.ownedDB == nil {
		return a
	}
	lookup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var username, email string
	if err := s.ownedDB.db.QueryRowContext(lookup, `SELECT COALESCE(u.username, ''), u.email, g.client_name FROM metatrash_oauth_grants g JOIN metatrash_users u ON u.user_id = g.user_id WHERE g.grant_id = ? AND g.user_id = ?`, id.GrantID, id.UserID).Scan(&username, &email, &a.App); err != nil {
		return a
	}
	a.Name = username
	if a.Name == "" {
		a.Name = emailName(email)
	}
	return a
}

// keyAuthor is the author of a change through a configured space's endpoint:
// a space key, or none on a public space.
func keyAuthor(key string) func() changeAuthor {
	return func() changeAuthor {
		if key == "" {
			return changeAuthor{App: "anonymous", Kind: "agent"}
		}
		return changeAuthor{App: "space key", Kind: "agent"}
	}
}
