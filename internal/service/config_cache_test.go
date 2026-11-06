package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConfigCacheBatchAndCopies(t *testing.T) {
	var c configCache
	calls := 0
	load := func(_ context.Context, hashes []string) (map[string][]byte, error) {
		calls++
		if !reflect.DeepEqual(hashes, []string{"a", "b", "invalid"}) {
			t.Fatalf("unexpected cold requests: %v", hashes)
		}
		return map[string][]byte{
			"a": []byte(`{"purpose":"before","readOnly":{"*.md":"agents"}}`),
			"b": []byte(`{}`), "invalid": []byte(`{"unknown":true}`),
		}, nil
	}
	first, err := c.load(context.Background(), []string{"a", "b", "a", "invalid"}, load)
	if err != nil || !first["a"].valid || !first["b"].valid || first["invalid"].valid {
		t.Fatalf("parse semantics: %#v %v", first, err)
	}
	*first["a"].config.Purpose = "mutated"
	first["a"].config.ReadOnly["*.md"] = "everyone"
	second, err := c.load(context.Background(), []string{"a", "b", "invalid"}, load)
	if err != nil || calls != 1 || *second["a"].config.Purpose != "before" || second["a"].config.ReadOnly["*.md"] != "agents" || second["invalid"].valid {
		t.Fatalf("cache isolation/hits: %#v calls=%d err=%v", second, calls, err)
	}
}

func TestConfigCacheDeepCopy(t *testing.T) {
	purpose, footer, inherit, version, web := "purpose", "footer", true, 1, true
	original := folderConfig{
		Purpose: &purpose, Footer: &footer, Inherit: &inherit, PublishedVersion: &version,
		Print: &folderPrint{Image: "image"}, Children: map[string]string{"child": "text"},
		Locked: lockMap{"purpose": "agents"}, ReadOnly: lockMap{"a": "agents"},
		Services: []folderService{{Type: "acknowledge", Files: []string{"a"}}},
		Actions:  []folderAction{{On: actionPatterns{"a"}, Tools: []string{"read"}, Web: &web}},
	}
	want, clone := cloneFolderConfig(original), cloneFolderConfig(original)
	*clone.Purpose, *clone.Footer = "changed", "changed"
	*clone.Inherit, *clone.PublishedVersion, *clone.Actions[0].Web = false, 2, false
	clone.Print.Image, clone.Children["child"] = "changed", "changed"
	clone.Locked["purpose"], clone.ReadOnly["a"] = "everyone", "everyone"
	clone.Services[0].Files[0], clone.Actions[0].On[0], clone.Actions[0].Tools[0] = "b", "b", "write"
	if !reflect.DeepEqual(original, want) {
		t.Fatal("a nested config value still aliases its copy")
	}
}

func TestConfigCacheBudgets(t *testing.T) {
	ctx := context.Background()
	var c configCache
	// Two exactly half-budget configs, followed by an LRU eviction.
	text := []byte(strings.Repeat(" ", int((configCacheBytes/2-4096)/8)-2) + "{}")
	load := func(_ context.Context, hashes []string) (map[string][]byte, error) {
		out := map[string][]byte{}
		for _, hash := range hashes {
			out[hash] = text
		}
		return out, nil
	}
	for _, hash := range []string{"a", "b", "a", "c"} {
		if _, err := c.load(ctx, []string{hash}, load); err != nil {
			t.Fatal(err)
		}
	}
	if c.bytes != configCacheBytes || len(c.entries) != 2 || c.lru.Front().Value.(configEntry).hash != "c" || c.lru.Back().Value.(configEntry).hash != "a" {
		t.Fatalf("byte budget/LRU: bytes=%d entries=%d", c.bytes, len(c.entries))
	}
	if _, err := c.load(ctx, []string{"large"}, func(context.Context, []string) (map[string][]byte, error) {
		return map[string][]byte{"large": []byte(strings.Repeat(" ", int(configCacheBytes/8)) + "{}")}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(c.entries) != 2 || c.entries["large"] != nil || c.lru.Front().Value.(configEntry).hash != "c" {
		t.Fatal("oversized config retained or evicted useful data")
	}
	var small configCache
	text = []byte("{}")
	for i := 0; i < configCacheEntries+5; i++ {
		if _, err := small.load(ctx, []string{fmt.Sprint(i)}, load); err != nil {
			t.Fatal(err)
		}
	}
	if len(small.entries) != configCacheEntries || small.bytes > configCacheBytes {
		t.Fatalf("entry bound: %d/%d", len(small.entries), small.bytes)
	}
}

func TestConfigCacheConcurrentCancelAndRetry(t *testing.T) {
	var c configCache
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	load := func(context.Context, []string) (map[string][]byte, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return map[string][]byte{"a": []byte("{}")}, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.load(context.Background(), []string{"a"}, load); err != nil {
				t.Error(err)
			}
		}()
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.load(ctx, []string{"a"}, load); !errors.Is(err, context.Canceled) {
		t.Errorf("waiting cancellation: %v", err)
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("duplicate cold config batches: %d", calls.Load())
	}
	broken := errors.New("missing object")
	if _, err := c.load(context.Background(), []string{"b"}, func(context.Context, []string) (map[string][]byte, error) {
		return nil, broken
	}); !errors.Is(err, broken) {
		t.Fatal(err)
	}
	if _, err := c.load(context.Background(), []string{"b"}, func(context.Context, []string) (map[string][]byte, error) {
		return map[string][]byte{"b": []byte("{}")}, nil
	}); err != nil {
		t.Fatalf("failed load retained: %v", err)
	}
}

func TestConfigCacheSharedBlobFolders(t *testing.T) {
	s := openPublicTestService(t)
	r := s.repos["public"]
	ctx := context.Background()
	content := []byte(`{"readOnly":{"*.md":"agents"}}`)
	object, err := git(ctx, r.path, "", content, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(object))
	files := map[string]record{
		"a/" + folderConfigName: {Blob: hash},
		"b/" + folderConfigName: {Blob: hash},
	}
	configs, err := r.folderConfigs(ctx, files)
	if err != nil {
		t.Fatal(err)
	}
	configs["a/"].ReadOnly["*.md"] = "everyone"
	if configs["b/"].ReadOnly["*.md"] != "agents" {
		t.Fatal("different folders share mutable settings")
	}
	cached, err := r.cachedConfig(ctx, hash)
	if err != nil || cached.ReadOnly["*.md"] != "agents" {
		t.Fatalf("cache changed through folder config: %v", err)
	}
}
