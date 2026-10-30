package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// folderEntry is one registered folder: the service keeps it in sync with
// space on server.
type folderEntry struct {
	Space  string `json:"space"`
	Folder string `json:"folder"`
	Server string `json:"server"`
}

type registry struct {
	Folders []folderEntry `json:"folders"`
}

// configDir is mtsync's folder in the user's config directory: credentials,
// the registry, service status, lock and log.
func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "metatrash"), nil
}

func configFile(name string) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func loadRegistry() (registry, error) {
	var r registry
	path, err := configFile("sync-folders.json")
	if err != nil {
		return r, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s is damaged: %v", path, err)
	}
	return r, nil
}

func saveRegistry(r registry) error {
	path, err := configFile("sync-folders.json")
	if err != nil {
		return err
	}
	sort.Slice(r.Folders, func(i, j int) bool { return r.Folders[i].Folder < r.Folders[j].Folder })
	b, _ := json.MarshalIndent(r, "", "  ")
	return writeFileAtomic(path, b, 0600)
}

// samePath compares folders as the file system does (Windows ignores case).
func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// within reports whether folder is inside parent (or is parent).
func within(folder, parent string) bool {
	rel, err := filepath.Rel(parent, folder)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		if r2, err := filepath.Rel(strings.ToLower(parent), strings.ToLower(folder)); err == nil {
			rel = r2
		}
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// addFolder registers folder for space after checking that this computer can
// reach the space and that no registered folder overlaps it.
func addFolder(ctx context.Context, server, space, folder string, out io.Writer) error {
	folder, err := filepath.Abs(folder)
	if err != nil {
		return err
	}
	r, err := loadRegistry()
	if err != nil {
		return err
	}
	for _, e := range r.Folders {
		same := within(folder, e.Folder) && within(e.Folder, folder)
		switch {
		case same && e.Space == space && e.Server == server:
			fmt.Fprintf(out, "%s already syncs %s; nothing to add.\n", folder, space)
			return nil
		case same:
			return fmt.Errorf("%s already syncs %s; run mtsync remove %q first to sync it with %s instead", folder, e.Space, folder, space)
		case within(folder, e.Folder) || within(e.Folder, folder):
			return fmt.Errorf("%s overlaps %s, which already syncs %s", folder, e.Folder, e.Space)
		}
	}
	c := newClient(server, space, tokenSource(server), true)
	if _, err := c.head(ctx); err != nil {
		if errorCode(err) == "not_found" {
			return fmt.Errorf("%s is not connected to this computer. Run mtsync spaces to see the ones it may use, or mtsync login to choose again", space)
		}
		return err
	}
	if err := os.MkdirAll(folder, 0755); err != nil {
		return err
	}
	// Refuses a folder that already syncs another space.
	if _, err := openSyncer(c, folder, server, space, func(string, ...any) {}); err != nil {
		return err
	}
	r.Folders = append(r.Folders, folderEntry{Space: space, Folder: folder, Server: server})
	if err := saveRegistry(r); err != nil {
		return err
	}
	fmt.Fprintf(out, "Added %s -> %s.\n", space, folder)
	if serviceRunning() {
		fmt.Fprintln(out, "The running service starts syncing it within a few seconds.")
	} else {
		fmt.Fprintln(out, "The service is not running: start it with mtsync start, or mtsync install to start it at every login too.")
	}
	return nil
}

// removeFolder unregisters a folder, named by its path or by its space when
// only one folder syncs that space. Files stay where they are.
func removeFolder(target string, out io.Writer) error {
	r, err := loadRegistry()
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(target)
	match := -1
	for i, e := range r.Folders {
		if samePath(e.Folder, abs) || e.Space == target {
			if match >= 0 {
				return fmt.Errorf("several folders sync %s; name the folder", target)
			}
			match = i
		}
	}
	if match < 0 {
		return fmt.Errorf("%s is not registered; mtsync list shows what is", target)
	}
	e := r.Folders[match]
	r.Folders = append(r.Folders[:match], r.Folders[match+1:]...)
	if err := saveRegistry(r); err != nil {
		return err
	}
	fmt.Fprintf(out, "Removed %s -> %s. Its files stay in the folder; delete %s to forget its sync state.\n", e.Space, e.Folder, filepath.Join(e.Folder, stateDir))
	return nil
}

// listFolders prints the registered folders and how each is doing.
func listFolders(asJSON bool, out io.Writer) error {
	r, err := loadRegistry()
	if err != nil {
		return err
	}
	board := readStatus()
	running := serviceRunning()
	if asJSON {
		type folder struct {
			Folder   string `json:"folder"`
			Space    string `json:"space"`
			Server   string `json:"server"`
			State    string `json:"state"`
			LastSync string `json:"lastSync,omitempty"`
			Error    string `json:"error,omitempty"`
		}
		list := []folder{}
		for _, e := range r.Folders {
			f := folder{Folder: e.Folder, Space: e.Space, Server: e.Server, State: "stopped"}
			if running {
				f.State = "starting"
				if st, ok := board.Folders[e.Folder]; ok {
					switch {
					case st.Error != "":
						f.State, f.Error = "error", st.Error
					case !st.LastSync.IsZero():
						f.State = "in-sync"
					}
					if !st.LastSync.IsZero() {
						f.LastSync = st.LastSync.UTC().Format(time.RFC3339)
					}
				}
			}
			list = append(list, f)
		}
		return printJSON(out, map[string]any{"serviceRunning": running, "folders": list})
	}
	if len(r.Folders) == 0 {
		fmt.Fprintln(out, "No folders registered. Add one with mtsync add SPACE FOLDER.")
		return nil
	}
	for _, e := range r.Folders {
		state := "service not running"
		if running {
			state = "starting"
			if st, ok := board.Folders[e.Folder]; ok {
				switch {
				case st.Error != "":
					state = "error: " + st.Error
				case !st.LastSync.IsZero():
					state = "in sync, checked " + st.LastSync.Local().Format("Jan 2 15:04:05")
				}
			}
		}
		fmt.Fprintf(out, "%s\n  %s  %s\n", e.Folder, e.Space, state)
		if e.Server != defaultServer {
			fmt.Fprintf(out, "  on %s\n", e.Server)
		}
	}
	return nil
}

// folderStatus is what the service last saw of one folder.
type folderStatus struct {
	Space    string    `json:"space"`
	LastSync time.Time `json:"lastSync,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// statusBoard is written by the service to sync-status.json; Updated also
// tells whether the service is still running.
type statusBoard struct {
	Updated time.Time               `json:"updated"`
	Folders map[string]folderStatus `json:"folders"`
}

func readStatus() statusBoard {
	board := statusBoard{Folders: map[string]folderStatus{}}
	path, err := configFile("sync-status.json")
	if err != nil {
		return board
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &board)
	}
	if board.Folders == nil {
		board.Folders = map[string]folderStatus{}
	}
	return board
}

// serviceRunning reports a fresh service lock.
func serviceRunning() bool {
	path, err := configFile("mtsync-service.lock")
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && time.Since(info.ModTime()) <= lockStale
}
