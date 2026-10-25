//go:build unix

package service

import (
	"fmt"
	"os"
	"os/user"
	"slices"
	"strconv"
	"syscall"
)

// readableBy reports, when run as root, whether the named account can read
// path, so metatrash check (run with sudo) catches a secret file the service
// account cannot open. It answers true when it cannot tell.
func readableBy(path, account string) (bool, error) {
	if os.Geteuid() != 0 {
		return true, nil
	}
	u, err := user.Lookup(account)
	if err != nil {
		return true, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("cannot read %s", path)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return true, nil
	}
	mode := info.Mode().Perm()
	if strconv.FormatUint(uint64(st.Uid), 10) == u.Uid {
		return mode&0o400 != 0, nil
	}
	groups, _ := u.GroupIds()
	if slices.Contains(append(groups, u.Gid), strconv.FormatUint(uint64(st.Gid), 10)) {
		return mode&0o040 != 0, nil
	}
	return mode&0o004 != 0, nil
}
