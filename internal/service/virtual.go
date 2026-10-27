package service

import (
	"context"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Virtual files (0.60.0): content kept outside Git, in the space-files
// bucket, that appears in a space as a normal file. Git holds a small UTF-8
// pointer at the file's own path, as Git LFS does:
//
//	metatrash-virtual 1
//	sha256 <hex of the content>
//	size <bytes>
//	type <media type>
//	text <hex of its text>   (optional: the content itself for text, its Markdown for PDF, Word and HTML)
//	source <url>             (optional: where it was fetched from)
//
// The space's index marks the file virtual when Metatrash writes the
// pointer, so a pointer-looking text from anywhere else (a GitHub pull, an
// export) stays an ordinary file. Because the pointer is an ordinary Git
// file, history, moves, deletes, locks and folder-action triggers work
// unchanged, and each pointer revision is a version of the content. Content
// writes over a pointer are refused (virtual_file); uploading again, or
// web_fetch to the path, makes a new version.
//
// Objects are stored once per space, by hash, under files/<space ID>/, in the
// bucket of agents.downloads, outside the web/ prefix its 30-day lifecycle
// rule covers. Keying by space means a pointer resolves only in the space
// that made it.

const (
	virtualHeader      = "metatrash-virtual 1"
	maxVirtualBytes    = 100 << 20 // one file
	maxVirtualPerSpace = 5 << 30   // the current files of one space
	maxVirtualText     = 4 << 20   // the text kept for a file
)

// VirtualInfo describes a virtual file's content.
type VirtualInfo struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Type   string `json:"type"`
	// TextSHA256 names the object holding the file's text: the content for
	// text, its Markdown for a PDF, Word or HTML file; "" when it has none.
	TextSHA256 string `json:"textSha256,omitempty"`
	Source     string `json:"source,omitempty"`
}

// pointer is the text Git holds for the file.
func (v VirtualInfo) pointer() string {
	var b strings.Builder
	b.WriteString(virtualHeader + "\n")
	b.WriteString("sha256 " + v.SHA256 + "\n")
	b.WriteString("size " + strconv.FormatInt(v.Size, 10) + "\n")
	b.WriteString("type " + v.Type + "\n")
	if v.TextSHA256 != "" {
		b.WriteString("text " + v.TextSHA256 + "\n")
	}
	if v.Source != "" {
		b.WriteString("source " + v.Source + "\n")
	}
	return b.String()
}

// looksVirtual reports text that would read as a pointer.
func looksVirtual(text string) bool {
	return strings.HasPrefix(text, "metatrash-virtual ")
}

func (v VirtualInfo) key(spaceID string) string     { return "files/" + spaceID + "/" + v.SHA256 }
func (v VirtualInfo) textKey(spaceID string) string { return "files/" + spaceID + "/" + v.TextSHA256 }

// textIsContent reports whether the content itself is the text.
func (v VirtualInfo) textIsContent() bool { return v.TextSHA256 == v.SHA256 }

// image reports a picture a page or a model can show.
func (v VirtualInfo) image() bool {
	_, ok := virtualImageTypes[v.Type]
	return ok
}

var virtualImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

func virtualRefused(path string) *Error {
	return problem(409, "virtual_file", path+" is a virtual file: its content is kept outside Git, so it cannot be written, edited or appended to. Upload or fetch it again to replace it, move or delete it, or write the text to another path.")
}

func pointerRefused() *Error {
	return problem(400, "virtual_file", "Text starting with \"metatrash-virtual \" is a virtual file's pointer, which only Metatrash writes. Upload the file or fetch it with web_fetch's path instead.")
}

// spaceFiles is the bucket virtual files live in, and the PDF reader; nil
// when the server has no agents.downloads bucket.
func (s *Service) spaceFiles() (objectStore, pdfTextFunc) {
	a := s.agents.Load()
	if a == nil || a.settings == nil || a.settings.downloads == nil {
		return nil, nil
	}
	return a.settings.downloads, a.pdfText
}

func virtualUnavailable() *Error {
	return problem(503, "virtual_unavailable", "This server does not keep virtual files: it needs the agents.downloads bucket (docs/deployment.md).")
}

// virtualMedia is the media type a virtual file is kept as: the one given,
// without parameters, else one guessed from the name and the bytes.
func virtualMedia(given, path string, body []byte) string {
	media, _, err := mime.ParseMediaType(given)
	if err != nil || media == "" || media == "application/octet-stream" || media == "binary/octet-stream" {
		media = ""
		switch strings.ToLower(pathpkg.Ext(path)) {
		case ".pdf":
			media = "application/pdf"
		case ".docx":
			media = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		case ".md", ".markdown":
			media = "text/markdown"
		}
		if media == "" {
			media, _, _ = mime.ParseMediaType(http.DetectContentType(body))
		}
	}
	return strings.ToLower(media)
}

