package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"metatrash.com/metatrash/extension"
)

// Filing a received message (email_in.go): one Markdown file named
// <date>-<time>-<subject>-<id>.md in the folder, where id is 12 hex digits
// of the Message-ID, and its attachments and original message under
// assets/<id>/. The id lets a reply find the message it answers anywhere in
// the space, however agents sorted it, and stops one message being filed
// twice: agents keep the -<id>.md ending when they rename a message.

type emailStored struct {
	name string
	path string
	info VirtualInfo
}

// storeEmailFiles puts a message's attachments and the original message in
// the bucket, under assets/<id>/ of the folder.
func (s *Service) storeEmailFiles(ctx context.Context, target emailTarget, msg *emailMessage, raw []byte, id string) ([]emailStored, []string, error) {
	dir := target.folder + "assets/" + id + "/"
	var kept []emailStored
	var notes []string
	used := map[string]bool{"message.eml": true}
	for _, a := range msg.attachments {
		name := safeFileName(a.name)
		base, ext := strings.TrimSuffix(name, path.Ext(name)), path.Ext(name)
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%s-%d%s", base, n, ext)
		}
		used[strings.ToLower(name)] = true
		p := dir + name
		if !validPath(p) {
			notes = append(notes, a.name+" not kept: the folder is too deep for its name.")
			continue
		}
		info, err := s.storeVirtual(ctx, target.repo, p, a.data, a.media, "")
		if err != nil {
			return nil, nil, err
		}
		kept = append(kept, emailStored{name: a.name, path: p, info: info})
	}
	if p := dir + "message.eml"; validPath(p) && len(raw) <= maxVirtualBytes {
		info, err := s.storeVirtual(ctx, target.repo, p, raw, "message/rfc822", "")
		if err != nil {
			return nil, nil, err
		}
		kept = append(kept, emailStored{name: "message.eml", path: p, info: info})
	}
	for _, skipped := range msg.skipped {
		notes = append(notes, "Not kept: "+skipped+".")
	}
	return kept, notes, nil
}

// fileEmail commits a message's file and virtual files. It runs in the
// space's write queue.
func fileEmail(ctx context.Context, target emailTarget, msg *emailMessage, checks extension.EmailChecks, kept []emailStored, notes []string, id string, now time.Time) (any, error) {
	r := target.repo
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	receipt := extension.EmailReceipt{SpaceID: r.id}
	if existing := pathWithID(files, id); existing != "" {
		receipt.Path, receipt.Duplicate = existing, true
		return receipt, nil
	}
	parent := ""
	if pid := msg.parentID(); pid != "" {
		parent = pathWithID(files, messageHash(pid))
	}
	thread := ""
	if parent != "" {
		if b, err := r.blob(ctx, files[parent].Blob); err == nil {
			thread = frontmatterValue(string(b), "thread")
		}
	}
	if thread == "" {
		switch {
		case len(msg.references) > 0:
			thread = messageHash(msg.references[0])
		case msg.inReplyTo != "":
			thread = messageHash(msg.inReplyTo)
		default:
			thread = id
		}
	}
	body, stripped := msg.body, false
	if parent != "" && target.service.History != emailHistoryKeep {
		body, stripped = stripQuoted(body)
	}
	if stripped {
		cut := *msg
		cut.body = body
		msg = &cut
	}
	slug := slugText(msg.subject)
	name := ""
	for len(slug) > 0 || name == "" {
		name = target.folder + now.Format("2006-01-02-1504") + "-" + slug + "-" + id + ".md"
		if validPath(name) || slug == "" {
			break
		}
		slug = strings.TrimRight(slug[:len(slug)/2], "-")
	}
	if !validPath(name) {
		return nil, problem(409, "conflict", "The folder is too deep to file email in.")
	}
	github, err := r.githubFolders(ctx, files, nil)
	if err != nil {
		return nil, err
	}
	if githubFolderAround(github, target.folder) != "" {
		return nil, problem(409, "conflict", "The folder mirrors a GitHub repository, so it cannot take email.")
	}
	readOnly, err := r.readOnlyRules(ctx, files, nil)
	if err != nil {
		return nil, err
	}
	who := writerOf(ctx)
	if level := fileLock(readOnly, name); blocks(level, who) {
		return nil, readOnlyFile(name, level)
	}
	if virtualSpaceBytes(files, "")+storedBytes(kept) > maxVirtualPerSpace {
		notes = append(notes, fmt.Sprintf("Attachments not kept: they would take the space's virtual files over %d GB.", maxVirtualPerSpace>>30))
		kept = nil
	}
	var paths []string
	texts := map[string]string{}
	var changes []commitChange
	var keptFiles []emailStored
	for _, k := range kept {
		if _, taken := files[k.path]; taken || blocks(fileLock(readOnly, k.path), who) {
			notes = append(notes, k.name+" not kept: its place is taken or locked.")
			continue
		}
		keptFiles = append(keptFiles, k)
	}
	text := renderEmail(msg, checks, keptFiles, notes, name, parent, thread, stripped, now)
	if int64(len(text)) > r.limits.MaxFileBytes {
		return nil, problem(413, "payload_too_large", "The message's text is larger than a file can be.")
	}
	all := append([]emailStored{{path: name}}, keptFiles...)
	for _, k := range all {
		for p := range files {
			if strings.HasPrefix(k.path, p+"/") {
				return nil, problem(409, "conflict", p+" is a file where the email's folder should be.")
			}
		}
	}
	fileID, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	files[name] = record{File: File{ID: fileID, Path: name, Bytes: int64(len(text)), Deletable: true}}
	texts[name] = text
	changes = append(changes, commitChange{fileID, "create", name})
	paths = append(paths, name)
	for _, k := range keptFiles {
		vid, err := randomHex(16)
		if err != nil {
			return nil, err
		}
		info := k.info
		pointer := info.pointer()
		files[k.path] = record{File: File{ID: vid, Path: k.path, Bytes: int64(len(pointer)), Deletable: true, Virtual: &info}}
		texts[k.path] = pointer
		changes = append(changes, commitChange{vid, "create", k.path})
		paths = append(paths, k.path)
	}
	added, err := addFolderFiles(files, texts, github, r.rootAgents, paths...)
	if err != nil {
		return nil, err
	}
	changes = append(changes, added...)
	subject := msg.subject
	if len(subject) > 80 {
		subject = strings.ToValidUTF8(subject[:80], "") + "…"
	}
	if subject == "" {
		subject = "(no subject)"
	}
	if _, err := r.commitFiles(ctx, head, files, texts, nil, changes, "Email: "+subject); err != nil {
		return nil, err
	}
	receipt.Path = name
	return receipt, nil
}

