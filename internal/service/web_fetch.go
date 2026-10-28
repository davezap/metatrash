package service

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"metatrash.com/metatrash"
)

// web_fetch downloads with GET only, from public addresses only (checked as
// each connection is made, so after every redirect and whatever a name
// resolves to), on the standard ports, at most maxWebBytes within
// maxWebFetchTime.

const (
	maxWebBytes     = 25 << 20
	maxWebFetchTime = 30 * time.Second
	maxWebRedirects = 5
	maxWebURL       = 2048
)

// webPage is what a fetch brought back. Status is 200, or 304 when the site
// says the copy we hold (etag, lastModified) is still current.
type webPage struct {
	URL, ContentType   string
	Status             int
	Body               []byte
	ETag, LastModified string
}

// webFetcher downloads one URL. Tests use one that may reach httptest
// servers.
type webFetcher interface {
	fetch(ctx context.Context, u *url.URL, etag, lastModified string) (webPage, error)
}

type httpFetcher struct {
	client *http.Client
	// private allows any address and port, for tests only.
	private bool
}

func newWebFetcher(private bool) *httpFetcher {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	if !private {
		dialer.Control = webDialControl
	}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: maxWebFetchTime, MaxIdleConns: 10, IdleConnTimeout: 30 * time.Second}
	return &httpFetcher{private: private, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// webDialControl refuses connections to addresses that are not public.
func webDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !publicAddr(addr) {
		return fmt.Errorf("the address is not on the public internet")
	}
	return nil
}

// parseWebURL checks a URL the agent asked for and returns it normalised:
// http or https, no user name or password, no fragment.
func parseWebURL(raw string) (*url.URL, error) {
	bad := invalid("url must be a full http or https address, such as https://example.org/page.")
	if raw == "" || len(raw) > maxWebURL || strings.ContainsAny(raw, " \t\r\n") {
		return nil, bad
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return nil, bad
	}
	u.Scheme, u.Host, u.Fragment, u.RawFragment = strings.ToLower(u.Scheme), strings.ToLower(u.Host), "", ""
	if port := u.Port(); port == "80" && u.Scheme == "http" || port == "443" && u.Scheme == "https" {
		u.Host = u.Hostname()
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

// checkHost refuses names and ports that cannot be a public web site. The
// dialer checks the address itself.
func (f *httpFetcher) checkHost(u *url.URL) error {
	if f.private {
		return nil
	}
	host := u.Hostname()
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return problem(403, "forbidden_url", "Only the standard web ports (80 and 443) can be fetched.")
	}
	if addr, err := netip.ParseAddr(host); err == nil && !publicAddr(addr) || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || !strings.Contains(host, ".") {
		return problem(403, "forbidden_url", "Only addresses on the public internet can be fetched.")
	}
	return nil
}

func (f *httpFetcher) fetch(ctx context.Context, u *url.URL, etag, lastModified string) (webPage, error) {
	ctx, cancel := context.WithTimeout(ctx, maxWebFetchTime)
	defer cancel()
	for redirects := 0; ; redirects++ {
		if err := f.checkHost(u); err != nil {
			return webPage{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return webPage{}, invalid("The URL could not be requested.")
		}
		req.Header.Set("User-Agent", "Metatrash/"+metatrash.Version+" (folder actions; +https://metatrash.com)")
		// Markdown first (0.59.1): sites that keep it, Metatrash among
		// them, send the document itself rather than the page around it.
		req.Header.Set("Accept", "text/markdown,text/html;q=0.9,application/xhtml+xml;q=0.9,text/plain;q=0.8,*/*;q=0.7")
		if redirects == 0 {
			if etag != "" {
				req.Header.Set("If-None-Match", etag)
			}
			if lastModified != "" {
				req.Header.Set("If-Modified-Since", lastModified)
			}
		}
		resp, err := f.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return webPage{}, problem(504, "fetch_timeout", fmt.Sprintf("The site did not answer within %d seconds.", int(maxWebFetchTime/time.Second)))
			}
			if strings.Contains(err.Error(), "not on the public internet") {
				return webPage{}, problem(403, "forbidden_url", "Only addresses on the public internet can be fetched.")
			}
			return webPage{}, problem(502, "fetch_failed", "The site could not be reached.")
		}
		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			resp.Body.Close()
			next, err := u.Parse(resp.Header.Get("Location"))
			if err != nil || redirects == maxWebRedirects {
				return webPage{}, problem(502, "fetch_failed", "The site redirected too often or to a bad address.")
			}
			if u, err = parseWebURL(next.String()); err != nil {
				return webPage{}, problem(502, "fetch_failed", "The site redirected to an address that is not http or https.")
			}
			continue
		case http.StatusNotModified:
			resp.Body.Close()
			return webPage{URL: u.String(), Status: http.StatusNotModified}, nil
		case http.StatusOK:
		case http.StatusUnauthorized, http.StatusForbidden:
			resp.Body.Close()
			return webPage{}, problem(502, "fetch_failed", fmt.Sprintf("The site answered %d %s: the page needs a sign-in, and fetches have none. A file in a private Metatrash space can be read from the space itself.", resp.StatusCode, http.StatusText(resp.StatusCode)))
		default:
			resp.Body.Close()
			return webPage{}, problem(502, "fetch_failed", fmt.Sprintf("The site answered %d %s.", resp.StatusCode, http.StatusText(resp.StatusCode)))
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxWebBytes+1))
		resp.Body.Close()
		if err != nil {
			if ctx.Err() != nil {
				return webPage{}, problem(504, "fetch_timeout", fmt.Sprintf("The download took longer than %d seconds.", int(maxWebFetchTime/time.Second)))
			}
			return webPage{}, problem(502, "fetch_failed", "The download was cut off.")
		}
		if len(body) > maxWebBytes {
			return webPage{}, problem(413, "too_large", fmt.Sprintf("The file is over %d MB, the most web_fetch downloads.", maxWebBytes>>20))
		}
		page := webPage{URL: u.String(), Status: http.StatusOK, Body: body, ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}
		if media, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err == nil {
			page.ContentType = strings.ToLower(media)
		}
		if len(page.ETag) > 200 {
			page.ETag = ""
		}
		if len(page.LastModified) > 64 {
			page.LastModified = ""
		}
		return page, nil
	}
}

