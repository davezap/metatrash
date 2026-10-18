package service

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"time"
)

// RefreshREADME replaces the protected root README.md of the repository at
// gitDir with readme (the built-in template) when they differ, keeping the
// file's ID and history: one commit with a write entry, as an agent's write
// would make. Agents cannot change README.md, so this is the console's way
// to bring an existing space up to date. It may run beside the service: the
// branch moves only if nothing else committed meanwhile. It returns the
// space state afterwards and whether README.md changed.
func RefreshREADME(ctx context.Context, gitDir string, readme []byte) (string, bool, error) {
	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		return "", false, fmt.Errorf("no space repository at %s", gitDir)
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// The console does not know the space's limits; the template replaces
	// the old text whatever they are.
	r := &repository{path: gitDir, limits: Storage{MaxFiles: math.MaxInt, MaxCurrentTextBytes: math.MaxInt64, MaxRepositoryBytes: math.MaxInt64}}
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return "", false, err
	}
	f, ok := files["README.md"]
	if !ok {
		return "", false, fmt.Errorf("the space has no README.md")
	}
	current, err := r.blob(ctx, f.Blob)
	if err != nil {
		return "", false, err
	}
	if bytes.Equal(current, readme) {
		return head, false, nil
	}
	f.Bytes = int64(len(readme))
	files["README.md"] = f
	text := string(readme)
	state, err := r.commit(ctx, head, files, "README.md", f.ID, &text, "write")
	if err != nil {
		return "", false, err
	}
	return state, true, nil
}
