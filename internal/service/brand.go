package service

import (
	"bytes"
	"fmt"
	"html/template"
	"regexp"
	"strings"

	"metatrash.com/metatrash/extension"
)

// The service's name for people, and an edition's Brand (extension.Brand,
// 0.95.28). Core is "Metatrash"; Company sets its own through a module.

const coreBrandName = "Metatrash"

var brandName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 ]{0,31}$`)

// unsafeWordmark finds what a wordmark may not carry: the sign-in pages'
// CSP would block it, or it would run or fetch something.
var unsafeWordmark = regexp.MustCompile(`(?i)<\s*(script|style|foreignobject|iframe|object|embed|image|use|a)\b|\son[a-z]+\s*=|\sstyle\s*=|\s(xlink:)?href\s*=|javascript:|url\s*\(`)

func validBrand(b extension.Brand) error {
	if !brandName.MatchString(b.Name) || strings.TrimSpace(b.Name) != b.Name || strings.Contains(b.Name, "  ") {
		return fmt.Errorf("name must be a letter then letters, digits and single spaces, at most 32")
	}
	for _, text := range []string{b.HomeTitle, b.Description} {
		if strings.ContainsAny(text, "<>\r\n") || len(text) > 300 {
			return fmt.Errorf("home title and description are one line of text, at most 300 bytes")
		}
	}
	if w := strings.TrimSpace(b.Wordmark); w != "" {
		if !strings.HasPrefix(w, "<svg") || !strings.HasSuffix(w, "</svg>") || !strings.Contains(w, "<title") || unsafeWordmark.MatchString(w) || len(w) > 64<<10 {
			return fmt.Errorf("wordmark must be one inline <svg> with a <title> and no script, style, event handler, link or foreignObject")
		}
	}
	if len(b.CSS) > 256<<10 || len(b.Favicon) > 256<<10 || len(b.TouchIcon) > 256<<10 {
		return fmt.Errorf("css and icons are at most 256 KiB each")
	}
	if len(b.ShareImage) > 0 && (len(b.ShareImage) > 1<<20 || !bytes.HasPrefix(b.ShareImage, []byte("\x89PNG\r\n\x1a\n"))) {
		return fmt.Errorf("share image is a PNG of at most 1 MiB")
	}
	if h := b.MailHeader; h != "" && (len(h) > 8<<10 || unsafeMailHeader.MatchString(h)) {
		return fmt.Errorf("mail header is at most 8 KiB of markup with no script, link, image, form or url()")
	}
	for _, c := range []string{b.MailAccent, b.MailBackground} {
		if c != "" && !mailColour.MatchString(c) {
			return fmt.Errorf("mail colours are #rrggbb")
		}
	}
	return nil
}

// unsafeMailHeader finds what a mail header may not carry: anything that
// runs, fetches or leads somewhere. Mail clients strip most of it anyway.
var unsafeMailHeader = regexp.MustCompile(`(?i)<\s*(script|style|iframe|object|embed|img|image|svg|a|form|input|link|meta|base)\b|\son[a-z]+\s*=|\s(xlink:)?(href|src)\s*=|javascript:|url\s*\(|@import|\{\{`)

var mailColour = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// brandName is the service's name for people: the brand's, else Metatrash.
func (s *Service) brandName() string {
	if s != nil && s.brand != nil {
		return s.brand.Name
	}
	return coreBrandName
}

// brandKeeps are "Metatrash" phrases that name a tool or a technical thing,
// not the service, so a brand leaves them alone.
var brandKeeps = []string{
	"Metatrash Sync", "Metatrash app", "Metatrash GitHub app", "Metatrash runner",
	"Metatrash refresh", "Metatrash website", "Metatrash-", "Metatrash/", "Metatrash 0.",
}

// brandText puts name for "Metatrash" where the text names the service.
// With Core's own name it returns text unchanged.
func brandText(name, text string) string {
	if name == "" || name == coreBrandName || !strings.Contains(text, coreBrandName) {
		return text
	}
	pairs := make([]string, 0, 2*len(brandKeeps)+2)
	for _, keep := range brandKeeps {
		pairs = append(pairs, keep, keep)
	}
	return strings.NewReplacer(append(pairs, coreBrandName, name)...).Replace(text)
}

// pageTemplates are the sign-in, app connection and provider return pages.
type pageTemplates struct{ login, oauth, github *template.Template }

// pages are those templates in the service's brand: Core's own without one.
func (s *Service) pages() pageTemplates {
	core := pageTemplates{loginTemplate, oauthTemplate, githubTemplate}
	if s == nil {
		return core
	}
	s.brandOnce.Do(func() {
		s.brandPages = core
		if s.brand == nil {
			return
		}
		b := *s.brand
		parse := func(name, source string) *template.Template {
			return template.Must(template.New(name).Parse(brandPage(b, source)))
		}
		s.brandPages = pageTemplates{parse("login", loginHTML), parse("oauth", oauthHTML), parse("github", githubHTML)}
	})
	return s.brandPages
}

const textWordmark = `metatrash<span>.</span>`
const signinStylesheet = `<link rel="stylesheet" href="{{.BasePath}}/assets/signin.css">`

// brandPage is a page template's source in brand b: the name in its fixed
// text, the wordmark at the top and brand.css after signin.css. Only the
// template's own text changes, never what it fills in.
func brandPage(b extension.Brand, source string) string {
	source = brandText(b.Name, source)
	wordmark := strings.ToLower(b.Name) + "<span>.</span>"
	if w := strings.TrimSpace(b.Wordmark); w != "" {
		// Template actions in trusted markup would be run: there are none
		// in a valid wordmark, and {{ is escaped to be sure.
		wordmark = strings.ReplaceAll(w, "{{", "{{`{{`}}")
	}
	source = strings.ReplaceAll(source, textWordmark, wordmark)
	if len(b.CSS) > 0 {
		source = strings.ReplaceAll(source, signinStylesheet, signinStylesheet+`<link rel="stylesheet" href="{{.BasePath}}/assets/brand.css">`)
	}
	return source
}

// mailConfig is cfg with the service's brand, for sendMail: its name, and
// its HTML email look when it has a mail header (0.95.28).
func (s *Service) mailConfig(cfg accountConfig) accountConfig {
	cfg.brand = s.brandName()
	if b := s.brand; b != nil && b.MailHeader != "" {
		cfg.mailLook = &mailLook{header: b.MailHeader, accent: b.MailAccent, background: b.MailBackground}
	}
	return cfg
}
