//go:build unix

package service

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

func TestReadableBy(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	if _, err := user.Lookup("nobody"); err != nil {
		t.Skip("no nobody account")
	}
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := readableBy(path, "nobody"); ok || err != nil {
		t.Errorf("0600 root file: %v %v", ok, err)
	}
	_ = os.Chmod(path, 0o644)
	if ok, err := readableBy(path, "nobody"); !ok || err != nil {
		t.Errorf("0644 file: %v %v", ok, err)
	}
	if ok, _ := readableBy(path, "no-such-account-here"); !ok {
		t.Error("unknown account should not fail the check")
	}
}
