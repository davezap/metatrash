package service

import (
	"context"
	"errors"
	"io"
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
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if upload {
		ifInState := q.Get("ifInState")
		if !hashPattern.MatchString(ifInState) {
			sendError(w, invalid("ifInState must be a commit hash from read/list."))
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxVirtualBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				sendError(w, problem(413, "payload_too_large", "A virtual file holds at most 100 MB."))
			} else {
				sendError(w, invalid("Could not read body."))
			}
			return
		}
		result, err := h.service.putVirtual(ctx, repo, space, path, ifInState, body, r.Header.Get("Content-Type"), "")
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
	kind := "text/plain; charset=utf-8"
	var body []byte
	if f.Virtual != nil {
		kind = f.Virtual.Type
		body, err = h.service.virtualContent(ctx, repo, *f.Virtual)
	} else {
		body, err = repo.blob(ctx, f.Blob)
	}
	if err != nil {
		sendError(w, err)
		return
	}
	w.Header().Set("X-Metatrash-State", state)
	sendDownload(w, r, pathpkg.Base(path), kind, body, false)
}

// sendDownload sends a file's bytes so that nothing in them runs on this
// site: inline only for the pictures a page shows, else as an attachment.
func sendDownload(w http.ResponseWriter, r *http.Request, name, kind string, body []byte, inline bool) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", kind)
	disposition := "attachment"
	if inline {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// serveVirtual sends a virtual file from a space page: with ?raw its
// content (inline for pictures), or, for a client asking for Markdown, its
// text. The caller has checked the visitor may read the space.
func (h *httpAdapter) serveVirtual(w http.ResponseWriter, r *http.Request, repo *repository, path string, v VirtualInfo, raw bool) {
	if raw {
		body, err := h.service.virtualContent(r.Context(), repo, v)
		if err != nil {
			sendError(w, err)
			return
		}
		sendDownload(w, r, pathpkg.Base(path), v.Type, body, v.image())
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
func (h *httpAdapter) virtualPage(ctx context.Context, repo *repository, path string, v VirtualInfo) ([]byte, error) {
	name := pathpkg.Base(path)
	link := (&url.URL{Path: name}).EscapedPath()
	var b strings.Builder
	b.WriteString("> **Virtual file** · " + v.Type + " · " + humanBytes(v.Size) + " · [Download](" + link + "?raw)")
	if v.Source != "" {
		b.WriteString(" · [Source](<" + v.Source + ">)")
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