// virtualText is a file's text: the content when it is UTF-8 text, else its
// Markdown for HTML, Word and PDF files (PDF only with pdftotext), else "".
func virtualText(ctx context.Context, body []byte, media, source string, pdfText pdfTextFunc) string {
	switch {
	case media == "text/html" || media == "application/xhtml+xml":
		base, err := url.Parse(source)
		if err != nil || source == "" {
			base = &url.URL{Scheme: "https", Host: "example.invalid"}
		}
		_, md := htmlToMarkdown(decodeWebText(body), base)
		return md
	case media == "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		_, md, err := docxToMarkdown(body)
		if err != nil {
			log.Printf("virtual file: %v", err)
		}
		return md
	case media == "application/pdf":
		if pdfText == nil {
			return ""
		}
		raw, err := pdfText(ctx, body)
		if err != nil {
			log.Printf("virtual file: %v", err)
			return ""
		}
		return pdfMarkdown(raw)
	}
	if utf8.Valid(body) && !strings.ContainsRune(string(body), 0) {
		return string(body)
	}
	return ""
}

// putVirtual stores content and writes path's pointer: a new virtual file,
// or a new version of one. ifInState works as for write.
func (s *Service) putVirtual(ctx context.Context, r *repository, space, path, ifInState string, body []byte, media, source string) (WriteResult, error) {
	if !r.owned || r.id == "" {
		return WriteResult{}, problem(403, "forbidden", "Virtual files are kept in account spaces only.")
	}
	store, pdfText := s.spaceFiles()
	if store == nil {
		return WriteResult{}, virtualUnavailable()
	}
	if !validPath(path) || isFolderFile(path) {
		return WriteResult{}, invalid("A valid path for an ordinary file is required.")
	}
	if len(body) > maxVirtualBytes {
		return WriteResult{}, problem(413, "payload_too_large", fmt.Sprintf("A virtual file holds at most %d MB.", maxVirtualBytes>>20))
	}
	info := VirtualInfo{SHA256: webHash(body), Size: int64(len(body)), Type: virtualMedia(media, path, body), Source: source}
	if err := store.put(ctx, info.key(r.id), info.Type, body); err != nil {
		log.Printf("virtual file: %v", err)
		return WriteResult{}, problem(503, "unavailable", "The file could not be stored; try again later.")
	}
	if text := virtualText(ctx, body, info.Type, source, pdfText); text == string(body) {
		info.TextSHA256 = info.SHA256
	} else if text != "" {
		if len(text) > maxVirtualText {
			text = strings.ToValidUTF8(text[:maxVirtualText], "") + "\n\n…(the rest of the text is left out)\n"
		}
		info.TextSHA256 = webHash([]byte(text))
		if err := store.put(ctx, info.textKey(r.id), "text/markdown; charset=utf-8", []byte(text)); err != nil {
			log.Printf("virtual file: %v", err)
			return WriteResult{}, problem(503, "unavailable", "The file could not be stored; try again later.")
		}
	}
	pointer := info.pointer()
	value, err := s.dispatchRepository(ctx, r, space, "write", Input{Path: path, Text: &pointer, IfInState: ifInState, Virtual: &info})
	if err != nil {
		return WriteResult{}, err
	}
	return value.(WriteResult), nil
}

// virtualSpaceBytes adds up the content of a space's current virtual files,
// leaving out path.
func virtualSpaceBytes(files map[string]record, path string) int64 {
	var total int64
	for p, f := range files {
		if f.Virtual != nil && p != path {
			total += f.Virtual.Size
		}
	}
	return total
}

// readVirtual fills a read of a virtual file with its text, or says why it
// has none.
func (s *Service) readVirtual(ctx context.Context, r *repository, path string, v VirtualInfo, result *ReadResult) error {
	if v.TextSHA256 == "" {
		result.Note = fmt.Sprintf("%s is a virtual file (%s, %s) with no text to read: its content is kept outside Git.", path, v.Type, humanBytes(v.Size))
		if v.Type == "application/pdf" {
			result.Note += " A PDF has text only when the server can read it (pdftotext) and it is not scanned pages."
		}
		return nil
	}
	store, _ := s.spaceFiles()
	if store == nil {
		return virtualUnavailable()
	}
	b, err := store.get(ctx, v.textKey(r.id))
	if err != nil {
		log.Printf("virtual file %s: %v", path, err)
		return problem(503, "unavailable", path+" is a virtual file whose content could not be fetched; try again later.")
	}
	result.File.Text = string(b)
	if !v.textIsContent() {
		result.Note = fmt.Sprintf("%s is a virtual %s file (%s); this is its text as Markdown.", path, v.Type, humanBytes(v.Size))
	}
	return nil
}

// virtualContent is a virtual file's content.
func (s *Service) virtualContent(ctx context.Context, r *repository, v VirtualInfo) ([]byte, error) {
	store, _ := s.spaceFiles()
	if store == nil {
		return nil, virtualUnavailable()
	}
	b, err := store.get(ctx, v.key(r.id))
	if err != nil {
		log.Printf("virtual file: %v", err)
		return nil, problem(503, "unavailable", "The file's content could not be fetched; try again later.")
	}
	return b, nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
