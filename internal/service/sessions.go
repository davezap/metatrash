package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

// Browser sessions (0.31.0, schema v10). The accounts map is the working copy
// the service reads on every request; metatrash_sessions holds the same
// sessions so that a restart or deploy does not sign anyone out. A session is
// written to the database before it is used, and removed from the database
// when it ends. Rows hold the SHA-256 of the cookie's random token (the map
// key), never the token. Sign-in codes and other short-lived state stay in
// memory only.

// lastUsedEvery is how stale a session's last use may get before a request
// records a new one, so ordinary browsing does not write on every request.
const lastUsedEvery = 5 * time.Minute

// sessionSweepEvery is how often signing in also deletes expired rows.
const sessionSweepEvery = time.Hour

// maxSessions bounds sessions held at once, across all accounts.
const maxSessions = 4096

// maxUserSessions bounds one account's sessions; signing in again ends the
// oldest.
const maxUserSessions = 8

func (db *accountDatabase) checkSessionSchema(ctx context.Context) error {
	bad := fmt.Errorf("account schema v11 required; follow docs/deployment.md")
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'metatrash_sessions' AND engine = 'InnoDB'").Scan(&count); err != nil || count != 1 {
		return bad
	}
	var columns sql.NullString
	var nonUnique, prefixes int
	if err := db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ','), COALESCE(SUM(non_unique), 0), COUNT(sub_part) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'metatrash_sessions' AND index_name = 'PRIMARY'").Scan(&columns, &nonUnique, &prefixes); err != nil || columns.String != "session_digest" || nonUnique != 0 || prefixes != 0 {
		return bad
	}
	rows, err := db.db.QueryContext(ctx, "SELECT session_digest, user_id, created_at, expires_at, auth_at, last_used_at, device, ip FROM metatrash_sessions LIMIT 0")
	if err != nil {
		return bad
	}
	rows.Close()
	return nil
}

func sessionContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

// loadSessions returns the unexpired sessions, newest first, at most
// maxSessions, after deleting expired ones.
func (db *accountDatabase) loadSessions(ctx context.Context, now time.Time) (map[string]accountSession, error) {
	if _, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_sessions WHERE expires_at <= ?", now.Unix()); err != nil {
		return nil, fmt.Errorf("cannot sweep expired sessions")
	}
	rows, err := db.db.QueryContext(ctx, "SELECT session_digest, user_id, created_at, expires_at, auth_at, last_used_at, device, ip FROM metatrash_sessions WHERE expires_at > ? ORDER BY created_at DESC LIMIT ?", now.Unix(), maxSessions)
	if err != nil {
		return nil, fmt.Errorf("cannot read sessions")
	}
	defer rows.Close()
	sessions := map[string]accountSession{}
	for rows.Next() {
		var key, user, device, ip string
		var created, expires, authAt, used int64
		if err := rows.Scan(&key, &user, &created, &expires, &authAt, &used, &device, &ip); err != nil {
			return nil, fmt.Errorf("cannot read sessions")
		}
		if len(key) != 64 || !isLowerHex(key) || len(user) != 32 || !isLowerHex(user) {
			return nil, fmt.Errorf("invalid session records")
		}
		s := accountSession{UserID: user, Created: time.Unix(created, 0), Expires: time.Unix(expires, 0), Device: device, IP: ip}
		if authAt > 0 {
			s.AuthAt = time.Unix(authAt, 0)
		}
		if used > 0 {
			s.LastUsed = time.Unix(used, 0)
		}
		sessions[key] = s
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot read sessions")
	}
	return sessions, nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// clip keeps printable ASCII and at most n bytes.
