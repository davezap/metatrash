package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"metatrash.com/metatrash"
)

func TestLongPollConfiguration(t *testing.T) {
	for _, tc := range []struct{ input, want int }{{0, 256}, {1, 1}, {256, 256}, {65536, 65536}} {
		if got, err := listWaiterLimit(tc.input); err != nil || got != tc.want {
			t.Fatalf("limit %d: %d %v", tc.input, got, err)
		}
	}
	for _, input := range []int{-1, 65537} {
		if _, err := listWaiterLimit(input); err == nil {
			t.Fatalf("accepted limit %d", input)
		}
	}
	data, err := os.ReadFile("../../config/spaces.example.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{"0", "1", "65536", "-1", "65537"} {
		path := filepath.Join(t.TempDir(), "spaces.json")
		fixture := strings.Replace(string(data), `"maxListWaiters": 256`, `"maxListWaiters": `+replacement, 1)
		if fixture == string(data) {
			t.Fatal("config fixture did not replace maxListWaiters")
		}
		if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := loadConfig(path, "")
		invalid := replacement == "-1" || replacement == "65537"
		if invalid && (err == nil || !strings.Contains(err.Error(), "maxListWaiters")) || !invalid && err != nil {
			t.Fatalf("config %s: %v", replacement, err)
		}
		if replacement == "0" && cfg.Global.MaxListWaiters != 256 {
			t.Fatal("legacy zero did not retain default")
		}
	}
	path := filepath.Join(t.TempDir(), "legacy.json")
	var legacy Config
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Global.MaxListWaiters = 0
	data, err = json.Marshal(legacy)
	if err != nil || strings.Contains(string(data), "maxListWaiters") {
		t.Fatal("could not construct omitted-limit fixture")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if cfg, _, err := loadConfig(path, ""); err != nil || cfg.Global.MaxListWaiters != 256 {
		t.Fatalf("omitted limit: %d %v", cfg.Global.MaxListWaiters, err)
	}
}

func awaitLongPollSlot(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for s.listWaiters.Load() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("long poll did not reserve a slot")
		case <-tick.C:
		}
	}
}

func TestLongPollSaturationAdvice(t *testing.T) {
	s := openPublicTestService(t)
	s.config.Global.MaxListWaiters = 1
	s.listWait = time.Minute // Keep the held fixture stable until explicit cancel.
	r := s.repos["public"]
	head, err := r.head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h, err := s.Handler(metatrash.ToolSchema, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- s.waitForChange(ctx, r, head) }()
	awaitLongPollSlot(t, s)
	for _, tc := range []struct {
		query string
		retry int
	}{
		{"after=" + head, 5},
		{"after=" + head + "&since=" + head, 5},
		{"after=" + strings.Repeat("0", 40), 0},
		{"since=" + head, 0},
		{"", 0},
	} {
		req := httptest.NewRequest("GET", "/api/v1/spaces/public/files?"+tc.query, nil)
		req.RemoteAddr = "127.0.0.1:1"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var out ListResult
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.State != head || out.RetryAfterSeconds != tc.retry {
			t.Fatalf("%s: %d %s %v", tc.query, w.Code, w.Body, err)
		}
		if tc.retry > 0 && (w.Header().Get("Retry-After") != "5" || w.Header().Get("Cache-Control") != "no-store") {
			t.Fatalf("missing advisory headers: %v", w.Header())
		}
		if tc.retry == 0 && w.Header().Get("Retry-After") != "" {
			t.Fatal("ordinary/changed listing received retry advice")
		}
		if s.listWaiters.Load() != 1 {
			t.Fatal("saturated request leaked a slot")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not release held request")
	}
	if s.listWaiters.Load() != 0 {
		t.Fatal("canceled request leaked a slot")
	}
}

func TestLongPollSlotRelease(t *testing.T) {
	s := openPublicTestService(t)
	r := s.repos["public"]
	head, err := r.head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, shutdown := range []bool{false, true} {
		waiter := &Service{stop: make(chan struct{}), listWait: time.Millisecond}
		if shutdown {
			close(waiter.stop)
		}
		if retry := waiter.waitForChange(context.Background(), r, head); retry != 0 || waiter.listWaiters.Load() != 0 {
			t.Fatalf("timeout/shutdown leaked slot: retry=%d active=%d", retry, waiter.listWaiters.Load())
		}
		if retry := waiter.waitForChange(context.Background(), r, strings.Repeat("0", 40)); retry != 0 || waiter.listWaiters.Load() != 0 {
			t.Fatal("stale state leaked slot")
		}
	}
	broken := &repository{path: filepath.Join(t.TempDir(), "missing.git")}
	if retry := s.waitForChange(context.Background(), broken, head); retry != 0 || s.listWaiters.Load() != 0 {
		t.Fatal("failed HEAD probe leaked slot")
	}
}
