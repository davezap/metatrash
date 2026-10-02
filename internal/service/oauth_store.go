package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"
)

const scopeRead = "spaces:read"
const scopeReadWrite = "spaces:read spaces:write"

// oauthSpaceChoice is one consented space from the consent form.
type oauthSpaceChoice struct {
	SpaceID    string
	Permission string // read_only or read_write
}

// accessibleSpace is a space the account can currently consent to.
type accessibleSpace struct {
	ID, Name, Slug, Owner string
	Owned                 bool
	CanWrite              bool // owner, or member with read_write agent permission
}

// oauthTokenPair is returned to the client once; only digests are stored.
type oauthTokenPair struct {
	Access, Refresh string
	Scope           string
	AccessTTL       time.Duration
}

// oauthIdentity is the result of validating an access token for one request.
type oauthIdentity struct {
	GrantID, UserID, ClientID, Scope string
}

func (id oauthIdentity) canWrite() bool { return id.Scope == scopeReadWrite }

// newOAuthToken returns a 256-bit random token with a recognisable prefix.
func newOAuthToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// accessibleSpaces lists ready owned spaces and active memberships for the consent page.
func (db *accountDatabase) accessibleSpaces(ctx context.Context, user userAccount) ([]accessibleSpace, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	spaces := []accessibleSpace{}
	rows, err := db.db.QueryContext(ctx, "SELECT "+ownedSpaceColumns+" FROM metatrash_spaces WHERE owner_user_id = ? AND provisioning_state = 'ready' ORDER BY created_at, space_id LIMIT 200", user.ID)
	if err != nil {
		return nil, fmt.Errorf("cannot list spaces")
	}
	for rows.Next() {
		space, err := scanOwnedSpace(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		spaces = append(spaces, accessibleSpace{ID: space.ID, Name: space.Name, Slug: space.Slug, Owner: user.Username, Owned: true, CanWrite: true})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("cannot list spaces")
	}
	rows, err = db.db.QueryContext(ctx, `SELECT s.space_id, s.name, s.slug, COALESCE(u.username, ''), m.agent_permission FROM metatrash_memberships m JOIN metatrash_spaces s ON s.space_id = m.space_id JOIN metatrash_users u ON u.user_id = s.owner_user_id WHERE m.user_id = ? AND m.status = 'active' AND s.provisioning_state = 'ready' ORDER BY m.joined_at, s.space_id LIMIT 200`, user.ID)
	if err != nil {
		return nil, fmt.Errorf("cannot list joined spaces")
	}
	defer rows.Close()
	for rows.Next() {
		var item accessibleSpace
		var permission string
		if err := rows.Scan(&item.ID, &item.Name, &item.Slug, &item.Owner, &permission); err != nil {
			return nil, fmt.Errorf("cannot list joined spaces")
		}
		item.CanWrite = permission == "read_write"
		spaces = append(spaces, item)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot list joined spaces")
	}
	return spaces, nil
}

// grantConsent returns the current consent for an existing connection, keyed by space ID.
func (db *accountDatabase) grantConsent(ctx context.Context, userID, clientID, resource string) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := db.db.QueryContext(ctx, "SELECT gs.space_id, gs.permission FROM metatrash_oauth_grants g JOIN metatrash_oauth_grant_spaces gs ON gs.grant_id = g.grant_id WHERE g.user_id = ? AND g.client_id = ? AND g.resource = ?", userID, clientID, resource)
	if err != nil {
		return nil, fmt.Errorf("cannot read existing connection")
	}
	defer rows.Close()
	consent := map[string]string{}
	for rows.Next() {
		var id, permission string
		if err := rows.Scan(&id, &permission); err != nil {
			return nil, fmt.Errorf("cannot read existing connection")
		}
		consent[id] = permission
	}
	return consent, rows.Err()
}

