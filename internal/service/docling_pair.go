package service

import (
	"context"
	"log"
	"net/url"
	"path"
	"strings"
	"time"
)

// Pairing (0.95.8): once an uploaded PDF (or Word, PowerPoint or Excel
// file) has Docling Markdown, the server
// puts an editable page in its place, as Slate Viewer's Convert to Markdown
// does: report.pdf moves to assets/report.pdf beside it (a move, so its ID
// and history go with it) and report.md is written where it was, starting
// with a link to the original. After that report.md is an ordinary file: a
// later Docling never rewrites it, and the person may delete the original.
//
// Only uploads are paired (David, 2026-11-07); files stored before keep
// their place unless someone converts them. An upload stays where it is
// when:
//   - it is in an assets/ folder (an original kept for a page);
//   - report.md or assets/report.pdf already exists;
//   - it refreshes from a web source (the page would go out of date);
//   - it is in a folder that mirrors GitHub (pairing would push a move);
//   - it changed or moved before the conversion finished;
//   - the space's locks or limits refuse the move or the write.

// doclingPairLink starts a paired page; Slate Viewer 0.28.0 writes the same
// for a PDF.
func doclingPairLink(name, label string) string {
	return "> [Open original " + label + "](assets/" + url.PathEscape(name) + ")\n\n"
}

// enqueueUpload converts an uploaded file and pairs it at path once
// converted.
func (q *doclingQueue) enqueueUpload(r *repository, path string, v VirtualInfo) {
	if !q.wants(r, v) {
		return
	}
	if q.settings.pair && v.Refresh == "" {
		key := q.key(r, v)
		q.mu.Lock()
		if len(q.pairWant[key]) < 16 {
			q.pairWant[key] = append(q.pairWant[key], path)
		}
		q.mu.Unlock()
	}
	q.enqueue(r, v)
}

// pairLater pairs the uploaded paths in the background.
func (q *doclingQueue) pairLater(r *repository, v VirtualInfo, paths []string) {
	q.mu.Lock()
	base := q.ctx
	if base == nil || base.Err() != nil {
		q.mu.Unlock()
		return
	}
	q.wg.Add(1)
	q.mu.Unlock()
	go func() {
		defer q.wg.Done()
		ctx, cancel := context.WithTimeout(base, 2*time.Minute)
		defer cancel()
		n, err := q.pair(ctx, r, v, paths)
		if err != nil && ctx.Err() == nil {
			log.Printf("docling: space %s, file %s: pairing: %v", r.id, v.SHA256[:12], err)
		}
		if q.paired != nil {
			q.paired(n, err)
		}
	}()
}

// pair moves each uploaded PDF into assets/ and writes its page, returning
// how many it paired.
func (q *doclingQueue) pair(ctx context.Context, r *repository, v VirtualInfo, paths []string) (int, error) {
	text, ok, _ := q.text(ctx, r, v)
	if !ok {
		return 0, nil
	}
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return 0, err
	}
	github, err := r.githubFolders(ctx, files, nil)
	if err != nil {
		return 0, err
	}
	paired := 0
	for _, p := range paths {
		f, ok := files[p]
		if !ok || f.Virtual == nil || f.Virtual.SHA256 != v.SHA256 || !pairable(p, *f.Virtual, github) {
			continue
		}
		dir, name := path.Split(p)
		page := dir + strings.TrimSuffix(name, path.Ext(name)) + ".md"
		original := dir + "assets/" + name
		if _, exists := files[page]; exists {
			continue
		}
		if _, exists := files[original]; exists {
			continue
		}
		body := doclingPairLink(name, doclingFormats[f.Virtual.Type].label) + text
		if int64(len(body)) > r.limits.MaxFileBytes {
			continue
		}
		moved, err := q.s.dispatchRepository(ctx, r, r.id, "move", Input{From: p, To: original, IfInState: head})
		if err != nil {
			return paired, err
		}
		head = moved.(Mutation).NewState
		written, err := q.s.dispatchRepository(ctx, r, r.id, "write", Input{Path: page, Text: &body, IfInState: head, CreateOnly: true})
		if err != nil {
			return paired, err
		}
		head = written.(WriteResult).NewState
		files[original], files[page] = f, record{}
		paired++
	}
	return paired, nil
}

// pairable reports an uploaded file that pairing may move.
func pairable(p string, v VirtualInfo, github map[string]bool) bool {
	format, ok := doclingFormats[v.Type]
	if !ok || v.Refresh != "" || !strings.EqualFold(path.Ext(p), "."+format.ext) {
		return false
	}
	if path.Base(path.Dir(p)) == "assets" {
		return false
	}
	for folder := range github {
		if strings.HasPrefix(p, folder) {
			return false
		}
	}
	return true
}
