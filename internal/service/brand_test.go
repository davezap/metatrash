package service

import (
	"net/http/httptest"
	"strings"
	"testing"

	"metatrash.com/metatrash/extension"
)

const testMailHeader = `<span style="font-weight:700;color:#172B36;">collum<span style="color:#147D78;">ii</span></span>`

const testWordmark = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10" role="img" aria-labelledby="t"><title id="t">Collumii</title><rect width="10" height="10" fill="#147D78"/></svg>`

func TestBrandText(t *testing.T) {
	text := "Your Metatrash account; Metatrash Sync; the Metatrash app; Metatrash-By: x; Metatrash/0.95; since Metatrash 0.50.0; metatrash.com"
	if got := brandText("Metatrash", text); got != text {
		t.Fatalf("Core's name changed text: %q", got)
	}
	want := "Your Collumii account; Metatrash Sync; the Metatrash app; Metatrash-By: x; Metatrash/0.95; since Metatrash 0.50.0; metatrash.com"
	if got := brandText("Collumii", text); got != want {
		t.Fatalf("brandText = %q", got)
	}
}

func TestValidBrand(t *testing.T) {
	good := extension.Brand{Name: "Collumii", HomeTitle: "Collumii — make room", Wordmark: testWordmark,
		ShareImage: []byte("\x89PNG\r\n\x1a\nrest"), MailHeader: testMailHeader, MailAccent: "#147D78", MailBackground: "#F7F4ED"}
	if err := validBrand(good); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]extension.Brand{
		"empty name":    {},
		"name newline":  {Name: "Coll\numii"},
		"name too long": {Name: strings.Repeat("a", 33)},
		"name symbol":   {Name: "Collumii<b>"},
		"title html":    {Name: "Collumii", HomeTitle: "<b>x</b>"},
		"no svg":        {Name: "Collumii", Wordmark: "<b>Collumii</b>"},
		"no title":      {Name: "Collumii", Wordmark: `<svg viewBox="0 0 1 1"></svg>`},
		"script":        {Name: "Collumii", Wordmark: `<svg><title>x</title><script>alert(1)</script></svg>`},
		"handler":       {Name: "Collumii", Wordmark: `<svg onload="x()"><title>x</title></svg>`},
		"style":         {Name: "Collumii", Wordmark: `<svg><title>x</title><rect style="fill:red"/></svg>`},
		"link":          {Name: "Collumii", Wordmark: `<svg><title>x</title><a href="https://x"><rect/></a></svg>`},
		"image":         {Name: "Collumii", Wordmark: `<svg><title>x</title><image href="https://x/y.png"/></svg>`},
		"css url":       {Name: "Collumii", Wordmark: `<svg><title>x</title><rect fill="url(https://x)"/></svg>`},
		"share not png": {Name: "Collumii", ShareImage: []byte("GIF89a")},
		"mail image":    {Name: "Collumii", MailHeader: `<img src="https://x/t.png">`},
		"mail link":     {Name: "Collumii", MailHeader: `<a href="https://x">x</a>`},
		"mail script":   {Name: "Collumii", MailHeader: `<script>x</script>`},
		"mail handler":  {Name: "Collumii", MailHeader: `<b onclick="x()">x</b>`},
		"mail css url":  {Name: "Collumii", MailHeader: `<b style="background:url(https://x)">x</b>`},
		"mail template": {Name: "Collumii", MailHeader: `{{.X}}`},
		"mail colour":   {Name: "Collumii", MailHeader: "<b>x</b>", MailAccent: "red"},
	} {
		if validBrand(b) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBrandOneModule(t *testing.T) {
	brand := func(extension.Host) (extension.Hooks, error) {
		return extension.Hooks{Name: "one", Brand: &extension.Brand{Name: "Collumii"}}, nil
	}
	other := func(extension.Host) (extension.Hooks, error) {
		return extension.Hooks{Name: "two", Brand: &extension.Brand{Name: "Other"}}, nil
	}
	s := &Service{modules: []extension.Module{brand, other}}
	if err := s.startExtensions(); err == nil || !strings.Contains(err.Error(), "two extensions brand") {
		t.Fatalf("two brands: %v", err)
	}
	s.closeExtensions()
	bad := func(extension.Host) (extension.Hooks, error) {
		return extension.Hooks{Name: "bad", Brand: &extension.Brand{Name: "Bad", Wordmark: "<svg><title>x</title><script/></svg>"}}, nil
	}
	s = &Service{modules: []extension.Module{bad}}
	if err := s.startExtensions(); err == nil {
		t.Fatal("unsafe wordmark accepted")
	}
	s.closeExtensions()
}

func TestBrandPagesAndAssets(t *testing.T) {
	s, _, _ := testAccounts(t)
	h := &httpAdapter{service: s, publicHost: "metatrash.com", publicOrigin: "https://metatrash.com"}
	render := func() string {
		w := httptest.NewRecorder()
		h.renderLogin(w, httptest.NewRequest("GET", "https://metatrash.com/login", nil), 200, loginView{})
		return w.Body.String()
	}
	core := render()
	if !strings.Contains(core, "<title>Sign in — Metatrash</title>") || !strings.Contains(core, textWordmark) || strings.Contains(core, "brand.css") {
		t.Fatalf("Core sign-in page changed:\n%s", core)
	}
	if s.brandName() != "Metatrash" || h.homeTitle() != siteHomeTitle || h.homeDescription() != siteHomeDescription {
		t.Fatal("Core home title or description changed")
	}

	s2, _, _ := testAccounts(t)
	s2.brand = &extension.Brand{Name: "Collumii", HomeTitle: "Collumii — make room for every mind", Description: "People and agents. Side by side.", Wordmark: testWordmark, CSS: []byte(".auth-brand{}"), Favicon: []byte("ico"), TouchIcon: []byte("png")}
	h = &httpAdapter{service: s2, publicHost: "metatrash.com", publicOrigin: "https://metatrash.com"}
	page := render()
	for _, want := range []string{"<title>Sign in — Collumii</title>", `<title id="t">Collumii</title>`, `/assets/signin.css"><link rel="stylesheet" href="/assets/brand.css">`} {
		if !strings.Contains(page, want) {
			t.Errorf("branded sign-in page lacks %q", want)
		}
	}
	if strings.Contains(page, "Metatrash") || strings.Contains(page, textWordmark) {
		t.Errorf("branded sign-in page still says Metatrash:\n%s", page)
	}
	if h.homeTitle() != "Collumii — make room for every mind" || h.homeDescription() != "People and agents. Side by side." {
		t.Error("brand home title or description not used")
	}
	s2.brand.ShareImage = []byte("\x89PNG share")
	for path, want := range map[string]string{"/favicon.ico": "ico", "/apple-touch-icon.png": "png", "/assets/brand.css": ".auth-brand{}", "/assets/share.png": "\x89PNG share"} {
		w := httptest.NewRecorder()
		h.serveAsset(w, httptest.NewRequest("GET", "https://metatrash.com"+path, nil))
		if w.Code != 200 || w.Body.String() != want {
			t.Errorf("%s: %d %q", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/assets/brand.css", "/assets/share.png"} {
		w := httptest.NewRecorder()
		(&httpAdapter{service: s}).serveAsset(w, httptest.NewRequest("GET", "https://metatrash.com"+path, nil))
		if w.Code != 404 {
			t.Errorf("Core serves %s: %d", path, w.Code)
		}
	}
	if got := totpURI(s2.brandName(), "a@b.c", []byte("12345678901234567890")); !strings.HasPrefix(got, "otpauth://totp/Collumii:a@b.c?") || !strings.HasSuffix(got, "&issuer=Collumii") {
		t.Errorf("totp URI %q", got)
	}
}

func TestBrandMail(t *testing.T) {
	s := &Service{brand: &extension.Brand{Name: "Collumii"}}
	cfg := s.mailConfig(accountConfig{SMTPFrom: "info@metatrash.com", SMTPHost: "mail.test"})
	subject, body := brandText(cfg.brand, "Your Metatrash login code"), brandText(cfg.brand, "Your Metatrash login code is: 1. Metatrash Sync is unchanged.")
	raw, err := mailMessage(cfg, "a@example.com", subject, body)
	if err != nil {
		t.Fatal(err)
	}
	msg := string(raw)
	for _, want := range []string{"From: Collumii <info@metatrash.com>", "Subject: Your Collumii login code", "Your Collumii login code is: 1. Metatrash Sync is unchanged."} {
		if !strings.Contains(msg, want) {
			t.Errorf("mail lacks %q:\n%s", want, msg)
		}
	}
	core, err := mailMessage((&Service{}).mailConfig(accountConfig{SMTPFrom: "info@metatrash.com"}), "a@example.com", "Hi", "x")
	if err != nil || !strings.Contains(string(core), "From: Metatrash <info@metatrash.com>") {
		t.Fatalf("Core mail From changed: %s %v", core, err)
	}
}

func TestBrandLinkPreviews(t *testing.T) {
	render := func(s *Service) string {
		h := &httpAdapter{service: s, publicHost: "metatrash.com", publicOrigin: "https://metatrash.com"}
		w := httptest.NewRecorder()
		h.writeAppPage(w, &appBuild{Entry: "assets/main.js"}, appPage{Space: "public", File: "about.md", Title: "about.md — Collumii public space", Address: "/spaces/public/"})
		return w.Body.String()
	}
	core := render(&Service{})
	for _, want := range []string{`<meta property="og:site_name" content="Metatrash">`, `<meta property="og:title" content="about.md — Collumii public space">`, `<meta name="twitter:card" content="summary">`} {
		if !strings.Contains(core, want) {
			t.Errorf("Core app page lacks %q", want)
		}
	}
	if strings.Contains(core, "og:image") {
		t.Error("Core app page has a share image")
	}
	page := render(&Service{brand: &extension.Brand{Name: "Collumii", Description: "People & agents.", ShareImage: []byte("\x89PNG")}})
	for _, want := range []string{`<meta property="og:site_name" content="Collumii">`, `<meta property="og:description" content="People &amp; agents.">`, `<meta property="og:image" content="https://metatrash.com/assets/share.png">`, `<meta name="twitter:card" content="summary_large_image">`} {
		if !strings.Contains(page, want) {
			t.Errorf("branded app page lacks %q", want)
		}
	}
}

func TestBrandHTMLMail(t *testing.T) {
	s := &Service{brand: &extension.Brand{Name: "Collumii", MailHeader: testMailHeader, MailAccent: "#147D78", MailBackground: "#F7F4ED"}}
	cfg := s.mailConfig(accountConfig{SMTPFrom: "info@metatrash.com", SMTPHost: "mail.test"})
	raw, err := mailMessage(cfg, "a@example.com", "Your Collumii login code", "Your code is: 123456 <b>\n\nSign in at https://metatrash.com/login.\nThanks.\n")
	if err != nil {
		t.Fatal(err)
	}
	msg := string(raw)
	if !strings.Contains(msg, "Content-Type: multipart/alternative; boundary=") || strings.Count(msg, "Content-Transfer-Encoding: quoted-printable") != 2 {
		t.Fatalf("not two quoted-printable parts:\n%s", msg)
	}
	html := mailHTML(cfg.mailLook, "Collumii", "Your code is: 123456 <b>\n\nSign in at https://metatrash.com/login.\nThanks.\n")
	for _, want := range []string{testMailHeader, "Your code is: 123456 &lt;b&gt;", `<a href="https://metatrash.com/login" style="color:#147D78;">https://metatrash.com/login</a>.<br>Thanks.`, "background:#F7F4ED"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML mail lacks %q:\n%s", want, html)
		}
	}
	plain, err := mailMessage((&Service{brand: &extension.Brand{Name: "Collumii"}}).mailConfig(accountConfig{SMTPFrom: "info@metatrash.com"}), "a@example.com", "Hi", "x")
	if err != nil || strings.Contains(string(plain), "multipart") || !strings.Contains(string(plain), "Content-Type: text/plain; charset=UTF-8") {
		t.Fatalf("brand without a mail header sent HTML: %s %v", plain, err)
	}
}

func TestLoginMailLink(t *testing.T) {
	body := loginMailBody("https://metatrash.com", "482913")
	for _, want := range []string{"Your Metatrash login code is: 482913", "\nhttps://metatrash.com/login#code=482913\n", "in the browser where you asked for the code"} {
		if !strings.Contains(body, want) {
			t.Errorf("login mail lacks %q:\n%s", want, body)
		}
	}
	// The code rides after #, so the link never sends it to the server.
	if strings.Contains(body, "?code=") {
		t.Error("code in a query string")
	}
	html := mailHTML(&mailLook{header: "<b>x</b>"}, "Collumii", body)
	if !strings.Contains(html, `<a href="https://metatrash.com/login#code=482913"`) {
		t.Errorf("HTML mail doesn't link the code:\n%s", html)
	}
}
