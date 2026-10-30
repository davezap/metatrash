package service

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Streamed uploads (0.68.0): PUT …/content copies the body to a temporary
// file beside the space's repository while hashing it, then sends the file
// to the bucket, so an upload holds a few kilobytes in memory rather than the
// whole file. Only a file whose text has to be worked out from all of it at
// once (HTML) is read back into memory; Word files are read a part at a
// time and PDFs by pdftotext from the file.

// spooledUpload is an upload on disk with what storing it needs.
type spooledUpload struct {
	file           *os.File
	size           int64
	sha256, gitSHA string
	// head is the start of the content, for sniffing its type.
	head []byte
	// text is set when the content is UTF-8 without NUL: its own text.
	text bool
}

func (u *spooledUpload) close() {
	u.file.Close()
	os.Remove(u.file.Name())
}

// textCheck follows whether a stream is UTF-8 without NUL, carrying an
// incomplete character from one write to the next.
type textCheck struct {
	ok      bool
	partial []byte
}

func (c *textCheck) Write(p []byte) (int, error) {
	n := len(p)
	if !c.ok {
		return n, nil
	}
	if len(c.partial) > 0 {
		p = append(c.partial, p...)
		c.partial = nil
	}
	// Keep back a character cut off at the end.
	end := len(p)
	for i := 1; i <= utf8.UTFMax-1 && i <= len(p); i++ {
		if b := p[len(p)-i]; utf8.RuneStart(b) {
			if !utf8.FullRune(p[len(p)-i:]) {
				end = len(p) - i
			}
			break
		}
	}
	if !utf8.Valid(p[:end]) || strings.IndexByte(string(p[:end]), 0) >= 0 {
		c.ok = false
		return n, nil
	}
	c.partial = append([]byte(nil), p[end:]...)
	return n, nil
}

func (c *textCheck) valid() bool { return c.ok && len(c.partial) == 0 }

// headBuffer keeps the first 512 bytes, as http.DetectContentType reads.
type headBuffer struct{ b []byte }

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := 512 - len(h.b); room > 0 {
		h.b = append(h.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// spoolUpload copies body to a temporary file in dir. The caller limits
// body's size and closes the result. Errors reading body are returned as
// they are, so the caller can tell a body that is too large.
func spoolUpload(dir string, body io.Reader) (*spooledUpload, error) {
	f, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return nil, fmt.Errorf("cannot store the upload")
	}
	u := &spooledUpload{file: f}
	sum := sha256.New()
	head := &headBuffer{}
	text := &textCheck{ok: true}
	size, err := io.Copy(io.MultiWriter(f, sum, head, text), body)
	if err != nil {
		u.close()
		return nil, err
	}
	u.size, u.sha256, u.head, u.text = size, hex.EncodeToString(sum.Sum(nil)), head.b, text.valid()
	// Git's blob hash starts with the size, so it takes a second pass.
	blob := sha1.New()
	fmt.Fprintf(blob, "blob %d\x00", size)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		u.close()
		return nil, fmt.Errorf("cannot store the upload")
	}
	if _, err := io.Copy(blob, f); err != nil {
		u.close()
		return nil, fmt.Errorf("cannot store the upload")
	}
	u.gitSHA = hex.EncodeToString(blob.Sum(nil))
	return u, nil
}

// putVirtualUpload stores an upload and writes path's pointer, as
// putVirtual does for content in memory.
func (s *Service) putVirtualUpload(ctx context.Context, r *repository, space, path, ifInState string, u *spooledUpload, media string) (WriteResult, error) {
	if err := s.virtualUploadReady(r, path); err != nil {
		return WriteResult{}, err
	}
	store, pdfText := s.spaceFiles()
	if store == nil {
		return WriteResult{}, virtualUnavailable()
	}
	if u.size > maxVirtualBytes {
		return WriteResult{}, problem(413, "payload_too_large", fmt.Sprintf("A virtual file holds at most %d MB.", maxVirtualBytes>>20))
	}
	info := VirtualInfo{SHA256: u.sha256, Size: u.size, Type: virtualMedia(media, path, u.head), GitBlob: u.gitSHA}
	unavailable := problem(503, "unavailable", "The file could not be stored; try again later.")
	var err error
	if putter, ok := store.(objectFilePutter); ok {
		err = putter.putFile(ctx, info.key(r.id), info.Type, u.file, u.size, u.sha256)
	} else {
		var body []byte
		if body, err = u.readAll(); err == nil {
			err = store.put(ctx, info.key(r.id), info.Type, body)
		}
	}
	if err != nil {
		log.Printf("virtual file: %v", err)
		return WriteResult{}, unavailable
	}
	if !convertedTypes[info.Type] && u.text {
		info.TextSHA256 = info.SHA256
	} else if text := s.uploadText(ctx, u, info.Type, pdfText); text != "" {
		if len(text) > maxVirtualText {
			text = strings.ToValidUTF8(text[:maxVirtualText], "") + "\n\n…(the rest of the text is left out)\n"
		}
		info.TextSHA256 = webHash([]byte(text))
		if err := store.put(ctx, info.textKey(r.id), "text/markdown; charset=utf-8", []byte(text)); err != nil {
			log.Printf("virtual file: %v", err)
			return WriteResult{}, unavailable
		}
	}
	pointer := info.pointer()
	value, err := s.dispatchRepository(ctx, r, space, "write", Input{Path: path, Text: &pointer, IfInState: ifInState, Virtual: &info})
	if err != nil {
		return WriteResult{}, err
	}
	return value.(WriteResult), nil
}

// virtualUploadReady refuses an upload before its body is read, for what
// does not depend on the content.
func (s *Service) virtualUploadReady(r *repository, path string) error {
	if !validPath(path) || isFolderFile(path) {
		return invalid("A valid path for an ordinary file is required.")
	}
	if !r.owned || r.id == "" {
		return problem(403, "forbidden", "Virtual files are kept in account spaces only.")
	}
	if store, _ := s.spaceFiles(); store == nil {
		return virtualUnavailable()
	}
	return nil
}

func (u *spooledUpload) readAll() ([]byte, error) {
	if _, err := u.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(u.file)
}

// convertedTypes are the types whose text is Markdown made from them.
var convertedTypes = map[string]bool{
	"text/html": true, "application/xhtml+xml": true, "application/pdf": true,
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": true,
}

// uploadText is an upload's Markdown for HTML, Word and PDF files, as
// virtualText gives for content in memory, else "".
func (s *Service) uploadText(ctx context.Context, u *spooledUpload, media string, pdfText pdfTextFunc) string {
	switch media {
	case "text/html", "application/xhtml+xml":
		body, err := u.readAll()
		if err != nil {
			return ""
		}
		return virtualText(ctx, body, media, "", nil)
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		_, md, err := docxReaderToMarkdown(u.file, u.size)
		if err != nil {
			log.Printf("virtual file: %v", err)
		}
		return md
	case "application/pdf":
		var raw string
		var err error
		if a := s.agents.Load(); a != nil && a.pdfFile != nil {
			raw, err = a.pdfFile(ctx, u.file.Name())
		} else if pdfText != nil {
			var body []byte
			if body, err = u.readAll(); err == nil {
				raw, err = pdfText(ctx, body)
			}
		} else {
			return ""
		}
		if err != nil {
			log.Printf("virtual file: %v", err)
			return ""
		}
		return pdfMarkdown(raw)
	}
	return ""
}

// uploadDir is where a space's uploads are spooled: beside its repository,
// on the data disk (not /tmp, which may be memory).
func uploadDir(r *repository) string { return filepath.Dir(r.path) }
