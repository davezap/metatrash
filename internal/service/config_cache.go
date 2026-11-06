package service

import (
	"container/list"
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
)

const configCacheBytes int64 = 4 << 20
const configCacheEntries = 256

type parsedConfig struct {
	config folderConfig
	valid  bool
}

type configEntry struct {
	hash string
	parsedConfig
	bytes int64
}

// Serialize cold config batches per repository. Waiters can cancel while the
// leader reads, and recheck the LRU after admission instead of reloading blobs.
// Values are immutable inside this cache; all callers receive deep copies.
type configCache struct {
	once    sync.Once
	gate    chan struct{}
	entries map[string]*list.Element
	lru     list.List // most recently used first
	bytes   int64
}

func (c *configCache) load(ctx context.Context, hashes []string, load func(context.Context, []string) (map[string][]byte, error)) (map[string]parsedConfig, error) {
	c.once.Do(func() {
		c.gate = make(chan struct{}, 1)
		c.entries = make(map[string]*list.Element)
	})
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-c.gate }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]parsedConfig, len(hashes))
	var missing []string
	seen := make(map[string]bool, len(hashes))
	for _, hash := range hashes {
		if seen[hash] {
			continue
		}
		seen[hash] = true
		if element := c.entries[hash]; element != nil {
			entry := element.Value.(configEntry)
			c.lru.MoveToFront(element)
			out[hash] = parsedConfig{cloneFolderConfig(entry.config), entry.valid}
		} else {
			missing = append(missing, hash)
		}
	}
	if len(missing) == 0 {
		return out, nil
	}
	texts, err := load(ctx, missing)
	if err != nil {
		return nil, err // Missing objects and cancellation are never cached.
	}
	for _, hash := range missing {
		text, exists := texts[hash]
		if !exists {
			return nil, fmt.Errorf("missing configuration blob in batch")
		}
		cfg, err := parseFolderConfig(string(text))
		if err != nil {
			cfg = folderConfig{}
		}
		value := parsedConfig{cfg, err == nil}
		// Include map/slice/string overhead conservatively; this is estimated
		// retained storage, not an RSS limit. Oversized values bypass retention.
		size := int64(len(text))*8 + 4096
		if size <= configCacheBytes {
			for len(c.entries) > 0 && (len(c.entries) >= configCacheEntries || c.bytes+size > configCacheBytes) {
				last := c.lru.Back()
				entry := last.Value.(configEntry)
				c.bytes -= entry.bytes
				delete(c.entries, entry.hash)
				c.lru.Remove(last)
			}
			c.entries[hash] = c.lru.PushFront(configEntry{hash, value, size})
			c.bytes += size
		}
		out[hash] = parsedConfig{cloneFolderConfig(cfg), value.valid}
	}
	return out, nil
}

func (r *repository) cachedConfigs(ctx context.Context, hashes []string) (map[string]parsedConfig, error) {
	return r.configBlobs.load(ctx, hashes, r.blobs)
}

func cloneConfigPointer[T any](p *T) *T {
	if p == nil {
		return nil
	}
	value := *p
	return &value
}

func cloneFolderConfig(c folderConfig) folderConfig {
	c.Purpose = cloneConfigPointer(c.Purpose)
	c.Inherit = cloneConfigPointer(c.Inherit)
	c.PublishedVersion = cloneConfigPointer(c.PublishedVersion)
	c.Print = cloneConfigPointer(c.Print)
	c.Footer = cloneConfigPointer(c.Footer)
	c.Children = maps.Clone(c.Children)
	c.Locked = maps.Clone(c.Locked)
	c.ReadOnly = maps.Clone(c.ReadOnly)
	c.Services = slices.Clone(c.Services)
	for i := range c.Services {
		c.Services[i].Files = slices.Clone(c.Services[i].Files)
	}
	c.Actions = slices.Clone(c.Actions)
	for i := range c.Actions {
		c.Actions[i].On = slices.Clone(c.Actions[i].On)
		c.Actions[i].Tools = slices.Clone(c.Actions[i].Tools)
		c.Actions[i].Web = cloneConfigPointer(c.Actions[i].Web)
	}
	return c
}
