package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const codeLifetime = 10 * time.Minute
const sessionLifetime = 24 * time.Hour
const maxAccountRecords = 10000

type accountConfig struct {
	Origin             string        `json:"origin"`
	SMTPHost           string        `json:"smtpHost"`
	SMTPPort           int           `json:"smtpPort"`
	SMTPUsername       string        `json:"smtpUsername"`
	SMTPFrom           string        `json:"smtpFrom"`
	SMTPPasswordFile   string        `json:"smtpPasswordFile"`
	DatabaseConfigFile string        `json:"databaseConfigFile"`
	OAuth              *oauthConfig  `json:"oauth"`
	GitHub             *githubConfig `json:"github"`
}

type userAccount struct {
	Username         string    `json:"-"`
	ID               string    `json:"id"`
	Email            string    `json:"email"`
	CreatedAt        time.Time `json:"createdAt"`
	MaxPrivateSpaces int       `json:"maxPrivateSpaces"`
}

type accountFile struct {
	Version int                    `json:"version"`
	Users   map[string]userAccount `json:"users"`
}

type loginChallenge struct {
	Email    string
	Digest   string
	Expires  time.Time
	Attempts int
	Ready    bool
}

type accountSession struct {
	UserID  string
	Expires time.Time
}

type accounts struct {
	mu         sync.Mutex
	config     accountConfig
	store      accountStore
	challenges map[string]loginChallenge
	sessions   map[string]accountSession
	// powUsed holds spent login proof-of-work challenges until they expire.
	powUsed   map[string]time.Time
	secret    []byte
	mailSlots chan struct{}
	send      func(context.Context, string, string) error
	// sendInvite emails an invitation; nil when accounts are not configured.
	sendInvite func(context.Context, string, invitationMail) error
	// oauth is nil unless the account configuration enables OAuth agent access.
	oauth *oauthSettings
	// github is nil unless the account configuration enables the GitHub App.
	github *githubSettings
}

// Email identity is case-insensitive. Do not collapse dots or plus aliases.
func normalizeEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("invalid email")
	}
	for _, c := range value {
		if c < 33 || c > 126 {
			return "", fmt.Errorf("use an ASCII email address")
		}
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value {
		return "", fmt.Errorf("invalid email")
	}
	parts := strings.Split(value, "@")
	if len(parts) != 2 || len(parts[0]) > 64 || !strings.Contains(parts[1], ".") {
		return "", fmt.Errorf("invalid email")
	}
	return strings.ToLower(value), nil
}

func secretDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (a *accounts) mac(value string) string {
	hash := hmac.New(sha256.New, a.secret)
	_, _ = hash.Write([]byte(value))
	return hex.EncodeToString(hash.Sum(nil))
}

// EnableAccounts is called once before serving HTTP, while the service owns the data lock.
func (s *Service) EnableAccounts(configPath string) error {
	b, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read account configuration: %w", err)
	}
	var cfg accountConfig
	if err := strictJSON(b, &cfg); err != nil {
		return fmt.Errorf("invalid account configuration")
	}
	origin, err := url.Parse(cfg.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || origin.ForceQuery || origin.Opaque != "" {
		return fmt.Errorf("account origin must be an HTTPS origin without a trailing slash")
	}
	if cfg.SMTPHost == "" || strings.ContainsAny(cfg.SMTPHost, "/:@ \r\n") || cfg.SMTPPort < 1 || cfg.SMTPPort > 65535 || cfg.SMTPUsername == "" || cfg.SMTPPasswordFile == "" {
		return fmt.Errorf("incomplete SMTP configuration")
	}
	sender, err := normalizeEmail(cfg.SMTPFrom)
	if err != nil {
		return fmt.Errorf("invalid SMTP sender")
	}
	cfg.SMTPFrom = sender
	password, err := os.ReadFile(cfg.SMTPPasswordFile)
	if err != nil {
		return fmt.Errorf("read SMTP password file: %w", err)
	}
	passwordText := strings.TrimSpace(string(password))
	if passwordText == "" {
		return fmt.Errorf("SMTP password file is empty")
	}
	a := &accounts{config: cfg, challenges: map[string]loginChallenge{}, sessions: map[string]accountSession{}, powUsed: map[string]time.Time{}, secret: make([]byte, 32), mailSlots: make(chan struct{}, 2)}
	if cfg.OAuth != nil && cfg.OAuth.Enabled {
		settings, err := cfg.OAuth.settings()
		if err != nil {
			return err
		}
		a.oauth = &settings
	} else if cfg.OAuth != nil {
		// Validate disabled settings too, so enabling later cannot fail on startup.
		if _, err := cfg.OAuth.settings(); err != nil {
			return err
		}
	}
	if cfg.GitHub != nil && cfg.GitHub.Enabled {
		if a.github, err = cfg.GitHub.settings(); err != nil {
			return err
		}
	}
	if _, err := rand.Read(a.secret); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := openAccountDatabase(ctx, cfg.DatabaseConfigFile)
	if err != nil {
		return err
	}
	if err := store.ready(ctx); err != nil {
		store.Close()
		return err
	}
	if err := s.loadOwnedSpaces(ctx, store); err != nil {
		store.Close()
		return err
	}
	a.store = store
	a.send = func(ctx context.Context, email, code string) error {
		return sendLoginMail(ctx, cfg, passwordText, email, code)
	}
	a.sendInvite = func(ctx context.Context, email string, m invitationMail) error {
		return sendMail(ctx, cfg, passwordText, email, "You're invited to a Metatrash space", m.body(cfg.Origin))
	}
	s.accounts = a
	return nil
}

