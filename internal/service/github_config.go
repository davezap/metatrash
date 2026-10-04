package service

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"regexp"
	"strings"
)

// githubConfig is the optional "github" object in the account configuration:
// the Metatrash GitHub App. GitHub stays off unless enabled is true; while it
// is off the other fields are not checked, so the section can be filled in
// gradually. Secrets live in files, never in the JSON.
type githubConfig struct {
	Enabled           bool   `json:"enabled"`
	AppID             int64  `json:"appId"`
	AppSlug           string `json:"appSlug"`
	ClientID          string `json:"clientId"`
	ClientSecretFile  string `json:"clientSecretFile"`
	WebhookSecretFile string `json:"webhookSecretFile"`
	PrivateKeyFile    string `json:"privateKeyFile"`
}

// githubSettings are the loaded, validated settings. webBase and apiBase are
// fixed to GitHub; tests point them at a fake GitHub.
type githubSettings struct {
	appID         int64
	appSlug       string
	clientID      string
	clientSecret  string
	webhookSecret []byte
	privateKey    *rsa.PrivateKey
	webBase       string
	apiBase       string
}

var githubSlugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
var githubClientIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

func readSecretFile(path, name string, limit int64) (string, error) {
	if path == "" {
		return "", fmt.Errorf("github %s is required when github is enabled", name)
	}
	b, err := readLimitedFile(path, limit)
	if err != nil {
		return "", fmt.Errorf("cannot read github %s", name)
	}
	value := strings.TrimSpace(string(b))
	if value == "" {
		return "", fmt.Errorf("github %s is empty", name)
	}
	return value, nil
}

func parseGitHubPrivateKey(text string) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode([]byte(text))
	if block == nil || strings.TrimSpace(string(rest)) != "" {
		return nil, fmt.Errorf("github privateKeyFile must hold one PEM private key")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	return nil, fmt.Errorf("github privateKeyFile must hold an RSA private key")
}

// settings loads the secrets and key. Call it only when enabled.
func (c *githubConfig) settings() (*githubSettings, error) {
	if c.AppID <= 0 {
		return nil, fmt.Errorf("github appId must be the app's numeric ID")
	}
	if !githubSlugPattern.MatchString(c.AppSlug) {
		return nil, fmt.Errorf("github appSlug must be the app's URL name, as in github.com/apps/<appSlug>")
	}
	if !githubClientIDPattern.MatchString(c.ClientID) {
		return nil, fmt.Errorf("github clientId is missing or invalid")
	}
	secret, err := readSecretFile(c.ClientSecretFile, "clientSecretFile", 4096)
	if err != nil {
		return nil, err
	}
	webhook, err := readSecretFile(c.WebhookSecretFile, "webhookSecretFile", 4096)
	if err != nil {
		return nil, err
	}
	if len(webhook) < 16 {
		return nil, fmt.Errorf("github webhook secret must be at least 16 characters")
	}
	keyText, err := readSecretFile(c.PrivateKeyFile, "privateKeyFile", 16*1024)
	if err != nil {
		return nil, err
	}
	key, err := parseGitHubPrivateKey(keyText)
	if err != nil {
		return nil, fmt.Errorf("github privateKeyFile must hold the app's RSA private key (.pem)")
	}
	if key.N.BitLen() < 2048 {
		return nil, fmt.Errorf("github private key must be at least 2048 bits")
	}
	return &githubSettings{appID: c.AppID, appSlug: c.AppSlug, clientID: c.ClientID, clientSecret: secret,
		webhookSecret: []byte(webhook), privateKey: key, webBase: "https://github.com", apiBase: "https://api.github.com"}, nil
}