// saveGrant creates or replaces the connection for (user, client, resource).
// Spaces are locked in ID order, as membership changes lock a space first, and
// current ownership or active membership is re-checked inside the transaction.
// Write access needs the owner or a read_write member permission.
func (db *accountDatabase) saveGrant(ctx context.Context, userID string, client oauthClient, resource string, choices []oauthSpaceChoice, now time.Time, idle time.Duration) (string, string, error) {
	if !idPattern.MatchString(userID) || len(choices) > 200 {
		return "", "", invalid("Invalid connection request.")
	}
	selected := append([]oauthSpaceChoice(nil), choices...)
	sort.Slice(selected, func(i, j int) bool { return selected[i].SpaceID < selected[j].SpaceID })
	scope := scopeRead
	for i, choice := range selected {
		if !idPattern.MatchString(choice.SpaceID) || (choice.Permission != "read_only" && choice.Permission != "read_write") || (i > 0 && selected[i-1].SpaceID == choice.SpaceID) {
			return "", "", invalid("Invalid space selection.")
		}
		if choice.Permission == "read_write" {
			scope = scopeReadWrite
		}
	}
	grantID, err := randomHex(16)
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return "", "", fmt.Errorf("connection storage unavailable")
	}
	defer tx.Rollback()
	members := make([]sql.NullString, len(selected))
	for i, choice := range selected {
		if members[i], err = checkGrantChoice(ctx, tx, userID, choice); err != nil {
			return "", "", err
		}
	}
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT grant_id FROM metatrash_oauth_grants WHERE user_id = ? AND client_id = ? AND resource = ? FOR UPDATE", userID, client.ID, resource).Scan(&existing)
	unix := now.Unix()
	expires := now.Add(idle).Unix()
	switch {
	case err == nil:
		grantID = existing
		if _, err := tx.ExecContext(ctx, "UPDATE metatrash_oauth_grants SET client_name = ?, scope = ?, updated_at = ?, expires_at = ? WHERE grant_id = ?", client.Name, scope, unix, expires, grantID); err != nil {
			return "", "", fmt.Errorf("cannot update connection")
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_oauth_grant_spaces WHERE grant_id = ?", grantID); err != nil {
			return "", "", fmt.Errorf("cannot update connection")
		}
	case err == sql.ErrNoRows:
		if _, err := tx.ExecContext(ctx, "INSERT INTO metatrash_oauth_grants (grant_id, user_id, client_id, client_name, resource, scope, created_at, updated_at, last_used_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", grantID, userID, client.ID, client.Name, resource, scope, unix, unix, unix, expires); err != nil {
			return "", "", fmt.Errorf("cannot save connection")
		}
	default:
		return "", "", fmt.Errorf("cannot read connection")
	}
	for i, choice := range selected {
		if _, err := tx.ExecContext(ctx, "INSERT INTO metatrash_oauth_grant_spaces (grant_id, space_id, member_user_id, permission) VALUES (?, ?, ?, ?)", grantID, choice.SpaceID, members[i], choice.Permission); err != nil {
			return "", "", fmt.Errorf("cannot save connection spaces")
		}
	}
	if err := tx.Commit(); err != nil {
		return "", "", fmt.Errorf("cannot confirm connection; try again")
	}
	return grantID, scope, nil
}

