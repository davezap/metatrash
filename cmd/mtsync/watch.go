package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"
)

// loop syncs until ctx ends. A pass runs when the space changes (the server
// tells a waiting request at once) or when a scan of the folder, every
// interval, finds a local change; a quiet folder costs no requests beyond
// the waiting one. A pass interrupted by a change in the space runs again at
// once.
func (s *syncer) loop(ctx context.Context, interval time.Duration, once bool) error {
	if once {
		return s.syncOnce(ctx, interval)
	}
	s.logf("syncing %s with %s", s.st.Space, s.root)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	remote := make(chan struct{}, 1)
	go s.watchRemote(ctx, interval, remote)
	dirty, failures := true, 0
	var lastScan map[string]string
	for {
		if !dirty {
			local, unstable, err := s.scan()
			dirty = err != nil || len(unstable) > 0 || !maps.Equal(local, lastScan)
		}
		wait := interval
		if dirty {
			err := s.cycle(ctx)
			if ctx.Err() != nil {
				return nil
			}
			if s.onPass != nil && !errors.Is(err, errRestart) {
				s.onPass(err)
			}
			if stop, err := s.fatal(err); stop {
				return err
			}
			switch {
			case errors.Is(err, errRestart):
				wait = 0
			case err != nil:
				failures++
				wait = interval * time.Duration(min(failures, 12))
				s.logf("sync failed, retrying in %s: %v", wait, err)
			default:
				failures = 0
				// A pass that left something for later keeps State empty.
				dirty = s.st.State == ""
				lastScan, _, _ = s.scan()
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-remote:
			dirty = true
		case <-time.After(wait):
		}
	}
}

// syncOnce runs one complete pass, retrying a pass the space interrupted.
func (s *syncer) syncOnce(ctx context.Context, interval time.Duration) error {
	for failures := 0; ; {
		err := s.cycle(ctx)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		if stop, err := s.fatal(err); stop {
			return err
		}
		if !errors.Is(err, errRestart) {
			if failures++; failures > 3 {
				return err
			}
			s.logf("sync failed, retrying in %s: %v", interval, err)
			if sleepContext(ctx, interval) != nil {
				return nil
			}
		}
	}
}

// fatal reports errors that retrying cannot fix.
func (s *syncer) fatal(err error) (bool, error) {
	if errors.Is(err, errSignedOut) {
		return true, err
	}
	var e *apiError
	if errors.As(err, &e) && (e.Code == "not_found" || e.Code == "forbidden") {
		return true, fmt.Errorf("%s: %v. Run mtsync spaces to see the spaces this computer may use", s.st.Space, err)
	}
	return false, nil
}

// watchRemote keeps one request waiting on the server for the space to move
// past the last state it saw, and signals changed when it does. Against a
// server without after= (before 0.34.0) it checks every interval instead.
func (s *syncer) watchRemote(ctx context.Context, interval time.Duration, changed chan<- struct{}) {
	signal := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	after, polling, failures := "", false, 0
	for ctx.Err() == nil {
		start := time.Now()
		wait := after
		if polling {
			wait = ""
		}
		state, err := s.c.waitHead(ctx, wait)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errorCode(err) == "invalid_request" && wait != "" {
				polling = true
				s.logf("this server does not announce changes; checking every %s", interval)
				continue
			}
			failures++
			if sleepContext(ctx, interval*time.Duration(min(failures, 12))) != nil {
				return
			}
			continue
		}
		failures = 0
		if after != "" && state != after {
			signal()
		}
		unchanged := state == after
		after = state
		// Polling, or a server that answered without waiting (too busy to
		// hold the request), is checked again after the interval.
		if polling || (unchanged && time.Since(start) < time.Second) {
			if sleepContext(ctx, interval) != nil {
				return
			}
		}
	}
}
