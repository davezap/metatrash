package service

import (
	"log"
	"sync"
	"time"
)

// listCheckMetrics deliberately measures only HEAD probes, excluding any
// long-poll wait and the subsequent diff work. Counters are process totals,
// reset on restart. No tenant, path, state or credential is logged.
type listCheckMetrics struct {
	mu        sync.Mutex
	lastLog   time.Time
	checks    uint64
	unchanged uint64
	errors    uint64
	headTime  time.Duration
}

func (m *listCheckMetrics) record(elapsed time.Duration, unchanged, failed bool) {
	m.mu.Lock()
	m.checks++
	m.headTime += elapsed
	if unchanged {
		m.unchanged++
	}
	if failed {
		m.errors++
	}
	now := time.Now()
	if m.lastLog.IsZero() {
		m.lastLog = now
	}
	if now.Sub(m.lastLog) < time.Minute {
		m.mu.Unlock()
		return
	}
	m.lastLog = now
	checks, same, failures, headTime := m.checks, m.unchanged, m.errors, m.headTime
	m.mu.Unlock()
	log.Printf("list_change_checks totals checks=%d unchanged=%d fallback=%d head_errors=%d head_ns=%d", checks, same, checks-same-failures, failures, headTime.Nanoseconds())
}