// Kinds of download.
const (
	webHTML      = "html"
	webText      = "text"
	webKindImage = "image"
	webPDF       = "pdf"
	webWord      = "word"
)

// webKind classifies a download by its Content-Type, sniffing when the site
// sent none or a generic one. It returns the kind, the media type and the
// file extension references use; kind "" means web_fetch does not take it.
func webKind(page webPage) (kind, media, ext string) {
	media = page.ContentType
	path := strings.ToLower(page.URL)
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if media == "" || media == "application/octet-stream" || media == "binary/octet-stream" {
		switch {
		case strings.HasSuffix(path, ".pdf"):
			media = "application/pdf"
		case strings.HasSuffix(path, ".docx"):
			media = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		case strings.HasSuffix(path, ".md"):
			media = "text/markdown"
		default:
			media, _, _ = mime.ParseMediaType(http.DetectContentType(page.Body))
		}
	}
	switch {
	case media == "text/html" || media == "application/xhtml+xml":
		return webHTML, media, "html"
	case media == "text/markdown" || media == "text/x-markdown":
		return webText, media, "md"
	case media == "text/csv":
		return webText, media, "csv"
	case media == "application/json" || strings.HasSuffix(media, "+json"):
		return webText, media, "json"
	case media == "text/xml" || media == "application/xml" || strings.HasSuffix(media, "+xml"):
		return webText, media, "xml"
	case strings.HasPrefix(media, "text/"):
		return webText, media, "txt"
	case media == "image/jpeg":
		return webKindImage, media, "jpg"
	case media == "image/png":
		return webKindImage, media, "png"
	case media == "image/gif":
		return webKindImage, media, "gif"
	case media == "image/webp":
		return webKindImage, media, "webp"
	case media == "application/pdf":
		return webPDF, media, "pdf"
	case media == "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return webWord, media, "docx"
	}
	return "", media, ""
}

// textKind reports whether a kind's original is text an agent can read.
func textKind(kind string) bool { return kind == webHTML || kind == webText }

// convertedKind reports whether a kind's text is a Markdown version of the
// original (web/<ref>.md beside web/<ref>.<ext>): web pages, and since
// 0.59.0 PDF and Word files.
func convertedKind(kind string) bool { return kind == webHTML || kind == webPDF || kind == webWord }

// decodeWebText makes a download's text valid UTF-8. Text that is valid
// UTF-8 is taken as UTF-8 whatever the site declared; anything else is read
// as Windows-1252 (a superset of ISO 8859-1), the usual encoding of older
// Western pages. Other encodings come out garbled.
func decodeWebText(body []byte) string {
	text := strings.TrimPrefix(string(body), "\uFEFF")
	if utf8.ValidString(text) {
		return text
	}
	var b strings.Builder
	for _, c := range []byte(text) {
		if c >= 0x80 && c < 0xA0 && cp1252[c-0x80] != 0 {
			b.WriteRune(cp1252[c-0x80])
		} else {
			b.WriteRune(rune(c))
		}
	}
	return b.String()
}

// cp1252 maps bytes 0x80 to 0x9F of Windows-1252.
var cp1252 = [32]rune{
	'€', 0, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0, 'Ž', 0,
	0, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0, 'ž', 'Ÿ',
}
