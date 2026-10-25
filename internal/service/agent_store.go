package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Users' agents (0.55.0, metatrash_agents in schema v16): each user adds the
// model providers and API keys their folder actions run on. A job runs on
// the space owner's agent, so the owner pays; the key is sealed with
// AES-256-GCM under agents.keyFile and never shown again after it is saved.

const maxAgentsPerUser = 10

var agentNamePattern = regexp.MustCompile(`^[\pL\pN][\pL\pN ._'()-]{0,63}$`)
var agentModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)

// userAgent is one row of metatrash_agents.
type userAgent struct {
	ID, UserID, Name, Provider, BaseURL, Model string
	MaxTokens                                  int
	DailyCap                                   int64
	Sealed                                     []byte
	KeyHint                                    string
	Default                                    bool
	Created                                    time.Time
}

// App is how History names this agent's changes.
func (u userAgent) App() string { return agentProviders[u.Provider].app }

// ProviderName is the provider's display name.
func (u userAgent) ProviderName() string { return agentProviders[u.Provider].name }

// Endpoint is the Messages API base address the agent uses.
func (u userAgent) Endpoint() string {
	if u.BaseURL != "" {
		return u.BaseURL
	}
	return agentProviders[u.Provider].baseURL
}

// Ref is how an action names this agent's model: "minimax/MiniMax-M3".
func (u userAgent) Ref() string { return u.Provider + "/" + u.Model }

// loadSealKey reads a key file holding 64 hex digits (32 bytes).
func loadSealKey(path string) (cipher.AEAD, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s", path)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("%s must hold 64 hex digits (openssl rand -hex 32)", path)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealAgentKey encrypts an API key for one agent: version byte 1, nonce,
// ciphertext. The user and agent IDs are authenticated, so a sealed key
// copied to another row does not open.
func sealAgentKey(aead cipher.AEAD, userID, agentID, key string) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{1}, nonce...)
	return aead.Seal(out, nonce, []byte(key), []byte("metatrash-agent-key:"+userID+":"+agentID)), nil
}

func openAgentKey(aead cipher.AEAD, userID, agentID string, sealed []byte) (string, error) {
	n := aead.NonceSize()
	if len(sealed) < 1+n+aead.Overhead() || sealed[0] != 1 {
		return "", fmt.Errorf("the agent's API key cannot be opened; add the key again")
	}
	key, err := aead.Open(nil, sealed[1:1+n], sealed[1+n:], []byte("metatrash-agent-key:"+userID+":"+agentID))
	if err != nil {
		return "", fmt.Errorf("the agent's API key cannot be opened; add the key again")
	}
	return string(key), nil
}

// agentForm is what the Add agent form sends, before checks.
type agentForm struct {
	Name, Provider, Model, Key, Endpoint, Cap string
}

// newUserAgent checks the form and seals the key.
func newUserAgent(aead cipher.AEAD, userID string, f agentForm, now time.Time) (userAgent, error) {
	u := userAgent{UserID: userID, Name: strings.TrimSpace(f.Name), Provider: strings.ToLower(strings.TrimSpace(f.Provider)), Model: strings.TrimSpace(f.Model), MaxTokens: defaultAgentMaxTokens, DailyCap: defaultDailyTokenCap, Created: now}
	if !agentNamePattern.MatchString(u.Name) || !utf8.ValidString(u.Name) {
		return u, invalid("Give the agent a name of up to 64 letters, numbers, spaces and . _ ' ( ) -, such as MiniMax main.")
	}
	p, ok := agentProviders[u.Provider]
	if !ok {
		return u, invalid("Choose MiniMax or Anthropic.")
	}
	if u.Model == "" {
		u.Model = p.model
	}
	if !agentModelPattern.MatchString(u.Model) {
		return u, invalid("The model name has letters, numbers and . _ : - only, such as " + p.model + ".")
	}
	if endpoint := strings.TrimSuffix(strings.TrimSpace(f.Endpoint), "/"); endpoint != "" && endpoint != p.baseURL {
		if err := checkAgentEndpoint(endpoint); err != nil {
			return u, err
		}
		u.BaseURL = endpoint
	}
	key := strings.TrimSpace(f.Key)
	if key == "" || len(key) > 512 || strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return u, invalid("Paste the API key from " + p.keysURL + ": one line, no spaces.")
	}
	if c := strings.ReplaceAll(strings.TrimSpace(f.Cap), ",", ""); c != "" {
		var n int64
		if _, err := fmt.Sscan(c, &n); err != nil || n < 0 || n > 1e12 || fmt.Sprint(n) != c {
			return u, invalid("The daily token cap is a whole number of tokens; 0 means no limit.")
		}
		u.DailyCap = n
	}
	id, err := randomHex(16)
	if err != nil {
		return u, err
	}
	u.ID = id
	if u.Sealed, err = sealAgentKey(aead, userID, id, key); err != nil {
		return u, err
	}
	u.KeyHint = key[max(0, len(key)-4):]
	return u, nil
}

// checkAgentEndpoint allows https addresses on public hosts only, so a user's
// endpoint cannot reach the server's own network. Dialing checks again
// (agentDialControl), since a name can resolve differently later.
func checkAgentEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(endpoint) > 255 {
		return invalid("The endpoint must be an https address without a query, such as https://api.minimax.io/anthropic.")
	}
	host := u.Hostname()
	if addr, err := netip.ParseAddr(host); err == nil {
		if !publicAddr(addr) {
			return invalid("The endpoint must be on the public internet.")
		}
		return nil
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") {
		return invalid("The endpoint must be on the public internet.")
	}
	return nil
}

func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(a) && !netip.MustParsePrefix("fc00::/7").Contains(a)
}