// checkGrantChoice locks one chosen space and re-checks, inside the caller's
// transaction, that the user owns it or is an active member, and that write
// access is allowed. It returns the member_user_id value for the consent row
// (NULL for the owner, so owner rows survive membership changes).
func checkGrantChoice(ctx context.Context, tx *sql.Tx, userID string, choice oauthSpaceChoice) (sql.NullString, error) {
	space, err := scanOwnedSpace(tx.QueryRowContext(ctx, "SELECT "+ownedSpaceColumns+" FROM metatrash_spaces WHERE space_id = ? FOR UPDATE", choice.SpaceID))
	if err == sql.ErrNoRows || (err == nil && space.State != "ready") {
		return sql.NullString{}, problem(409, "conflict", "A selected space is no longer available. Reload and try again.")
	}
	if err != nil {
		return sql.NullString{}, fmt.Errorf("cannot check selected space")
	}
	if space.OwnerID == userID {
		return sql.NullString{}, nil
	}
	var status, permission string
	err = tx.QueryRowContext(ctx, "SELECT status, agent_permission FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", choice.SpaceID, userID).Scan(&status, &permission)
	if err == sql.ErrNoRows || (err == nil && status != "active") {
		return sql.NullString{}, problem(409, "conflict", "A selected space is no longer available. Reload and try again.")
	}
	if err != nil {
		return sql.NullString{}, fmt.Errorf("cannot check selected space")
	}
	if choice.Permission == "read_write" && permission != "read_write" {
		return sql.NullString{}, problem(409, "conflict", "The space owner allows read-only app access to a selected space.")
	}
	return sql.NullString{String: userID, Valid: true}, nil
}

// changeGrantSpaces edits a live connection in place (Change spaces on Your
// account). Each choice sets one space to none, read_only or read_write; spaces
// not in choices keep their consent, so a space hidden while its owner has
// suspended the user is not dropped. Chosen spaces get the same checks as
// consent, locked in the same order (spaces, then the connection). Tokens are
// untouched, so the app sees the change on its next operation. Write access
// needs a connection approved with write scope, because tokens never widen.
func (db *accountDatabase) changeGrantSpaces(ctx context.Context, userID, grantID string, choices []oauthSpaceChoice, now time.Time) error {
	if !idPattern.MatchString(userID) || !idPattern.MatchString(grantID) {
		return missing()
	}
	if len(choices) > 400 {
		return invalid("Invalid space selection.")
	}
	selected := append([]oauthSpaceChoice(nil), choices...)
	sort.Slice(selected, func(i, j int) bool { return selected[i].SpaceID < selected[j].SpaceID })
	wantWrite := false
	for i, choice := range selected {
		if !idPattern.MatchString(choice.SpaceID) || (choice.Permission != "none" && choice.Permission != "read_only" && choice.Permission != "read_write") || (i > 0 && selected[i-1].SpaceID == choice.SpaceID) {
			return invalid("Invalid space selection.")
		}
		wantWrite = wantWrite || choice.Permission == "read_write"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("connection storage unavailable")
	}
	defer tx.Rollback()
	members := make([]sql.NullString, len(selected))
	for i, choice := range selected {
		if choice.Permission == "none" {
			continue
		}
		if members[i], err = checkGrantChoice(ctx, tx, userID, choice); err != nil {
			return err
		}
	}
	var scope string
	err = tx.QueryRowContext(ctx, "SELECT scope FROM metatrash_oauth_grants WHERE grant_id = ? AND user_id = ? AND expires_at > ? FOR UPDATE", grantID, userID, now.Unix()).Scan(&scope)
	if err == sql.ErrNoRows {
		return problem(404, "not_found", "This connection no longer exists. It may have been revoked or expired.")
	}
	if err != nil {
		return fmt.Errorf("cannot read connection")
	}
	if wantWrite && scope != scopeReadWrite {
		return problem(409, "conflict", "This app was connected read-only. To let it write, connect it again from the app.")
	}
	for i, choice := range selected {
		if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_oauth_grant_spaces WHERE grant_id = ? AND space_id = ?", grantID, choice.SpaceID); err != nil {
			return fmt.Errorf("cannot update connection spaces")
		}
		if choice.Permission == "none" {
			continue
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO metatrash_oauth_grant_spaces (grant_id, space_id, member_user_id, permission) VALUES (?, ?, ?, ?)", grantID, choice.SpaceID, members[i], choice.Permission); err != nil {
			return fmt.Errorf("cannot update connection spaces")
		}
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_oauth_grant_spaces WHERE grant_id = ?", grantID).Scan(&count); err != nil {
		return fmt.Errorf("cannot update connection spaces")
	}
	if count > 200 {
		return invalid("A connection can use at most 200 private spaces.")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE metatrash_oauth_grants SET updated_at = ? WHERE grant_id = ?", now.Unix(), grantID); err != nil {
		return fmt.Errorf("cannot update connection")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cannot confirm the change; try again")
	}
	return nil
}

