package service

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// htmlToMarkdown turns a downloaded page into Markdown for an agent: headings,
// paragraphs, lists, links and images with absolute addresses, tables, quotes
// and code. It reads the page's <main> or <article> when it has one, otherwise
// its <body>, and leaves out scripts, styles, forms and hidden parts. Pages
// built by JavaScript come out mostly empty.
func htmlToMarkdown(page string, base *url.URL) (title, markdown string) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return "", ""
	}
	if t := findElement(doc, atom.Title); t != nil {
		title = oneLine(textOf(t))
	}
	root := findElement(doc, atom.Main)
	if root == nil {
		root = findElement(doc, atom.Article)
	}
	if root == nil {
		root = findElement(doc, atom.Body)
	}
	if root == nil {
		root = doc
	}
	c := mdConverter{base: base}
	markdown = strings.ReplaceAll(tidyMarkdown(c.children(root)), indentUnit, "  ")
	if title == "" {
		if h := findElement(root, atom.H1); h != nil {
			title = oneLine(textOf(h))
		}
	}
	if len(title) > 300 {
		title = strings.ToValidUTF8(title[:300], "")
	}
	for i, block := range c.pre {
		markdown = strings.Replace(markdown, preToken(i), block, 1)
	}
	return title, markdown
}

func findElement(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, a); found != nil {
			return found
		}
	}
	return nil
}

// textOf is all the text under n.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// Markers that survive whitespace tidying: indentUnit becomes two spaces of
// list indentation, and preToken(i) is replaced by code block i at the end.
const indentUnit = "\x01"

func preToken(i int) string { return "\x02" + strconv.Itoa(i) + "\x02" }

type mdConverter struct {
	base *url.URL
	pre  []string
}

var skipped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true, atom.Svg: true, atom.Math: true,
	atom.Iframe: true, atom.Canvas: true, atom.Head: true, atom.Button: true, atom.Input: true, atom.Select: true,
	atom.Textarea: true, atom.Option: true, atom.Object: true, atom.Embed: true, atom.Audio: true, atom.Video: true, atom.Dialog: true,
}

var blockElements = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Section: true, atom.Article: true, atom.Main: true, atom.Header: true,
	atom.Footer: true, atom.Nav: true, atom.Aside: true, atom.Figure: true, atom.Figcaption: true, atom.Address: true,
	atom.Details: true, atom.Summary: true, atom.Form: true, atom.Fieldset: true, atom.Body: true, atom.Center: true,
}

