package service

import (
	"context"
	"fmt"
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
	if q == nil {
		return
	}
	if format, ok := doclingFormats[v.Type]; ok && v.Size > q.settings.maxBytes && r.owned && r.id != "" {
		q.mu.Lock()
		q.note(r.id, path, pathNote{state: "kept", until: time.Now().Add(activityKeptFor),
			message: fmt.Sprintf("Not converted to Markdown: this %s is over %d MB, the largest the converter takes. Agents read its plain text instead.", format.label, q.settings.maxBytes>>20)})
		q.mu.Unlock()
	}
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

// pairOutcome is what pairing did with one uploaded path: page and
// original when it paired it, why when it left it where it is.
type pairOutcome struct{ path, page, original, why string }

// pairLater pairs the uploaded paths in the background.
func (q *doclingQueue) pairLater(r *repository, v VirtualInfo, paths []string) {
	q.mu.Lock()
	base := q.ctx
	if base == nil || base.Err() != nil {
		for _, p := range paths {
			q.forget(r.id, p)
		}
		q.mu.Unlock()
		return
	}
	q.wg.Add(1)
	q.mu.Unlock()
	go func() {
		defer q.wg.Done()
		ctx, cancel := context.WithTimeout(base, 2*time.Minute)
		defer cancel()
		outcomes, err := q.pair(ctx, r, v, paths)
		if err != nil && ctx.Err() == nil {
			log.Printf("docling: space %s, file %s: pairing: %v", r.id, v.SHA256[:12], err)
		}
		n := 0
		q.mu.Lock()
		for _, p := range paths {
			q.forget(r.id, p)
		}
		q.pairedNotes(r, outcomes)
		q.mu.Unlock()
		for _, o := range outcomes {
			if o.page != "" {
				n++
			}
		}
		if q.paired != nil {
			q.paired(n, err)
		}
	}()
}

// pair moves each uploaded PDF into assets/ and writes its page, saying
// what it did with each path.
func (q *doclingQueue) pair(ctx context.Context, r *repository, v VirtualInfo, paths []string) ([]pairOutcome, error) {
	text, ok, _ := q.text(ctx, r, v)
	if !ok {
		return nil, nil
	}
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	github, err := r.githubFolders(ctx, files, nil)
	if err != nil {
		return nil, err
	}
	var outcomes []pairOutcome
	for _, p := range paths {
		f, ok := files[p]
		if !ok || f.Virtual == nil || f.Virtual.SHA256 != v.SHA256 {
			continue // changed, moved or deleted before the conversion finished
		}
		if why := pairWhy(p, *f.Virtual, github); why != "" {
			outcomes = append(outcomes, pairOutcome{path: p, why: why})
			continue
		}
		dir, name := path.Split(p)
		page := dir + strings.TrimSuffix(name, path.Ext(name)) + ".md"
		original := dir + "assets/" + name
		if _, exists := files[page]; exists {
			outcomes = append(outcomes, pairOutcome{path: p, why: path.Base(page) + " already exists"})
			continue
		}
		if _, exists := files[original]; exists {
			outcomes = append(outcomes, pairOutcome{path: p, why: "assets/" + name + " already exists"})
			continue
		}
		body := doclingPairLink(name, doclingFormats[f.Virtual.Type].label) + text
		if int64(len(body)) > r.limits.MaxFileBytes {
			outcomes = append(outcomes, pairOutcome{path: p, why: "its Markdown is larger than a file in this space may be"})
			continue
		}
		moved, err := q.s.dispatchRepository(ctx, r, r.id, "move", Input{From: p, To: original, IfInState: head})
		if err != nil {
			outcomes = append(outcomes, pairOutcome{path: p, why: "moving it into assets/ was refused (" + err.Error() + ")"})
			return outcomes, err
		}
		head = moved.(Mutation).NewState
		written, err := q.s.dispatchRepository(ctx, r, r.id, "write", Input{Path: page, Text: &body, IfInState: head, CreateOnly: true})
		if err != nil {
			// The original is in assets/ now; its note goes there.
			outcomes = append(outcomes, pairOutcome{path: original, why: "writing " + path.Base(page) + " was refused (" + err.Error() + ")"})
			return outcomes, err
		}
		head = written.(WriteResult).NewState
		files[original], files[page] = f, record{}
		outcomes = append(outcomes, pairOutcome{path: p, page: page, original: original})
	}
	return outcomes, nil
}

// pairable reports an uploaded file that pairing may move.
func pairable(p string, v VirtualInfo, github map[string]bool) bool {
	return pairWhy(p, v, github) == ""
}

// pairWhy says why pairing leaves an upload where it is, "" when it may move.
func pairWhy(p string, v VirtualInfo, github map[string]bool) string {
	format, ok := doclingFormats[v.Type]
	switch {
	case !ok:
		return "Metatrash doesn't pair this type"
	case v.Refresh != "":
		return "it is fetched from the web again on a schedule"
	case !strings.EqualFold(path.Ext(p), "."+format.ext):
		return "its name doesn't end in ." + format.ext
	}
	if path.Base(path.Dir(p)) == "assets" {
		return "it is in an assets/ folder"
	}
	for folder := range github {
		if strings.HasPrefix(p, folder) {
			return "it is in a folder that mirrors GitHub"
		}
	}
	return ""
}
