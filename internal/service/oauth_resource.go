package service

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
)

// bearerToken returns the single Authorization: Bearer credential, or "" when
// none is present. Malformed or repeated headers are reported as invalid.
func bearerToken(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) == 0 {
		return "", true
	}
	fields := strings.Fields(values[0])
	if len(values) != 1 || len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") || len(fields[1]) > 512 {
		return "", false
	}
	return fields[1], true
}

// oauthChallenge writes a 401 or 403 with an RFC 6750 / RFC 9728 challenge
// that points clients at the protected resource metadata.
func (h *httpAdapter) oauthChallenge(w http.ResponseWriter, metadataPath string, err *Error) {
	value := `Bearer resource_metadata="` + h.publicOrigin + h.basePath + metadataPath + `", scope="` + scopeReadWrite + `"`
	if err.Code == "invalid_token" || err.Code == "insufficient_scope" || err.Code == "invalid_request" {
		value += `, error="` + err.Code + `", error_description="` + strings.ReplaceAll(err.Message, `"`, "'") + `"`
	}
	w.Header().Set("WWW-Authenticate", value)
	sendJSON(w, err.Status, struct {
		Error *Error `json:"error"`
	}{err})
}

// authenticateOAuth validates the bearer token for one protected resource.
// It writes the challenge and returns false when the request cannot proceed.
func (h *httpAdapter) authenticateOAuth(w http.ResponseWriter, r *http.Request, resource, metadataPath string) (oauthIdentity, bool) {
	token, ok := bearerToken(r)
	if !ok {
		h.oauthChallenge(w, metadataPath, &Error{Status: 400, Code: "invalid_request", Message: "Send one Authorization: Bearer header."})
		return oauthIdentity{}, false
	}
	if token == "" {
		h.oauthChallenge(w, metadataPath, &Error{Status: 401, Code: "unauthorized", Message: "Sign in through OAuth to use this endpoint."})
		return oauthIdentity{}, false
	}
	if err := h.service.rates.reserve(allowance{"oauth:bearer:" + secretDigest(token)[:16], 600, 60}); err != nil {
		sendError(w, err)
		return oauthIdentity{}, false
	}
	id, err := h.service.ownedDB.validateAccessToken(r.Context(), token, resource, h.oauth.now(), h.oauth.settings)
	if err != nil {
		var p *Error
		if errors.As(err, &p) {
			h.oauthChallenge(w, metadataPath, p)
		} else {
			log.Printf("access token check failed (%T)", err)
			sendError(w, problem(503, "unavailable", "Authorization is temporarily unavailable."))
		}
		return oauthIdentity{}, false
	}
	return id, true
}

// serveMCPAccount is the OAuth-protected MCP endpoint (D1). Clients start
// OAuth from its 401 challenge. Space keys are never accepted here.
func (h *httpAdapter) serveMCPAccount(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if h.oauth == nil || h.mcpAccount == nil || h.service.ownedDB == nil {
		sendError(w, missing())
		return
	}
	if !h.mcpHostAndOrigin(w, r) {
		return
	}
	if r.URL.RawQuery != "" {
		sendError(w, invalid("The MCP endpoint takes no query parameters."))
		return
	}
	const metadataPath = "/.well-known/oauth-protected-resource/mcp/account"
	id, ok := h.authenticateOAuth(w, r, h.oauth.mcpResource, metadataPath)
	if !ok {
		return
	}
	clean := r.Clone(context.WithValue(r.Context(), mcpIdentityKey{}, mcpIdentity{client: h.client(r), oauth: &id}))
	clean.Header.Del("Authorization")
	h.mcpAccount.ServeHTTP(w, clean)
}

// serveAccountREST is the OAuth-protected REST resource (D2):
// GET /api/v1/account/spaces lists connected spaces and
// /api/v1/account/spaces/{space}/{file,files,history,move} mirror the
// anonymous routes with the same access check as MCP.
func (h *httpAdapter) serveAccountREST(w http.ResponseWriter, r *http.Request, client string) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if h.oauth == nil || h.service.ownedDB == nil {
		sendError(w, missing())
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/account/"), "/")
	if !(len(parts) == 1 && parts[0] == "spaces") && !(len(parts) == 3 && parts[0] == "spaces") {
		sendError(w, missing())
		return
	}
	// Browser callers must be same-origin; native clients send no Origin.
	if origins := r.Header.Values("Origin"); len(origins) > 0 && (len(origins) != 1 || origins[0] != h.publicOrigin) {
		sendError(w, problem(403, "forbidden", "Unrecognized origin."))
		return
	}
	const metadataPath = "/.well-known/oauth-protected-resource/api/v1/account"
	id, ok := h.authenticateOAuth(w, r, h.oauth.restResource, metadataPath)
	if !ok {
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			sendError(w, problem(405, "invalid_request", "Use GET."))
			return
		}
		if r.URL.RawQuery != "" {
			sendError(w, invalid("Unknown query parameter."))
			return
		}
		if err := h.service.rates.reserve(allowance{"oauth:spaces:" + id.GrantID, 120, 60}); err != nil {
			sendError(w, err)
			return
		}
		value, err := h.service.listConnectedSpaces(r.Context(), id)
		if err != nil {
			sendError(w, err)
			return
		}
		sendJSON(w, 200, value)
		return
	}
	space := parts[1]
	if !spacePattern.MatchString(space) {
		sendError(w, missing())
		return
	}
	h.serveRESTOperation(w, r, space, parts[2], metadataPath, func(write bool) (*repository, error) {
		return h.service.agentSpaceAccess(r.Context(), id, space, client, write)
	})
}
