package service

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	// Schedules and runner.md times use IANA zones; carry the zone data so a
	// server without it still works.
	_ "time/tzdata"
)

// agentsConfig is the optional "agents" object in the account configuration:
// the model provider that runs folder actions (actions.go, agent_runner.go).
// Actions are checked and stored either way; they run only while enabled is
// true. The API key lives in a file, never in the JSON or in Git.
//
// provider is "minimax" (MiniMax's Anthropic-compatible API) or
// "anthropic"; both speak the Anthropic Messages API, so baseURL and model
// are all that differ. dailyTokenCap limits the tokens (input and output) a
// space's jobs may use per day in timezone: 2,000,000 when left out, 0 for no
// limit.
//
// owners lists the usernames whose own spaces may run actions. The key pays
// for every job, so it is required: nobody else's spaces run actions, even
// with actions in their folders.
type agentsConfig struct {
	Enabled       bool     `json:"enabled"`
	Owners        []string `json:"owners"`
	Provider      string   `json:"provider"`
	BaseURL       string   `json:"baseURL"`
	Model         string   `json:"model"`
	APIKeyFile    string   `json:"apiKeyFile"`
	MaxTokens     int      `json:"maxTokens"`
	Timezone      string   `json:"timezone"`
	DailyTokenCap *int64   `json:"dailyTokenCap"`
}

// agentSettings are the loaded, validated settings.
type agentSettings struct {
	provider string
	// app is how History names the agent: "MiniMax (agent) for dave".
	app       string
	endpoint  string
	model     string
	apiKey    string
	maxTokens int
	zone      *time.Location
	dailyCap  int64
	keyFile   string
	// owners are the usernames whose spaces run actions.
	owners []string
}

// allows reports whether a space owner's actions run.
func (s *agentSettings) allows(username string) bool {
	return username != "" && slices.Contains(s.owners, username)
}

var agentProviders = map[string]struct{ app, baseURL, model string }{
	"minimax":   {"MiniMax", "https://api.minimax.io/anthropic", "MiniMax-M3"},
	"anthropic": {"Claude", "https://api.anthropic.com", "claude-sonnet-5-5"},
}

const (
	defaultAgentMaxTokens = 16384
	defaultAgentTimezone  = "Pacific/Auckland"
	defaultDailyTokenCap  = 2000000
)

// settings checks the section and loads the key. Call it only when enabled.
func (c *agentsConfig) settings() (*agentSettings, error) {
	provider := strings.ToLower(strings.TrimSpace(c.Provider))
	p, ok := agentProviders[provider]
	if !ok {
		return nil, fmt.Errorf(`agents provider must be "minimax" or "anthropic", not %q`, c.Provider)
	}
	if len(c.Owners) == 0 {
		return nil, fmt.Errorf(`agents owners must list the usernames whose own spaces may run folder actions, such as ["dave"]; the API key pays for their jobs`)
	}
	for _, name := range c.Owners {
		if !usernamePattern.MatchString(name) {
			return nil, fmt.Errorf("agents owners: %q is not a username", name)
		}
	}
	base := c.BaseURL
	if base == "" {
		base = p.baseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("agents baseURL must be an https URL without query, such as %s", p.baseURL)
	}
	model := c.Model
	if model == "" {
		model = p.model
	}
	if len(model) > 100 || strings.ContainsAny(model, " \t\r\n\"") {
		return nil, fmt.Errorf("agents model is not a valid model name")
	}
	if c.APIKeyFile == "" {
		return nil, fmt.Errorf("agents apiKeyFile is required when agents is enabled")
	}
	b, err := readLimitedFile(c.APIKeyFile, 4096)
	if err != nil {
		return nil, fmt.Errorf("cannot read agents apiKeyFile")
	}
	key := strings.TrimSpace(string(b))
	if key == "" || strings.ContainsAny(key, " \t\r\n") {
		return nil, fmt.Errorf("agents apiKeyFile must hold one API key")
	}
	maxTokens := c.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultAgentMaxTokens
	}
	if maxTokens < 1024 || maxTokens > 128000 {
		return nil, fmt.Errorf("agents maxTokens (most output tokens per model reply) must be 1024 to 128000")
	}
	zoneName := c.Timezone
	if zoneName == "" {
		zoneName = defaultAgentTimezone
	}
	zone, err := time.LoadLocation(zoneName)
	if err != nil {
		return nil, fmt.Errorf("agents timezone must be an IANA time zone such as %s", defaultAgentTimezone)
	}
	dailyCap := int64(defaultDailyTokenCap)
	if c.DailyTokenCap != nil {
		dailyCap = *c.DailyTokenCap
	}
	if dailyCap < 0 {
		return nil, fmt.Errorf("agents dailyTokenCap must be 0 (no limit) or more")
	}
	return &agentSettings{provider: provider, app: p.app, endpoint: strings.TrimSuffix(base, "/") + "/v1/messages", model: model,
		apiKey: key, maxTokens: maxTokens, zone: zone, dailyCap: dailyCap, keyFile: c.APIKeyFile, owners: slices.Clone(c.Owners)}, nil
}
