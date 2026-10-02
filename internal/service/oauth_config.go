package service

import (
	"fmt"
	"strings"
	"time"
)

// oauthConfig is the optional "oauth" object in the account configuration.
// OAuth stays off unless enabled is true. Zero lifetimes take the defaults.
type oauthConfig struct {
	Enabled            bool     `json:"enabled"`
	ClientHosts        []string `json:"clientHosts"`
	AccessTokenMinutes int      `json:"accessTokenMinutes"`
	RefreshTokenDays   int      `json:"refreshTokenDays"`
	CodeSeconds        int      `json:"codeSeconds"`
	GrantIdleDays      int      `json:"grantIdleDays"`
}

// Effective settings after defaults and validation.
type oauthSettings struct {
	clientHosts map[string]bool
	accessTTL   time.Duration
	refreshTTL  time.Duration
	codeTTL     time.Duration
	grantIdle   time.Duration
}

var defaultOAuthClientHosts = []string{"claude.ai", "chatgpt.com"}

func validOAuthHost(host string) bool {
	if len(host) < 3 || len(host) > 253 || !strings.Contains(host, ".") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func (c *oauthConfig) settings() (oauthSettings, error) {
	s := oauthSettings{clientHosts: map[string]bool{}}
	hosts := c.ClientHosts
	if hosts == nil {
		hosts = defaultOAuthClientHosts
	}
	if len(hosts) == 0 || len(hosts) > 32 {
		return s, fmt.Errorf("oauth clientHosts must list 1-32 client hosts")
	}
	for _, host := range hosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if !validOAuthHost(host) {
			return s, fmt.Errorf("invalid oauth client host %q; use a plain DNS name such as claude.ai", host)
		}
		s.clientHosts[host] = true
	}
	pick := func(value, fallback, min, max int, name string) (int, error) {
		if value == 0 {
			value = fallback
		}
		if value < min || value > max {
			return 0, fmt.Errorf("oauth %s must be %d-%d", name, min, max)
		}
		return value, nil
	}
	access, err := pick(c.AccessTokenMinutes, 60, 5, 1440, "accessTokenMinutes")
	if err != nil {
		return s, err
	}
	refresh, err := pick(c.RefreshTokenDays, 30, 1, 365, "refreshTokenDays")
	if err != nil {
		return s, err
	}
	code, err := pick(c.CodeSeconds, 60, 10, 600, "codeSeconds")
	if err != nil {
		return s, err
	}
	idle, err := pick(c.GrantIdleDays, 90, 1, 730, "grantIdleDays")
	if err != nil {
		return s, err
	}
	if idle < refresh {
		return s, fmt.Errorf("oauth grantIdleDays must be at least refreshTokenDays")
	}
	s.accessTTL = time.Duration(access) * time.Minute
	s.refreshTTL = time.Duration(refresh) * 24 * time.Hour
	s.codeTTL = time.Duration(code) * time.Second
	s.grantIdle = time.Duration(idle) * 24 * time.Hour
	return s, nil
}
