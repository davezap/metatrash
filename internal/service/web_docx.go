package service

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// Word files as Markdown (0.59.0): a .docx is a zip of XML parts. The text is
// in word/document.xml; links' addresses are in word/_rels/document.xml.rels
// and list styles in word/numbering.xml. Headings (Title and Heading 1–6
// styles), lists, tables, bold, italic, links and line breaks come through;
// pictures, headers, footers, comments and tracked deletions are left out.

const (
	maxDocxPartBytes = 64 << 20 // the most any one part may unpack to
	maxDocxText      = 4 << 20  // the most Markdown kept
)

const wordNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
const relNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"

// docxToMarkdown returns a Word file's title (its first heading, if any)
// and its text as Markdown.
func docxToMarkdown(body []byte) (string, string, error) {
	return docxReaderToMarkdown(bytes.NewReader(body), int64(len(body)))
}

// docxReaderToMarkdown reads the Word file from r, which holds size bytes,
// a part at a time.
func docxReaderToMarkdown(r io.ReaderAt, size int64) (string, string, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return "", "", fmt.Errorf("not a Word file: %v", err)
	}
	part := func(name string) ([]byte, error) {
		for _, f := range z.File {
			if f.Name != name {
				continue
			}
			if f.UncompressedSize64 > maxDocxPartBytes {
				return nil, fmt.Errorf("%s is too large", name)
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			b, err := io.ReadAll(io.LimitReader(rc, maxDocxPartBytes+1))
			if err == nil && len(b) > maxDocxPartBytes {
				err = fmt.Errorf("%s is too large", name)
			}
			return b, err
		}
		return nil, nil
	}
	doc, err := part("word/document.xml")
	if err != nil {
		return "", "", err
	}
	if doc == nil {
		return "", "", fmt.Errorf("not a Word file: it has no word/document.xml")
	}
	rels, err := part("word/_rels/document.xml.rels")
	if err != nil {
		return "", "", err
	}
	numbering, err := part("word/numbering.xml")
	if err != nil {
		return "", "", err
	}
	c := docxConverter{links: docxLinks(rels), ordered: docxOrderedLists(numbering), counters: map[string]int{}}
	if err := c.convert(doc); err != nil {
		return "", "", fmt.Errorf("the Word file cannot be read: %v", err)
	}
	text := strings.TrimSpace(c.out.String())
	if len(text) > maxDocxText {
		cut := strings.LastIndex(text[:maxDocxText], "\n")
		if cut < maxDocxText/2 {
			cut = maxDocxText
		}
		text = strings.ToValidUTF8(text[:cut], "") + "\n\n…(the rest of the document is left out)"
	}
	if text == "" {
		return c.title, "", nil
	}
	return c.title, text + "\n", nil
}

// docxLinks maps relationship IDs to external link addresses.
func docxLinks(rels []byte) map[string]string {
	links := map[string]string{}
	var doc struct {
		Rels []struct {
			ID         string `xml:"Id,attr"`
			Target     string `xml:"Target,attr"`
			TargetMode string `xml:"TargetMode,attr"`
		} `xml:"Relationship"`
	}
	if xml.Unmarshal(rels, &doc) != nil {
		return links
	}
	for _, r := range doc.Rels {
		if r.TargetMode != "External" {
			continue
		}
		if u, err := url.Parse(r.Target); err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "mailto") {
			links[r.ID] = r.Target
		}
	}
	return links
}

