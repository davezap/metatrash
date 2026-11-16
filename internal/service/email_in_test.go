package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"metatrash.com/metatrash/extension"
)

func TestEmailServiceConfig(t *testing.T) {
	good := []string{
		`{"services":[{"type":"email"}]}`,
		`{"services":[{"type":"email","allow":["no-reply@sns.amazonaws.com","example.com","*.amazon.com"],"block":["spam@example.com"],"address":3,"history":"keep"}]}`,
	}
	for _, text := range good {
		if _, err := parseFolderConfig(text); err != nil {
			t.Errorf("%s: %v", text, err)
		}
	}
	bad := map[string]string{
		`{"services":[{"type":"email","repo":"a/b"}]}`:                    "allow, block, address and history only",
		`{"services":[{"type":"email","allow":["Bob@Example.com"]}]}`:     "lower case",
		`{"services":[{"type":"email","allow":["not a domain"]}]}`:        "lower case",
		`{"services":[{"type":"email","block":["@example.com"]}]}`:        "lower case",
		`{"services":[{"type":"email","address":-1}]}`:                    "version",
		`{"services":[{"type":"email","history":"drop"}]}`:                `"strip"`,
		`{"services":[{"type":"github","repo":"a/b","allow":["x.com"]}]}`: "belong to an email service",
		`{"services":[{"type":"acknowledge","history":"keep"}]}`:          "acknowledge takes only files",
		`{"services":[{"type":"email"},{"type":"email"}]}`:                "its own name",
	}
	for text, want := range bad {
		if _, err := parseFolderConfig(text); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", text, err, want)
		}
	}
}

func TestSenderRules(t *testing.T) {
	rules := []string{"no-reply@sns.amazonaws.com", "example.com", "*.amazon.com"}
	for address, want := range map[string]bool{
		"no-reply@sns.amazonaws.com": true,
		"other@sns.amazonaws.com":    false,
		"bob@example.com":            true,
		"bob@mail.example.com":       false,
		"x@amazon.com":               true,
		"x@eu.mail.amazon.com":       true,
		"x@notamazon.com":            false,
		"":                           false,
	} {
		if got := senderMatches(rules, address); got != want {
			t.Errorf("%q: %v, want %v", address, got, want)
		}
	}
}

const testEmailPDF = "%PDF-1.4\n1 0 obj << >> endobj\ntrailer << >>\n%%EOF\n"

