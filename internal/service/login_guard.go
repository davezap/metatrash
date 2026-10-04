package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"log"
	"math/bits"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Bot checks on the login form, and the log line that records them.
//
// Sending a login code needs two things a person's browser does without
// noticing: it leaves a hidden honeypot field empty, and it solves a small
// proof of work (find a nonce so SHA-256(challenge ":" nonce) starts with
// loginPowBits zero bits; about a second in a browser, checked in microseconds
// here). The challenge is signed, bound to the browser's login cookie, expires
// with that cookie and is accepted once.
//
// Every /login/send and /login/verify request writes one journal line, so it
// is visible which checks a bot passes:
//
//	login send ip=203.0.113.5 email=e***@gmail.com emailid=3f2a9c1b honeypot=pass pow=pass age=6s result=sent ua="..."

const loginPowBits = 18
const loginPowLifetime = 20 * time.Minute // the login cookie's lifetime
const loginHoneypotField = "website"

var powNoncePattern = regexp.MustCompile(`^[0-9]{1,12}$`)

// newPowChallenge returns "issued.random.signature" for this browser.
func (a *accounts) newPowChallenge(browser string, now time.Time) string {
	random, err := randomHex(8)
	if err != nil {
		return ""
	}
	body := strconv.FormatInt(now.Unix(), 10) + "." + random
	return body + "." + a.powSignature(browser, body)
}

func (a *accounts) powSignature(browser, body string) string {
	return a.mac("login-pow:" + secretDigest(browser) + ":" + body)[:32]
}

func powZeroBits(challenge, nonce string) int {
	sum := sha256.Sum256([]byte(challenge + ":" + nonce))
	n := 0
	for _, b := range sum {
		if b != 0 {
			return n + bits.LeadingZeros8(b)
		}
		n += 8
	}
	return n
}

// checkPow reports pass, missing, malformed, forged, expired, reused, wrong or
// busy, and how long ago the challenge was issued (zero unless it is genuine).
// A genuine challenge is spent by this call whatever the nonce, so a page can
// never submit twice with one.
func (a *accounts) checkPow(browser, challenge, nonce string, now time.Time) (string, time.Duration) {
	if challenge == "" || nonce == "" {
		return "missing", 0
	}
	parts := strings.Split(challenge, ".")
	if len(parts) != 3 || len(parts[1]) != 16 || len(parts[2]) != 32 || !powNoncePattern.MatchString(nonce) {
		return "malformed", 0
	}
	issued, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return "malformed", 0
	}
	body := parts[0] + "." + parts[1]
	if browser == "" || subtle.ConstantTimeCompare([]byte(parts[2]), []byte(a.powSignature(browser, body))) != 1 {
		return "forged", 0
	}
	age := now.Sub(time.Unix(issued, 0))
	if age < -time.Minute || age > loginPowLifetime {
		return "expired", age
	}
	a.mu.Lock()
	a.cleanup(now)
	if a.powUsed == nil {
		a.powUsed = map[string]time.Time{}
	}
	if _, used := a.powUsed[body]; used {
		a.mu.Unlock()
		return "reused", age
	}
	if len(a.powUsed) >= 16384 {
		a.mu.Unlock()
		return "busy", age
	}
	a.powUsed[body] = time.Unix(issued, 0).Add(loginPowLifetime)
	a.mu.Unlock()
	if powZeroBits(challenge, nonce) < loginPowBits {
		return "wrong", age
	}
	return "pass", age
}

// honeypotStatus: the login forms always send the hidden field empty.
func honeypotStatus(r *http.Request) string {
	values, ok := r.PostForm[loginHoneypotField]
	if !ok {
		return "absent"
	}
	if len(values) != 1 || values[0] != "" {
		return "filled"
	}
	return "pass"
}

// loginLog is one journal line for a /login/send or /login/verify request.
// Never add codes, cookies or full addresses.
type loginLog struct {
	kind, ip, ua   string
	email, emailID string
	honeypot, pow  string
	age            time.Duration
	result         string
}

func (a *accounts) logEmail(entry *loginLog, raw string) {
	email, err := normalizeEmail(raw)
	if err != nil {
		if strings.TrimSpace(raw) == "" {
			entry.email = "-"
		} else {
			entry.email = "invalid"
		}
		return
	}
	entry.email = maskEmail(email)
	entry.emailID = a.mac("log-email:" + email)[:8]
}

// maskEmail keeps the first character and the domain: e***@gmail.com.
func maskEmail(email string) string {
	at := strings.LastIndexByte(email, '@')
	if at < 1 {
		return "invalid"
	}
	return email[:1] + "***" + email[at:]
}

func logField(value string, limit int) string {
	if value == "" {
		return "-"
	}
	var b strings.Builder
	for _, c := range value {
		if b.Len() >= limit {
			b.WriteString("…")
			break
		}
		if c < 32 || c == 127 || c == '"' || c == '\\' {
			c = '?'
		}
		b.WriteRune(c)
	}
	return b.String()
}

func (e *loginLog) write() {
	var b strings.Builder
	b.WriteString("login " + e.kind)
	b.WriteString(" ip=" + logField(e.ip, 64))
	b.WriteString(" email=" + logField(e.email, 80))
	if e.emailID != "" {
		b.WriteString(" emailid=" + e.emailID)
	}
	if e.kind == "send" {
		b.WriteString(" honeypot=" + logField(e.honeypot, 16))
		b.WriteString(" pow=" + logField(e.pow, 16))
		if e.age > 0 {
			b.WriteString(" age=" + strconv.FormatInt(int64(e.age/time.Second), 10) + "s")
		}
	}
	b.WriteString(" result=" + logField(e.result, 32))
	b.WriteString(` ua="` + logField(e.ua, 160) + `"`)
	log.Print(b.String())
}
