package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// serviceTick is how often the service rereads the registry, writes its
// status and looks for a stop or sync request.
var serviceTick = 3 * time.Second

// workerRetry is the wait before a folder that stopped with an error (signed
// out, space no longer connected, folder in use) is tried again.
var workerRetry = time.Minute

// runService syncs every registered folder in one process until ctx ends or
// mtsync stop asks it to. Folders added or removed with mtsync add and
// remove are picked up within a few seconds.
func runService(ctx context.Context, interval time.Duration, logOut io.Writer) error {
	lockPath, err := configFile("mtsync-service.lock")
	if err != nil {
		return err
	}
	lock, err := acquireLock(lockPath, "the mtsync service")
	if err != nil {
		return err
	}
	defer lock.release()
	stopPath, _ := configFile("mtsync-service.stop")
	os.Remove(stopPath)
	syncPath, _ := configFile("mtsync-service.sync")
	os.Remove(syncPath)
	logger := log.New(logOut, "", log.LstdFlags)
	logger.Printf("mtsync service started")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	status := &statusWriter{board: statusBoard{Folders: map[string]folderStatus{}}}
	tokens := map[string]func(context.Context, bool) (string, error){}
	workers := map[string]*worker{}
	defer func() {
		for _, w := range workers {
			w.stop()
		}
		status.write()
		logger.Printf("mtsync service stopped")
	}()
	regPath, _ := configFile("sync-folders.json")
	var regTime time.Time
	reload := func() {
		r, err := loadRegistry()
		if err != nil {
			logger.Printf("cannot read the folder list: %v", err)
			return
		}
		want := map[string]folderEntry{}
		for _, e := range r.Folders {
			want[e.Folder] = e
		}
		for folder, w := range workers {
			if e, ok := want[folder]; !ok || e != w.entry {
				w.stop()
				delete(workers, folder)
				status.drop(folder)
				logger.Printf("[%s] stopped syncing %s", w.entry.Space, folder)
			}
		}
		for folder, e := range want {
			if workers[folder] != nil {
				continue
			}
			if tokens[e.Server] == nil {
				tokens[e.Server] = tokenSource(e.Server)
			}
			workers[folder] = startWorker(ctx, e, tokens[e.Server], interval, logger, status)
		}
	}
	tick := time.NewTicker(serviceTick)
	defer tick.Stop()
	for first := true; ; first = false {
		if info, err := os.Stat(regPath); first || (err == nil && !info.ModTime().Equal(regTime)) {
			if err == nil {
				regTime = info.ModTime()
			}
			reload()
		}
		if _, err := os.Stat(stopPath); err == nil {
			os.Remove(stopPath)
			logger.Printf("stop requested")
			return nil
		}
		if b, err := os.ReadFile(syncPath); err == nil {
			os.Remove(syncPath)
			folder := strings.TrimSpace(string(b))
			for f, w := range workers {
				if folder == "" || samePath(f, folder) {
					w.entry.logf(logger)("sync requested")
					w.nudge()
				}
			}
		}
		status.write()
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

type worker struct {
	entry  folderEntry
	cancel context.CancelFunc
	done   chan struct{}
	// poke asks the folder for a pass at once, or to stop waiting to try
	// again after an error.
	poke chan struct{}
}

// nudge asks the worker's folder for a pass now (mtsync sync).
func (w *worker) nudge() {
	select {
	case w.poke <- struct{}{}:
	default:
	}
}

// logf is how a folder's lines start in the service log: [owner/slug].
func (e folderEntry) logf(logger *log.Logger) func(string, ...any) {
	return func(format string, args ...any) { logger.Printf("[%s] "+format, append([]any{e.Space}, args...)...) }
}

func (w *worker) stop() {
	w.cancel()
	<-w.done
}

// startWorker syncs one folder, starting again after workerRetry when it
// stops with an error.
func startWorker(parent context.Context, e folderEntry, token func(context.Context, bool) (string, error), interval time.Duration, logger *log.Logger, status *statusWriter) *worker {
	ctx, cancel := context.WithCancel(parent)
	w := &worker{entry: e, cancel: cancel, done: make(chan struct{}), poke: make(chan struct{}, 1)}
	logf := e.logf(logger)
	go func() {
		defer close(w.done)
		for {
			err := syncFolder(ctx, e, token, interval, logf, status, w.poke)
			if ctx.Err() != nil {
				return
			}
			status.set(e, err)
			logf("stopped: %v; trying again in %s", err, workerRetry)
			select {
			case <-ctx.Done():
				return
			case <-w.poke:
			case <-time.After(workerRetry):
			}
		}
	}()
	return w
}

func syncFolder(ctx context.Context, e folderEntry, token func(context.Context, bool) (string, error), interval time.Duration, logf func(string, ...any), status *statusWriter, poke <-chan struct{}) error {
	c := folderClient(e, token)
	s, err := openSyncer(c, e.Folder, e.Server, e.Space, logf)
	if err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	s.onPass = func(err error) { status.set(e, err) }
	s.poke = poke
	err = s.loop(ctx, interval, false)
	if err == nil && ctx.Err() == nil {
		err = fmt.Errorf("sync ended")
	}
	return err
}

// folderClient reaches a registered folder's space over the sign-in routes;
// tests replace it.
var folderClient = func(e folderEntry, token func(context.Context, bool) (string, error)) *client {
	return newClient(e.Server, e.Space, token, true)
}

// statusWriter collects folder states and writes sync-status.json for
// mtsync list and status.
type statusWriter struct {
	mu    sync.Mutex
	board statusBoard
}

func (s *statusWriter) set(e folderEntry, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.board.Folders[e.Folder]
	st.Space = e.Space
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Error, st.LastSync = "", time.Now().UTC()
	}
	s.board.Folders[e.Folder] = st
}

func (s *statusWriter) drop(folder string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.board.Folders, folder)
}