func storedBytes(kept []emailStored) int64 {
	var n int64
	for _, k := range kept {
		n += k.info.Size
	}
	return n
}

// pathWithID finds the message file whose name ends in -<id>.md.
func pathWithID(files map[string]record, id string) string {
	suffix := "-" + id + ".md"
	best := ""
	for p, f := range files {
		if f.Virtual == nil && strings.HasSuffix(p, suffix) && (best == "" || p < best) {
			best = p
		}
	}
	return best
}

// frontmatterValue reads a top-level key of a file's front matter written
// by renderEmail, or "".
func frontmatterValue(text, key string) string {
	if !strings.HasPrefix(text, "---\n") {
		return ""
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(text[4:4+end], "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || k != key {
			continue
		}
		v = strings.TrimSpace(v)
		var s string
		if json.Unmarshal([]byte(v), &s) == nil {
			return s
		}
		return v
	}
	return ""
}

// relativeLink is a Markdown link target from file from to file to.
func relativeLink(from, to string) string {
	fromParts := strings.Split(fileFolder(from), "/")
	toParts := strings.Split(to, "/")
	fromParts = fromParts[:len(fromParts)-1]
	i := 0
	for i < len(fromParts) && i < len(toParts)-1 && fromParts[i] == toParts[i] {
		i++
	}
	out := strings.Repeat("../", len(fromParts)-i) + strings.Join(toParts[i:], "/")
	return (&urlPath{out}).escaped()
}

type urlPath struct{ p string }

