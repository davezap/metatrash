package service

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Pricing and checkout with Paddle (0.90.0), phase 3 of the payment rollout
// plan, test mode first. Paddle is the merchant of record: Paddle.js shows
// the prices and runs the checkout in an overlay, so card details never
// reach this server. There is no billing state yet (no webhooks, no
// entitlements): a completed checkout makes a Paddle subscription and
// nothing more.
//
//	/pricing    the plan and the storage add-on, with prices from Paddle
//	/checkout   the same page; Paddle's default payment link. A link Paddle
//	            makes for a transaction (?_ptxn=txn_...) opens its checkout here
//	/welcome    where a completed checkout lands
//
// The pages are the Viewer's website build's second entry (src/pricing.ts),
// told what to show in <meta name="metatrash-pricing">: the environment,
// the client-side token, the price IDs, the visitor's country when
// Cloudflare names it, and the signed-in person's email to fill in. Without
// a "paddle" part in accounts.json the pages aren't there (404).

// paddleConfig is the "paddle" part of accounts.json. Nothing has a default:
// a missing or wrong value stops the server at startup, so it never runs
// against the wrong Paddle account.
type paddleConfig struct {
	Enabled bool `json:"enabled"`
	// Environment is "sandbox" or "production".
	Environment string `json:"environment"`
	// ClientToken is a client-side token from Paddle (Developer tools >
	// Authentication): test_... for sandbox, live_... for production. It is
	// public, made for browser code; the server-side API key never belongs
	// here or anywhere in the page.
	ClientToken string `json:"clientToken"`
	// PlanPriceID is the Personal plan's monthly price, StoragePriceID the
	// extra storage (1 GB) add-on's: pri_... from the Paddle catalog.
	PlanPriceID    string `json:"planPriceId"`
	StoragePriceID string `json:"storagePriceId"`
}

var paddlePricePattern = regexp.MustCompile(`^pri_[a-z0-9]{26}$`)
var paddleTokenPattern = regexp.MustCompile(`^(test|live)_[A-Za-z0-9]{10,100}$`)

// paddleSettings checks the configuration. Errors name the setting.
func (c *paddleConfig) settings() (*paddleConfig, error) {
	out := *c
	prefix := ""
	switch out.Environment {
	case "sandbox":
		prefix = "test_"
	case "production":
		prefix = "live_"
	default:
		return nil, fmt.Errorf(`paddle.environment must be "sandbox" or "production"`)
	}
	if !paddleTokenPattern.MatchString(out.ClientToken) || !strings.HasPrefix(out.ClientToken, prefix) {
		return nil, fmt.Errorf("paddle.clientToken must be a %s client-side token (%s...)", out.Environment, prefix)
	}
	if !paddlePricePattern.MatchString(out.PlanPriceID) {
		return nil, fmt.Errorf("paddle.planPriceId must be a Paddle price ID (pri_...)")
	}
	if !paddlePricePattern.MatchString(out.StoragePriceID) || out.StoragePriceID == out.PlanPriceID {
		return nil, fmt.Errorf("paddle.storagePriceId must be another Paddle price ID (pri_...)")
	}
	return &out, nil
}

// pricingPage is what the page's script reads from its meta tag.
type pricingPage struct {
	Page        string            `json:"page"` // "pricing" or "welcome"
	Environment string            `json:"environment"`
	Token       string            `json:"token"`
	Prices      map[string]string `json:"prices"`
	// Country is the visitor's country (ISO 3166-1 alpha-2) when Cloudflare
	// named one; empty otherwise, and Paddle finds it from their IP address.
	Country string `json:"country,omitempty"`
	// Email is the signed-in person's address, filled in at checkout.
	Email    string `json:"email,omitempty"`
	SignedIn bool   `json:"signedIn"`
	// UserID goes into the checkout's custom data, so that a later webhook
	// (phase 3's entitlements) can tell whose subscription it is.
	UserID string `json:"userId,omitempty"`
	// Welcome is the address a completed checkout goes to; Home the site.
	Welcome string `json:"welcome"`
	Home    string `json:"home"`
}

