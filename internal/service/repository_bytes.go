package service

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// repositoryBytes is quota authority, independent of SQL-seeded usage reporting.
// mu excludes commits and reconciliation, including direct provisioning calls.
// Nothing survives reopening a repository: crashes always require a fresh walk.
type repositoryBytes struct {
	mu       sync.Mutex
	valid    bool
	fast     bool
	bytes    int64
	measured time.Time
}

func (r *repository) measureRepositoryBytes() (int64, error) {
	r.quota.valid = false
	size, err := dirBytes(r.path)
	if err != nil {
		return 0, err
	}
	r.quota.bytes, r.quota.measured = size, time.Now()
	r.quota.fast = plainRepository(r.path)
	r.quota.valid = true
	return size, nil
}

// Only the ordinary files-backend bare repository created by provision is
// eligible. Unknown config (including includes, extensions, reflog settings and
// alternate ref backends) retains the old full-walk admission path. Git's
// update-ref then only replaces refs/heads/main (41 bytes), with a transient
// same-sized lock. Existing reflogs can be written even without enabling them
// in config, so the presence of logs also disables incremental accounting.
// External writers/maintenance must stop the service and reopen the handle.
func plainRepository(path string) bool {
	if _, err := os.Lstat(filepath.Join(path, "logs")); !os.IsNotExist(err) {
		return false
	}
	// update-ref follows symbolic refs. They are not part of Core's layout.
	refPath := filepath.Join(path, "refs", "heads", "main")
	refInfo, err := os.Lstat(refPath)
	if err == nil && (!refInfo.Mode().IsRegular() || refInfo.Size() != 41) {
		return false
	}
	if err != nil && !os.IsNotExist(err) {
		return false
	}
	ref, err := os.ReadFile(refPath)
	if err == nil {
		if len(ref) != 41 || ref[40] != '\n' || !hashPattern.MatchString(string(ref[:40])) {
			return false
		}
	} else if !os.IsNotExist(err) {
		return false
	}
	config := filepath.Join(path, "config")
	info, err := os.Lstat(config)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return false
	}
	b, err := os.ReadFile(config)
	if err != nil {
		return false
	}
	core := false
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if line == "[core]" && !core {
			core = true
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !core || !ok || seen[key] {
			return false
		}
		seen[key] = true
		switch key {
		case "repositoryformatversion":
			if value != "0" {
				return false
			}
		case "bare":
			if value != "true" {
				return false
			}
		case "filemode", "symlinks", "ignorecase", "precomposeunicode":
			// Worktree/platform probes made by git init do not affect refs.
			if value != "true" && value != "false" {
				return false
			}
		default:
			return false
		}
	}
	return seen["repositoryformatversion"] && seen["bare"]
}

func repositoryRefBytes(path string) (int64, error) {
	i, err := os.Lstat(filepath.Join(path, "refs", "heads", "main"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !i.Mode().IsRegular() {
		return 0, fmt.Errorf("invalid repository reference")
	}
	return i.Size(), nil
}

func quotaFits(size, stage, limit int64) bool {
	return size >= 0 && stage >= 0 && limit >= 4096 && size <= limit-4096 && stage <= limit-4096-size
}

// Called with quota.mu held. Even configured/public repositories without a
// storage meter reconcile daily on their next write. A conservative refusal
// receives at most one fresh walk before returning the existing quota error.
func (r *repository) admitRepositoryBytes(stage int64) (int64, error) {
	measured := false
	if !r.quota.valid || !r.quota.fast || time.Since(r.quota.measured) >= storageMeasureEvery {
		if _, err := r.measureRepositoryBytes(); err != nil {
			return 0, err
		}
		measured = true
	}
	if !quotaFits(r.quota.bytes, stage, r.limits.MaxRepositoryBytes) && !measured {
		if _, err := r.measureRepositoryBytes(); err != nil {
			return 0, err
		}
	}
	if !quotaFits(r.quota.bytes, stage, r.limits.MaxRepositoryBytes) {
		return 0, storageLimit()
	}
	size := r.quota.bytes
	// Admission proved this addition fits in a positive int64 limit.
	r.quota.bytes += stage + 4096
	return size, nil
}

// checkedBytes keeps the filesystem oracle safe on malformed/oversized input.
func checkedBytes(total, size int64) (int64, error) {
	if size < 0 || total < 0 || total > math.MaxInt64-size {
		return 0, fmt.Errorf("repository size overflow")
	}
	return total + size, nil
}
