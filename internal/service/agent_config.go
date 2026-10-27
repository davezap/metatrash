package service

import (
	"crypto/cipher"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
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
// keys in the database. timezone is the time zone of spaces that have not
// chosen one (schedules, runner.md times) and sets when the daily caps reset.
// search and downloads turn on the web tools (0.57.0, web_tools.go); without
// them actions run without web search or fetch.
type agentsConfig struct {
	Enabled   bool                `json:"enabled"`
	KeyFile   string              `json:"keyFile"`
	Timezone  string              `json:"timezone"`
	Search    *webSearchConfig    `json:"search"`
	Downloads *webDownloadsConfig `json:"downloads"`
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

// webSearchConfig is agents.search: the web search provider for
// web_search. Brave is the only one so far.
type webSearchConfig struct {
	Provider string `json:"provider"`
	KeyFile  string `json:"keyFile"`
	// DailyLimit is the most searches each payer's jobs may make in a day
	// (default 200).
	DailyLimit int `json:"dailyLimit"`
}

// webDownloadsConfig is agents.downloads: the S3-compatible bucket (a
// Backblaze B2 bucket of its own, with a lifecycle rule that deletes files
// 30 days after upload) where web_fetch keeps what it downloads.
type webDownloadsConfig struct {
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	Bucket   string `json:"bucket"`
	KeyID    string `json:"keyID"`
	KeyFile  string `json:"keyFile"`
	// DailyLimit is the most fetches each payer's jobs may make in a day
	// (default 500).
	DailyLimit int `json:"dailyLimit"`
}

// agentSettings are the loaded, validated settings.
type agentSettings struct {
	zone    *time.Location
	seal    cipher.AEAD
	keyFile string
	// search and downloads are nil when the web tools are not set up.
	search                  webSearcher
	downloads               objectStore
	searchLimit, fetchLimit int
	// The key files, for metatrash check.
	searchKeyFile, downloadsKeyFile string
	// pdftotext is the program that reads PDFs' text (0.59.0), "" when it is
	// not installed.
	pdftotext string
}

const (
	defaultSearchDailyLimit = 200
	defaultFetchDailyLimit  = 500
)

var (
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	regionPattern = regexp.MustCompile(`^[a-z0-9-]{2,40}$`)
	b2HostPattern = regexp.MustCompile(`^s3\.([a-z0-9-]+)\.backblazeb2\.com$`)
)

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
	settings := &agentSettings{zone: zone, seal: seal, keyFile: c.KeyFile}
	if c.Search != nil {
		if c.Search.Provider != "brave" {
			return nil, fmt.Errorf(`agents search provider must be "brave"`)
		}
		key, err := readLimitedFile(c.Search.KeyFile, 4096)
		if err != nil || strings.TrimSpace(string(key)) == "" {
			return nil, fmt.Errorf("agents search keyFile must hold the Brave Search API key")
		}
		settings.search, settings.searchKeyFile = newBraveSearch(strings.TrimSpace(string(key))), c.Search.KeyFile
		if settings.searchLimit = c.Search.DailyLimit; settings.searchLimit == 0 {
			settings.searchLimit = defaultSearchDailyLimit
		}
		if settings.searchLimit < 1 || settings.searchLimit > 100000 {
			return nil, fmt.Errorf("agents search dailyLimit must be 1 to 100000")
		}
	}
	if d := c.Downloads; d != nil {
		u, err := url.Parse(d.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
			return nil, fmt.Errorf("agents downloads endpoint must be the bucket's S3 address, such as https://s3.us-west-004.backblazeb2.com")
		}
		region := d.Region
		if m := b2HostPattern.FindStringSubmatch(u.Host); region == "" && m != nil {
			region = m[1]
		}
		if !regionPattern.MatchString(region) {
			return nil, fmt.Errorf("agents downloads region is required (such as us-west-004) unless the endpoint is a Backblaze B2 address")
		}
		if !bucketPattern.MatchString(d.Bucket) {
			return nil, fmt.Errorf("agents downloads bucket must be a bucket name")
		}
		if d.KeyID == "" || len(d.KeyID) > 128 || strings.ContainsAny(d.KeyID, " /\t\n") {
			return nil, fmt.Errorf("agents downloads keyID must be the application key's ID")
		}
		secret, err := readLimitedFile(d.KeyFile, 4096)
		if err != nil || strings.TrimSpace(string(secret)) == "" {
			return nil, fmt.Errorf("agents downloads keyFile must hold the application key")
		}
		settings.downloadsKeyFile = d.KeyFile
		settings.pdftotext = findPDFToText()
		settings.downloads = &s3Store{endpoint: "https://" + u.Host, region: region, bucket: d.Bucket, keyID: d.KeyID, secret: strings.TrimSpace(string(secret)), http: s3HTTPClient()}
		if settings.fetchLimit = d.DailyLimit; settings.fetchLimit == 0 {
			settings.fetchLimit = defaultFetchDailyLimit
		}
		if settings.fetchLimit < 1 || settings.fetchLimit > 100000 {
			return nil, fmt.Errorf("agents downloads dailyLimit must be 1 to 100000")
		}
	}
	return settings, nil
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
