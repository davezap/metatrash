package service

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// testDocx builds a small Word file: a title, headings, bold and italic
// runs, a link, bulleted and numbered lists, a table, a picture with a text
// box and a tracked deletion (both left out).
func testDocx(t *testing.T) []byte {
	t.Helper()
	const w = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
	doc := `<?xml version="1.0" encoding="UTF-8"?><w:document ` + w + `><w:body>
<w:p><w:pPr><w:pStyle w:val="Title"/></w:pPr><w:r><w:t>Leave policy</w:t></w:r></w:p>
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Annual leave</w:t></w:r></w:p>
<w:p><w:r><w:t xml:space="preserve">Workers get </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>four weeks</w:t></w:r><w:r><w:t xml:space="preserve"> a year, </w:t></w:r><w:r><w:rPr><w:i/></w:rPr><w:t>after</w:t></w:r><w:r><w:t xml:space="preserve"> twelve months.</w:t></w:r><w:del><w:r><w:delText>removed text</w:delText></w:r></w:del></w:p>
<w:p><w:r><w:t xml:space="preserve">See the </w:t></w:r><w:hyperlink r:id="rId5"><w:r><w:t>Holidays Act</w:t></w:r></w:hyperlink><w:r><w:t>.</w:t></w:r><w:r><w:drawing><wp:inline xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"><w:txbxContent><w:p><w:r><w:t>text box words</w:t></w:r></w:p></w:txbxContent></wp:inline></w:drawing></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>Sick leave</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="1"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>Ten days</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="2"/></w:numPr></w:pPr><w:r><w:t>Ask your manager</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="2"/></w:numPr></w:pPr><w:r><w:t>Book it</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Kind</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Days</w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>Sick | other</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>10</w:t></w:r></w:p><w:p><w:r><w:t>a year</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
<w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>Notes</w:t><w:br/><w:t>Second line</w:t></w:r></w:p>
</w:body></w:document>`
	rels := `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://legislation.govt.nz/holidays" TargetMode="External"/>
</Relationships>`
	numbering := `<?xml version="1.0" encoding="UTF-8"?><w:numbering ` + w + `>
<w:abstractNum w:abstractNumId="0"><w:lvl w:ilvl="0"><w:numFmt w:val="bullet"/></w:lvl></w:abstractNum>
<w:abstractNum w:abstractNumId="1"><w:lvl w:ilvl="0"><w:numFmt w:val="decimal"/></w:lvl></w:abstractNum>
<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num><w:num w:numId="2"><w:abstractNumId w:val="1"/></w:num>
</w:numbering>`
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, text := range map[string]string{"word/document.xml": doc, "word/_rels/document.xml.rels": rels, "word/numbering.xml": numbering, "[Content_Types].xml": "<Types/>"} {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(text))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDocxToMarkdown(t *testing.T) {
	title, md, err := docxToMarkdown(testDocx(t))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Leave policy\n\n# Annual leave\n\nWorkers get **four weeks** a year, *after* twelve months.\n\n" +
		"See the [Holidays Act](https://legislation.govt.nz/holidays).\n\n" +
		"- Sick leave\n   - Ten days\n\n1. Ask your manager\n2. Book it\n\n" +
		"| Kind | Days |\n| --- | --- |\n| Sick \\| other | 10<br>a year |\n\n## Notes  \nSecond line\n"
	if title != "Leave policy" || md != want {
		t.Errorf("title %q\n%s", title, md)
	}
	for _, bad := range []string{"removed text", "text box words"} {
		if strings.Contains(md, bad) {
			t.Errorf("kept %q", bad)
		}
	}
	if _, _, err := docxToMarkdown([]byte("not a zip")); err == nil {
		t.Error("read a file that is not a Word file")
	}
}

func TestPDFMarkdown(t *testing.T) {
	got := pdfMarkdown("Leave policy  \n\n\n\n# not a heading\nWorkers get four weeks.\n\fPage two text\n\f\f")
	want := "Leave policy\n\n\\# not a heading\nWorkers get four weeks.\n\n<!-- page 2 -->\n\nPage two text\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if pdfMarkdown("\f \n\f") != "" {
		t.Error("blank pages gave text")
	}
}

// testPDF builds a two-page PDF with one line of text on each page.
func testPDF(lines ...string) []byte {
	var b bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	b.WriteString("%PDF-1.4\n")
	kids := ""
	for i := range lines {
		kids += fmt.Sprintf("%d 0 R ", 4+2*i)
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, len(lines)))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	for i, line := range lines {
		stream := fmt.Sprintf("BT /F1 18 Tf 72 720 Td (%s) Tj ET", line)
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", 5+2*i))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes()
}

