package service

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const blobBatchObjects = 64

// blobs reads deduplicated objects in bounded, request-scoped Git batches.
// Stdout is parsed as it arrives rather than retaining both the entire wire
// response and its decoded copies. Callers still own the requested content map.
func (r *repository) blobs(ctx context.Context, hashes []string) (map[string][]byte, error) {
	out := make(map[string][]byte)
	unique := make([]string, 0, len(hashes))
	for _, hash := range hashes {
		if !hashPattern.MatchString(hash) {
			return nil, fmt.Errorf("invalid stored blob")
		}
		if _, exists := out[hash]; !exists {
			unique = append(unique, hash)
			out[hash] = nil
		}
	}
	for start := 0; start < len(unique); start += blobBatchObjects {
		batch := unique[start:min(start+blobBatchObjects, len(unique))]
		texts, err := r.blobBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		for hash, text := range texts {
			out[hash] = text
		}
	}
	return out, nil
}

// A generous fallback also admits index blobs for console/sweep callers without
// Storage settings. Larger configured text/file limits expand the read ceiling.
// This is a per-object wire guard, not a cache or whole-operation memory budget.
func (r *repository) batchObjectLimit() int64 {
	return max(128<<20, r.limits.MaxFileBytes, r.limits.MaxCurrentTextBytes)
}

func (r *repository) blobBatch(ctx context.Context, hashes []string) (map[string][]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := gitCommand(ctx, r.path, "", "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(strings.Join(hashes, "\n") + "\n")
	var stderr gitBatchError
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out, readErr := readBlobBatch(stdout, hashes, r.batchObjectLimit())
	if err := ctx.Err(); err != nil {
		readErr = err
	}
	if readErr != nil {
		cancel() // Kill and reap the child even on malformed/oversized output.
	}
	waitErr := cmd.Wait()
	if readErr != nil {
		return nil, fmt.Errorf("git cat-file batch: %w", readErr)
	}
	if waitErr != nil {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("git cat-file batch: %w", err)
		}
		return nil, fmt.Errorf("git cat-file batch: %w (%s)", waitErr, stderr.String())
	}
	return out, nil
}

func readBlobBatch(input io.Reader, hashes []string, limit int64) (map[string][]byte, error) {
	rd := bufio.NewReader(input)
	out := make(map[string][]byte, len(hashes))
	for _, hash := range hashes {
		// ReadSlice bounds the header at the reader's buffer size.
		header, err := rd.ReadSlice('\n')
		if err != nil {
			return nil, fmt.Errorf("invalid blob batch header: %w", err)
		}
		fields := strings.Fields(string(header))
		if len(fields) != 3 || fields[0] != hash || fields[1] != "blob" {
			return nil, fmt.Errorf("missing or unexpected blob in batch")
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || size < 0 || size > limit {
			return nil, fmt.Errorf("invalid or oversized blob batch body")
		}
		var body bytes.Buffer
		// Grow only for bytes actually received, not an untrusted declared size.
		if _, err := io.CopyN(&body, rd, size); err != nil {
			return nil, fmt.Errorf("short blob batch body: %w", err)
		}
		if terminator, err := rd.ReadByte(); err != nil || terminator != '\n' {
			return nil, fmt.Errorf("invalid blob batch terminator")
		}
		out[hash] = body.Bytes()
	}
	if _, err := rd.ReadByte(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing blob batch data")
	}
	return out, nil
}

// A failing child cannot fill memory through stderr while stdout is parsed.
type gitBatchError struct{ buffer bytes.Buffer }

func (b *gitBatchError) String() string { return b.buffer.String() }

func (b *gitBatchError) Write(p []byte) (int, error) {
	if remaining := 8192 - b.buffer.Len(); remaining > 0 {
		_, _ = b.buffer.Write(p[:min(remaining, len(p))])
	}
	return len(p), nil
}
