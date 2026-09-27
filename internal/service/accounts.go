package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const codeLifetime = 10 * time.Minute
const sessionLifetime = 24 * time.Hour
const maxAccountRecords = 10000

type accountConfig struct {
	Origin           string `json:"origin"`
	SMTPHost         string `json:"smtpHost"`
	SMTPPort         int    `json:"smtpPort"`
	SMTPUsername     string `json:"smtpUsername"`
	SMTPFrom         string `json:"smtpFrom"`
	SMTPPasswordFile string `json:"smtpPasswordFile"`
}

type userAccount struct {
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
	Email   string
	Expires time.Time
}

type accounts struct {
	mu         sync.Mutex
	config     accountConfig
	path       string
	users      map[string]userAccount
	challenges map[string]loginChallenge
	sessions   map[string]accountSession
	secret     []byte
	mailSlots  chan struct{}
	send       func(context.Context, string, string) error
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
	a := &accounts{config: cfg, path: filepath.Join(filepath.Dir(s.lockPath), "accounts.json"), users: map[string]userAccount{}, challenges: map[string]loginChallenge{}, sessions: map[string]accountSession{}, secret: make([]byte, 32), mailSlots: make(chan struct{}, 2)}
	if _, err := rand.Read(a.secret); err != nil {
		return err
	}
	f, err := os.Open(a.path)
	if err == nil {
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, 8*1024*1024+1))
		if err != nil || len(b) > 8*1024*1024 {
			return fmt.Errorf("account store exceeds limit or cannot be read")
		}
		var saved accountFile
		if strictJSON(b, &saved) != nil || saved.Version != 1 || saved.Users == nil || len(saved.Users) > maxAccountRecords {
			return fmt.Errorf("invalid account store")
		}
		ids := map[string]bool{}
		for email, user := range saved.Users {
			normalized, err := normalizeEmail(email)
			if err != nil || normalized != email || user.Email != email || !idPattern.MatchString(user.ID) || ids[user.ID] || user.CreatedAt.IsZero() || user.MaxPrivateSpaces < 0 {
				return fmt.Errorf("invalid account record")
			}
			ids[user.ID] = true
		}
		a.users = saved.Users
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read account store: %w", err)
	}
	a.send = func(ctx context.Context, email, code string) error {
		return sendLoginMail(ctx, cfg, passwordText, email, code)
	}
	s.accounts = a
	return nil
}

// Write to a sibling file and publish atomically before updating in-memory state.
func (a *accounts) saveUsers(users map[string]userAccount) error {
	b, err := json.MarshalIndent(accountFile{Version: 1, Users: users}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(a.path), ".accounts-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), a.path)
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

func (a *accounts) verify(browser, code string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.cleanup(now)
	key := secretDigest(browser)
	c, ok := a.challenges[key]
	failed := problem(400, "invalid_code", "That code is invalid or expired. Try again, or request a new code.")
	if !ok || !c.Ready {
		return "", failed
	}
	c.Attempts++
	match := len(code) == 6 && subtle.ConstantTimeCompare([]byte(c.Digest), []byte(a.mac(key+":"+code))) == 1
	if !match {
		if c.Attempts >= 5 {
			delete(a.challenges, key)
		} else {
			a.challenges[key] = c
		}
		return "", failed
	}
	// Consume before persistence or session creation, even if either fails.
	delete(a.challenges, key)
	if len(a.sessions) >= 4096 {
		return "", problem(503, "sessions_busy", "Please try again later.")
	}
	token, err := randomHex(32)
	if err != nil {
		return "", err
	}
	if _, exists := a.users[c.Email]; !exists {
		if len(a.users) >= maxAccountRecords {
			return "", problem(503, "accounts_full", "Registration is temporarily unavailable.")
		}
		id, err := randomHex(16)
		if err != nil {
			return "", err
		}
		users := make(map[string]userAccount, len(a.users)+1)
		for email, user := range a.users {
			users[email] = user
		}
		users[c.Email] = userAccount{ID: id, Email: c.Email, CreatedAt: now.UTC(), MaxPrivateSpaces: 1}
		if err := a.saveUsers(users); err != nil {
			return "", problem(503, "account_unavailable", "We could not save your account. Please request a new code later.")
		}
		a.users = users
	}
	// Bound active sessions per account; revoke the oldest when signing in again.
	count, oldestKey := 0, ""
	var oldest time.Time
	for k, session := range a.sessions {
		if session.Email == c.Email {
			count++
			if oldestKey == "" || session.Expires.Before(oldest) {
				oldest, oldestKey = session.Expires, k
			}
		}
	}
	if count >= 8 {
		delete(a.sessions, oldestKey)
	}
	a.sessions[secretDigest(token)] = accountSession{Email: c.Email, Expires: now.Add(sessionLifetime)}
	// A successful login also invalidates outstanding codes for this email.
	for k, pending := range a.challenges {
		if pending.Email == c.Email {
			delete(a.challenges, k)
		}
	}
	return token, nil
}

func (a *accounts) currentUser(token string) (userAccount, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleanup(time.Now())
	session, ok := a.sessions[secretDigest(token)]
	if !ok {
		return userAccount{}, false
	}
	user, ok := a.users[session.Email]
	return user, ok
}