// All callers hold mu. Expired entries are reclaimed and maps have hard caps.
func (a *accounts) cleanup(now time.Time) {
	for key, c := range a.challenges {
		if !now.Before(c.Expires) {
			delete(a.challenges, key)
		}
	}
	for key, s := range a.sessions {
		if !now.Before(s.Expires) {
			delete(a.sessions, key)
		}
	}
	for key, expires := range a.powUsed {
		if !now.Before(expires) {
			delete(a.powUsed, key)
		}
	}
}

func (a *accounts) issue(ctx context.Context, browser, email string) error {
	select {
	case a.mailSlots <- struct{}{}:
		defer func() { <-a.mailSlots }()
	default:
		return problem(503, "mail_busy", "Email is busy. Please try again shortly.")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	key := secretDigest(browser)
	challenge := loginChallenge{Email: email, Digest: a.mac(key + ":" + code), Expires: time.Now().Add(codeLifetime)}
	a.mu.Lock()
	a.cleanup(time.Now())
	if len(a.challenges) >= 2048 {
		a.mu.Unlock()
		return problem(503, "mail_busy", "Please try again shortly.")
	}
	a.challenges[key] = challenge
	a.mu.Unlock()
	// Do not hold the account lock during network I/O. A newer send wins.
	err = a.send(ctx, email, code)
	a.mu.Lock()
	defer a.mu.Unlock()
	if current, ok := a.challenges[key]; ok && current.Digest == challenge.Digest {
		if err != nil {
			delete(a.challenges, key)
		} else {
			current.Ready = true
			a.challenges[key] = current
		}
	}
	if err != nil {
		// SMTP responses may contain addresses or authentication details; never log them.
		return problem(503, "mail_unavailable", "We could not send a code. Please try again later.")
	}
	return nil
}

// verify also returns the email the code was sent to ("" when this browser has
// no code waiting), for the login log.
func (a *accounts) verify(ctx context.Context, browser, code string) (string, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.cleanup(now)
	key := secretDigest(browser)
	c, ok := a.challenges[key]
	failed := problem(400, "invalid_code", "That code is invalid or expired. Try again, or request a new code.")
	if !ok || !c.Ready {
		return "", "", failed
	}
	c.Attempts++
	match := len(code) == 6 && subtle.ConstantTimeCompare([]byte(c.Digest), []byte(a.mac(key+":"+code))) == 1
	if !match {
		if c.Attempts >= 5 {
			delete(a.challenges, key)
		} else {
			a.challenges[key] = c
		}
		return "", c.Email, failed
	}
	// Consume before persistence or session creation, even if either fails.
	delete(a.challenges, key)
	if len(a.sessions) >= 4096 {
		return "", c.Email, problem(503, "sessions_busy", "Please try again later.")
	}
	token, err := randomHex(32)
	if err != nil {
		return "", c.Email, err
	}
	user, err := a.store.FindOrCreate(ctx, c.Email)
	if err != nil {
		return "", c.Email, problem(503, "account_unavailable", "We could not load or save your account. Please request a new code later.")
	}
	// Bound active sessions per account; revoke the oldest when signing in again.
	count, oldestKey := 0, ""
	var oldest time.Time
	for k, session := range a.sessions {
		if session.UserID == user.ID {
			count++
			if oldestKey == "" || session.Expires.Before(oldest) {
				oldest, oldestKey = session.Expires, k
			}
		}
	}
	if count >= 8 {
		delete(a.sessions, oldestKey)
	}
	a.sessions[secretDigest(token)] = accountSession{UserID: user.ID, Expires: time.Now().Add(sessionLifetime)}
	// A successful login also invalidates outstanding codes for this email.
	for k, pending := range a.challenges {
		if pending.Email == c.Email {
			delete(a.challenges, k)
		}
	}
	return token, c.Email, nil
}

func (a *accounts) currentUser(ctx context.Context, token string) (userAccount, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleanup(time.Now())
	session, ok := a.sessions[secretDigest(token)]
	if !ok {
		return userAccount{}, false, nil
	}
	return a.store.ByID(ctx, session.UserID)
}