func hidden(n *html.Node) bool {
	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "hidden":
			return true
		case "aria-hidden":
			if a.Val == "true" {
				return true
			}
		case "style":
			v := strings.ReplaceAll(strings.ToLower(a.Val), " ", "")
			if strings.Contains(v, "display:none") || strings.Contains(v, "visibility:hidden") {
				return true
			}
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// link resolves an address against the page; "" for anything but http(s).
func (c *mdConverter) link(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "#") {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if c.base != nil {
		u = c.base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

func (c *mdConverter) children(n *html.Node) string {
	var b strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		b.WriteString(c.node(child))
	}
	return b.String()
}

var spaceRun = regexp.MustCompile(`[ \t\r\n\f]+`)

func (c *mdConverter) node(n *html.Node) string {
	switch n.Type {
	case html.TextNode:
		return spaceRun.ReplaceAllString(n.Data, " ")
	case html.ElementNode:
	default:
		return ""
	}
	if skipped[n.DataAtom] || hidden(n) {
		return ""
	}
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		text := oneLine(c.children(n))
		if text == "" {
			return ""
		}
		level := int(n.Data[1] - '0')
		return "\n\n" + strings.Repeat("#", level) + " " + text + "\n\n"
	case atom.Br:
		return "\n"
	case atom.Hr:
		return "\n\n---\n\n"
	case atom.Strong, atom.B:
		if text := strings.TrimSpace(c.children(n)); text != "" {
			return "**" + text + "**"
		}
		return ""
	case atom.Em, atom.I:
		if text := strings.TrimSpace(c.children(n)); text != "" {
			return "_" + text + "_"
		}
		return ""
	case atom.Code, atom.Kbd, atom.Samp, atom.Tt:
		if text := strings.TrimSpace(textOf(n)); text != "" {
			return "`" + oneLine(text) + "`"
		}
		return ""
	case atom.A:
		text := strings.TrimSpace(c.children(n))
		href := c.link(attr(n, "href"))
		if href == "" {
			return text
		}
		if text == "" {
			text = href
		}
		return "[" + oneLine(text) + "](" + href + ")"
	case atom.Img:
		src := c.link(attr(n, "src"))
		if src == "" {
			return ""
		}
		return "![" + oneLine(attr(n, "alt")) + "](" + src + ")"
	case atom.Pre:
		c.pre = append(c.pre, "```\n"+strings.Trim(textOf(n), "\n")+"\n```")
		return "\n\n" + preToken(len(c.pre)-1) + "\n\n"
	case atom.Blockquote:
		inner := tidyMarkdown(c.children(n))
		if inner == "" {
			return ""
		}
		lines := strings.Split(inner, "\n")
		for i, line := range lines {
			lines[i] = strings.TrimRight("> "+line, " ")
		}
		return "\n\n" + strings.Join(lines, "\n") + "\n\n"
	case atom.Ul, atom.Ol:
		return "\n\n" + c.list(n) + "\n\n"
	case atom.Li:
		// A list item outside a list.
		return "\n\n- " + oneLine(c.children(n)) + "\n\n"
	case atom.Dt:
		return "\n\n**" + oneLine(c.children(n)) + "**\n"
	case atom.Dd:
		return "\n" + oneLine(c.children(n)) + "\n\n"
	case atom.Table:
		return "\n\n" + c.table(n) + "\n\n"
	}
	if blockElements[n.DataAtom] {
		return "\n\n" + c.children(n) + "\n\n"
	}
	return c.children(n)
}

// list renders a ul or ol; nested lists are indented under their item.
func (c *mdConverter) list(n *html.Node) string {
	var items []string
	number, _ := strconv.Atoi(attr(n, "start"))
	if number == 0 {
		number = 1
	}
	for li := n.FirstChild; li != nil; li = li.NextSibling {
		if li.Type != html.ElementNode || li.DataAtom != atom.Li || hidden(li) {
			continue
		}
		body := tidyMarkdown(c.children(li))
		body = blankLines.ReplaceAllString(body, "\n")
		marker := "- "
		if n.DataAtom == atom.Ol {
			marker = strconv.Itoa(number) + ". "
			number++
		}
		lines := strings.Split(body, "\n")
		for i := 1; i < len(lines); i++ {
			lines[i] = indentUnit + lines[i]
		}
		items = append(items, marker+strings.Join(lines, "\n"))
	}
	return strings.Join(items, "\n")
}

// table renders a table as a pipe table, its first row as the header.
func (c *mdConverter) table(n *html.Node) string {
	var rows [][]string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			if child.Type != html.ElementNode {
				continue
			}
			switch child.DataAtom {
			case atom.Tr:
				var row []string
				for cell := child.FirstChild; cell != nil; cell = cell.NextSibling {
					if cell.Type == html.ElementNode && (cell.DataAtom == atom.Td || cell.DataAtom == atom.Th) {
						text := oneLine(strings.ReplaceAll(c.children(cell), "\x02", ""))
						row = append(row, strings.ReplaceAll(text, "|", `\|`))
					}
				}
				if len(row) > 0 {
					rows = append(rows, row)
				}
			case atom.Table:
				// A nested table is flattened into its own rows.
				walk(child)
			case atom.Thead, atom.Tbody, atom.Tfoot:
				walk(child)
			case atom.Caption:
				if text := oneLine(c.children(child)); text != "" {
					rows = append(rows, []string{text})
				}
			}
		}
	}
	walk(n)
	if len(rows) == 0 {
		return ""
	}
	width := 0
	for _, row := range rows {
		width = max(width, len(row))
	}
	var b strings.Builder
	for i, row := range rows {
		for len(row) < width {
			row = append(row, "")
		}
		b.WriteString("| " + strings.Join(row, " | ") + " |\n")
		if i == 0 {
			b.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

var (
	blankRuns  = regexp.MustCompile(`\n{3,}`)
	blankLines = regexp.MustCompile(`\n{2,}`)
	spaceRuns  = regexp.MustCompile(` {2,}`)
)

// tidyMarkdown trims each line (keeping its list indentation markers) and
// leaves at most one blank line between blocks.
func tidyMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		line = spaceRuns.ReplaceAllString(strings.Trim(line, " \t"), " ")
		indent := 0
		for strings.HasPrefix(line, indentUnit) {
			line, indent = strings.TrimLeft(line[len(indentUnit):], " \t"), indent+1
		}
		lines[i] = strings.Repeat(indentUnit, indent) + line
	}
	return strings.Trim(blankRuns.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"), "\n")
}
