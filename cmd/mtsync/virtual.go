package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
)

// virtualItem is one line of mtsync -json virtual, for editors: a file whose
// content the space keeps outside Git (metatrash 0.60.0).
type virtualItem struct {
	Path string `json:"path"`
	// File is the path on this computer.
	File   string `json:"file"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Text is true when the server holds text for the file (a PDF's
	// Markdown), which mtsync text prints.
	Text bool `json:"text"`
	// Source is the address the file is refreshed from; such a file can't be
	// changed in the folder.
	Source  string `json:"source,omitempty"`
	Refresh string `json:"refresh,omitempty"`
}

// showVirtual lists the virtual files of the folder synced at place (or
// under place, when it is a folder inside one), as of the last pass.
func showVirtual(place string, asJSON bool, out io.Writer) error {
	e, err := syncedPlace(place)
	if err != nil {
		return err
	}
	st, err := folderState(e)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(place)
	if err != nil {
		return err
	}
	under, _ := filepath.Rel(e.Folder, abs)
	under = filepath.ToSlash(under)
	if under == "." {
		under = ""
	}
	items := []virtualItem{}
	for p, f := range st.Files {
		if f.Virtual == nil || (under != "" && p != under && !strings.HasPrefix(p, under+"/")) {
			continue
		}
		v := f.Virtual
		items = append(items, virtualItem{Path: p, File: filepath.Join(e.Folder, filepath.FromSlash(p)), Type: v.Type, Size: v.Size, SHA256: v.SHA256, Text: v.TextSHA256 != "", Source: v.Source, Refresh: v.Refresh})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	if asJSON {
		return printJSON(out, items)
	}
	if len(items) == 0 {
		fmt.Fprintln(out, "No virtual files.")
		return nil
	}
	for _, it := range items {
		line := fmt.Sprintf("%s  %s, %s", it.Path, it.Type, sizeText(it.Size))
		if it.Source != "" {
			line += ", from " + it.Source
			if it.Refresh != "" {
				line += " (" + it.Refresh + ")"
			}
		}
		fmt.Fprintln(out, line)
	}
	return nil
}

// showText prints the server's text for a synced file: a virtual file's
// Markdown (a PDF or Word file's), or an ordinary file's text.
func showText(ctx context.Context, file string, out io.Writer) error {
	e, rel, err := syncedFile(file)
	if err != nil {
		return err
	}
	c := folderClient(e, tokenSource(e.Server))
	text, virtual, err := c.readFile(ctx, rel, "")
	if err != nil {
		return err
	}
	if virtual != nil && virtual.TextSHA256 == "" {
		return fmt.Errorf("the space has no text for %s (%s)", rel, virtual.Type)
	}
	_, err = io.WriteString(out, text)
	return err
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}
