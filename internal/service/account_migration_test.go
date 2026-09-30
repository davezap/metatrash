package service

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccountsMigrationValidation(t *testing.T) {
	created, err := time.Parse(time.RFC3339Nano, "2026-09-27T13:14:15.123456789Z")
	if err != nil {
		t.Fatal(err)
	}
	user := userAccount{ID: strings.Repeat("a", 32), Email: "person@example.com", CreatedAt: created, MaxPrivateSpaces: 7}
	source, err := json.Marshal(accountFile{Version: 1, Users: map[string]userAccount{user.Email: user}})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := decodeLegacyAccounts(source)
	if err != nil || saved.Users[user.Email] != user {
		t.Fatal("migration changed identity, timestamp, or allowance")
	}
	for _, invalid := range []string{
		strings.Replace(string(source), `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(string(source), `"version":1`, `"version":2`, 1),
		strings.Replace(string(source), `"maxPrivateSpaces":7`, `"maxPrivateSpaces":-1`, 1),
		strings.Replace(string(source), user.Email, "different@example.com", 1),
		string(source) + `{}`,
	} {
		if _, err := decodeLegacyAccounts([]byte(invalid)); err == nil {
			t.Fatal("invalid migration source accepted")
		}
	}
	other := user
	other.Email = "other@example.com"
	duplicate, _ := json.Marshal(accountFile{Version: 1, Users: map[string]userAccount{user.Email: user, other.Email: other}})
	if _, err := decodeLegacyAccounts(duplicate); err == nil {
		t.Fatal("duplicate account IDs accepted")
	}
}

func TestAccountsMailReservation(t *testing.T) {
	l := limiter{buckets: map[string]bucket{}}
	global := allowance{"global", 7, 3600}
	email := allowance{"email", 1, 3600}
	if err := l.reserve(global, email); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := l.reserve(global, email); err == nil {
			t.Fatal("email limit not enforced")
		}
	}
	if l.buckets[global.key].count != 1 {
		t.Fatal("rejected requests consumed shared delivery budget")
	}
	var admitted atomic.Int32
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if l.reserve(global) == nil {
				admitted.Add(1)
			}
		}()
	}
	workers.Wait()
	if admitted.Load() != 6 || l.buckets[global.key].count != 7 {
		t.Fatal("concurrent reservations exceeded or lost the remaining budget")
	}
}