func narrowerScope(a, b string) string {
	if a == scopeReadWrite && b == scopeReadWrite {
		return scopeReadWrite
	}
	return scopeRead
}

func insertTokenPair(ctx context.Context, tx *sql.Tx, grantID, scope string, now time.Time, accessTTL, refreshTTL time.Duration) (oauthTokenPair, error) {
	access, err := newOAuthToken("mt_at_")
	if err != nil {
		return oauthTokenPair{}, err
	}
	refresh, err := newOAuthToken("mt_rt_")
	if err != nil {
		return oauthTokenPair{}, err
	}
	for _, token := range []struct {
		value, kind string
		ttl         time.Duration
	}{{access, "access", accessTTL}, {refresh, "refresh", refreshTTL}} {
		if _, err := tx.ExecContext(ctx, "INSERT INTO metatrash_oauth_tokens (token_digest, grant_id, kind, scope, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)", secretDigest(token.value), grantID, token.kind, scope, now.Unix(), now.Add(token.ttl).Unix()); err != nil {
			return oauthTokenPair{}, fmt.Errorf("cannot save token")
		}
	}
	return oauthTokenPair{Access: access, Refresh: refresh, Scope: scope, AccessTTL: accessTTL}, nil
}

func oauthGrantError(description string) *Error {
	return &Error{Status: 400, Code: "invalid_grant", Message: description}
}

// issueForCode exchanges a verified authorization code for the first token pair.
func (db *accountDatabase) issueForCode(ctx context.Context, code oauthCode, now time.Time, s oauthSettings) (oauthTokenPair, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return oauthTokenPair{}, fmt.Errorf("token storage unavailable")
	}
	defer tx.Rollback()
	var userID, clientID, resource, scope string
	var expires int64
	err = tx.QueryRowContext(ctx, "SELECT user_id, client_id, resource, scope, expires_at FROM metatrash_oauth_grants WHERE grant_id = ? FOR UPDATE", code.GrantID).Scan(&userID, &clientID, &resource, &scope, &expires)
	if err == sql.ErrNoRows {
		return oauthTokenPair{}, oauthGrantError("The connection was revoked.")
	}
	if err != nil {
		return oauthTokenPair{}, fmt.Errorf("cannot read connection")
	}
	if userID != code.UserID || clientID != code.ClientID || resource != code.Resource || expires <= now.Unix() {
		return oauthTokenPair{}, oauthGrantError("The connection is no longer valid.")
	}
	pair, err := insertTokenPair(ctx, tx, code.GrantID, narrowerScope(scope, code.Scope), now, s.accessTTL, s.refreshTTL)
	if err != nil {
		return oauthTokenPair{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE metatrash_oauth_grants SET last_used_at = ?, expires_at = ? WHERE grant_id = ?", now.Unix(), now.Add(s.grantIdle).Unix(), code.GrantID); err != nil {
		return oauthTokenPair{}, fmt.Errorf("cannot update connection")
	}
	if err := tx.Commit(); err != nil {
		return oauthTokenPair{}, fmt.Errorf("cannot confirm token")
	}
	return pair, nil
}

