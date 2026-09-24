package service

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
)

type Rates struct {
	WindowSeconds int `json:"windowSeconds"`
	ClientReads   int `json:"clientReads"`
	ClientWrites  int `json:"clientWrites"`
	SpaceReads    int `json:"spaceReads"`
	SpaceWrites   int `json:"spaceWrites"`
}

type Storage struct {
	MaxFileBytes        int64 `json:"maxFileBytes"`
	MaxRequestBytes     int64 `json:"maxRequestBytes"`
	MaxFiles            int   `json:"maxFiles"`
	MaxCurrentTextBytes int64 `json:"maxCurrentTextBytes"`
	MaxRepositoryBytes  int64 `json:"maxRepositoryBytes"`
}

type Limits struct {
	Rates   Rates   `json:"rates"`
	Storage Storage `json:"storage"`
}

type Global struct {
	Rates struct {
		WindowSeconds int `json:"windowSeconds"`
		Reads         int `json:"reads"`
		Writes        int `json:"writes"`
	} `json:"rates"`
	MaxQueuedWrites int `json:"maxQueuedWrites"`
}

type SpaceConfig struct {
	Visibility string          `json:"visibility"`
	Overrides  json.RawMessage `json:"overrides"`
	Limits     Limits          `json:"-"`
}

type Config struct {
	ConfigVersion string                 `json:"configVersion"`
	Defaults      Limits                 `json:"defaults"`
	Global        Global                 `json:"global"`
	Spaces        map[string]SpaceConfig `json:"spaces"`
}

type Keys struct {
	ReadHashes  []string `json:"readHashes"`
	WriteHashes []string `json:"writeHashes"`
}

var spacePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var pathPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*){0,7}$`)

func strictJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}

func validLimits(l Limits) bool {
	r, s := l.Rates, l.Storage
	return r.WindowSeconds > 0 && r.WindowSeconds <= 86400 && r.ClientReads > 0 && r.ClientWrites > 0 && r.SpaceReads > 0 && r.SpaceWrites > 0 && s.MaxFileBytes > 0 && s.MaxRequestBytes > 0 && s.MaxFiles > 0 && s.MaxCurrentTextBytes > 0 && s.MaxRepositoryBytes > 0
}

func loadConfig(path, keyPath string) (Config, map[string]Keys, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, nil, err
	}
	if err = strictJSON(b, &c); err != nil {
		return c, nil, err
	}
	if c.ConfigVersion != "0.1.0" || !validLimits(c.Defaults) || c.Global.Rates.WindowSeconds <= 0 || c.Global.Rates.WindowSeconds > 86400 || c.Global.Rates.Reads <= 0 || c.Global.Rates.Writes <= 0 || c.Global.MaxQueuedWrites <= 0 || c.Global.MaxQueuedWrites > 10000 {
		return c, nil, fmt.Errorf("invalid configuration version or limits")
	}
	keys := map[string]Keys{}
	if keyPath != "" {
		b, err = os.ReadFile(keyPath)
		if err != nil {
			return c, nil, err
		}
		if err = strictJSON(b, &keys); err != nil {
			return c, nil, err
		}
	}
	if c.Spaces["public"].Visibility != "public" {
		return c, nil, fmt.Errorf("public space is required")
	}
	for name, sc := range c.Spaces {
		if !spacePattern.MatchString(name) || (name != "public" && sc.Visibility != "private") {
			return c, nil, fmt.Errorf("invalid space configuration: %s", name)
		}
		sc.Limits = c.Defaults
		if len(sc.Overrides) > 0 {
			// Reject null: it must not silently disable or preserve a setting.
			var raw any
			if err = json.Unmarshal(sc.Overrides, &raw); err != nil || containsNull(raw) {
				return c, nil, fmt.Errorf("invalid overrides: %s", name)
			}
			if err = strictJSON(sc.Overrides, &sc.Limits); err != nil {
				return c, nil, fmt.Errorf("invalid overrides: %s: %w", name, err)
			}
		}
		if !validLimits(sc.Limits) {
			return c, nil, fmt.Errorf("invalid limits: %s", name)
		}
		c.Spaces[name] = sc
		if sc.Visibility == "private" && len(keys[name].WriteHashes) == 0 {
			return c, nil, fmt.Errorf("private space %s needs a write key digest", name)
		}
	}
	for name, k := range keys {
		if c.Spaces[name].Visibility != "private" {
			return c, nil, fmt.Errorf("keys must belong to a configured private space")
		}
		for _, h := range append(append([]string{}, k.ReadHashes...), k.WriteHashes...) {
			if !digestPattern.MatchString(h) {
				return c, nil, fmt.Errorf("invalid SHA-256 key digest for %s", name)
			}
		}
	}
	return c, keys, nil
}

func containsNull(v any) bool {
	if v == nil {
		return true
	}
	switch x := v.(type) {
	case map[string]any:
		for _, value := range x {
			if containsNull(value) {
				return true
			}
		}
	case []any:
		for _, value := range x {
			if containsNull(value) {
				return true
			}
		}
	}
	return false
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateKey returns a bearer secret and its storage digest. Save only the digest server-side.
func GenerateKey() (string, string, error) {
	key, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	h := sha256.Sum256([]byte(key))
	return key, hex.EncodeToString(h[:]), nil
}

func matchesKey(key string, hashes []string) bool {
	h := sha256.Sum256([]byte(key))
	encoded := hex.EncodeToString(h[:])
	found := 0
	for _, candidate := range hashes {
		found |= subtle.ConstantTimeCompare([]byte(encoded), []byte(candidate))
	}
	return key != "" && found == 1
}
