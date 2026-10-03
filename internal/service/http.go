package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

type httpAdapter struct {
	service      *Service
	schema       []byte
	proxies      []*net.IPNet
	mcp          http.Handler
	mcpAccount   http.Handler
	basePath     string
	publicOrigin string
	publicHost   string
	// oauth is nil unless accounts are enabled with oauth.enabled.
	oauth *oauthServer
}

func (s *Service) Handler(schema []byte, trustedProxies []string, publicURLs ...string) (http.Handler, error) {
	h := &httpAdapter{service: s, schema: schema}
	publicURL := "https://metatrash.com"
	if len(publicURLs) > 0 {
		publicURL = publicURLs[0]
	}
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return nil, fmt.Errorf("public URL must be an HTTPS origin with an optional plain path prefix")
	}
	h.basePath = strings.TrimSuffix(u.Path, "/")
	for _, segment := range strings.Split(strings.TrimPrefix(h.basePath, "/"), "/") {
		if h.basePath == "" {
			break
		}
		if segment == "" || segment == "." || segment == ".." {
			return nil, fmt.Errorf("invalid public URL path prefix")
		}
		for _, c := range segment {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return nil, fmt.Errorf("public URL path supports letters, digits, hyphens and underscores")
			}
		}
	}
	h.publicHost = strings.ToLower(u.Host)
	h.publicOrigin = "https://" + h.publicHost
	if s.accounts != nil && s.accounts.config.Origin != h.publicOrigin {
		return nil, fmt.Errorf("account origin must match public URL origin (without path)")
	}
	for _, value := range trustedProxies {
		_, subnet, err := net.ParseCIDR(value)
		if err != nil {
			return nil, err
		}
		h.proxies = append(h.proxies, subnet)
	}
	if s.accounts != nil && s.accounts.oauth != nil {
		h.oauth = newOAuthServer(*s.accounts.oauth, h.publicOrigin+h.basePath)
	}
	h.mcp, err = h.mcpHandler(schema, false)
	if err != nil {
		return nil, err
	}
	if h.oauth != nil {
		h.mcpAccount, err = h.mcpHandler(schema, true)
		if err != nil {
			return nil, err
		}
	}
	return h, nil
}