// docxOrderedLists reports, by list ID (w:numId), whether its first level
// is numbered rather than bulleted.
func docxOrderedLists(numbering []byte) map[string]bool {
	ordered := map[string]bool{}
	var doc struct {
		Abstract []struct {
			ID     string `xml:"abstractNumId,attr"`
			Levels []struct {
				Level  string `xml:"ilvl,attr"`
				Format struct {
					Val string `xml:"val,attr"`
				} `xml:"numFmt"`
			} `xml:"lvl"`
		} `xml:"abstractNum"`
		Nums []struct {
			ID       string `xml:"numId,attr"`
			Abstract struct {
				Val string `xml:"val,attr"`
			} `xml:"abstractNumId"`
		} `xml:"num"`
	}
	if xml.Unmarshal(numbering, &doc) != nil {
		return ordered
	}
	formats := map[string]string{}
	for _, a := range doc.Abstract {
		for _, l := range a.Levels {
			if l.Level == "0" {
				formats[a.ID] = l.Format.Val
			}
		}
	}
	for _, n := range doc.Nums {
		f := formats[n.Abstract.Val]
		ordered[n.ID] = f != "" && f != "bullet" && f != "none"
	}
	return ordered
}

type docxConverter struct {
	out      strings.Builder
	title    string
	links    map[string]string
	ordered  map[string]bool
	counters map[string]int

	// The paragraph being read.
	para                          strings.Builder
	style, numID, level           string
	bold, italic, inDel, textOpen bool
	picDepth                      int
	link                          string
	linkText                      strings.Builder
	inList                        bool
	listID                        string

	// Tables: rows of cells; depth counts nested tables, whose text joins
	// the outer cell.
	depth int
	rows  [][]string
	cell  strings.Builder
	cells []string
}

func (c *docxConverter) convert(doc []byte) error {
	d := xml.NewDecoder(bytes.NewReader(doc))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// Pictures, and the text boxes drawn inside them, are skipped whole.
		switch t := tok.(type) {
		case xml.StartElement:
			if c.picDepth > 0 || t.Name.Space == wordNS && (t.Name.Local == "drawing" || t.Name.Local == "pict" || t.Name.Local == "object") {
				c.picDepth++
				continue
			}
			if t.Name.Space == wordNS {
				c.start(t)
			}
		case xml.EndElement:
			if c.picDepth > 0 {
				c.picDepth--
				continue
			}
			if t.Name.Space == wordNS {
				c.end(t.Name.Local)
			}
		case xml.CharData:
			if c.textOpen && !c.inDel && c.picDepth == 0 {
				c.text(string(t))
			}
		}
	}
}

