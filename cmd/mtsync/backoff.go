package main

import (
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func jitterBetween(low, high time.Duration) time.Duration {
	if high <= low {
		return low
	}
	return low + time.Duration(rand.Int64N(int64(high-low)+1))
}

// retryWait spreads reconnects with bounded exponential backoff. Preserve at
// least the requested interval and five seconds; normal intervals cap at two
// minutes. Large configured intervals keep their own minimum.
func retryWait(interval time.Duration, failures int) time.Duration {
	base := max(interval, 5*time.Second)
	ceiling := max(base, 2*time.Minute)
	high := base
	for i := 0; i < min(max(failures, 1), 6); i++ {
		if high >= ceiling/2 {
			high = ceiling
			break
		}
		high *= 2
	}
	return jitterBetween(max(base, high/2), high)
}

// retryAfterFloor accepts both HTTP Retry-After forms. An excessive/untrusted
// delay is bounded at 24 hours; request cancellation remains interruptible.
func retryAfterFloor(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		return time.Duration(min(seconds, 86400)) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return min(when.Sub(now), 24*time.Hour)
	}
	return 0
}

// HTTP retries retain the server's floor and add positive jitter. A missing
// Retry-After uses exponential spacing rather than synchronized one-second loops.
func httpRetryWait(value string, attempt int, now time.Time) time.Duration {
	base := time.Second << min(max(attempt, 0), 6)
	base = max(base, retryAfterFloor(value, now))
	return jitterBetween(base, min(24*time.Hour, base+base/4))
}

func pollRetryWait(interval, hint time.Duration) time.Duration {
	base := max(interval, hint)
	return jitterBetween(base, base+base/4)
}