// agentDialControl refuses connections to addresses that are not public.
func agentDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !publicAddr(addr) {
		return fmt.Errorf("the agent's endpoint resolves to an address that is not public")
	}
	return nil
}

// agentHTTPClient is the client for providers: public addresses only, no
// proxies from the environment, no redirects (an API answers directly).
func agentHTTPClient(private bool) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	if !private {
		dialer.Control = agentDialControl
	}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 30 * time.Second, ResponseHeaderTimeout: 5 * time.Minute, MaxIdleConns: 10, IdleConnTimeout: 90 * time.Second}
	return &http.Client{Timeout: 5 * time.Minute, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

const userAgentColumns = "agent_id, user_id, name, provider, base_url, model, max_tokens, daily_cap, key_sealed, key_hint, is_default, created_at"

func scanUserAgent(scan func(...any) error) (userAgent, error) {
	var u userAgent
	var created int64
	err := scan(&u.ID, &u.UserID, &u.Name, &u.Provider, &u.BaseURL, &u.Model, &u.MaxTokens, &u.DailyCap, &u.Sealed, &u.KeyHint, &u.Default, &created)
	u.Created = time.Unix(created, 0).UTC()
	return u, err
}

// userAgents lists a user's agents, the default first, then oldest first.
func (db *accountDatabase) userAgents(ctx context.Context, userID string) ([]userAgent, error) {
	rows, err := db.db.QueryContext(ctx, "SELECT "+userAgentColumns+" FROM metatrash_agents WHERE user_id = ? ORDER BY is_default DESC, created_at, agent_id LIMIT 100", userID)
	if err != nil {
		return nil, fmt.Errorf("cannot read agents")
	}
	defer rows.Close()
	var out []userAgent
	for rows.Next() {
		u, err := scanUserAgent(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("cannot read agents")
		}
		out = append(out, u)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot read agents")
	}
	return out, nil
}

// addUserAgent stores a new agent; a user's first agent is their default.
func (db *accountDatabase) addUserAgent(ctx context.Context, u userAgent) error {
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cannot save the agent")
	}
	defer tx.Rollback()
	// Lock the user's row so two adds cannot both pass the count.
	var id string
	if err := tx.QueryRowContext(ctx, "SELECT user_id FROM metatrash_users WHERE user_id = ? FOR UPDATE", u.UserID).Scan(&id); err != nil {
		return fmt.Errorf("cannot save the agent")
	}
	var count, named int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(name = ?), 0) FROM metatrash_agents WHERE user_id = ?", u.Name, u.UserID).Scan(&count, &named); err != nil {
		return fmt.Errorf("cannot save the agent")
	}
	if count >= maxAgentsPerUser {
		return problem(409, "conflict", fmt.Sprintf("You have the most agents allowed (%d). Remove one first.", maxAgentsPerUser))
	}
	if named > 0 {
		return problem(409, "conflict", "You already have an agent called "+u.Name+". Choose another name.")
	}
	u.Default = count == 0
	if _, err := tx.ExecContext(ctx, "INSERT INTO metatrash_agents ("+userAgentColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		u.ID, u.UserID, u.Name, u.Provider, u.BaseURL, u.Model, u.MaxTokens, u.DailyCap, u.Sealed, u.KeyHint, u.Default, u.Created.Unix()); err != nil {
		return fmt.Errorf("cannot save the agent")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cannot save the agent")
	}
	return nil
}

// removeUserAgent deletes one of the user's agents. If it was the default,
// the oldest remaining one becomes the default.
func (db *accountDatabase) removeUserAgent(ctx context.Context, userID, agentID string) error {
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cannot remove the agent")
	}
	defer tx.Rollback()
	var wasDefault bool
	switch err := tx.QueryRowContext(ctx, "SELECT is_default FROM metatrash_agents WHERE agent_id = ? AND user_id = ? FOR UPDATE", agentID, userID).Scan(&wasDefault); {
	case errors.Is(err, sql.ErrNoRows):
		return missing()
	case err != nil:
		return fmt.Errorf("cannot remove the agent")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_agents WHERE agent_id = ? AND user_id = ?", agentID, userID); err != nil {
		return fmt.Errorf("cannot remove the agent")
	}
	if wasDefault {
		if _, err := tx.ExecContext(ctx, "UPDATE metatrash_agents SET is_default = 1 WHERE user_id = ? ORDER BY created_at, agent_id LIMIT 1", userID); err != nil {
			return fmt.Errorf("cannot remove the agent")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cannot remove the agent")
	}
	return nil
}

// setDefaultUserAgent makes one of the user's agents their default.
func (db *accountDatabase) setDefaultUserAgent(ctx context.Context, userID, agentID string) error {
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cannot change the default agent")
	}
	defer tx.Rollback()
	var id string
	switch err := tx.QueryRowContext(ctx, "SELECT agent_id FROM metatrash_agents WHERE agent_id = ? AND user_id = ? FOR UPDATE", agentID, userID).Scan(&id); {
	case errors.Is(err, sql.ErrNoRows):
		return missing()
	case err != nil:
		return fmt.Errorf("cannot change the default agent")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE metatrash_agents SET is_default = (agent_id = ?) WHERE user_id = ?", agentID, userID); err != nil {
		return fmt.Errorf("cannot change the default agent")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cannot change the default agent")
	}
	return nil
}

// pickAgent chooses the agent a job runs on from the owner's agents: the
// default one, or, when the action names a model ("minimax/MiniMax-M3"), the
// default among those for that provider and model.
func pickAgent(agents []userAgent, want string) (userAgent, bool) {
	var found *userAgent
	for i := range agents {
		u := &agents[i]
		if want != "" && u.Ref() != want {
			continue
		}
		if found == nil || u.Default && !found.Default {
			found = u
		}
	}
	if found == nil {
		return userAgent{}, false
	}
	return *found, true
}
