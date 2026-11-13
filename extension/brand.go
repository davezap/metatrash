package extension

// Brand names and dresses the service for an edition (Core 0.95.28). Without
// one the service is "Metatrash". At most one module may set it.
//
// Only the name people see for the service changes: page titles, the sign-in,
// app connection and provider return pages, emails, the authenticator-app
// entry and the site's icons. Tools and technical names keep theirs: Metatrash
// Sync, the GitHub app, HTTP headers, Git trailers, the User-Agent and what
// agents are told.
type Brand struct {
	// Name replaces "Metatrash" in those places: a letter, then letters,
	// digits and spaces, at most 32 (it goes into email subjects).
	Name string
	// HomeTitle and Description are the home page's title and meta
	// description; empty keeps Core's.
	HomeTitle, Description string
	// Wordmark is trusted markup shown at the top of the sign-in, app
	// connection and provider return pages instead of the text wordmark:
	// an inline <svg> with a <title>, and no script, style attribute,
	// event handler, link or foreignObject (those pages' CSP allows none).
	Wordmark string
	// CSS is served as /assets/brand.css and linked after signin.css on
	// those pages.
	CSS []byte
	// Favicon (ICO) and TouchIcon (PNG, 180 to 256 px) replace the site's
	// /favicon.ico and /apple-touch-icon.png; empty keeps Core's.
	Favicon, TouchIcon []byte
	// ShareImage is the picture link previews show (Open Graph and X
	// cards): a PNG of 1200 by 630 px, at most 1 MiB, served as
	// /assets/share.png. Empty: previews show the title and description only.
	ShareImage []byte
	// MailHeader is trusted markup at the top of the HTML version of every
	// email, such as the wordmark in styled text: inline styles only, no
	// script, link, image or form. Empty: emails stay plain text.
	MailHeader string
	// MailAccent and MailBackground are the HTML emails' link colour and
	// page colour, as #rrggbb; empty uses Core's neutral ones.
	MailAccent, MailBackground string
}
