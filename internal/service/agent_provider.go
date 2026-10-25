package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The runner talks to the model through the Anthropic Messages API, which
// both MiniMax (https://api.minimax.io/anthropic) and Anthropic serve. Content
// blocks are kept as raw JSON so the model's own blocks (thinking included,
// which MiniMax requires back unchanged) return exactly as they came.

type agentMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type agentRequest struct {
	Model     string            `json:"model"`
	MaxTokens int               `json:"max_tokens"`
	System    []json.RawMessage `json:"system,omitempty"`
	Messages  []agentMessage    `json:"messages"`
	Tools     []json.RawMessage `json:"tools,omitempty"`
}

type agentUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// input is every input token the call was charged for, cached or not.
func (u agentUsage) input() int64 {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

type agentResponse struct {
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      agentUsage        `json:"usage"`
}

// agentProvider sends one Messages request. Tests use a fake.
type agentProvider interface {
	send(ctx context.Context, req agentRequest) (agentResponse, error)
}

// providerError is a failed call; retry marks one worth trying again (rate
// limits, overload, server errors, network failures).
type providerError struct {
	status  int
	message string
	retry   bool
}

func (e *providerError) Error() string {
	if e.status != 0 {
		return fmt.Sprintf("model provider answered %d: %s", e.status, e.message)
	}
	return "model provider unreachable: " + e.message
}

type messagesClient struct {
	endpoint, apiKey string
	http             *http.Client
}

func newMessagesClient(s *agentSettings) *messagesClient {
	return &messagesClient{endpoint: s.endpoint, apiKey: s.apiKey, http: &http.Client{Timeout: 5 * time.Minute}}
}

func (c *messagesClient) send(ctx context.Context, req agentRequest) (agentResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return agentResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return agentResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return agentResponse{}, ctx.Err()
		}
		return agentResponse{}, &providerError{message: "the request failed", retry: true}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return agentResponse{}, &providerError{status: resp.StatusCode, message: "the answer could not be read", retry: true}
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		message := http.StatusText(resp.StatusCode)
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			message = e.Error.Message
		}
		if len(message) > 300 {
			message = message[:300]
		}
		retry := resp.StatusCode == 408 || resp.StatusCode == 409 || resp.StatusCode == 429 || resp.StatusCode >= 500
		return agentResponse{}, &providerError{status: resp.StatusCode, message: strings.TrimSpace(message), retry: retry}
	}
	var out agentResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return agentResponse{}, &providerError{status: resp.StatusCode, message: "the answer was not a Messages response"}
	}
	return out, nil
}

// Content blocks the runner reads or writes.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	// CacheControl marks a prompt-cache breakpoint (Anthropic only).
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"`
}

func rawBlock(b contentBlock) json.RawMessage {
	out, _ := json.Marshal(b)
	return out
}

func textBlock(text string) json.RawMessage {
	return rawBlock(contentBlock{Type: "text", Text: text})
}