func clip(s string, n int) string {
	s = strings.Map(func(c rune) rune {
		if c < 32 || c > 126 {
			return -1
		}
		return c
	}, s)
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func isLowerHex(s string) bool {
	return strings.Trim(s, "0123456789abcdef") == ""
}

func (db *accountDatabase) insertSession(key string, s accountSession) error {
	ctx, cancel := sessionContext()
	defer cancel()
	_, err := db.db.ExecContext(ctx, "INSERT INTO metatrash_sessions (session_digest, user_id, created_at, expires_at, auth_at, last_used_at, device, ip) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		key, s.UserID, s.Created.Unix(), s.Expires.Unix(), unixOrZero(s.AuthAt), unixOrZero(s.LastUsed), s.Device, s.IP)
	return err
}

func (db *accountDatabase) deleteSessions(keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	ctx, cancel := sessionContext()
	defer cancel()
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	_, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_sessions WHERE session_digest IN (?"+strings.Repeat(", ?", len(keys)-1)+")", args...)
	return err
}

func (db *accountDatabase) sweepSessions(now time.Time) error {
	ctx, cancel := sessionContext()
	defer cancel()
	_, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_sessions WHERE expires_at <= ? LIMIT 1000", now.Unix())
	return err
}

func (db *accountDatabase) updateSessionTimes(key string, s accountSession) error {
	ctx, cancel := sessionContext()
	defer cancel()
	_, err := db.db.ExecContext(ctx, "UPDATE metatrash_sessions SET auth_at = ?, last_used_at = ? WHERE session_digest = ?", unixOrZero(s.AuthAt), unixOrZero(s.LastUsed), key)
	return err
}

// sessionDB is the database that keeps sessions, or nil when the account
// store is not one (tests that run without MariaDB).
func (a *accounts) sessionDB() *accountDatabase {
	db, _ := a.store.(*accountDatabase)
	return db
}

// loadStoredSessions fills the session map from the database at startup.
func (a *accounts) loadStoredSessions(ctx context.Context, now time.Time) error {
	db := a.sessionDB()
	if db == nil {
		return nil
	}
	sessions, err := db.loadSessions(ctx, now)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, s := range sessions {
		a.sessions[k] = s
	}
	a.sessionSwept = now
	log.Printf("account sessions-loaded count=%d", len(sessions))
	return nil
}

// startSessionLocked creates a session for userID that has just proved who
// the user is, and stores it before returning its token. The browser and
// address come from ctx (withSessionOrigin). Callers hold mu.
func (a *accounts) startSessionLocked(ctx context.Context, userID string, now time.Time) (string, error) {
	if len(a.sessions) >= maxSessions {
		return "", problem(503, "sessions_busy", "Please try again later.")
	}
	token, err := randomHex(32)
	if err != nil {
		return "", err
	}
	// Bound active sessions per account; end the oldest when signing in again.
	count, oldestKey := 0, ""
	var oldest time.Time
	for k, session := range a.sessions {
		if session.UserID == userID {
			count++
			if oldestKey == "" || session.Expires.Before(oldest) {
				oldest, oldestKey = session.Expires, k
			}
		}
	}
	unavailable := problem(503, "account_unavailable", "Sign-in is temporarily unavailable. Please try again later.")
	if count >= maxUserSessions {
		if err := a.endSessionsLocked(oldestKey); err != nil {
			return "", unavailable
		}
	}
	origin := originFrom(ctx)
	session := accountSession{UserID: userID, Created: now, Expires: now.Add(sessionLifetime), AuthAt: now, LastUsed: now, Device: clip(origin.Device, 64), IP: clip(origin.IP, 45)}
	key := secretDigest(token)
	if db := a.sessionDB(); db != nil {
		if now.Sub(a.sessionSwept) >= sessionSweepEvery {
			a.sessionSwept = now
			if err := db.sweepSessions(now); err != nil {
				log.Printf("account sessions-sweep failed")
			}
		}
		if err := db.insertSession(key, session); err != nil {
			log.Printf("account session-store failed user=%s", userID)
			return "", unavailable
		}
	}
	a.sessions[key] = session
	return token, nil
}

// endSessionsLocked ends the sessions with these keys (digests of their
// tokens). They end in this process whatever happens; the error says the
// database still has them, so they would come back after a restart. Callers
// hold mu.
func (a *accounts) endSessionsLocked(keys ...string) error {
	for _, k := range keys {
		delete(a.sessions, k)
	}
	db := a.sessionDB()
	if db == nil {
		return nil
	}
	if err := db.deleteSessions(keys); err != nil {
		log.Printf("account session-delete failed count=%d", len(keys))
		return err
	}
	return nil
}

// endSession signs out the session token (sign out, or a browser replacing
// its session). A database failure is logged: the browser loses its cookie
// anyway, and the row expires within a day.
func (a *accounts) endSession(token string) {
	if token == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	key := secretDigest(token)
	if _, ok := a.sessions[key]; ok {
		_ = a.endSessionsLocked(key)
	}
}

// otherSessions counts the user's unexpired sessions besides token.
func (a *accounts) otherSessions(userID, token string, now time.Time) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	keep, n := secretDigest(token), 0
	for k, session := range a.sessions {
		if k != keep && session.UserID == userID && now.Before(session.Expires) {
			n++
		}
	}
	return n
}

// endOtherSessions signs the user out everywhere except the session token and
// returns how many sessions it ended. An error means the database could not
// delete them: they are signed out until the service restarts.
func (a *accounts) endOtherSessions(userID, token string) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.endOtherSessionsLocked(userID, token)
}