func (u *urlPath) escaped() string {
	var b strings.Builder
	for _, c := range []byte(u.p) {
		switch {
		case c == ' ':
			b.WriteString("%20")
		case c == '(' || c == ')':
			fmt.Fprintf(&b, "%%%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func jsonText(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func markdownEscape(s string) string {
	return strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "<", "&lt;", ">", "&gt;", "`", "\\`").Replace(s)
}

// renderEmail is a message's Markdown file.
func renderEmail(msg *emailMessage, checks extension.EmailChecks, kept []emailStored, notes []string, file, parent, thread string, stripped bool, now time.Time) string {
	var b strings.Builder
	b.WriteString("---\ntype: email\n")
	field := func(key, value string) {
		if value != "" {
			b.WriteString(key + ": " + jsonText(value) + "\n")
		}
	}
	list := func(key string, values []string) {
		if len(values) == 0 {
			return
		}
		b.WriteString(key + ":\n")
		for _, v := range values {
			b.WriteString("  - " + jsonText(v) + "\n")
		}
	}
	from := msg.fromText
	if msg.from != nil {
		from = addressText(msg.from)
	}
	field("from", from)
	list("to", msg.to)
	list("cc", msg.cc)
	field("subject", msg.subject)
	if !msg.date.IsZero() {
		field("date", msg.date.Format(time.RFC3339))
	}
	field("received", now.Format(time.RFC3339))
	field("message_id", msg.messageID)
	field("in_reply_to", msg.inReplyTo)
	if parent != "" {
		field("reply_to_file", relativeLink(file, parent))
	}
	field("thread", thread)
	var names []string
	for _, k := range kept {
		if k.name != "message.eml" {
			names = append(names, k.name)
		}
	}
	list("attachments", names)
	var verdicts []string
	for _, v := range []struct{ k, v string }{{"spf", checks.SPF}, {"dkim", checks.DKIM}, {"dmarc", checks.DMARC}, {"spam", checks.Spam}, {"virus", checks.Virus}} {
		if v.v != "" {
			verdicts = append(verdicts, v.k+": "+v.v)
		}
	}
	if len(verdicts) > 0 {
		b.WriteString("checks: {" + strings.Join(verdicts, ", ") + "}\n")
	}
	b.WriteString("---\n\n")
	subject := msg.subject
	if subject == "" {
		subject = "(no subject)"
	}
	b.WriteString("# " + markdownEscape(subject) + "\n\n")
	b.WriteString("**From:** " + markdownEscape(from))
	if len(msg.to) > 0 {
		b.WriteString("  \n**To:** " + markdownEscape(strings.Join(msg.to, ", ")))
	}
	if !msg.date.IsZero() {
		b.WriteString("  \n**Date:** " + msg.date.Format("Mon 2 Jan 2006 15:04 -0700"))
	}
	b.WriteString("\n\n")
	if parent != "" {
		b.WriteString("_In reply to [an earlier message](" + relativeLink(file, parent) + ")")
		if stripped {
			b.WriteString("; the history it quoted is left out (it is in the original message)")
		}
		b.WriteString("._\n\n")
	}
	body := strings.TrimSpace(msg.body)
	if body == "" {
		body = "_(The message has no text.)_"
	}
	b.WriteString(body + "\n")
	if len(kept) > 0 || len(notes) > 0 {
		b.WriteString("\n## Attachments\n\n")
		for _, k := range kept {
			if k.name == "message.eml" {
				continue
			}
			b.WriteString("- [" + markdownEscape(k.name) + "](" + relativeLink(file, k.path) + ") (" + humanBytes(k.info.Size) + ")\n")
		}
		for _, k := range kept {
			if k.name == "message.eml" {
				b.WriteString("- Original message: [message.eml](" + relativeLink(file, k.path) + ")\n")
			}
		}
		for _, n := range notes {
			b.WriteString("- " + markdownEscape(n) + "\n")
		}
	}
	return b.String()
}

var (
	quoteOnWrote    = regexp.MustCompile(`(?i)^\s*(on|le|am|el|il|op)\s.{0,300}(wrote|a écrit|schrieb|escribió|ha scritto|schreef)\s*:\s*$`)
	quoteOnStart    = regexp.MustCompile(`(?i)^\s*on\s.{0,300}$`)
	quoteWroteEnd   = regexp.MustCompile(`(?i)^.{0,200}wrote\s*:\s*$`)
	quoteOriginal   = regexp.MustCompile(`(?i)^\s*-{2,}\s*(original message|forwarded message)\s*-{2,}\s*$`)
	quoteFromHeader = regexp.MustCompile(`(?i)^\s*\**\s*from\s*:\**\s*\S`)
	quoteSentHeader = regexp.MustCompile(`(?i)^\s*\**\s*(sent|date)\s*:\**\s*\S`)
	quoteRule       = regexp.MustCompile(`^\s*_{10,}\s*$`)
)

// stripQuoted cuts the earlier messages a reply quotes: from the first
// "On ... wrote:", "-----Original Message-----" or Outlook From:/Sent:
// header block, or the block of > lines that ends the message. It reports
// whether it cut anything; a message that would be left empty is kept.
func stripQuoted(body string) (string, bool) {
	lines := strings.Split(body, "\n")
	cut := -1
	for i, line := range lines {
		switch {
		case quoteOnWrote.MatchString(line), quoteOriginal.MatchString(line):
			cut = i
		case quoteOnStart.MatchString(line) && i+1 < len(lines) && quoteWroteEnd.MatchString(lines[i+1]) && len(line) < 300:
			cut = i
		case quoteRule.MatchString(line) && i+1 < len(lines) && quoteFromHeader.MatchString(nextText(lines[i+1:])):
			cut = i
		case quoteFromHeader.MatchString(line):
			for j := i + 1; j < len(lines) && j <= i+5; j++ {
				if quoteSentHeader.MatchString(lines[j]) {
					cut = i
					break
				}
			}
		}
		if cut >= 0 {
			break
		}
	}
	if cut < 0 {
		// A trailing block of quoted lines.
		start := -1
		for i := len(lines) - 1; i >= 0; i-- {
			t := strings.TrimSpace(lines[i])
			if t == "" {
				continue
			}
			if !strings.HasPrefix(t, ">") {
				break
			}
			start = i
		}
		cut = start
	}
	if cut <= 0 {
		return body, false
	}
	kept := strings.TrimRight(strings.Join(lines[:cut], "\n"), " \t\n")
	if strings.TrimSpace(kept) == "" {
		return body, false
	}
	return kept + "\n", true
}

func nextText(lines []string) string {
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			return l
		}
	}
	return ""
}
