package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PDF files as Markdown (0.59.0): the text comes from pdftotext (Poppler,
// package poppler-utils), run on a temporary copy with a time limit and
// without a shell. Each page after the first starts with a <!-- page N -->
// line, so an agent can say where something is. A scanned PDF has no text
// to give; there is no OCR. Without pdftotext on the server, PDFs are kept
// without text, as before.

const (
	maxPDFPages   = 500
	maxPDFText    = 4 << 20
	pdfTextTimout = 60 * time.Second
)

// pdfTextFunc extracts a PDF's text; nil means PDFs have no text.
type pdfTextFunc func(ctx context.Context, body []byte) (string, error)

// findPDFToText returns pdftotext's path, or "" when it is not installed.
func findPDFToText() string {
	path, err := exec.LookPath("pdftotext")
	if err != nil {
		return ""
	}
	return path
}

// pdfToText runs pdftotext at path.
func pdfToText(path string) pdfTextFunc {
	return func(ctx context.Context, body []byte) (string, error) {
		f, err := os.CreateTemp("", "metatrash-*.pdf")
		if err != nil {
			return "", fmt.Errorf("cannot read the PDF")
		}
		defer os.Remove(f.Name())
		if _, err := f.Write(body); err != nil {
			f.Close()
			return "", fmt.Errorf("cannot read the PDF")
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("cannot read the PDF")
		}
		ctx, cancel := context.WithTimeout(ctx, pdfTextTimout)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, "-q", "-enc", "UTF-8", "-l", strconv.Itoa(maxPDFPages), f.Name(), "-")
		cmd.Env = []string{}
		var out limitedBuffer
		out.limit = maxPDFText
		cmd.Stdout = &out
		err = cmd.Run()
		if ctx.Err() != nil {
			return "", fmt.Errorf("the PDF took too long to read")
		}
		if err != nil && !out.full {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return "", fmt.Errorf("the PDF cannot be read (it may be damaged or password-protected)")
			}
			return "", fmt.Errorf("cannot read the PDF")
		}
		text := out.String()
		if out.full {
			text += "\n…(the rest of the document is left out)"
		}
		return text, nil
	}
}

// limitedBuffer keeps the first limit bytes written to it and then reports
// a short write, which stops the command.
type limitedBuffer struct {
	bytes.Buffer
	limit int
	full  bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.Len(); len(p) > room {
		b.Buffer.Write(p[:max(room, 0)])
		b.full = true
		return max(room, 0), io.ErrShortWrite
	}
	return b.Buffer.Write(p)
}

var pdfBlankLines = regexp.MustCompile(`\n{3,}`)

// pdfMarkdown turns pdftotext's output into Markdown: pages (separated by
// form feeds) marked with <!-- page N -->, trailing spaces and runs of
// blank lines removed, and characters Markdown would take as formatting at
// the start of a line escaped.
func pdfMarkdown(text string) string {
	text = strings.ToValidUTF8(strings.ReplaceAll(text, "\r\n", "\n"), "")
	pages := strings.Split(text, "\f")
	var b strings.Builder
	for i, page := range pages {
		lines := strings.Split(page, "\n")
		for j, line := range lines {
			line = strings.TrimRight(line, " \t")
			trimmed := strings.TrimLeft(line, " \t")
			if trimmed != "" && strings.ContainsRune("#>|`=", rune(trimmed[0])) {
				line = `\` + trimmed
			}
			lines[j] = line
		}
		page = strings.TrimSpace(strings.Join(lines, "\n"))
		if page == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if i > 0 {
			b.WriteString("<!-- page " + strconv.Itoa(i+1) + " -->\n\n")
		}
		b.WriteString(page)
	}
	out := pdfBlankLines.ReplaceAllString(b.String(), "\n\n")
	if out == "" {
		return ""
	}
	return out + "\n"
}
