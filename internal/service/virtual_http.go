package service

import (
	"context"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"time"
)

// serveContent handles .../content on the account REST resource (0.60.0):
// PUT ?path=&ifInState= stores the request body as a virtual file (a new
// one, or a new version), with the Content-Type it was sent with; GET
// ?path=[&revision=] sends a file's content: a virtual file's bytes, or an
// ordinary file's text. Sync clients use them for files that are not text.
func (h *httpAdapter) serveContent(w http.ResponseWriter, r *http.Request, repo *repository, space string, upload bool) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	allowed := map[string]bool{"path": true, "revision": !upload, "ifInState": upload}
	for k, values := range q {
		if !allowed[k] || len(values) != 1 {
			err = errors.New("unknown")
		}
	}
	path := q.Get("path")
	if err != nil || !validPath(path) {
		sendError(w, invalid("Send path (and revision for GET, ifInState for PUT), each once."))
		return
	}
	// Up to 100 MB each way: past the server's read and write timeouts.
	allowLong(w, maxTransferTime)
	ctx, cancel := context.WithTimeout(r.Context(), maxTransferTime)
	defer cancel()
	if upload {
		ifInState := q.Get("ifInState")
		if !hashPattern.MatchString(ifInState) {
			sendError(w, invalid("ifInState must be a commit hash from read/list."))
			return
		}
		if err := h.service.virtualUploadReady(repo, path); err != nil {
			sendError(w, err)
			return
		}
		// The body goes to disk as it arrives (0.68.0), never all in memory.
		spooled, err := spoolUpload(uploadDir(repo), http.MaxBytesReader(w, r.Body, maxVirtualBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				sendError(w, problem(413, "payload_too_large", "A virtual file holds at most 100 MB."))
			} else {
				sendError(w, invalid("Could not read body."))
			}
			return
		}
		defer spooled.close()
		result, err := h.service.putVirtualUpload(ctx, repo, space, path, ifInState, spooled, r.Header.Get("Content-Type"))
		if err != nil {
			sendError(w, err)
			return
		}
		status := 200
		if result.Created {
			status = 201
		}
		sendJSON(w, status, result)
		return
	}
	revision := q.Get("revision")
	if _, ok := q["revision"]; ok && !hashPattern.MatchString(revision) {
		sendError(w, invalid("Invalid revision."))
		return
	}
	state, files, err := repo.snapshot(ctx, revision)
	if err != nil {
		sendError(w, err)
		return
	}
	f, ok := files[path]
	if !ok {
		sendError(w, missing())
		return
	}
	w.Header().Set("X-Metatrash-State", state)
	if f.Virtual != nil {
		// As bytes, whatever the file is: a proxy may rewrite an HTML
		// answer (Cloudflare adds its analytics script), and the client
		// checks the content against its sha256 (0.61.1).
		h.streamVirtual(w, r, repo, path, *f.Virtual, "application/octet-stream", false)
		return
	}
	body, err := repo.blob(ctx, f.Blob)
	if err != nil {
		sendError(w, err)
		return
	}
	sendDownload(w, r, pathpkg.Base(path), "text/plain; charset=utf-8", body, false)
}

// streamVirtual sends a virtual file's content as it arrives from the
// bucket (0.60.1): as an attachment, or inline for a picture a page shows.
func (h *httpAdapter) streamVirtual(w http.ResponseWriter, r *http.Request, repo *repository, path string, v VirtualInfo, kind string, inline bool) {
	if r.Method == http.MethodHead {
		downloadHeaders(w, pathpkg.Base(path), kind, v.Size, inline)
		return
	}
	// A download may take longer than a page: allow ten minutes.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	body, size, err := h.service.openVirtual(ctx, repo, v)
	if err != nil {
		sendError(w, err)
		return
	}
	defer body.Close()
	downloadHeaders(w, pathpkg.Base(path), kind, size, inline)
	if _, err := io.Copy(w, body); err != nil {
		// The headers are sent, so the client sees a short download.
		log.Printf("virtual file %s: download cut short: %v", path, err)
	}
}

