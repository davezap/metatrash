package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The parts of a received message that are filed (email_in.go): its
// headers, one body as text, and its attachments. Parsing uses the standard
// library only; charsets other than UTF-8, ASCII, Latin-1 and Windows-1252
// are read as UTF-8 with invalid bytes replaced.

type emailMessage struct {
	from       *mail.Address
	fromText   string
	to, cc     []string
	subject    string
	date       time.Time
	messageID  string
	inReplyTo  string
	references []string
	// body is Markdown: the plain-text part as it is, else the HTML part
	// converted.
	body        string
	attachments []emailAttachment
	rawHash     string
	// skipped describes parts left out for the limits.
	skipped []string
}

type emailAttachment struct {
	name  string
	media string
	data  []byte
}

const (
	maxEmailDepth = 8
	maxEmailParts = 300
	maxEmailBody  = 2 << 20
)

var emailWords = &mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
	b, err := io.ReadAll(io.LimitReader(input, 1<<20))
	if err != nil {
		return nil, err
	}
	return strings.NewReader(decodeCharset(b, charset)), nil
}}

// decodeCharset turns text in charset into UTF-8.
func decodeCharset(b []byte, charset string) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "latin-1", "iso8859-1", "windows-1252", "cp1252", "iso-8859-15":
		if !utf8.Valid(b) {
			return decodeWebText(b)
		}
	}
	return strings.ToValidUTF8(string(b), "�")
}

func parseEmail(raw []byte) (*emailMessage, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("the headers could not be read")
	}
	sum := sha256.Sum256(raw)
	m := &emailMessage{rawHash: hex.EncodeToString(sum[:])}
	h := msg.Header
	parser := &mail.AddressParser{WordDecoder: emailWords}
	if list, err := parser.ParseList(h.Get("From")); err == nil && len(list) > 0 {
		m.from = list[0]
	}
	m.fromText = headerText(h.Get("From"))
	for _, a := range addressList(parser, h.Get("To")) {
		m.to = append(m.to, a)
	}
	m.cc = addressList(parser, h.Get("Cc"))
	m.subject = headerText(h.Get("Subject"))
	if d, err := mail.ParseDate(h.Get("Date")); err == nil {
		m.date = d
	}
	ids := messageIDs(h.Get("Message-ID"))
	if len(ids) > 0 {
		m.messageID = ids[0]
	}
	if ids := messageIDs(h.Get("In-Reply-To")); len(ids) > 0 {
		m.inReplyTo = ids[0]
	}
	m.references = messageIDs(h.Get("References"))
	if len(m.references) > 50 {
		m.references = append(m.references[:1], m.references[len(m.references)-49:]...)
	}
	w := &emailWalk{m: m}
	if err := w.part(textproto.MIMEHeader(h), msg.Body, 0, false); err != nil {
		return nil, err
	}
	switch {
	case strings.TrimSpace(w.plain.String()) != "":
		m.body = w.plain.String()
	case w.html.Len() > 0:
		_, m.body = htmlToMarkdown(w.html.String(), &url.URL{Scheme: "https", Host: "example.invalid"})
	}
	m.body = strings.ReplaceAll(strings.ReplaceAll(m.body, "\r\n", "\n"), "\r", "\n")
	if len(m.body) > maxEmailBody {
		m.body = strings.ToValidUTF8(m.body[:maxEmailBody], "") + "\n\n…(the rest of the message is left out; it is in the original message)\n"
	}
	return m, nil
}

func headerText(v string) string {
	if d, err := emailWords.DecodeHeader(v); err == nil {
		v = d
	}
	v = strings.Join(strings.Fields(strings.ToValidUTF8(v, "�")), " ")
	if len(v) > 1000 {
		v = strings.ToValidUTF8(v[:1000], "")
	}
	return v
}

func addressList(parser *mail.AddressParser, v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	list, err := parser.ParseList(v)
	if err != nil {
		return []string{headerText(v)}
	}
	var out []string
	for i, a := range list {
		if i == 50 {
			out = append(out, fmt.Sprintf("and %d more", len(list)-50))
			break
		}
		out = append(out, addressText(a))
	}
	return out
}

func addressText(a *mail.Address) string {
	name := strings.Join(strings.Fields(a.Name), " ")
	if name == "" {
		return a.Address
	}
	return name + " <" + a.Address + ">"
}

// messageIDs reads the <id> values of a Message-ID, In-Reply-To or
// References header, without the angle brackets.
func messageIDs(v string) []string {
	var ids []string
	for len(ids) < 200 {
		start := strings.IndexByte(v, '<')
		if start < 0 {
			break
		}
		end := strings.IndexByte(v[start:], '>')
		if end < 0 {
			break
		}
		id := strings.TrimSpace(v[start+1 : start+end])
		if id != "" && len(id) <= 250 && !strings.ContainsAny(id, " \t\r\n<>") {
			ids = append(ids, id)
		}
		v = v[start+end+1:]
	}
	if len(ids) == 0 {
		if id := strings.TrimSpace(v); id != "" && len(id) <= 250 && !strings.ContainsAny(id, " \t\r\n<>") {
			ids = append(ids, id)
		}
	}
	return ids
}

// fromAddress is the sender's address in lower case, or "".
func (m *emailMessage) fromAddress() string {
	if m.from == nil {
		return ""
	}
	return strings.ToLower(m.from.Address)
}

