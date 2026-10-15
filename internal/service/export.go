package service

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// exportManifestName is the file an export adds beside the space's files.
// Paths with a leading dot are refused at the root of a space, so it cannot
// collide with one.
const exportManifestName = ".metatrash-export.json"

// ExportManifest describes an export: which space, which state, and each
// file's ID, so a backup records what a restore would need to match.
type ExportManifest struct {
	Space     string       `json:"space"`
	State     string       `json:"state"`
	Committed string       `json:"committed"`
	Files     []ExportFile `json:"files"`
}

type ExportFile struct {
	Path  string `json:"path"`
	ID    string `json:"id"`
	Bytes int64  `json:"bytes"`
}

// exportZip returns the space at its head as a zip: every file at its path,
// dated with the head commit's time, plus exportManifestName. The whole
// archive is built before anything is sent, so a failure is still an error
// response; a space's current text is bounded by maxCurrentTextBytes.
func (r *repository) exportZip(ctx context.Context, space string) (string, []byte, error) {
	state, files, err := r.snapshot(ctx, "")
	if err != nil {
		return "", nil, err
	}
	b, err := git(ctx, r.path, "", nil, "show", "-s", "--format=%cI", state)
	if err != nil {
		return "", nil, err
	}
	committed, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil {
		return "", nil, fmt.Errorf("invalid commit time")
	}
	committed = committed.UTC()
	paths := make([]string, 0, len(files))
	hashes := make([]string, 0, len(files))
	for p, f := range files {
		paths = append(paths, p)
		hashes = append(hashes, f.Blob)
	}
	sort.Strings(paths)
	texts, err := r.blobs(ctx, hashes)
	if err != nil {
		return "", nil, err
	}
	manifest := ExportManifest{Space: space, State: state, Committed: committed.Format(time.RFC3339), Files: make([]ExportFile, 0, len(paths))}
	for _, p := range paths {
		manifest.Files = append(manifest.Files, ExportFile{Path: p, ID: files[p].ID, Bytes: files[p].Bytes})
	}
	m, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, text []byte) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: committed})
		if err != nil {
			return err
		}
		_, err = w.Write(text)
		return err
	}
	if err := add(exportManifestName, append(m, '\n')); err != nil {
		return "", nil, err
	}
	for _, p := range paths {
		if err := add(p, texts[files[p].Blob]); err != nil {
			return "", nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return "", nil, err
	}
	return state, buf.Bytes(), nil
}

// blobs reads many blobs with one git process.
func (r *repository) blobs(ctx context.Context, hashes []string) (map[string][]byte, error) {
	out := map[string][]byte{}
	var input bytes.Buffer
	for _, h := range hashes {
		if !hashPattern.MatchString(h) {
			return nil, fmt.Errorf("invalid stored blob")
		}
		if _, ok := out[h]; !ok {
			out[h] = nil
			input.WriteString(h + "\n")
		}
	}
	if len(out) == 0 {
		return out, nil
	}
	b, err := git(ctx, r.path, "", input.Bytes(), "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	rd := bufio.NewReader(bytes.NewReader(b))
	for range len(out) {
		header, err := rd.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("short blob batch")
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[1] != "blob" {
			return nil, fmt.Errorf("stored blob missing")
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("invalid blob batch")
		}
		text := make([]byte, size+1)
		if _, err := io.ReadFull(rd, text); err != nil || text[size] != '\n' {
			return nil, fmt.Errorf("short blob batch")
		}
		if _, ok := out[fields[0]]; !ok {
			return nil, fmt.Errorf("unexpected blob in batch")
		}
		out[fields[0]] = text[:size]
	}
	return out, nil
}

// ExportRepository exports the Git repository at gitDir the way the REST
// export does, for console commands that run beside the service. Reading
// needs no lock: Git never changes the objects a branch points to.
func ExportRepository(ctx context.Context, gitDir, space string) (string, []byte, error) {
	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		return "", nil, fmt.Errorf("no space repository at %s", gitDir)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return (&repository{path: gitDir}).exportZip(ctx, space)
}

// exportFileName is the suggested name for an export: owner-slug, the UTC
// date and the start of the state.
func exportFileName(space, state string, now time.Time) string {
	return strings.ReplaceAll(space, "/", "-") + "-" + now.UTC().Format("2006-01-02") + "-" + state[:12] + ".zip"
}

// ExportFileName is exportFileName for console commands.
func ExportFileName(space, state string) string { return exportFileName(space, state, time.Now()) }

// SpaceRepositoryPath is where the service in dataDir keeps a space's Git
// repository: owned spaces by ID, spaces.json spaces by name.
func SpaceRepositoryPath(dataDir string, owned bool, idOrName string) (string, error) {
	if owned {
		if !idPattern.MatchString(idOrName) {
			return "", fmt.Errorf("invalid space ID")
		}
		return filepath.Join(dataDir, "owned-repos", idOrName+".git"), nil
	}
	if !spacePattern.MatchString(idOrName) {
		return "", fmt.Errorf("invalid space name")
	}
	return filepath.Join(dataDir, "repos", idOrName+".git"), nil
}

// ConfiguredSpaces lists the spaces.json spaces with a repository in dataDir.
func ConfiguredSpaces(dataDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dataDir, "repos"))
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".git"); ok && e.IsDir() && spacePattern.MatchString(name) {
			names = append(names, name)
		}
	}
	return names, nil
}
