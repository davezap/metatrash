package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyncBackoffBounds(t *testing.T) {
	for _, tc := range []struct {
		interval  time.Duration
		failures  int
		low, high time.Duration
	}{
		{time.Second, 1, 5 * time.Second, 10 * time.Second},
		{time.Second, 2, 10 * time.Second, 20 * time.Second},
		{time.Second, 6, time.Minute, 2 * time.Minute},
		{time.Second, 999, time.Minute, 2 * time.Minute},
		{time.Hour, 1, time.Hour, time.Hour},
	} {
		for i := 0; i < 50; i++ {
			if got := retryWait(tc.interval, tc.failures); got < tc.low || got > tc.high {
				t.Fatalf("backoff (%s,%d)=%s outside [%s,%s]", tc.interval, tc.failures, got, tc.low, tc.high)
			}
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		header string
		floor  time.Duration
	}{
		{"10", 10 * time.Second},
		{now.Add(30 * time.Second).Format(http.TimeFormat), 30 * time.Second},
		{"999999999", 24 * time.Hour},
		{"-1", 0}, {"invalid", 0}, {"0", 0},
	} {
		if got := retryAfterFloor(tc.header, now); got != tc.floor {
			t.Fatalf("Retry-After %q: %s != %s", tc.header, got, tc.floor)
		}
	}
	for attempt := 0; attempt < 25; attempt++ {
		if got := httpRetryWait("10", attempt, now); got < 10*time.Second || got > 80*time.Second {
			t.Fatalf("HTTP backoff out of bounds: %s", got)
		}
	}
}

func TestSyncWatchSaturationAdvice(t *testing.T) {
	for _, hint := range []int{5, 0} {
		t.Run(fmt.Sprintf("hint_%d", hint), func(t *testing.T) {
			var requests atomic.Int32
			state := strings.Repeat("a", 40)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				if n == 2 && (r.URL.Query().Get("after") != state || r.URL.Query().Get("since") != state) {
					t.Error("watch did not retain last observed revision")
				}
				fmt.Fprintf(w, `{"state":%q,"retryAfterSeconds":%d}`, state, hint)
			}))
			defer srv.Close()
			c := newClient(srv.URL, "public", nil, false)
			sleeps := 0
			c.sleep = func(_ context.Context, wait time.Duration) error {
				sleeps++
				floor := max(100*time.Millisecond, time.Duration(hint)*time.Second)
				if wait < floor || wait > floor+floor/4 {
					t.Errorf("watch retry=%s floor=%s", wait, floor)
				}
				return errors.New("stop test watch")
			}
			s := &syncer{c: c}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s.watchRemote(ctx, 100*time.Millisecond, make(chan struct{}, 1))
			if sleeps != 1 || requests.Load() != 2 || !s.remoteLive.Load() {
				t.Fatalf("watch requests=%d sleeps=%d live=%t", requests.Load(), sleeps, s.remoteLive.Load())
			}
		})
	}
}

func TestSyncWatchChangedAdviceDoesNotDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		if n == 1 {
			fmt.Fprintf(w, `{"state":%q}`, strings.Repeat("a", 40))
		} else if n == 2 {
			fmt.Fprintf(w, `{"state":%q,"retryAfterSeconds":5}`, strings.Repeat("b", 40))
		} else {
			cancel()
		}
	}))
	defer srv.Close()
	c := newClient(srv.URL, "public", nil, false)
	c.sleep = func(context.Context, time.Duration) error {
		t.Error("a changed revision was delayed by saturation advice")
		return context.Canceled
	}
	changed := make(chan struct{}, 1)
	s := &syncer{c: c}
	s.watchRemote(ctx, time.Second, changed)
	if len(changed) != 1 {
		t.Fatal("changed state did not wake sync loop")
	}
}

func TestSyncHTTPRetryAndCancellation(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"wait"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"state":"ready"}`))
	}))
	defer srv.Close()
	c := newClient(srv.URL, "public", nil, false)
	sleeps := 0
	c.sleep = func(_ context.Context, wait time.Duration) error {
		sleeps++
		if wait < 3*time.Second || wait > 3750*time.Millisecond {
			t.Errorf("HTTP retry ignored server floor: %s", wait)
		}
		return nil
	}
	if state, err := c.head(context.Background()); err != nil || state != "ready" || sleeps != 1 {
		t.Fatalf("HTTP retry: state=%s err=%v sleeps=%d", state, err, sleeps)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("retry sleep did not cancel: %v", err)
	}
}

func TestSyncWaitSinceCompatibility(t *testing.T) {
	var probes, requests atomic.Int32
	state := strings.Repeat("a", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Has("since") {
			probes.Add(1)
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"old server"}}`))
			return
		}
		if r.URL.Query().Get("after") != state {
			t.Error("compatibility retry lost after= long polling")
		}
		fmt.Fprintf(w, `{"state":%q}`, state)
	}))
	defer srv.Close()
	c := newClient(srv.URL, "public", nil, false)
	for i := 0; i < 2; i++ {
		if got, err := c.waitHead(context.Background(), state); err != nil || got != state {
			t.Fatalf("compatibility: %s %v", got, err)
		}
	}
	if probes.Load() != 1 || requests.Load() != 3 {
		t.Fatalf("repeated capability probes: %d/%d", probes.Load(), requests.Load())
	}
}

func TestSyncWatchFailureBackoff(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":{"code":"internal_error","message":"retry later"}}`))
	}))
	defer srv.Close()
	c := newClient(srv.URL, "public", nil, false)
	sleeps := 0
	c.sleep = func(_ context.Context, wait time.Duration) error {
		sleeps++
		if wait < 5*time.Second || wait > 10*time.Second {
			t.Errorf("initial reconnect delay: %s", wait)
		}
		return errors.New("stop test watch")
	}
	s := &syncer{c: c}
	s.watchRemote(context.Background(), time.Second, make(chan struct{}, 1))
	if sleeps != 1 || requests.Load() != 1 || s.remoteLive.Load() {
		t.Fatalf("failed watcher spun: sleeps=%d requests=%d live=%t", sleeps, requests.Load(), s.remoteLive.Load())
	}
}

func TestSyncHTTPVirtualUnavailableNotRetried(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"code":"virtual_unavailable","message":"not configured"}}`))
	}))
	defer srv.Close()
	c := newClient(srv.URL, "public", nil, false)
	c.sleep = func(context.Context, time.Duration) error {
		t.Fatal("permanent virtual-unavailable error was retried")
		return nil
	}
	if _, err := c.head(context.Background()); errorCode(err) != "virtual_unavailable" {
		t.Fatalf("permanent error changed: %v", err)
	}
}