func TestPDFToText(t *testing.T) {
	path := findPDFToText()
	if path == "" {
		t.Skip("pdftotext is not installed")
	}
	text, err := pdfToText(path)(context.Background(), testPDF("Annual leave is four weeks", "Sick leave is ten days"))
	if err != nil {
		t.Fatal(err)
	}
	md := pdfMarkdown(text)
	if !strings.HasPrefix(md, "Annual leave is four weeks\n\n<!-- page 2 -->\n\nSick leave is ten days") {
		t.Errorf("%q", md)
	}
	if _, err := pdfToText(path)(context.Background(), []byte("%PDF-1.4 broken")); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("a broken PDF: %v", err)
	}
	if _, err := exec.LookPath(path); err != nil {
		t.Error(err)
	}
}

// web_fetch gives PDF and Word files a Markdown reference (0.59.0), and a
// file fetched before that, without text, gets it on the next fetch.
func TestWebFetchConverts(t *testing.T) {
	w, _, _, refs := webWorld(t)
	docx := testDocx(t)
	pdf := testPDF("Annual leave is four weeks", "Sick leave is ten days")
	var mu sync.Mutex
	requests := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.URL.Path]++
		mu.Unlock()
		rw.Header().Set("ETag", `"v1"`)
		if r.Header.Get("If-None-Match") == `"v1"` {
			rw.WriteHeader(http.StatusNotModified)
			return
		}
		switch r.URL.Path {
		case "/policy.docx":
			rw.Header().Set("Content-Type", "application/octet-stream")
			rw.Write(docx)
		case "/policy.pdf":
			rw.Header().Set("Content-Type", "application/pdf")
			rw.Write(pdf)
		}
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	session := w.runner.newWebSession(w.repo.owner, false, true)
	session.add(srv.URL + "/policy.docx")
	session.add(srv.URL + "/policy.pdf")

	res, err := session.webFetch(ctx, srv.URL+"/policy.docx")
	if err != nil {
		t.Fatal(err)
	}
	doc := res.(webFetchResult)
	if !strings.HasSuffix(doc.Path, ".md") || !strings.HasSuffix(doc.Original, ".docx") || doc.Title != "Leave policy" || doc.Note != "" || !strings.Contains(doc.Preview, "**four weeks**") {
		t.Fatalf("docx %+v", doc)
	}
	if _, text, err := session.textOf(ctx, doc.Path); err != nil || !strings.Contains(text, "| Kind | Days |") {
		t.Errorf("read docx text: %v %q", err, text)
	}
	if _, _, err := session.textOf(ctx, doc.Original); err == nil || !strings.Contains(err.Error(), doc.Path) {
		t.Errorf("read the original: %v", err)
	}

	// Without pdftotext a PDF is kept without text, and says why.
	w.runner.pdfText = nil
	res, err = session.webFetch(ctx, srv.URL+"/policy.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if first := res.(webFetchResult); !strings.HasSuffix(first.Path, ".pdf") || first.Original != "" || !strings.Contains(first.Note, "pdftotext") {
		t.Fatalf("pdf without pdftotext %+v", first)
	}
	path := findPDFToText()
	if path == "" {
		t.Skip("pdftotext is not installed")
	}
	// With it, fetching again downloads the file anew (the site would answer
	// 304) and adds the text.
	w.runner.pdfText = pdfToText(path)
	res, err = session.webFetch(ctx, srv.URL+"/policy.pdf")
	if err != nil {
		t.Fatal(err)
	}
	p := res.(webFetchResult)
	if !strings.HasSuffix(p.Path, ".md") || !strings.HasSuffix(p.Original, ".pdf") || p.Unchanged || !strings.Contains(p.Preview, "<!-- page 2 -->") {
		t.Fatalf("pdf %+v", p)
	}
	if requests["/policy.pdf"] != 3 {
		t.Errorf("%d PDF requests", requests["/policy.pdf"])
	}
	// The next fetch is a plain 304: same reference.
	res, err = session.webFetch(ctx, srv.URL+"/policy.pdf")
	if again := res.(webFetchResult); err != nil || !again.Unchanged || again.Path != p.Path {
		t.Errorf("refetch %+v %v", again, err)
	}
	if n := len(refs.refs); n != 3 {
		t.Errorf("%d references", n)
	}
}
