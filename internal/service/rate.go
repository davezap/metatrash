package service

import (
	"sync"
	"time"
)

type bucket struct {
	count int
	until int64
}
type allowance struct {
	key           string
	limit, window int
}
type limiter struct {
	mu      sync.Mutex
	buckets map[string]bucket
}

func (l *limiter) take(rules ...allowance) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().Unix()
	if len(l.buckets) >= 8192 {
		for k, b := range l.buckets {
			if b.until <= now {
				delete(l.buckets, k)
			}
		}
	}
	wait := 0
	for _, rule := range rules {
		b, exists := l.buckets[rule.key]
		if !exists && len(l.buckets) >= 16384 {
			if wait < 1 {
				wait = 1
			}
			continue
		}
		if b.until <= now {
			b = bucket{until: (now/int64(rule.window) + 1) * int64(rule.window)}
		}
		if b.count < rule.limit {
			b.count++
		} else if left := int(b.until - now); left > wait {
			wait = left
		}
		l.buckets[rule.key] = b
	}
	if wait > 0 {
		return &Error{Status: 429, Code: "rate_limited", Message: "Rate limit reached.", RetryAfterSeconds: wait}
	}
	return nil
}

// reserve charges all delivery rules atomically, or none. Admitted failures
// (including SMTP failures) keep their reservation; never refund concurrently.
func (l *limiter) reserve(rules ...allowance) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().Unix()
	if len(l.buckets) >= 8192 {
		for key, b := range l.buckets {
			if b.until <= now {
				delete(l.buckets, key)
			}
		}
	}
	pending := make(map[string]bucket, len(rules))
	wait, additional := 0, 0
	for _, rule := range rules {
		b, staged := pending[rule.key]
		if !staged {
			var exists bool
			b, exists = l.buckets[rule.key]
			if !exists {
				additional++
			}
		}
		if b.until <= now {
			b = bucket{until: (now/int64(rule.window) + 1) * int64(rule.window)}
		}
		if b.count >= rule.limit {
			if left := int(b.until - now); left > wait {
				wait = left
			}
		}
		b.count++
		pending[rule.key] = b
	}
	if len(l.buckets)+additional > 16384 && wait < 1 {
		wait = 1
	}
	if wait > 0 {
		return &Error{Status: 429, Code: "rate_limited", Message: "Rate limit reached.", RetryAfterSeconds: wait}
	}
	for key, b := range pending {
		l.buckets[key] = b
	}
	return nil
}
