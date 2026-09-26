package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"metatrash.com/metatrash"
)

type mcpIdentityKey struct{}
type mcpIdentity struct{ key, client string }

// MCP arguments deliberately exclude credentials, which belong to each HTTP request.
type mcpInput struct {
	Space      string  `json:"space"`
	Path       string  `json:"path"`
	Revision   string  `json:"revision"`
	Prefix     string  `json:"prefix"`
	ID         string  `json:"id"`
	Cursor     string  `json:"cursor"`
	Limit      int     `json:"limit"`
	Text       *string `json:"text"`
	IfInState  string  `json:"ifInState"`
	CreateOnly bool    `json:"createOnly"`
	From       string  `json:"from"`
	To         string  `json:"to"`
}

func (h *httpAdapter) mcpHandler(schema []byte) (http.Handler, error) {
	var catalog struct {
		Tools []*mcp.Tool `json:"tools"`
	}
	if err := json.Unmarshal(schema, &catalog); err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "metatrash", Version: metatrash.Version}, &mcp.ServerOptions{
		SupportedProtocolVersions: []string{"2025-11-25", "2025-06-18"},
	})
	for _, tool := range catalog.Tools {
		op := tool.Name
		switch op {
		case "read", "write", "list", "move", "history":
		default:
			return nil, fmt.Errorf("unknown MCP tool %q", op)
		}
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			return nil, err
		}
		var inputSchema jsonschema.Schema
		if err = json.Unmarshal(b, &inputSchema); err != nil {
			return nil, err
		}
		resolved, err := inputSchema.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("MCP schema %s: %w", op, err)
		}
		server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			identity, ok := ctx.Value(mcpIdentityKey{}).(mcpIdentity)
			if !ok {
				return mcpFailure(problem(401, "unauthorized", "Missing transport credentials.")), nil
			}
			raw := req.Params.Arguments
			args := mcpInput{Limit: 50}
			if err := decodeMCPInput(raw, &args); err != nil {
				return mcpFailure(err), nil
			}
			write := op == "write" || op == "move"
			if err := h.service.Access(args.Space, identity.key, identity.client, write); err != nil {
				return mcpFailure(err), nil
			}
			if int64(len(raw)) > h.service.config.Spaces[args.Space].Limits.Storage.MaxRequestBytes {
				return mcpFailure(problem(413, "payload_too_large", "Arguments exceed the space request limit.")), nil
			}
			var instance any
			if err := json.Unmarshal(raw, &instance); err != nil {
				return mcpFailure(invalid("Invalid arguments.")), nil
			}
			if err := resolved.Validate(instance); err != nil {
				return mcpFailure(invalid("Arguments do not match the tool input schema.")), nil
			}
			in := Input{Path: args.Path, Revision: args.Revision, Prefix: args.Prefix, ID: args.ID, Cursor: args.Cursor, Limit: args.Limit,
				Text: args.Text, IfInState: args.IfInState, CreateOnly: args.CreateOnly, From: args.From, To: args.To}
			value, err := h.service.Dispatch(ctx, args.Space, op, in)
			if err != nil {
				return mcpFailure(err), nil
			}
			b, err := json.Marshal(value)
			if err != nil {
				return mcpFailure(err), nil
			}
			return &mcp.CallToolResult{StructuredContent: value, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
		})
	}
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 512 << 10,
		// Apache preserves the public Host on a loopback connection. serveMCP
		// applies an explicit Host allowlist before entering the SDK.
		DisableLocalhostProtection: true,
	}), nil
}

func (h *httpAdapter) serveMCP(w http.ResponseWriter, r *http.Request) {
	switch strings.ToLower(r.Host) {
	case "metatrash.com", "metatrash.com:443", "127.0.0.1:8080", "localhost:8080", "[::1]:8080":
	default:
		sendError(w, problem(403, "forbidden", "Unrecognized MCP host."))
		return
	}
	// Native MCP clients omit Origin. Browser callers must use the public HTTPS origin.
	if origins := r.Header.Values("Origin"); len(origins) > 0 && (len(origins) != 1 || origins[0] != "https://metatrash.com") {
		sendError(w, problem(403, "forbidden", "Unrecognized MCP origin."))
		return
	}
	key := ""
	if auth := strings.Fields(r.Header.Get("Authorization")); len(auth) == 2 && strings.EqualFold(auth[0], "Bearer") {
		key = auth[1]
	}
	ctx := context.WithValue(r.Context(), mcpIdentityKey{}, mcpIdentity{key: key, client: h.client(r)})
	h.mcp.ServeHTTP(w, r.WithContext(ctx))
}

func decodeMCPInput(raw []byte, args *mcpInput) error {
	if !validUnicodeJSON(raw) {
		return invalid("Arguments must contain valid UTF-8/Unicode.")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return invalid("Expected an arguments object.")
	}
	seen := map[string]bool{}
	for d.More() {
		token, err = d.Token()
		if err != nil {
			return invalid("Invalid arguments.")
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return invalid("Duplicate argument.")
		}
		seen[key] = true
		var value json.RawMessage
		if err = d.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid("Invalid or null argument.")
		}
	}
	if _, err = d.Token(); err != nil {
		return invalid("Invalid arguments.")
	}
	if _, err = d.Token(); err != io.EOF {
		return invalid("Unexpected trailing arguments.")
	}
	if err = strictJSON(raw, args); err != nil {
		return invalid("Invalid argument fields.")
	}
	return nil
}

func mcpFailure(err error) *mcp.CallToolResult {
	var p *Error
	if !errors.As(err, &p) {
		log.Printf("MCP operation failed: %v", err)
		p = problem(500, "internal_error", "Service operation failed.")
	}
	b, _ := json.Marshal(struct {
		Error *Error `json:"error"`
	}{p})
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
}