// serveConfigure handles POST .../configure (0.60.2): {path, source?,
// refresh?, ifInState?, strict?} changes a virtual file's source and
// refresh schedule, or with a source makes one by fetching it.
func (h *httpAdapter) serveConfigure(w http.ResponseWriter, r *http.Request, repo *repository, space string) {
	var body struct {
		Path      string  `json:"path"`
		Source    *string `json:"source"`
		Refresh   *string `json:"refresh"`
		IfInState string  `json:"ifInState"`
		Strict    *bool   `json:"strict"`
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil || !validUnicodeJSON(raw) || strictJSON(raw, &body) != nil {
		sendError(w, invalid("Send a JSON object with path and source, refresh or both (ifInState and strict optional)."))
		return
	}
	if body.IfInState != "" && !hashPattern.MatchString(body.IfInState) {
		sendError(w, invalid("ifInState must be a commit hash from read/list."))
		return
	}
	in := Input{Path: body.Path, Source: body.Source, Refresh: body.Refresh, IfInState: body.IfInState, Strict: body.Strict == nil || *body.Strict}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result, err := h.service.configureVirtual(ctx, repo, space, in)
	if err != nil {
		sendError(w, err)
		return
	}
	status := 200
	if result.Created {
		status = 201
	}
	sendJSON(w, status, result)
}

// sendDownload sends a file's bytes so that nothing in them runs on this
// site: inline only for the pictures a page shows, else as an attachment.
func sendDownload(w http.ResponseWriter, r *http.Request, name, kind string, body []byte, inline bool) {
	downloadHeaders(w, name, kind, int64(len(body)), inline)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func downloadHeaders(w http.ResponseWriter, name, kind string, size int64, inline bool) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// no-transform: a proxy must send the file as it is.
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("Content-Type", kind)
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
}

// serveVirtual sends a virtual file from a space page: with ?raw its
// content (inline for pictures), or, for a client asking for Markdown, its
// text. The caller has checked the visitor may read the space.
func (h *httpAdapter) serveVirtual(w http.ResponseWriter, r *http.Request, repo *repository, path string, v VirtualInfo, raw bool) {
	if raw {
		h.streamVirtual(w, r, repo, path, v, v.Type, v.image())
		return
	}
	var result ReadResult
	if err := h.service.readVirtual(r.Context(), repo, path, v, &result); err != nil {
		sendError(w, err)
		return
	}
	if result.File.Text == "" {
		sendError(w, problem(404, "not_found", "This virtual file has no text."))
		return
	}
	kind := "text/plain; charset=utf-8"
	if !v.textIsContent() || isMarkdownPath(path) {
		kind = "text/markdown; charset=utf-8"
	}
	w.Header().Add("Vary", "Accept")
	sendText(w, r, kind, []byte(result.File.Text))
}

// virtualPage is the Markdown a space page shows for a virtual file: what
// it is, a download link, the picture, and its text.
func (h *httpAdapter) virtualPage(ctx context.Context, repo *repository, path, fileID string, v VirtualInfo) ([]byte, error) {
	name := pathpkg.Base(path)
	link := (&url.URL{Path: name}).EscapedPath()
	var b strings.Builder
	b.WriteString("> **Virtual file** · " + v.Type + " · " + humanBytes(v.Size) + " · [Download](" + link + "?raw)")
	if v.Source != "" {
		b.WriteString(" · [Source](<" + v.Source + ">)")
	}
	if v.Refresh != "" {
		b.WriteString(" · refreshed every " + v.Refresh)
		if since, reason := h.service.staleOf(ctx, repo, fileID); !since.IsZero() {
			b.WriteString("\n>\n> **Stale:** " + escapeInline(staleNote(since, reason)))
		}
	}
	b.WriteString("\n\n")
	if v.image() {
		b.WriteString("![" + strings.ReplaceAll(name, "]", `\]`) + "](" + link + ")\n")
		return []byte(b.String()), nil
	}
	var result ReadResult
	if err := h.service.readVirtual(ctx, repo, path, v, &result); err != nil {
		return nil, err
	}
	switch {
	case result.File.Text == "":
		b.WriteString(result.Note + "\n")
	case !v.textIsContent() || isMarkdownPath(path):
		b.WriteString(result.File.Text)
	default:
		fence := "```"
		for strings.Contains(result.File.Text, fence) {
			fence += "`"
		}
		b.WriteString(fence + "\n" + strings.TrimSuffix(result.File.Text, "\n") + "\n" + fence + "\n")
	}
	return []byte(b.String()), nil
}