func (a *accounts) endOtherSessionsLocked(userID, token string) (int, error) {
	keep := secretDigest(token)
	var keys []string
	for k, session := range a.sessions {
		if k != keep && session.UserID == userID {
			keys = append(keys, k)
		}
	}
	return len(keys), a.endSessionsLocked(keys...)
}

// sessionFresh reports whether the session signed in or confirmed within
// stepUpWindow, and until when.
func (a *accounts) sessionFresh(token string, now time.Time) (bool, time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[secretDigest(token)]
	if !ok || !now.Before(session.Expires) || session.AuthAt.IsZero() {
		return false, time.Time{}
	}
	until := session.AuthAt.Add(stepUpWindow)
	return now.Before(until), until
}

// markConfirmed records that the session's user has just proved who they are.
func (a *accounts) markConfirmed(token string, userID string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := secretDigest(token)
	session, ok := a.sessions[key]
	if !ok || session.UserID != userID || !now.Before(session.Expires) {
		return false
	}
	session.AuthAt, session.LastUsed = now, now
	a.sessions[key] = session
	if db := a.sessionDB(); db != nil {
		// Losing this only means confirming again after a restart.
		if err := db.updateSessionTimes(key, session); err != nil {
			log.Printf("account session-update failed user=%s", userID)
		}
	}
	return true
}

func (a *accounts) currentUser(ctx context.Context, token string) (userAccount, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.cleanup(now)
	key := secretDigest(token)
	session, ok := a.sessions[key]
	if !ok {
		return userAccount{}, false, nil
	}
	if now.Sub(session.LastUsed) >= lastUsedEvery {
		session.LastUsed = now
		a.sessions[key] = session
		if db := a.sessionDB(); db != nil {
			if err := db.updateSessionTimes(key, session); err != nil {
				log.Printf("account session-update failed user=%s", session.UserID)
			}
		}
	}
	return a.store.ByID(ctx, session.UserID)
}

// sessionView is one row of the signed-in devices table on Security.
type sessionView struct {
	ID, Device, IP     string
	SignedIn, LastUsed string
	Current            bool
}

// sessionRowID names a session in the devices table without revealing its
// digest. It changes when the service restarts; a stale form just fails.
func (a *accounts) sessionRowID(key string) string {
	return a.mac("session-row:" + key)[:32]
}

// userSessions lists the user's unexpired sessions: this one first, then the
// most recently used.
func (a *accounts) userSessions(userID, token string, now time.Time) []sessionView {
	a.mu.Lock()
	defer a.mu.Unlock()
	current := secretDigest(token)
	type row struct {
		view sessionView
		used time.Time
	}
	var rows []row
	for k, s := range a.sessions {
		if s.UserID != userID || !now.Before(s.Expires) {
			continue
		}
		v := sessionView{ID: a.sessionRowID(k), Device: s.Device, IP: s.IP, SignedIn: sinceText(s.Created, now), LastUsed: sinceText(s.LastUsed, now), Current: k == current}
		if v.Device == "" {
			v.Device = "Unknown browser"
		}
		if v.Current {
			v.LastUsed = "now"
		}
		rows = append(rows, row{v, s.LastUsed})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].view.Current != rows[j].view.Current {
			return rows[i].view.Current
		}
		if !rows[i].used.Equal(rows[j].used) {
			return rows[i].used.After(rows[j].used)
		}
		return rows[i].view.ID < rows[j].view.ID
	})
	views := make([]sessionView, len(rows))
	for i, r := range rows {
		views[i] = r.view
	}
	return views
}

// endSessionByID signs out one of the user's other sessions, by its row ID
// in the devices table. found is false when it is not (or no longer) there.
func (a *accounts) endSessionByID(userID, token, id string) (found bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	current := secretDigest(token)
	for k, s := range a.sessions {
		if k != current && s.UserID == userID && len(id) == 32 && a.sessionRowID(k) == id {
			return true, a.endSessionsLocked(k)
		}
	}
	return false, nil
}