func (h *httpAdapter) trusted(ip net.IP) bool {
	for _, subnet := range h.proxies {
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}

func (h *httpAdapter) client(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(host)
	if ip == nil {
		return "unknown"
	}
	if h.trusted(ip) {
		chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(chain) - 1; i >= 0; i-- {
			next := net.ParseIP(strings.TrimSpace(chain[i]))
			if next == nil {
				break
			}
			ip = next
			if !h.trusted(ip) {
				break
			}
		}
	}
	return ip.String()
}

func sendJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func sendError(w http.ResponseWriter, err error) {
	var p *Error
	if !errors.As(err, &p) {
		log.Printf("request failed: %v", err)
		p = problem(500, "internal_error", "Service operation failed.")
	}
	if p.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
	}
	if p.Status == 401 {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	sendJSON(w, p.Status, struct {
		Error *Error `json:"error"`
	}{p})
}

func (h *httpAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	client := h.client(r)
	// A separate ingress ceiling covers discovery, malformed routes, and authentication attempts.
	if err := h.service.rates.reserve(allowance{"ingress", 2400, 60}, allowance{"ingress:" + client, 240, 60}); err != nil {
		sendError(w, err)
		return
	}
	if len(r.RequestURI) > 4096 {
		sendError(w, invalid("Request URL too long."))
		return
	}
	// Credentials never belong in URLs (MCP authorization spec): refuse a key
	// query parameter on every route rather than risk it reaching logs or links.
	if credentialInQuery(r.URL.RawQuery) {
		sendError(w, invalid("Credentials belong in the Authorization header, never in the URL."))
		return
	}
	if r.URL.Path == "/mcp" {
		h.serveMCP(w, r)
		return
	}
	if r.URL.Path == "/mcp/account" {
		h.serveMCPAccount(w, r)
		return
	}
	if h.serveOAuth(w, r, client) || h.serveAsset(w, r) || h.serveAccounts(w, r, client) || h.serveBrowser(w, r, client) || h.serveOwnedBrowser(w, r, client) {
		return
	}
	if r.URL.Path == "/api/v1/spaces/public/recent" {
		h.serveRecent(w, r, client)
		return
	}
	// Health is for direct loopback checks, never requests forwarded by Apache.
	// Reject forwarding headers regardless of configured proxy trust.
	if r.URL.Path == "/healthz" {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() || len(r.Header.Values("X-Forwarded-For")) > 0 || len(r.Header.Values("Forwarded")) > 0 || len(r.Header.Values("X-Forwarded-Host")) > 0 {
			sendError(w, missing())
			return
		}
	}
	if r.URL.Path == "/healthz" || r.URL.Path == "/api/v1/tool-schema.json" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			sendError(w, problem(405, "invalid_request", "Use GET."))
			return
		}
		if r.URL.Path == "/healthz" {
			sendJSON(w, 200, map[string]string{"status": "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(h.schema)
		return
	}
	if r.URL.Path == "/api/v1/account" || strings.HasPrefix(r.URL.Path, "/api/v1/account/") {
		h.serveAccountREST(w, r, client)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) != 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "spaces" {
		sendError(w, missing())
		return
	}
	space := parts[3]
	key := ""
	auth := strings.Fields(r.Header.Get("Authorization"))
	if len(auth) == 2 && strings.EqualFold(auth[0], "Bearer") {
		key = auth[1]
	}
	h.serveRESTOperation(w, r, parts[4], "", func(write bool) (*repository, string, error) {
		if err := h.service.Access(space, key, client, write); err != nil {
			return nil, "", err
		}
		return h.service.repos[space], space, nil
	})
}

// serveRESTOperation maps one REST route to an operation, runs the
// transport's access check once (before quotas and Git) and dispatches to the
// repository that check admitted, under the canonical space name it returns.
// On the OAuth resource, metadataPath is set and an insufficient_scope denial
// carries an RFC 6750 challenge.
func (h *httpAdapter) serveRESTOperation(w http.ResponseWriter, r *http.Request, resource, metadataPath string, access func(write bool) (*repository, string, error)) {
	op, allow := "", "GET"
	switch resource {
	case "file":
		allow = "GET, PUT, DELETE"
		if r.Method == "GET" {
			op = "read"
		} else if r.Method == "PUT" {
			op = "write"
		} else if r.Method == "DELETE" {
			op = "delete"
		}
	case "files":
		if r.Method == "GET" {
			op = "list"
		}
	case "history":
		if r.Method == "GET" {
			op = "history"
		}
	case "move":
		allow = "POST"
		if r.Method == "POST" {
			op = "move"
		}
	default:
		sendError(w, missing())
		return
	}
	if op == "" {
		w.Header().Set("Allow", allow)
		sendError(w, problem(405, "invalid_request", "Unsupported method."))
		return
	}
	write := op == "write" || op == "move" || op == "delete"
	repo, space, err := access(write)
	if err != nil {
		var p *Error
		if metadataPath != "" && errors.As(err, &p) && p.Code == "insufficient_scope" {
			h.oauthChallenge(w, metadataPath, p)
			return
		}
		sendError(w, err)
		return
	}
	in, err := parseQuery(r.URL.RawQuery, op)
	if err != nil {
		sendError(w, err)
		return
	}
	if write {
		kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || kind != "application/json" {
			sendError(w, problem(415, "invalid_request", "Use application/json."))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, repo.limits.MaxRequestBytes)
		b, err := io.ReadAll(r.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				sendError(w, problem(413, "payload_too_large", "Request exceeds the byte limit."))
			} else {
				sendError(w, invalid("Could not read body."))
			}
			return
		}
		if err = parseBody(b, op, &in); err != nil {
			sendError(w, err)
			return
		}
	}
	value, err := h.service.dispatchRepository(r.Context(), repo, space, op, in)
	if err != nil {
		sendError(w, err)
		return
	}
	status := 200
	if result, ok := value.(WriteResult); ok && result.Created {
		status = 201
	}
	sendJSON(w, status, value)
}

// credentialInQuery reports a key or access_token parameter, including
// percent-encoded names and semicolon-separated pairs.
func credentialInQuery(raw string) bool {
	for _, pair := range strings.FieldsFunc(raw, func(r rune) bool { return r == '&' || r == ';' }) {
		name, _, _ := strings.Cut(pair, "=")
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "key" || name == "access_token" {
			return true
		}
	}
	return false
}

func parseQuery(raw, op string) (Input, error) {
	in := Input{Limit: 50}
	q, err := url.ParseQuery(raw)
	if err != nil {
		return in, invalid("Invalid query.")
	}
	allowed := map[string]bool{}
	switch op {
	case "read":
		allowed["path"], allowed["revision"] = true, true
	case "write", "delete":
		allowed["path"] = true
	case "list":
		allowed["prefix"], allowed["limit"], allowed["cursor"] = true, true, true
	case "history":
		allowed["id"], allowed["limit"], allowed["cursor"] = true, true, true
	}
	for k, values := range q {
		if !allowed[k] || len(values) != 1 {
			return in, invalid("Unknown or duplicate query parameter.")
		}
	}
	in.Path, in.Revision, in.Prefix, in.ID, in.Cursor = q.Get("path"), q.Get("revision"), q.Get("prefix"), q.Get("id"), q.Get("cursor")
	if _, ok := q["revision"]; ok && !hashPattern.MatchString(in.Revision) {
		return in, invalid("Invalid revision.")
	}
	if _, ok := q["limit"]; ok {
		in.Limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || in.Limit < 1 || in.Limit > 100 {
			return in, invalid("limit must be 1..100.")
		}
	}
	return in, nil
}

func parseBody(b []byte, op string, in *Input) error {
	if !validUnicodeJSON(b) {
		return invalid("Body must contain valid UTF-8/Unicode.")
	}
	allowed := map[string]bool{"ifInState": true}
	if op == "write" {
		allowed["text"], allowed["createOnly"] = true, true
	} else if op == "move" {
		allowed["from"], allowed["to"] = true, true
	}
	d := json.NewDecoder(bytes.NewReader(b))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return invalid("Expected a JSON object.")
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return invalid("Invalid JSON.")
		}
		key, ok := token.(string)
		if !ok || !allowed[key] || seen[key] {
			return invalid("Unknown or duplicate body field.")
		}
		seen[key] = true
		var value json.RawMessage
		if err := d.Decode(&value); err != nil || bytes.Equal(value, []byte("null")) {
			return invalid("Invalid or null body field.")
		}
	}
	if _, err := d.Token(); err != nil {
		return invalid("Invalid JSON.")
	}
	if err := strictJSON(b, in); err != nil {
		return invalid("Invalid body fields.")
	}
	return nil
}

// encoding/json replaces invalid surrogate escapes; reject them rather than changing stored text.
func validUnicodeJSON(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) {
			return false
		}
		if b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return false
		}
		n, err := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xDC00 && n <= 0xDFFF {
			return false
		}
		if n >= 0xD800 && n <= 0xDBFF {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return true
}
