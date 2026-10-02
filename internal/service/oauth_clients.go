package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"metatrash.com/metatrash"
)

// oauthClient is a public client described by a Client ID Metadata Document.
// The client_id is the HTTPS URL of that document.
type oauthClient struct {
	ID, Name, Host string
	RedirectURIs   []string
}

const maxClientDocumentBytes = 16 * 1024
const maxClientCacheEntries = 128
const maxClientCacheTTL = 24 * time.Hour
const defaultClientCacheTTL = time.Hour
const clientFailureTTL = time.Minute

var redirectSchemePattern = regexp.MustCompile(`^[a-z][a-z0-9+.-]{0,31}$`)

// Schemes that must never receive authorization responses.
var deniedRedirectSchemes = map[string]bool{"javascript": true, "data": true, "file": true, "vbscript": true, "about": true, "blob": true, "ftp": true, "ws": true, "wss": true, "mailto": true, "tel": true}

type clientCacheEntry struct {
	client  oauthClient
	err     error
	expires time.Time
}

// oauthClients fetches and caches client metadata from allow-listed hosts.
type oauthClients struct {
	hosts map[string]bool
	mu    sync.Mutex
	cache map[string]clientCacheEntry
	slots chan struct{}
	// fetch returns the document body and the cache lifetime its headers allow.
	fetch func(ctx context.Context, clientID string) ([]byte, time.Duration, error)
	now   func() time.Time
}

func newOAuthClients(hosts map[string]bool) *oauthClients {
	c := &oauthClients{hosts: hosts, cache: map[string]clientCacheEntry{}, slots: make(chan struct{}, 4), now: time.Now}
	httpClient := clientMetadataHTTPClient()
	c.fetch = func(ctx context.Context, clientID string) ([]byte, time.Duration, error) {
		return fetchClientDocument(ctx, httpClient, clientID)
	}
	return c
}

// clientIDHost validates the client_id URL shape and returns its allow-listed
// host. The URL must already be in canonical form: https, no port, userinfo,
// query, fragment, encoded or dot segments, and a path beyond "/".
func (c *oauthClients) clientIDHost(clientID string) (string, error) {
	bad := fmt.Errorf("unrecognized client")
	if len(clientID) < 10 || len(clientID) > 255 {
		return "", bad
	}
	for _, r := range clientID {
		if r < 33 || r > 126 {
			return "", bad
		}
	}
	u, err := url.Parse(clientID)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return "", bad
	}
	host := u.Hostname()
	if host != strings.ToLower(host) || !c.hosts[host] {
		return "", bad
	}
	if len(u.Path) < 2 || !strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "//") {
		return "", bad
	}
	for _, segment := range strings.Split(u.Path[1:], "/") {
		if segment == "." || segment == ".." {
			return "", bad
		}
	}
	if "https://"+host+u.Path != clientID {
		return "", bad
	}
	return host, nil
}

// lookup returns validated client metadata, from cache when still fresh.
func (c *oauthClients) lookup(ctx context.Context, clientID string) (oauthClient, error) {
	host, err := c.clientIDHost(clientID)
	if err != nil {
		return oauthClient{}, err
	}
	now := c.now()
	c.mu.Lock()
	if entry, ok := c.cache[clientID]; ok && now.Before(entry.expires) {
		c.mu.Unlock()
		return entry.client, entry.err
	}
	c.mu.Unlock()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	default:
		return oauthClient{}, fmt.Errorf("client lookup busy")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body, ttl, err := c.fetch(ctx, clientID)
	var client oauthClient
	if err == nil {
		client, err = parseClientDocument(body, clientID, host)
	}
	if err != nil {
		// Briefly remember failures so a broken or hostile document is not refetched per request.
		ttl = clientFailureTTL
		client = oauthClient{}
	}
	if ttl > maxClientCacheTTL {
		ttl = maxClientCacheTTL
	}
	if ttl > 0 {
		c.mu.Lock()
		if len(c.cache) >= maxClientCacheEntries {
			for key, entry := range c.cache {
				if !now.Before(entry.expires) {
					delete(c.cache, key)
				}
			}
			for key := range c.cache {
				if len(c.cache) < maxClientCacheEntries {
					break
				}
				delete(c.cache, key)
			}
		}
		c.cache[clientID] = clientCacheEntry{client: client, err: err, expires: now.Add(ttl)}
		c.mu.Unlock()
	}
	return client, err
}