func (s *statusWriter) write() {
	s.mu.Lock()
	s.board.Updated = time.Now().UTC()
	b, _ := json.MarshalIndent(s.board, "", "  ")
	s.mu.Unlock()
	if path, err := configFile("sync-status.json"); err == nil {
		_ = writeFileAtomic(path, b, 0600)
	}
}

// requestStop asks a running service to stop at its next tick and waits for
// it to go.
func requestStop(ctx context.Context, out io.Writer) error {
	if !serviceRunning() {
		fmt.Fprintln(out, "The service is not running.")
		return nil
	}
	stopPath, err := configFile("mtsync-service.stop")
	if err != nil {
		return err
	}
	if err := os.WriteFile(stopPath, nil, 0600); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for serviceRunning() && time.Now().Before(deadline) {
		if sleepContext(ctx, 200*time.Millisecond) != nil {
			return ctx.Err()
		}
	}
	if serviceRunning() {
		return fmt.Errorf("the service did not stop within 30 seconds")
	}
	fmt.Fprintln(out, "The service stopped. Login starts it again if it is installed; mtsync uninstall removes that.")
	return nil
}

// requestSync asks the running service for a pass now, in every folder or
// in the one registered folder named by its path or space, and waits for
// the service to take the request (it looks every few seconds).
func requestSync(ctx context.Context, target string, out io.Writer) error {
	if !serviceRunning() {
		return fmt.Errorf("the service is not running; start it with mtsync start")
	}
	folder := ""
	if target != "" {
		r, err := loadRegistry()
		if err != nil {
			return err
		}
		abs, _ := filepath.Abs(target)
		for _, e := range r.Folders {
			if samePath(e.Folder, abs) || e.Space == target {
				if folder != "" {
					return fmt.Errorf("several folders sync %s; name the folder", target)
				}
				folder = e.Folder
			}
		}
		if folder == "" {
			return fmt.Errorf("%s is not registered; mtsync list shows what is", target)
		}
	}
	syncPath, err := configFile("mtsync-service.sync")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(syncPath, []byte(folder), 0600); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(syncPath); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) || !serviceRunning() {
			os.Remove(syncPath)
			return fmt.Errorf("the service did not take the request; mtsync log may say why")
		}
		if sleepContext(ctx, 100*time.Millisecond) != nil {
			return ctx.Err()
		}
	}
	if folder == "" {
		fmt.Fprintln(out, "Syncing every folder now. mtsync list shows when each was checked.")
	} else {
		fmt.Fprintf(out, "Syncing %s now. mtsync list shows when it was checked.\n", folder)
	}
	return nil
}

// rotatingLog appends to path, moving it to path.1 past maxBytes.
type rotatingLog struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	size     int64
}

func openLog(name string) (*rotatingLog, error) {
	path, err := configFile(name)
	if err != nil {
		return nil, err
	}
	l := &rotatingLog{path: path, maxBytes: 5 << 20}
	return l, l.open()
}

func (l *rotatingLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	info, _ := f.Stat()
	l.f, l.size = f, info.Size()
	return nil
}

func (l *rotatingLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.size+int64(len(b)) > l.maxBytes {
		l.f.Close()
		_ = os.Rename(l.path, l.path+".1")
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(b)
	l.size += int64(n)
	return n, err
}

// printLogTail prints the last lines of the service log.
func printLogTail(out io.Writer, lines int) error {
	path, err := configFile("mtsync.log")
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		fmt.Fprintln(out, "No service log yet.")
		return nil
	}
	if err != nil {
		return err
	}
	start := len(b)
	for n := 0; start > 0 && n <= lines; {
		start--
		if b[start] == '\n' {
			n++
		}
	}
	if start > 0 {
		start++
	}
	fmt.Fprintf(out, "%s (last %d lines)\n", filepath.Base(path), lines)
	_, err = out.Write(b[start:])
	return err
}
