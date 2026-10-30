package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestTextCheckAcrossWrites(t *testing.T) {
	text := []byte("héllo € 𝄞 plain")
	for cut := 0; cut <= len(text); cut++ {
		for cut2 := cut; cut2 <= len(text); cut2++ {
			c := &textCheck{ok: true}
			c.Write(text[:cut])
			c.Write(text[cut:cut2])
			c.Write(text[cut2:])
			if !c.valid() {
				t.Fatalf("split at %d, %d: not text", cut, cut2)
			}
		}
	}
	for name, b := range map[string][]byte{"NUL": []byte("a\x00b"), "bad byte": {'a', 0xff}, "cut off": []byte("€")[:2], "lone continuation": {0x80, 'a'}} {
		c := &textCheck{ok: true}
		c.Write(b)
		if c.valid() {
			t.Errorf("%s counted as text", name)
		}
	}
}

func TestSpoolUpload(t *testing.T) {
	dir := t.TempDir()
	body := make([]byte, 3<<20+7)
	rand.Read(body)
	u, err := spoolUpload(dir, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if u.size != int64(len(body)) || u.sha256 != webHash(body) || u.gitSHA != blobHash(body) || !bytes.Equal(u.head, body[:512]) || u.text {
		t.Fatalf("spooled %d %s %s text=%v", u.size, u.sha256, u.gitSHA, u.text)
	}
	if b, err := u.readAll(); err != nil || !bytes.Equal(b, body) {
		t.Fatal("file differs from the upload")
	}
	u.close()
	// A body cut off part way leaves nothing behind.
	cut := io.MultiReader(bytes.NewReader(body[:1<<20]), errReader{})
	if _, err := spoolUpload(dir, cut); err == nil {
		t.Fatal("cut-off upload spooled")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
	text, err := spoolUpload(dir, strings.NewReader("# Notes\n"))
	if err != nil || !text.text || len(text.head) != 8 {
		t.Fatalf("text upload: %+v %v", text, err)
	}
	text.close()
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestS3PutFile(t *testing.T) {
	body := []byte(strings.Repeat("content ", 1000))
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || r.Header.Get("x-amz-content-sha256") != hash || r.ContentLength != int64(len(body)) || !strings.Contains(r.Header.Get("Authorization"), "x-amz-content-sha256") {
			w.WriteHeader(400)
			return
		}
		got, _ = io.ReadAll(r.Body)
	}))
	defer srv.Close()
	store := &s3Store{endpoint: srv.URL, region: "us-west-004", bucket: "downloads", keyID: "key", secret: "secret", http: s3HTTPClient()}
	f := bytes.NewReader(body)
	f.Seek(100, io.SeekStart) // putFile starts from the beginning
	if err := store.putFile(context.Background(), "files/x/abc", "application/zip", f, int64(len(body)), hash); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("bucket got %d bytes", len(got))
	}
}

// fileObjects is a bucket that takes files by stream, keeping only their
// hashes, so a large upload costs the test no memory either.
type fileObjects struct {
	*memoryObjects
	mu     sync.Mutex
	hashes map[string]string
}

func (f *fileObjects) putFile(_ context.Context, key, _ string, body io.ReadSeeker, size int64, sha string) error {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(h, body)
	if err != nil || n != size || hex.EncodeToString(h.Sum(nil)) != sha {
		return errors.New("bad upload")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hashes[key] = sha
	return nil
}

// A large upload through PUT …/content goes to the bucket as a stream, with
// little memory; one cut off part way stores and leaves nothing.
func TestStreamedUploadAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	settings := testAgentSettings(t)
	objects := &fileObjects{memoryObjects: &memoryObjects{objects: map[string][]byte{}}, hashes: map[string]string{}}
	settings.downloads = objects
	runner, err := newAgentRunner(w.s, settings, &memoryJobStore{jobs: map[string]agentJob{}})
	if err != nil {
		t.Fatal(err)
	}
	w.s.agents.Store(runner)
	t.Cleanup(func() { w.s.agents.Store(nil) })
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "https://metatrash.com/api/v1/account")
	mcp := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "")
	state := w.tool(mcp, "list", map[string]any{"space": w.ownedName}, "")["state"].(string)
	target := "https://metatrash.com/api/v1/account/spaces/" + w.ownedName + "/content?path="
	put := func(path string, body io.Reader) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PUT", target+path+"&ifInState="+state, body)
		r.RemoteAddr = "192.0.2.12:1"
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/zip")
		res := httptest.NewRecorder()
		w.h.ServeHTTP(res, r)
		return res
	}

	// 40 MB, generated as it is read.
	const size = 40 << 20
	gen := io.LimitReader(&pattern{}, size)
	want := sha256.New()
	io.Copy(want, io.LimitReader(&pattern{}, size))
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res := put("big/archive.zip", gen)
	runtime.ReadMemStats(&after)
	if res.Code != 201 {
		t.Fatalf("upload: %d %s", res.Code, res.Body)
	}
	hash := hex.EncodeToString(want.Sum(nil))
	if objects.hashes["files/"+w.ownedID+"/"+hash] != hash {
		t.Fatalf("bucket holds %v", objects.hashes)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 16<<20 {
		t.Errorf("a 40 MB upload allocated %d MB", grew>>20)
	}
	read := w.tool(mcp, "read", map[string]any{"space": w.ownedName, "path": "big/archive.zip"}, "")
	if v := read["file"].(map[string]any)["virtual"].(map[string]any); v["sha256"] != hash || v["size"] != float64(size) || v["type"] != "application/zip" {
		t.Fatalf("pointer: %v", v)
	}
	state = read["state"].(string)

	// Cut off part way: refused, nothing stored, no file left beside the
	// repositories.
	stored := len(objects.hashes)
	if res := put("big/cut.zip", io.MultiReader(io.LimitReader(&pattern{}, 1<<20), errReader{})); res.Code != 400 {
		t.Fatalf("cut-off upload: %d", res.Code)
	}
	if len(objects.hashes) != stored {
		t.Fatal("cut-off upload stored")
	}
	left, _ := filepath.Glob(filepath.Join(w.s.ownedRoot, ".upload-*"))
	if len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// pattern is an endless, not-text stream.
type pattern struct{ n byte }

func (p *pattern) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = p.n*31 + 7
		p.n++
	}
	return len(b), nil
}