var pricingTemplate = template.Must(template.New("pricing").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
{{if .NoIndex}}<meta name="robots" content="noindex, nofollow">
{{else}}<meta name="description" content="Metatrash pricing: one plan with a 30-day free trial, and more storage when you need it.">
{{end}}<meta name="metatrash-pricing" content="{{.Config}}">
<title>{{.Title}}</title>
<script src="{{.BasePath}}/assets/theme.js"></script>
{{range .Build.CSS}}<link rel="stylesheet" href="{{$.BasePath}}/app/{{.}}">
{{end}}</head>
<body>
<div id="pricing"></div>
<noscript><p>Prices and checkout need JavaScript.</p></noscript>
<script type="module" src="{{.BasePath}}/app/{{.Build.Entry}}"></script>
</body>
</html>
`))

// pricingCSP lets Paddle.js in: its script from cdn.paddle.com, the
// checkout in a frame from (sandbox-)buy.paddle.com, its API calls and its
// own inline styles. Nobody else's Markdown is on these pages, which is why
// they may have more than the app page's CSP (appCSP).
func pricingCSP() string {
	return "default-src 'none'; script-src 'self' https://cdn.paddle.com; style-src 'self' 'unsafe-inline' https://cdn.paddle.com; connect-src 'self' https://*.paddle.com; frame-src https://*.paddle.com; img-src 'self' https: data:; font-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
}

// visitorCountry is Cloudflare's CF-IPCountry header when it names a
// country: two capital letters other than XX (unknown) and T1 (Tor). The
// price shown follows it, but Paddle's checkout asks for the country again,
// and taxes and currency come from what is entered there.
func visitorCountry(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("CF-IPCountry"))
	if len(value) != 2 || value == "XX" || value == "T1" {
		return ""
	}
	for _, c := range value {
		if c < 'A' || c > 'Z' {
			return ""
		}
	}
	return value
}

func (h *httpAdapter) servePricing(w http.ResponseWriter, r *http.Request) bool {
	page := ""
	switch r.URL.Path {
	case "/pricing", "/checkout":
		page = "pricing"
	case "/welcome":
		page = "welcome"
	default:
		return false
	}
	accounts := h.service.accounts
	build := pricingBuildOnce()
	if accounts == nil || accounts.paddle == nil || build == nil {
		sendError(w, missing())
		return true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sendError(w, problem(405, "invalid_request", "Use GET or HEAD."))
		return true
	}
	paddle := accounts.paddle
	config := pricingPage{
		Page: page, Environment: paddle.Environment, Token: paddle.ClientToken,
		Prices:  map[string]string{"plan": paddle.PlanPriceID, "storage": paddle.StoragePriceID},
		Country: visitorCountry(r),
		Welcome: h.publicOrigin + h.basePath + "/welcome",
		Home:    h.basePath + "/",
	}
	lookup, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	user, signedIn, err := accounts.currentUser(lookup, cookieToken(r, h.sessionCookieName()))
	cancel()
	if err == nil && signedIn {
		config.SignedIn, config.Email, config.UserID = true, user.Email, user.ID
	}
	encoded, _ := json.Marshal(config)
	title := "Pricing — Metatrash"
	if page == "welcome" {
		title = "Welcome — Metatrash"
	}
	// The checkout page carries a transaction ID and the welcome page is
	// for one person: only /pricing is for search engines.
	noIndex := r.URL.Path != "/pricing"
	if noIndex {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
	// Paddle's checkout frame checks which site it is on: other sites get
	// this site's origin, never the page's address (it may hold _ptxn).
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", pricingCSP())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return true
	}
	_ = pricingTemplate.Execute(w, struct {
		Config, Title, BasePath string
		NoIndex                 bool
		Build                   *appBuild
	}{string(encoded), title, h.basePath, noIndex, build})
	return true
}