// messageHash names a message by its ID: 12 hex digits.
func messageHash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:12]
}

// idHash names this message: by its Message-ID, else by its bytes.
func (m *emailMessage) idHash() string {
	if m.messageID != "" {
		return messageHash(m.messageID)
	}
	return m.rawHash[:12]
}

// parentID is the message this one answers.
func (m *emailMessage) parentID() string {
	if m.inReplyTo != "" {
		return m.inReplyTo
	}
	if n := len(m.references); n > 0 {
		return m.references[n-1]
	}
	return ""
}

type emailWalk struct {
	m           *emailMessage
	plain, html strings.Builder
	parts       int
	bytes       int
}

func (w *emailWalk) part(h textproto.MIMEHeader, body io.Reader, depth int, decoded bool) error {
	w.parts++
	if w.parts > maxEmailParts {
		w.m.skipped = append(w.m.skipped, "parts after the first "+strconv.Itoa(maxEmailParts))
		return nil
	}
	media, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil || media == "" {
		media, params = "text/plain", map[string]string{"charset": "us-ascii"}
	}
	media = strings.ToLower(media)
	if strings.HasPrefix(media, "multipart/") {
		if depth >= maxEmailDepth || params["boundary"] == "" {
			return nil
		}
		r := multipart.NewReader(body, params["boundary"])
		for {
			p, err := r.NextRawPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				// A message cut off part way keeps what came before.
				return nil
			}
			if err := w.part(p.Header, p, depth+1, false); err != nil {
				return err
			}
		}
	}
	data, err := io.ReadAll(io.LimitReader(contentReader(h, body, decoded), maxEmailBytes+1))
	if err != nil {
		data = nil
	}
	disposition, dparams, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	name := dparams["filename"]
	if name == "" {
		name = params["name"]
	}
	name = headerText(name)
	attachment := strings.EqualFold(disposition, "attachment") || name != ""
	switch {
	case !attachment && media == "text/plain":
		if w.plain.Len() > 0 {
			w.plain.WriteString("\n\n")
		}
		w.plain.WriteString(decodeCharset(data, params["charset"]))
		return nil
	case !attachment && media == "text/html":
		if w.html.Len() == 0 {
			w.html.WriteString(decodeCharset(data, params["charset"]))
		}
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	if name == "" {
		name = "part-" + strconv.Itoa(len(w.m.attachments)+1) + mediaExtension(media)
	}
	if len(w.m.attachments) >= maxEmailAttachments {
		w.m.skipped = append(w.m.skipped, name+" (more than "+strconv.Itoa(maxEmailAttachments)+" attachments)")
		return nil
	}
	if w.bytes+len(data) > maxEmailAttachmentBytes {
		w.m.skipped = append(w.m.skipped, fmt.Sprintf("%s (%s: the attachments would come to more than %d MB)", name, humanBytes(int64(len(data))), maxEmailAttachmentBytes>>20))
		return nil
	}
	w.bytes += len(data)
	w.m.attachments = append(w.m.attachments, emailAttachment{name: name, media: media, data: data})
	return nil
}

// contentReader undoes a part's Content-Transfer-Encoding.
func contentReader(h textproto.MIMEHeader, body io.Reader, decoded bool) io.Reader {
	if decoded {
		return body
	}
	switch strings.ToLower(strings.TrimSpace(h.Get("Content-Transfer-Encoding"))) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &base64Clean{r: body})
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	}
	return body
}

// base64Clean drops the line breaks and other bytes outside the base64
// alphabet that mail puts in encoded parts.
type base64Clean struct{ r io.Reader }

func (b *base64Clean) Read(p []byte) (int, error) {
	for {
		n, err := b.r.Read(p)
		j := 0
		for _, c := range p[:n] {
			if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=' {
				p[j] = c
				j++
			}
		}
		if j > 0 || err != nil {
			return j, err
		}
	}
}

func mediaExtension(media string) string {
	switch media {
	case "message/rfc822":
		return ".eml"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "application/pdf":
		return ".pdf"
	case "text/calendar":
		return ".ics"
	case "text/plain":
		return ".txt"
	case "text/html":
		return ".html"
	}
	return ".bin"
}

// safeFileName makes an attachment's name a file name: ASCII letters,
// digits, dot, hyphen and underscore, no leading dot, at most 80 bytes with
// its extension kept.
func safeFileName(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	dash := false
	for _, r := range name {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
		if !ok {
			if !dash {
				b.WriteByte('-')
			}
			dash = true
			continue
		}
		b.WriteRune(r)
		dash = false
	}
	out := strings.Trim(b.String(), "-. ")
	if len(out) > 80 {
		ext := path.Ext(out)
		if len(ext) > 12 {
			ext = ""
		}
		out = strings.Trim(out[:80-len(ext)], "-.") + ext
	}
	if out == "" || out == "message.eml" || strings.EqualFold(out, agentsFileName) {
		out = "attachment" + path.Ext(out)
	}
	return out
}

// slugText is a file-name slug of a subject: lower-case ASCII words joined
// by hyphens, at most 50 bytes.
func slugText(s string) string {
	var b strings.Builder
	dash := true
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 50 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "email"
	}
	return out
}