func parseClientDocument(body []byte, clientID, host string) (oauthClient, error) {
	var doc struct {
		ClientID                string          `json:"client_id"`
		ClientName              *string         `json:"client_name"`
		RedirectURIs            []string        `json:"redirect_uris"`
		TokenEndpointAuthMethod *string         `json:"token_endpoint_auth_method"`
		GrantTypes              []string        `json:"grant_types"`
		ResponseTypes           []string        `json:"response_types"`
		ClientSecret            json.RawMessage `json:"client_secret"`
	}
	if !utf8.Valid(body) {
		return oauthClient{}, fmt.Errorf("client metadata is not UTF-8")
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return oauthClient{}, fmt.Errorf("client metadata is not a JSON object")
	}
	if doc.ClientID != clientID {
		return oauthClient{}, fmt.Errorf("client metadata client_id mismatch")
	}
	if doc.ClientSecret != nil {
		return oauthClient{}, fmt.Errorf("client metadata must not contain a secret")
	}
	if doc.TokenEndpointAuthMethod != nil && *doc.TokenEndpointAuthMethod != "none" {
		return oauthClient{}, fmt.Errorf("client must be a public client")
	}
	if doc.GrantTypes != nil && !containsString(doc.GrantTypes, "authorization_code") {
		return oauthClient{}, fmt.Errorf("client does not use the authorization code grant")
	}
	if doc.ResponseTypes != nil && !containsString(doc.ResponseTypes, "code") {
		return oauthClient{}, fmt.Errorf("client does not use code responses")
	}
	if len(doc.RedirectURIs) == 0 || len(doc.RedirectURIs) > 20 {
		return oauthClient{}, fmt.Errorf("client metadata needs 1-20 redirect URIs")
	}
	for _, uri := range doc.RedirectURIs {
		if !validRedirectURI(uri) {
			return oauthClient{}, fmt.Errorf("client metadata has an invalid redirect URI")
		}
	}
	name := host
	if doc.ClientName != nil {
		candidate := strings.TrimSpace(*doc.ClientName)
		ok := utf8.RuneCountInString(candidate) >= 1 && utf8.RuneCountInString(candidate) <= 100
		for _, r := range candidate {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				ok = false
			}
		}
		if ok {
			name = candidate
		}
	}
	return oauthClient{ID: clientID, Name: name, Host: host, RedirectURIs: doc.RedirectURIs}, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func loopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// validRedirectURI accepts https URLs, http loopback URLs (RFC 8252) and
// private-use schemes for native apps. Fragments and credentials are refused.
func validRedirectURI(raw string) bool {
	if len(raw) < 4 || len(raw) > 512 {
		return false
	}
	for _, r := range raw {
		if r < 33 || r > 126 {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" || strings.Contains(raw, "#") || u.User != nil || !redirectSchemePattern.MatchString(u.Scheme) || deniedRedirectSchemes[u.Scheme] {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != "" && u.Opaque == ""
	case "http":
		return u.Opaque == "" && loopbackHost(u.Hostname())
	default:
		return u.Opaque != "" || u.Host != "" || u.Path != ""
	}
}

// matchRedirectURI requires an exact match with a registered URI, except that
// loopback http redirects ignore the port (RFC 8252 section 7.3).
func (c oauthClient) matchRedirectURI(requested string) bool {
	if !validRedirectURI(requested) {
		return false
	}
	for _, registered := range c.RedirectURIs {
		if requested == registered {
			return true
		}
		reg, err1 := url.Parse(registered)
		req, err2 := url.Parse(requested)
		if err1 != nil || err2 != nil || reg.Scheme != "http" || req.Scheme != "http" || !loopbackHost(reg.Hostname()) {
			continue
		}
		if req.Hostname() == reg.Hostname() && req.EscapedPath() == reg.EscapedPath() && req.RawQuery == reg.RawQuery && !req.ForceQuery && !reg.ForceQuery {
			if port := req.Port(); port != "" {
				if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
					continue
				}
			}
			return true
		}
	}
	return false
}

// redirectSource is the CSP form-action source that allows the consent form's
// redirect to reach this client.
func redirectSource(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Scheme == "https" || u.Scheme == "http" {
		return u.Scheme + "://" + u.Host
	}
	return u.Scheme + ":"
}

// publicAddress reports whether an address may be contacted for metadata.
// Loopback, private, link-local (including cloud metadata), shared, multicast
// and unspecified addresses are refused at connection time.
func publicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "2001:db8::/32", "2002::/16"} {
		if netip.MustParsePrefix(prefix).Contains(addr) {
			return false
		}
	}
	return true
}

func clientMetadataHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		addrPort, err := netip.ParseAddrPort(address)
		if err != nil || !publicAddress(addrPort.Addr()) || addrPort.Port() != 443 {
			return fmt.Errorf("client metadata address refused")
		}
		return nil
	}}
	transport := &http.Transport{
		// Never use an environment proxy: the address check must see the real peer.
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		MaxResponseHeaderBytes: 16 * 1024,
		MaxIdleConns:           4,
		IdleConnTimeout:        30 * time.Second,
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func fetchClientDocument(ctx context.Context, client *http.Client, clientID string) ([]byte, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientID, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "metatrash/"+metatrash.Version)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("client metadata unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("client metadata unavailable")
	}
	kind, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		return nil, 0, fmt.Errorf("client metadata must be JSON")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxClientDocumentBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("client metadata unavailable")
	}
	if len(body) > maxClientDocumentBytes {
		return nil, 0, errors.New("client metadata too large")
	}
	return body, cacheLifetime(resp.Header), nil
}

// cacheLifetime honours Cache-Control no-store/no-cache/max-age; the caller caps it.
func cacheLifetime(header http.Header) time.Duration {
	ttl := defaultClientCacheTTL
	for _, value := range header.Values("Cache-Control") {
		for _, directive := range strings.Split(value, ",") {
			directive = strings.ToLower(strings.TrimSpace(directive))
			switch {
			case directive == "no-store" || directive == "no-cache":
				return 0
			case strings.HasPrefix(directive, "max-age="):
				seconds, err := strconv.Atoi(strings.TrimPrefix(directive, "max-age="))
				if err != nil || seconds < 0 {
					return 0
				}
				ttl = time.Duration(seconds) * time.Second
			}
		}
	}
	return ttl
}