// refreshTokens rotates a refresh token. A spent token presented again revokes
// the whole connection (replay detection); the caller reports invalid_grant.
func (db *accountDatabase) refreshTokens(ctx context.Context, refresh, clientID, resource, requestedScope string, now time.Time, s oauthSettings) (oauthTokenPair, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return oauthTokenPair{}, fmt.Errorf("token storage unavailable")
	}
	defer tx.Rollback()
	digest := secretDigest(refresh)
	var grantID, kind, tokenScope, grantClient, grantResource, grantScope string
	var tokenExpires, grantExpires int64
	var used sql.NullInt64
	err = tx.QueryRowContext(ctx, "SELECT t.grant_id, t.kind, t.scope, t.expires_at, t.used_at, g.client_id, g.resource, g.scope, g.expires_at FROM metatrash_oauth_tokens t JOIN metatrash_oauth_grants g ON g.grant_id = t.grant_id WHERE t.token_digest = ? FOR UPDATE", digest).Scan(&grantID, &kind, &tokenScope, &tokenExpires, &used, &grantClient, &grantResource, &grantScope, &grantExpires)
	if err == sql.ErrNoRows {
		return oauthTokenPair{}, oauthGrantError("Refresh token is invalid or expired.")
	}
	if err != nil {
		return oauthTokenPair{}, fmt.Errorf("cannot read token")
	}
	if kind != "refresh" || grantClient != clientID {
		return oauthTokenPair{}, oauthGrantError("Refresh token is invalid or expired.")
	}
	if used.Valid {
		if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_oauth_grants WHERE grant_id = ?", grantID); err != nil {
			return oauthTokenPair{}, fmt.Errorf("cannot revoke replayed connection")
		}
		if err := tx.Commit(); err != nil {
			return oauthTokenPair{}, fmt.Errorf("cannot revoke replayed connection")
		}
		return oauthTokenPair{}, oauthGrantError("Refresh token was already used; the connection has been revoked.")
	}
	if tokenExpires <= now.Unix() || grantExpires <= now.Unix() {
		return oauthTokenPair{}, oauthGrantError("Refresh token is invalid or expired.")
	}
	if resource != "" && resource != grantResource {
		return oauthTokenPair{}, &Error{Status: 400, Code: "invalid_target", Message: "The resource does not match this token."}
	}
	scope := narrowerScope(tokenScope, grantScope)
	if requestedScope != "" {
		requested, ok := parseScope(requestedScope, false)
		if !ok || (requested == scopeReadWrite && scope != scopeReadWrite) {
			return oauthTokenPair{}, &Error{Status: 400, Code: "invalid_scope", Message: "The requested scope exceeds the granted scope."}
		}
		scope = requested
	}
	result, err := tx.ExecContext(ctx, "UPDATE metatrash_oauth_tokens SET used_at = ? WHERE token_digest = ? AND used_at IS NULL", now.Unix(), digest)
	if err != nil {
		return oauthTokenPair{}, fmt.Errorf("cannot rotate token")
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return oauthTokenPair{}, oauthGrantError("Refresh token is invalid or expired.")
	}
	pair, err := insertTokenPair(ctx, tx, grantID, scope, now, s.accessTTL, s.refreshTTL)
	if err != nil {
		return oauthTokenPair{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE metatrash_oauth_grants SET last_used_at = ?, expires_at = ? WHERE grant_id = ?", now.Unix(), now.Add(s.grantIdle).Unix(), grantID); err != nil {
		return oauthTokenPair{}, fmt.Errorf("cannot update connection")
	}
	if err := tx.Commit(); err != nil {
		return oauthTokenPair{}, fmt.Errorf("cannot confirm token")
	}
	return pair, nil
}

