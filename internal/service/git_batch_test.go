package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestGitBatchProtocol(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	wire := func(hash, text string) string { return fmt.Sprintf("%s blob %d\n%s\n", hash, len(text), text) }
	for _, tc := range []struct {
		name, input string
		hashes      []string
		limit       int64
		want        map[string][]byte
	}{
		{"empty", "", nil, 10, map[string][]byte{}},
		{"binary and empty", wire(a, "a\x00\nb") + wire(b, ""), []string{a, b}, 4, map[string][]byte{a: []byte("a\x00\nb"), b: nil}},
		{"missing", a + " missing\n", []string{a}, 10, nil},
		{"wrong type", a + " tree 0\n\n", []string{a}, 10, nil},
		{"wrong order", wire(b, "ok") + wire(a, "ok"), []string{a, b}, 10, nil},
		{"duplicate response", wire(a, "ok") + wire(a, "ok"), []string{a, b}, 10, nil},
		{"short header", a + " blob 0", []string{a}, 10, nil},
		{"long header", strings.Repeat("x", 5000) + "\n", []string{a}, 10, nil},
		{"negative size", a + " blob -1\n", []string{a}, 10, nil},
		{"overflow", a + " blob 999999999999999999999999\n", []string{a}, 10, nil},
		{"over limit", a + " blob 11\n", []string{a}, 10, nil},
		{"short body", a + " blob 4\nx", []string{a}, 10, nil},
		{"bad terminator", a + " blob 1\nxy", []string{a}, 10, nil},
		{"missing terminator", a + " blob 1\nx", []string{a}, 10, nil},
		{"trailing data", wire(a, "ok") + "extra", []string{a}, 10, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := readBlobBatch(strings.NewReader(tc.input), tc.hashes, tc.limit)
			if tc.want == nil {
				if err == nil {
					t.Fatal("accepted invalid batch")
				}
				return
			}
			if err != nil || len(out) != len(tc.want) {
				t.Fatalf("batch: %v, err=%v", out, err)
			}
			for hash, want := range tc.want {
				if got, ok := out[hash]; !ok || !bytes.Equal(got, want) {
					t.Fatalf("%s: %q != %q", hash, got, want)
				}
			}
		})
	}
}

func TestGitBatchIgnoreSelection(t *testing.T) {
	s := openPublicTestService(t)
	r := s.repos["public"]
	ctx := context.Background()
	store := func(text string) string {
		t.Helper()
		out, err := git(ctx, r.path, "", []byte(text), "hash-object", "-w", "--stdin")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	files := map[string]record{
		"site/.gitignore":     {Blob: store("*.log\n")},
		"site/sub/.gitignore": {Blob: store("!keep.log\n")},
		"site/a.log":          {}, "site/sub/a.log": {}, "site/sub/keep.log": {},
		"site/a.md": {}, "site/.metatrash.json": {},
		// No object outside the GitHub folder may be requested.
		"other/.gitignore": {Blob: "invalid"},
	}
	send, skip, err := r.repoFiles(ctx, files, "site/")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(send, []string{".gitignore", "a.md", "sub/.gitignore", "sub/keep.log"}) || !reflect.DeepEqual(skip, []string{".metatrash.json", "a.log", "sub/a.log"}) {
		t.Fatalf("batched ignore selection: send=%v skip=%v", send, skip)
	}
	files["site/sub/.gitignore"] = record{Blob: strings.Repeat("0", 40)}
	if _, _, err := r.repoFiles(ctx, files, "site/"); err == nil {
		t.Fatal("missing ignore blob did not stop selection")
	}
}

func TestGitBatchStderrBound(t *testing.T) {
	var stderr gitBatchError
	n, err := io.Copy(&stderr, strings.NewReader(strings.Repeat("e", 1<<20)))
	if err != nil || n != 1<<20 || len(stderr.String()) != 8192 {
		t.Fatalf("stderr capture: %d/%d %v", n, len(stderr.String()), err)
	}
}

func TestGitBatchRepository(t *testing.T) {
	s := openPublicTestService(t)
	r := s.repos["public"]
	ctx := context.Background()
	state, files, err := r.snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	texts := map[string]string{}
	// More than one wire batch, including an empty object and repeated bodies.
	for i := 0; i < blobBatchObjects+5; i++ {
		path := fmt.Sprintf("batch-%03d.md", i)
		text := strings.Repeat("x", i)
		files[path] = record{File: File{ID: fmt.Sprintf("%032x", i+1), Path: path, Bytes: int64(len(text))}}
		texts[path] = text
	}
	if _, err := r.commitFiles(ctx, state, files, texts, nil, nil, "batch seed"); err != nil {
		t.Fatal(err)
	}
	var hashes []string
	for path := range texts {
		hashes = append(hashes, files[path].Blob, files[path].Blob)
	}
	objects, err := r.blobs(ctx, hashes)
	if err != nil || len(objects) != len(texts) {
		t.Fatalf("deduplicated batches: %d/%d %v", len(objects), len(texts), err)
	}
	for path, text := range texts {
		if string(objects[files[path].Blob]) != text {
			t.Fatalf("wrong content for %s", path)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.blobs(canceled, hashes); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled batch: %v", err)
	}
	if _, err := r.blobs(ctx, []string{strings.Repeat("0", 40)}); err == nil {
		t.Fatal("accepted missing object")
	}
	if _, err := r.blobs(ctx, []string{"main"}); err == nil {
		t.Fatal("accepted a non-hash object name")
	}
	tree, err := git(ctx, r.path, "", nil, "rev-parse", branch+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.blobs(ctx, []string{strings.TrimSpace(string(tree))}); err == nil {
		t.Fatal("accepted a tree as a blob")
	}
	// A failed/canceled child must not poison the next request.
	if _, err := r.blobs(ctx, hashes[:1]); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkGitBatchReads(b *testing.B) {
	ctx := context.Background()
	r := &repository{path: filepath.Join(b.TempDir(), "batch.git")}
	if _, err := git(ctx, "", "", nil, "init", "--bare", "--object-format=sha1", r.path); err != nil {
		b.Fatal(err)
	}
	var hashes []string
	for i := 0; i < 128; i++ {
		text := fmt.Sprintf("%08d", i) + strings.Repeat("x", 4088)
		out, err := git(ctx, r.path, "", []byte(text), "hash-object", "-w", "--stdin")
		if err != nil {
			b.Fatal(err)
		}
		hashes = append(hashes, strings.TrimSpace(string(out)))
	}
	for _, count := range []int{1, 16, 128} {
		for _, batch := range []bool{false, true} {
			b.Run(fmt.Sprintf("objects_%d/batch_%t", count, batch), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(count * 4096))
				for i := 0; i < b.N; i++ {
					if batch {
						if _, err := r.blobs(ctx, hashes[:count]); err != nil {
							b.Fatal(err)
						}
					} else {
						for _, hash := range hashes[:count] {
							if _, err := r.blob(ctx, hash); err != nil {
								b.Fatal(err)
							}
						}
					}
				}
			})
		}
	}
}