func testEmail(id, inReplyTo, subject, body string) []byte {
	var b strings.Builder
	b.WriteString("From: =?iso-8859-1?q?Andr=E9_Inspector?= <no-reply@sns.amazonaws.com>\r\n")
	b.WriteString("To: inbox@in.example.com, Dave <dave@example.com>\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: Sun, 15 Nov 2026 06:00:00 +0000\r\n")
	b.WriteString("Message-ID: <" + id + ">\r\n")
	if inReplyTo != "" {
		b.WriteString("In-Reply-To: <" + inReplyTo + ">\r\nReferences: <" + inReplyTo + ">\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"outer\"\r\n\r\n")
	b.WriteString("--outer\r\nContent-Type: multipart/alternative; boundary=\"alt\"\r\n\r\n")
	b.WriteString("--alt\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n") + "\r\n")
	b.WriteString("--alt\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>HTML version</p>\r\n--alt--\r\n")
	b.WriteString("--outer\r\nContent-Type: application/pdf; name=\"CVE report (high).pdf\"\r\nContent-Disposition: attachment; filename=\"CVE report (high).pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(testEmailPDF))
	for len(enc) > 60 {
		b.WriteString(enc[:60] + "\r\n")
		enc = enc[60:]
	}
	b.WriteString(enc + "\r\n--outer--\r\n")
	return []byte(b.String())
}

func TestParseEmail(t *testing.T) {
	m, err := parseEmail(testEmail("abc@example.com", "", "=?utf-8?b?Q1ZFLTIwMjYtMDAwMSBmb3VuZCDinIU=?=", "Finding: CVE-2026-0001 =E2=80=94 high\nPackage: openssl"))
	if err != nil {
		t.Fatal(err)
	}
	if m.from == nil || m.from.Name != "André Inspector" || m.fromAddress() != "no-reply@sns.amazonaws.com" {
		t.Errorf("from %+v", m.from)
	}
	if m.subject != "CVE-2026-0001 found ✅" || m.messageID != "abc@example.com" || len(m.to) != 2 || m.to[1] != "Dave <dave@example.com>" {
		t.Errorf("headers: %q %q %q", m.subject, m.messageID, m.to)
	}
	if !strings.Contains(m.body, "CVE-2026-0001 — high") || strings.Contains(m.body, "HTML version") {
		t.Errorf("body %q", m.body)
	}
	if len(m.attachments) != 1 || m.attachments[0].name != "CVE report (high).pdf" || string(m.attachments[0].data) != testEmailPDF || m.attachments[0].media != "application/pdf" {
		t.Fatalf("attachments %+v", m.attachments)
	}
	// HTML only becomes Markdown.
	html := "From: a@b.example\r\nSubject: hi\r\nContent-Type: text/html; charset=windows-1252\r\n\r\n<h1>Hello</h1><p>caf\xe9 <b>bold</b></p>"
	m, err = parseEmail([]byte(html))
	if err != nil || !strings.Contains(m.body, "# Hello") || !strings.Contains(m.body, "café") || m.idHash() != m.rawHash[:12] {
		t.Errorf("html body %q %v", m.body, err)
	}
	if _, err := parseEmail([]byte("not an email")); err == nil {
		t.Error("parsed junk")
	}
}

func TestStripQuoted(t *testing.T) {
	cases := map[string]string{
		"Thanks, fixed.\n\nOn Sun, 15 Nov 2026 at 06:00, Dave <d@example.com> wrote:\n> old\n> older\n":        "Thanks, fixed.\n",
		"Thanks.\n\nOn Sun, 15 Nov 2026 at 06:00, Dave Somebody\n<d@example.com> wrote:\n> old\n":              "Thanks.\n",
		"Done.\n\n-----Original Message-----\nFrom: x\n":                                                       "Done.\n",
		"Done.\n\n________________________________\nFrom: Dave\nSent: Sunday\nTo: x\nSubject: y\n\nold text\n": "Done.\n",
		"Done.\n\nFrom: Dave <d@example.com>\nSent: Sunday, 15 November 2026\nTo: x\n\nold\n":                  "Done.\n",
		"Agreed.\n\n> quoted\n>\n> more\n":                                                                     "Agreed.\n",
	}
	for in, want := range cases {
		if got, ok := stripQuoted(in); !ok || got != want {
			t.Errorf("%q: got %q", in, got)
		}
	}
	for _, in := range []string{"No quotes here.\n", "> only a quote\n", "Inline > not a quote\nmore\n"} {
		if got, ok := stripQuoted(in); ok || got != in {
			t.Errorf("%q changed to %q", in, got)
		}
	}
}

func emailWorld(t *testing.T) (*runnerWorld, *memoryObjects) {
	t.Helper()
	w, _, objects, _ := webWorld(t)
	w.s.emailIn = &emailSettings{domain: "in.example.com", key: bytes.Repeat([]byte{7}, 32), secret: []byte(strings.Repeat("s", 40))}
	return w, objects
}

func TestReceiveEmail(t *testing.T) {
	w, objects := emailWorld(t)
	ctx := context.Background()
	w.write("inbox/.metatrash.json", `{"services":[{"type":"email","block":["spam@example.com"]}],"actions":[{"name":"triage","on":"*.md","do":"Triage this finding."}]}`)
	address, err := w.s.folderEmailAddress(ctx, w.repo, "inbox/", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(address, w.repo.id+"-") || !strings.HasSuffix(address, "@in.example.com") || len(address) != 32+1+16+len("@in.example.com") {
		t.Fatalf("address %q", address)
	}
	if _, err := w.s.folderEmailAddress(ctx, w.repo, "hr/", ""); err == nil {
		t.Error("a folder without the service has an address")
	}

	first := testEmail("one@example.com", "", "CVE-2026-0001 in openssl", "Finding: CVE-2026-0001\nSeverity: HIGH")
	checks := extension.EmailChecks{SPF: "PASS", DKIM: "pass", DMARC: "pass", Spam: "pass", Virus: "pass"}
	receipt, err := w.s.receiveEmail(ctx, strings.ToUpper(address), first, checks)
	if err != nil {
		t.Fatal(err)
	}
	id := messageHash("one@example.com")
	if receipt.Duplicate || receipt.SpaceID != w.repo.id || !strings.HasPrefix(receipt.Path, "inbox/") || !strings.HasSuffix(receipt.Path, "-cve-2026-0001-in-openssl-"+id+".md") {
		t.Fatalf("receipt %+v", receipt)
	}
	text := w.read(receipt.Path)
	for _, want := range []string{
		"---\ntype: email\n", `from: "André Inspector \u003cno-reply@sns.amazonaws.com\u003e"`, `message_id: "one@example.com"`, `thread: "` + id + `"`,
		"checks: {spf: pass, dkim: pass, dmarc: pass, spam: pass, virus: pass}", "# CVE-2026-0001 in openssl", "Severity: HIGH",
		"- [CVE report (high).pdf](assets/" + id + "/CVE-report-high-.pdf)", "- Original message: [message.eml](assets/" + id + "/message.eml)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("file lacks %q:\n%s", want, text)
		}
	}
	pdf := w.do(ctx, "read", Input{Path: "inbox/assets/" + id + "/CVE-report-high-.pdf"}, "").(ReadResult)
	if pdf.File.Virtual == nil || pdf.File.Virtual.Type != "application/pdf" {
		t.Fatalf("attachment %+v", pdf.File)
	}
	if _, ok := objects.objects["files/"+w.repo.id+"/"+pdf.File.Virtual.SHA256]; !ok {
		t.Error("attachment not in the bucket")
	}
	if !w.exists("inbox/assets/" + id + "/message.eml") {
		t.Error("original not kept")
	}
	// The folder's action runs on the new message, not on its attachments.
	jobs := w.wait(1)
	if len(jobs) != 1 || jobs[0].File != receipt.Path || jobs[0].Action != "triage" {
		t.Fatalf("jobs %+v", jobs)
	}

	// The same message again is a duplicate.
	again, err := w.s.receiveEmail(ctx, address, first, checks)
	if err != nil || !again.Duplicate || again.Path != receipt.Path {
		t.Fatalf("again %+v %v", again, err)
	}

	// An agent sorts the first message; the reply still finds it, links it
	// and loses the quoted history.
	w.do(ctx, "move", Input{From: receipt.Path, To: "triage/high/" + receipt.Path[len("inbox/"):]}, "")
	reply := testEmail("two@example.com", "one@example.com", "Re: CVE-2026-0001 in openssl", "Patched in 3.4.1.\n\nOn Sun, 15 Nov 2026, Inspector wrote:\n> Finding: CVE-2026-0001\n> Severity: HIGH")
	second, err := w.s.receiveEmail(ctx, address, reply, extension.EmailChecks{})
	if err != nil {
		t.Fatal(err)
	}
	text = w.read(second.Path)
	if !strings.Contains(text, "Patched in 3.4.1.") || strings.Contains(text, "> Finding") || !strings.Contains(text, `reply_to_file: "../triage/high/`) || !strings.Contains(text, `thread: "`+id+`"`) || !strings.Contains(text, "history it quoted is left out") || strings.Contains(text, "checks:") {
		t.Errorf("reply:\n%s", text)
	}

	// Senders: block wins; an allow list refuses others and forged mail.
	spam := bytes.Replace(testEmail("three@example.com", "", "Win", "x"), []byte("no-reply@sns.amazonaws.com"), []byte("spam@example.com"), 1)
	if _, err := w.s.receiveEmail(ctx, address, spam, extension.EmailChecks{}); errorCode(err) != "sender_refused" {
		t.Errorf("blocked sender: %v", err)
	}
	if _, err := w.s.receiveEmail(ctx, address, testEmail("v@example.com", "", "x", "x"), extension.EmailChecks{Virus: "FAIL"}); errorCode(err) != "refused" {
		t.Errorf("virus: %v", err)
	}
	w.write("inbox/.metatrash.json", `{"services":[{"type":"email","allow":["sns.amazonaws.com"],"address":2}]}`)
	if _, err := w.s.receiveEmail(ctx, address, testEmail("four@example.com", "", "x", "x"), checks); errorCode(err) != "not_found" {
		t.Errorf("retired address: %v", err)
	}
	address2, err := w.s.folderEmailAddress(ctx, w.repo, "inbox/", "")
	if err != nil || address2 == address {
		t.Fatalf("new address %q %v", address2, err)
	}
	if _, err := w.s.receiveEmail(ctx, strings.Replace(address2, "@", "+cve@", 1), testEmail("five@example.com", "", "x", "x"), checks); err != nil {
		t.Errorf("allowed sender with +tag: %v", err)
	}
	if _, err := w.s.receiveEmail(ctx, address2, testEmail("six@example.com", "", "x", "x"), extension.EmailChecks{DMARC: "fail"}); errorCode(err) != "sender_refused" {
		t.Errorf("forged sender: %v", err)
	}
	other := bytes.Replace(testEmail("seven@example.com", "", "x", "x"), []byte("no-reply@sns.amazonaws.com"), []byte("bob@example.org"), 1)
	if _, err := w.s.receiveEmail(ctx, address2, other, checks); errorCode(err) != "sender_refused" {
		t.Errorf("sender not allowed: %v", err)
	}
	for _, bad := range []string{"x@in.example.com", w.repo.id + "-aaaaaaaaaaaaaaaa@in.example.com", strings.Replace(address2, "in.example.com", "other.example.com", 1), strings.Repeat("b", 32) + "-aaaaaaaaaaaaaaaa@in.example.com"} {
		if _, err := w.s.receiveEmail(ctx, bad, first, checks); errorCode(err) != "not_found" {
			t.Errorf("%s: %v", bad, err)
		}
	}
	// A GitHub folder cannot take email.
	w.do(asOwner(ctx, w.repo), "write", Input{Path: "site/.metatrash.json", Text: ptr(`{"services":[{"type":"github","repo":"a/b"},{"type":"email"}]}`)}, "invalid_request")
}

func errorCode(err error) string {
	if p, ok := err.(*Error); ok {
		return p.Code
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestEmailInboundHTTP(t *testing.T) {
	w, _ := emailWorld(t)
	w.write("inbox/.metatrash.json", `{"services":[{"type":"email"}]}`)
	address, err := w.s.folderEmailAddress(context.Background(), w.repo, "inbox/", "")
	if err != nil {
		t.Fatal(err)
	}
	h := &httpAdapter{service: w.s}
	post := func(secret, recipient string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/email/inbound", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("X-Metatrash-Recipient", recipient)
		r.Header.Set("X-Metatrash-Checks", "spf=pass; dkim=pass; dmarc=pass")
		rec := httptest.NewRecorder()
		if !h.serveEmailInbound(rec, r, "client") {
			t.Fatal("not served")
		}
		return rec
	}
	msg := testEmail("http@example.com", "", "Hello", "Body")
	if rec := post("wrong-secret", address, msg); rec.Code != 401 {
		t.Errorf("bad secret: %d", rec.Code)
	}
	rec := post(strings.Repeat("s", 40), address, msg)
	var receipt extension.EmailReceipt
	if rec.Code != 201 || json.Unmarshal(rec.Body.Bytes(), &receipt) != nil || !strings.HasPrefix(receipt.Path, "inbox/") {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(w.read(receipt.Path), "checks: {spf: pass, dkim: pass, dmarc: pass}") {
		t.Error("checks not recorded")
	}
	if rec := post(strings.Repeat("s", 40), address, msg); rec.Code != 200 {
		t.Errorf("duplicate: %d", rec.Code)
	}
	if rec := post(strings.Repeat("s", 40), "nobody@in.example.com", msg); rec.Code != 404 {
		t.Errorf("unknown address: %d", rec.Code)
	}
	// Without a secret there is no mail drop.
	w.s.emailIn.secret = nil
	if rec := post(strings.Repeat("s", 40), address, msg); rec.Code != 404 {
		t.Errorf("no secret: %d", rec.Code)
	}
	if h.serveEmailInbound(httptest.NewRecorder(), httptest.NewRequest("GET", "/other", nil), "c") {
		t.Error("served another path")
	}
	_ = http.MethodPost
}

func TestEmailConfigSettings(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	secret := filepath.Join(dir, "secret")
	os.WriteFile(key, []byte(strings.Repeat("ab", 32)+"\n"), 0o600)
	os.WriteFile(secret, []byte(strings.Repeat("x", 40)), 0o600)
	s, err := (&emailConfig{Enabled: true, Domain: "In.Example.com", KeyFile: key, SecretFile: secret}).settings()
	if err != nil || s.domain != "in.example.com" || len(s.key) != 32 || len(s.secret) != 40 {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := (&emailConfig{Domain: "localhost", KeyFile: key}).settings(); err == nil {
		t.Error("bad domain")
	}
	os.WriteFile(secret, []byte("short"), 0o600)
	if _, err := (&emailConfig{Domain: "in.example.com", KeyFile: key, SecretFile: secret}).settings(); err == nil {
		t.Error("short secret")
	}
	if _, err := (&emailConfig{Domain: "in.example.com", KeyFile: secret}).settings(); err == nil {
		t.Error("bad key")
	}
}

// TestSeveralEmailAddresses: a folder may have several email services, one
// per name, each with its own address; the unnamed one keeps its address.
func TestSeveralEmailAddresses(t *testing.T) {
	w, _ := emailWorld(t)
	ctx := context.Background()
	w.write("inbox/.metatrash.json", `{"services":[{"type":"email"}]}`)
	before, err := w.s.folderEmailAddress(ctx, w.repo, "inbox/", "")
	if err != nil {
		t.Fatal(err)
	}
	w.write("inbox/.metatrash.json", `{"services":[{"type":"email"},{"type":"email","name":"alerts","allow":["sns.amazonaws.com"]}]}`)
	plain, err := w.s.folderEmailAddress(ctx, w.repo, "inbox/", "")
	if err != nil || plain != before {
		t.Fatalf("unnamed address changed: %q %q %v", before, plain, err)
	}
	alerts, err := w.s.folderEmailAddress(ctx, w.repo, "inbox/", "alerts")
	if err != nil || alerts == plain {
		t.Fatalf("named address %q %v", alerts, err)
	}
	if _, err := w.s.folderEmailAddress(ctx, w.repo, "inbox/", "nope"); errorCode(err) != "not_found" {
		t.Errorf("unknown name: %v", err)
	}
	checks := extension.EmailChecks{DMARC: "pass"}
	if _, err := w.s.receiveEmail(ctx, alerts, testEmail("a1@example.com", "", "x", "x"), checks); err != nil {
		t.Errorf("named address: %v", err)
	}
	if _, err := w.s.receiveEmail(ctx, plain, testEmail("a2@example.com", "", "y", "y"), checks); err != nil {
		t.Errorf("unnamed address: %v", err)
	}
	for _, bad := range []string{
		`{"services":[{"type":"email","name":"a"},{"type":"email","name":"a"}]}`,
		`{"services":[{"type":"email"},{"type":"email"}]}`,
		`{"services":[{"type":"email","name":"Bad Name"}]}`,
		`{"services":[{"type":"acknowledge","name":"x"}]}`,
	} {
		if _, err := parseFolderConfig(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
