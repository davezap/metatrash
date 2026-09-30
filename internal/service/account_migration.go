package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Reject duplicate JSON object keys before unmarshalling, so an import cannot
// silently discard a conflicting account or field through last-value-wins.
func uniqueJSONKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate or invalid JSON key")
			}
			seen[name] = true
			if err := uniqueJSONKeys(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := uniqueJSONKeys(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

func decodeLegacyAccounts(b []byte) (accountFile, error) {
	var saved accountFile
	decoder := json.NewDecoder(bytes.NewReader(b))
	if err := uniqueJSONKeys(decoder); err != nil {
		return saved, fmt.Errorf("invalid account JSON or duplicate keys")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return saved, fmt.Errorf("unexpected data after account JSON")
	}
	if strictJSON(b, &saved) != nil || saved.Version != 1 || saved.Users == nil || len(saved.Users) > maxAccountRecords {
		return saved, fmt.Errorf("invalid legacy account store")
	}
	ids := map[string]bool{}
	for email, user := range saved.Users {
		if email != user.Email || !validAccount(user) || ids[user.ID] {
			return accountFile{}, fmt.Errorf("invalid or conflicting legacy account record")
		}
		ids[user.ID] = true
	}
	return saved, nil
}

// MigrateAccounts runs offline, sharing the service's data-directory lock.
// It never provisions Git repositories, changes the source, or sends mail.
func MigrateAccounts(ctx context.Context, configPath, dataDir string, empty bool) (int, error) {
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return 0, err
	}
	lock := filepath.Join(dataDir, ".service-lock")
	if err := os.Mkdir(lock, 0700); err != nil {
		return 0, fmt.Errorf("data directory locked; stop the service before migration (do not remove a live lock)")
	}
	defer os.RemoveAll(lock)
	if err := os.WriteFile(filepath.Join(lock, "pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return 0, err
	}
	sourcePath := filepath.Join(dataDir, "accounts.json")
	saved := accountFile{Version: 1, Users: map[string]userAccount{}}
	fingerprint := "empty"
	if empty {
		if _, err := os.Lstat(sourcePath); !os.IsNotExist(err) {
			return 0, fmt.Errorf("empty initialization requires no existing accounts.json; import the existing file instead")
		}
	} else {
		b, err := readLimitedFile(sourcePath, 8*1024*1024)
		if err != nil {
			return 0, fmt.Errorf("cannot read legacy accounts.json; use -empty only for an installation with no existing accounts")
		}
		saved, err = decodeLegacyAccounts(b)
		if err != nil {
			return 0, err
		}
		fingerprint = secretDigest(string(b))
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	store, err := openAccountDatabase(ctx, configPath)
	if err != nil {
		return 0, err
	}
	defer store.Close()
	return store.importAccounts(ctx, saved, fingerprint)
}

func (s *accountDatabase) importAccounts(ctx context.Context, saved accountFile, fingerprint string) (int, error) {
	if err := s.checkEngines(ctx); err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("cannot begin account migration")
	}
	defer tx.Rollback()
	previous, err := lockAccountMeta(ctx, tx)
	if err != nil {
		return 0, err
	}
	if previous != "" && previous != fingerprint {
		return 0, fmt.Errorf("database was initialized from a different source; refusing to merge or overwrite accounts")
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_users").Scan(&count); err != nil {
		return 0, fmt.Errorf("cannot inspect migration destination")
	}
	if previous == "" && count != 0 {
		return 0, fmt.Errorf("initial migration requires an empty users table; refusing to overwrite existing records")
	}
	for _, user := range saved.Users {
		if previous != "" {
			existing, err := scanAccount(tx.QueryRowContext(ctx, "SELECT user_id, email, created_at, max_private_spaces FROM metatrash_users WHERE user_id = ?", user.ID))
			if err != nil || existing.Email != user.Email || !existing.CreatedAt.Equal(user.CreatedAt) || existing.MaxPrivateSpaces != user.MaxPrivateSpaces {
				return 0, fmt.Errorf("imported account differs from source; no records changed")
			}
			continue
		}
		if err := insertAccount(ctx, tx, user); err != nil {
			return 0, fmt.Errorf("account import failed; transaction rolled back")
		}
	}
	if previous == "" {
		if _, err := tx.ExecContext(ctx, "UPDATE metatrash_account_meta SET migration_source = ? WHERE singleton_id = 1", fingerprint); err != nil {
			return 0, fmt.Errorf("cannot mark migration complete")
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("migration commit could not be confirmed; safely rerun with the same source")
	}
	return len(saved.Users), nil
}