func xmlAttr(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func (c *docxConverter) start(t xml.StartElement) {
	switch t.Name.Local {
	case "p":
		c.para.Reset()
		c.style, c.numID, c.level = "", "", ""
	case "pStyle":
		c.style = xmlAttr(t, "val")
	case "numId":
		c.numID = xmlAttr(t, "val")
	case "ilvl":
		c.level = xmlAttr(t, "val")
	case "r":
		c.bold, c.italic = false, false
	case "b":
		c.bold = xmlAttr(t, "val") != "0" && xmlAttr(t, "val") != "false"
	case "i":
		c.italic = xmlAttr(t, "val") != "0" && xmlAttr(t, "val") != "false"
	case "t":
		c.textOpen = true
	case "tab":
		c.text("\t")
	case "br", "cr":
		if xmlAttr(t, "type") != "page" {
			c.text("  \n")
		}
	case "del":
		c.inDel = true
	case "hyperlink":
		for _, a := range t.Attr {
			if a.Name.Space == relNS && a.Name.Local == "id" {
				c.link = c.links[a.Value]
			}
		}
		c.linkText.Reset()
	case "tbl":
		c.depth++
		if c.depth == 1 {
			c.rows = nil
		}
	case "tr":
		if c.depth == 1 {
			c.cells = nil
		}
	case "tc":
		if c.depth == 1 {
			c.cell.Reset()
		}
	}
}

func (c *docxConverter) end(local string) {
	switch local {
	case "t":
		c.textOpen = false
	case "del":
		c.inDel = false
	case "hyperlink":
		if c.link != "" {
			text := strings.TrimSpace(c.linkText.String())
			if text == "" {
				text = c.link
			}
			c.write("[" + strings.ReplaceAll(text, "]", `\]`) + "](" + c.link + ")")
		}
		c.link = ""
	case "p":
		c.endParagraph()
	case "tc":
		if c.depth == 1 {
			cell := strings.TrimSpace(c.cell.String())
			cell = strings.ReplaceAll(strings.ReplaceAll(cell, "\n", "<br>"), "|", `\|`)
			c.cells = append(c.cells, cell)
		}
	case "tr":
		if c.depth == 1 {
			c.rows = append(c.rows, c.cells)
		}
	case "tbl":
		c.depth--
		if c.depth == 0 {
			c.endTable()
		}
	}
}

// text adds characters to the paragraph, or to the link being read.
func (c *docxConverter) text(s string) {
	if c.link != "" {
		c.linkText.WriteString(s)
		return
	}
	if strings.TrimSpace(s) != "" && (c.bold || c.italic) {
		mark := "*"
		if c.bold && c.italic {
			mark = "***"
		} else if c.bold {
			mark = "**"
		}
		// Keep the spaces outside the marks, or Markdown will not see them.
		lead := s[:len(s)-len(strings.TrimLeft(s, " \t"))]
		trail := s[len(strings.TrimRight(s, " \t")):]
		s = lead + mark + strings.TrimSpace(s) + mark + trail
	}
	c.write(s)
}

func (c *docxConverter) write(s string) { c.para.WriteString(s) }

func (c *docxConverter) endParagraph() {
	text := strings.TrimSpace(strings.ReplaceAll(c.para.String(), "****", ""))
	c.para.Reset()
	if c.depth > 0 {
		if text != "" {
			if c.cell.Len() > 0 {
				c.cell.WriteString("\n")
			}
			c.cell.WriteString(text)
		}
		return
	}
	if text == "" {
		return
	}
	style := strings.ToLower(strings.ReplaceAll(c.style, " ", ""))
	switch {
	case style == "title":
		c.block("# " + text)
		if c.title == "" {
			c.title = text
		}
		return
	case strings.HasPrefix(style, "heading"):
		n, err := strconv.Atoi(strings.TrimPrefix(style, "heading"))
		if err == nil && n >= 1 && n <= 6 {
			if c.title == "" {
				c.title = text
			}
			c.block(strings.Repeat("#", n) + " " + text)
			return
		}
	}
	if c.numID != "" && c.numID != "0" {
		level, _ := strconv.Atoi(c.level)
		level = min(max(level, 0), 8)
		indent := strings.Repeat("   ", level)
		marker := "-"
		if c.ordered[c.numID] {
			key := c.numID + ":" + strconv.Itoa(level)
			c.counters[key]++
			marker = strconv.Itoa(c.counters[key]) + "."
		}
		c.list(c.numID, indent+marker+" "+text)
		return
	}
	c.block(text)
}

// block starts a paragraph-level block after a blank line.
func (c *docxConverter) block(s string) {
	if c.out.Len() > 0 {
		c.out.WriteString("\n\n")
	}
	c.out.WriteString(s)
	c.inList = false
}

// list adds a list item, keeping items of one list together.
func (c *docxConverter) list(numID, s string) {
	if c.out.Len() > 0 {
		if c.inList && numID == c.listID {
			c.out.WriteString("\n")
		} else {
			c.out.WriteString("\n\n")
		}
	}
	c.out.WriteString(s)
	c.inList, c.listID = true, numID
}

func (c *docxConverter) endTable() {
	rows := c.rows
	c.rows = nil
	width := 0
	for _, r := range rows {
		width = max(width, len(r))
	}
	if width == 0 {
		return
	}
	var b strings.Builder
	for i, r := range rows {
		for len(r) < width {
			r = append(r, "")
		}
		b.WriteString("| " + strings.Join(r, " | ") + " |")
		if i == 0 {
			b.WriteString("\n|" + strings.Repeat(" --- |", width))
		}
		if i < len(rows)-1 {
			b.WriteString("\n")
		}
	}
	c.block(b.String())
}