// revokeToken implements RFC 7009 for a client: an access token is deleted; a
// refresh token deletes every token of its connection (consent is kept).
// Unknown tokens and other clients' tokens are ignored.
func (db *accountDatabase) revokeToken(ctx context.Context, token, clientID string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var grantID, kind, grantClient string
	err := db.db.QueryRowContext(ctx, "SELECT t.grant_id, t.kind, g.client_id FROM metatrash_oauth_tokens t JOIN metatrash_oauth_grants g ON g.grant_id = t.grant_id WHERE t.token_digest = ?", secretDigest(token)).Scan(&grantID, &kind, &grantClient)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read token")
	}
	if grantClient != clientID {
		return nil
	}
	if kind == "access" {
		_, err = db.db.ExecContext(ctx, "DELETE FROM metatrash_oauth_tokens WHERE token_digest = ?", secretDigest(token))
	} else {
		_, err = db.db.ExecContext(ctx, "DELETE FROM metatrash_oauth_tokens WHERE grant_id = ?", grantID)
	}
	if err != nil {
		return fmt.Errorf("cannot revoke token")
	}
	return nil
}

// revokeGrantTokens deletes all tokens of a connection, used when an
// authorization code is presented twice.
func (db *accountDatabase) revokeGrantTokens(ctx context.Context, grantID string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_oauth_tokens WHERE grant_id = ?", grantID)
	return err
}

// validateAccessToken checks an access token for a request to one protected
// resource. The connection must still exist and be within its idle lifetime.
// Last use is recorded at most once an hour per connection.
func (db *accountDatabase) validateAccessToken(ctx context.Context, token, resource string, now time.Time, s oauthSettings) (oauthIdentity, error) {
	invalidToken := &Error{Status: 401, Code: "invalid_token", Message: "The access token is invalid or expired."}
	if !strings.HasPrefix(token, "mt_at_") || len(token) != 49 {
		return oauthIdentity{}, invalidToken
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var id oauthIdentity
	var grantResource string
	var tokenExpires, grantExpires, lastUsed int64
	err := db.db.QueryRowContext(ctx, "SELECT t.grant_id, t.scope, t.expires_at, g.user_id, g.client_id, g.resource, g.scope, g.expires_at, g.last_used_at FROM metatrash_oauth_tokens t JOIN metatrash_oauth_grants g ON g.grant_id = t.grant_id WHERE t.token_digest = ? AND t.kind = 'access'", secretDigest(token)).Scan(&id.GrantID, &id.Scope, &tokenExpires, &id.UserID, &id.ClientID, &grantResource, new(string), &grantExpires, &lastUsed)
	if err == sql.ErrNoRows {
		return oauthIdentity{}, invalidToken
	}
	if err != nil {
		return oauthIdentity{}, fmt.Errorf("cannot check access token")
	}
	if grantResource != resource || tokenExpires <= now.Unix() || grantExpires <= now.Unix() {
		return oauthIdentity{}, invalidToken
	}
	if now.Unix()-lastUsed >= 3600 {
		if _, err := db.db.ExecContext(ctx, "UPDATE metatrash_oauth_grants SET last_used_at = ?, expires_at = ? WHERE grant_id = ? AND last_used_at = ?", now.Unix(), now.Add(s.grantIdle).Unix(), id.GrantID, lastUsed); err != nil {
			return oauthIdentity{}, fmt.Errorf("cannot record connection use")
		}
	}
	return id, nil
}

// purgeExpired removes expired tokens and idle-expired connections.
func (db *accountDatabase) purgeExpired(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_oauth_tokens WHERE expires_at <= ? LIMIT 1000", now.Unix()); err != nil {
		return err
	}
	_, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_oauth_grants WHERE expires_at <= ? LIMIT 100", now.Unix())
	return err
}

// parseScope maps a requested scope string onto the two supported values.
// Unknown scope tokens are ignored when lenient; an empty or unknown-only
// request means the default read/write request (consent still decides).
func parseScope(raw string, lenient bool) (string, bool) {
	read, write := false, false
	for _, token := range strings.Fields(raw) {
		switch token {
		case "spaces:read":
			read = true
		case "spaces:write":
			write = true
		default:
			if !lenient {
				return "", false
			}
		}
	}
	if write {
		return scopeReadWrite, true
	}
	if read {
		return scopeRead, true
	}
	if lenient {
		return scopeReadWrite, true
	}
	return "", false
}
