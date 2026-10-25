package service

import (
	"crypto/cipher"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	// Schedules and runner.md times use IANA zones; carry the zone data so a
	// server without it still works.
	_ "time/tzdata"
)

// agentsConfig is the optional "agents" object in the account configuration.
// It turns folder actions on and holds what the server needs to run them;
// the agents themselves (provider, model, API key) are each user's, set up
// in Your account → Services (agent_store.go). Actions are checked and
// stored either way; they run only while enabled is true.
//
// keyFile holds 64 hex digits (openssl rand -hex 32) that seal the users' API
// keys in the database. timezone sets runner.md times and when the daily
// caps reset.
type agentsConfig struct {
	Enabled  bool   `json:"enabled"`
	KeyFile  string `json:"keyFile"`
	Timezone string `json:"timezone"`
	// Settings of 0.54.x, when the server held one API key. They are refused
	// with a pointer to the account page rather than silently ignored.
	Owners        json.RawMessage `json:"owners"`
	Provider      json.RawMessage `json:"provider"`
	BaseURL       json.RawMessage `json:"baseURL"`
	Model         json.RawMessage `json:"model"`
	APIKeyFile    json.RawMessage `json:"apiKeyFile"`
	MaxTokens     json.RawMessage `json:"maxTokens"`
	DailyTokenCap json.RawMessage `json:"dailyTokenCap"`
}

// agentSettings are the loaded, validated settings.
type agentSettings struct {
	zone    *time.Location
	seal    cipher.AEAD
	keyFile string
}

// agentProviderInfo describes a model provider: how History names its agent
// ("MiniMax (agent) for dave"), its Messages API address and its usual model.
type agentProviderInfo struct{ name, app, baseURL, model, keysURL string }

var agentProviders = map[string]agentProviderInfo{
	"minimax":   {"MiniMax", "MiniMax", "https://api.minimax.io/anthropic", "MiniMax-M3", "https://platform.minimax.io (API keys)"},
	"anthropic": {"Anthropic", "Claude", "https://api.anthropic.com", "claude-sonnet-5-5", "https://console.anthropic.com (API keys)"},
}

// agentProviderNames lists the providers in a stable order, for forms.
func agentProviderNames() []string {
	names := make([]string, 0, len(agentProviders))
	for name := range agentProviders {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

const (
	defaultAgentMaxTokens = 16384
	defaultAgentTimezone  = "Pacific/Auckland"
	defaultDailyTokenCap  = 2000000
)

// settings checks the section and loads the key. Call it only when enabled.
func (c *agentsConfig) settings() (*agentSettings, error) {
	for name, raw := range map[string]json.RawMessage{"owners": c.Owners, "provider": c.Provider, "baseURL": c.BaseURL, "model": c.Model, "apiKeyFile": c.APIKeyFile, "maxTokens": c.MaxTokens, "dailyTokenCap": c.DailyTokenCap} {
		if raw != nil {
			return nil, fmt.Errorf("agents %s is no longer a server setting (0.55.0): each user adds their own agent and API key in Your account → Services; remove %s and add keyFile (docs/deployment.md)", name, name)
		}
	}
	if c.KeyFile == "" {
		return nil, fmt.Errorf("agents keyFile is required when agents is enabled: 64 hex digits that seal users' API keys (openssl rand -hex 32)")
	}
	seal, err := loadSealKey(c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("agents keyFile: %w", err)
	}
	zoneName := c.Timezone
	if zoneName == "" {
		zoneName = defaultAgentTimezone
	}
	zone, err := time.LoadLocation(zoneName)
	if err != nil {
		return nil, fmt.Errorf("agents timezone must be an IANA time zone such as %s", defaultAgentTimezone)
	}
	return &agentSettings{zone: zone, seal: seal, keyFile: c.KeyFile}, nil
}

// agentModel is everything one job needs to call its agent's provider.
type agentModel struct {
	agentID, name, provider, app, endpoint, model, apiKey string
	maxTokens                                             int
	dailyCap                                              int64
}

// actionModel splits an action's "model" ("minimax/MiniMax-M3") into its
// provider and model.
func actionModel(value string) (provider, model string, ok bool) {
	provider, model, ok = strings.Cut(value, "/")
	if _, known := agentProviders[provider]; !known || !ok || !agentModelPattern.MatchString(model) {
		return "", "", false
	}
	return provider, model, true
}
