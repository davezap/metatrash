package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// A lock file is kept fresh while held. One whose time is older than
// lockStale was left by a process that stopped without removing it.
const lockHeartbeat = 30 * time.Second
const lockStale = 2 * time.Minute

type fileLock struct {
	path string
	stop chan struct{}
	done chan struct{}
}

// acquireLock takes the lock file at path for what (used in the error), or
// says which process holds it.
func acquireLock(path, what string) (*fileLock, error) {
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			host, _ := os.Hostname()
			fmt.Fprintf(f, "pid %d on %s\n", os.Getpid(), host)
			f.Close()
			l := &fileLock{path: path, stop: make(chan struct{}), done: make(chan struct{})}
			go l.heartbeat()
			return l, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, statErr := os.Stat(path)
		if statErr == nil && time.Since(info.ModTime()) > lockStale {
			os.Remove(path)
			continue
		}
		holder, _ := os.ReadFile(path)
		return nil, fmt.Errorf("%s is already in use by another mtsync (%s)", what, strings.TrimSpace(string(holder)))
	}
	return nil, fmt.Errorf("cannot take the lock for %s", what)
}

func (l *fileLock) heartbeat() {
	defer close(l.done)
	t := time.NewTicker(lockHeartbeat)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case now := <-t.C:
			_ = os.Chtimes(l.path, now, now)
		}
	}
}

func (l *fileLock) release() {
	close(l.stop)
	<-l.done
	os.Remove(l.path)
}
