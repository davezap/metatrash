package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPlanPrice, testStoragePrice = "pri_01m6d01aaxs5w1g9myee7m5cdp", "pri_01m6d01afz75y4er9q4mb5r4sn"
const testPlanYearlyPrice, testStorageYearlyPrice = "pri_01m6f0kf0v20v39czmf3x3h9ms", "pri_01m6f0kf28p4pdvgkjnpbt4e71"

// The paddle settings have no defaults: a missing environment, or a token for
// the other environment, stops the server rather than reach the wrong account.
func TestPaddleSettings(t *testing.T) {
	good := paddleConfig{Enabled: true, Environment: "sandbox", ClientToken: "test_ebbff7d957dece400c2e9677157", PlanPriceID: testPlanPrice, StoragePriceID: testStoragePrice}
	if _, err := good.settings(); err != nil {
		t.Fatal(err)
	}
	live := good
	live.Environment, live.ClientToken = "production", "live_0123456789abcdef0123456789"
	if _, err := live.settings(); err != nil {
		t.Fatal(err)
	}
	yearly := good
	yearly.PlanYearlyPriceID, yearly.StorageYearlyPriceID = testPlanYearlyPrice, testStorageYearlyPrice
	if c, err := yearly.settings(); err != nil {
		t.Fatal(err)
	} else if p := c.pagePrices(); len(p) != 4 || p["planYearly"] != testPlanYearlyPrice || p["storageYearly"] != testStorageYearlyPrice {
		t.Fatalf("yearly page prices: %v", p)
	}
	if p := good.pagePrices(); len(p) != 2 {
		t.Fatalf("monthly page prices: %v", p)
	}
	for name, change := range map[string]func(*paddleConfig){
		"yearly plan only":      func(c *paddleConfig) { c.StorageYearlyPriceID = "" },
		"yearly storage only":   func(c *paddleConfig) { c.PlanYearlyPriceID = "" },
		"yearly same as month":  func(c *paddleConfig) { c.PlanYearlyPriceID = c.PlanPriceID },
		"yearly storage twice":  func(c *paddleConfig) { c.StorageYearlyPriceID = c.PlanYearlyPriceID },
		"yearly not a price id": func(c *paddleConfig) { c.StorageYearlyPriceID = "pro_01m6d01aerc1b5j46rgn1e7awx" },
	} {
		c := yearly
		change(&c)
		if _, err := c.settings(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, change := range map[string]func(*paddleConfig){
		"no environment":     func(c *paddleConfig) { c.Environment = "" },
		"other environment":  func(c *paddleConfig) { c.Environment = "live" },
		"no token":           func(c *paddleConfig) { c.ClientToken = "" },
		"live token sandbox": func(c *paddleConfig) { c.ClientToken = "live_0123456789abcdef0123456789" },
		"test token live":    func(c *paddleConfig) { c.Environment = "production" },
		"api key":            func(c *paddleConfig) { c.ClientToken = "pdl_sdbx_apikey_01abc" },
		"no plan":            func(c *paddleConfig) { c.PlanPriceID = "" },
		"bad storage":        func(c *paddleConfig) { c.StoragePriceID = "pro_01m6d01aerc1b5j46rgn1e7awx" },
		"same prices":        func(c *paddleConfig) { c.StoragePriceID = c.PlanPriceID },
	} {
		c := good
		change(&c)
		if _, err := c.settings(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The webhook secret and API key (0.91.0) come from files, each checked.
	dir := t.TempDir()
	file := func(name, value string) string {
		path := filepath.Join(dir, name)
		_ = os.WriteFile(path, []byte(value), 0600)
		return path
	}
	withFiles := good
	withFiles.WebhookSecretFile = file("secret", "pdl_ntfset_01m6dd7pjhh0y7yymvjmse3xcw_8OCfAd02lErzJIdVkDAiC8ZuJLQaDrIw\n")
	withFiles.APIKeyFile = file("key", "pdl_sdbx_apikey_01abcdefghijklmnopqrstuvwxyz_TestOnlyKey")
	if c, err := withFiles.settings(); err != nil || c.webhookSecret == "" || c.api() == nil || c.api().base != "https://sandbox-api.paddle.com" {
		t.Fatalf("files: %v", err)
	}
	if good.api() != nil {
		t.Fatal("API without a key")
	}
	for name, change := range map[string]func(*paddleConfig){
		"missing secret file": func(c *paddleConfig) { c.WebhookSecretFile = filepath.Join(dir, "nope") },
		"not a secret":        func(c *paddleConfig) { c.WebhookSecretFile = file("bad-secret", "hunter2") },
		"live key in sandbox": func(c *paddleConfig) {
			c.APIKeyFile = file("live", "pdl_live_apikey_01abcdefghijklmnopqrstuvwxyz_TestOnlyKey")
		},
		"client token as key": func(c *paddleConfig) { c.APIKeyFile = file("token", "test_ebbff7d957dece400c2e9677157") },
		"two values": func(c *paddleConfig) {
			c.APIKeyFile = file("two", "pdl_sdbx_apikey_01abcdefghijklmnopqrstuvwxyz_A pdl_sdbx_apikey_01abcdefghijklmnopqrstuvwxyz_B")
		},
	} {
		c := withFiles
		change(&c)
		if _, err := c.settings(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestVisitorCountry(t *testing.T) {
	for header, want := range map[string]string{"NZ": "NZ", " DE ": "DE", "": "", "XX": "", "T1": "", "nz": "", "NZL": "", "N1": ""} {
		r := httptest.NewRequest("GET", "https://metatrash.com/pricing", nil)
		if header != "" {
			r.Header.Set("CF-IPCountry", header)
		}
		if got := visitorCountry(r); got != want {
			t.Errorf("%q: %q, want %q", header, got, want)
		}
	}
}

// Each entry gets its own script and every stylesheet it needs, through the
// chunks the two entries share.
func TestAppManifestEntries(t *testing.T) {
	m := appManifest{
		"_shared.js":     {File: "assets/shared.js", CSS: []string{"assets/shared.css"}},
		"src/main.ts":    {File: "assets/main.js", IsEntry: true, Imports: []string{"_shared.js"}, CSS: []string{"assets/main.css"}},
		"src/pricing.ts": {File: "assets/pricing.js", IsEntry: true, Imports: []string{"_shared.js"}, CSS: []string{"assets/pricing.css"}},
	}
	main, pricing := m.entry("src/main.ts"), m.entry("src/pricing.ts")
	if main == nil || main.Entry != "assets/main.js" || strings.Join(main.CSS, ",") != "assets/shared.css,assets/main.css" {
		t.Fatalf("main: %+v", main)
	}
	if pricing == nil || pricing.Entry != "assets/pricing.js" || strings.Join(pricing.CSS, ",") != "assets/shared.css,assets/pricing.css" {
		t.Fatalf("pricing: %+v", pricing)
	}
	if m.entry("_shared.js") != nil || m.entry("src/missing.ts") != nil {
		t.Fatal("not an entry")
	}
}

func TestPricingPagesAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	get := func(path string, headers ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://metatrash.com"+path, nil)
		r.RemoteAddr = "192.0.2.14:1"
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		res := httptest.NewRecorder()
		w.h.ServeHTTP(res, r)
		return res
	}
	// Off until accounts.json has a "paddle" part.
	if res := get("/pricing"); res.Code != 404 {
		t.Fatalf("without paddle: %d", res.Code)
	}
	build := pricingBuildOnce()
	if build == nil {
		t.Skip("no pricing page in the website build in web/app")
	}
	settings, err := (&paddleConfig{Enabled: true, Environment: "sandbox", ClientToken: "test_ebbff7d957dece400c2e9677157", PlanPriceID: testPlanPrice, StoragePriceID: testStoragePrice}).settings()
	if err != nil {
		t.Fatal(err)
	}
	w.s.accounts.paddle = settings
	t.Cleanup(func() { w.s.accounts.paddle = nil })

	res := get("/pricing", "CF-IPCountry", "NZ")
	body := res.Body.String()
	csp := res.Header().Get("Content-Security-Policy")
	if res.Code != 200 || !strings.Contains(body, `src="/app/`+build.Entry+`"`) || !strings.Contains(body, `&#34;environment&#34;:&#34;sandbox&#34;`) || !strings.Contains(body, `&#34;country&#34;:&#34;NZ&#34;`) || !strings.Contains(body, testPlanPrice) || !strings.Contains(body, `&#34;welcome&#34;:&#34;https://metatrash.com/welcome&#34;`) {
		t.Fatalf("pricing page: %d %s", res.Code, body)
	}
	if !strings.Contains(csp, "https://cdn.paddle.com") || !strings.Contains(csp, "frame-src https://*.paddle.com") || strings.Contains(csp, "unsafe-eval") || strings.Contains(body, "<script>") {
		t.Fatalf("csp: %s", csp)
	}
	if strings.Contains(body, "email") || strings.Contains(body, "noindex") || res.Header().Get("X-Robots-Tag") != "" {
		t.Fatalf("signed out, indexable: %s", body)
	}
	// No country from Cloudflare (or its unknown/Tor codes): none is passed to Paddle.
	if body := get("/pricing", "CF-IPCountry", "XX").Body.String(); strings.Contains(body, "country") {
		t.Fatalf("unknown country passed: %s", body)
	}
	// Signed in: the email to fill in, and the account for the checkout's custom data.
	user := w.user("payer")
	session := w.session(user)
	res = get("/checkout?_ptxn=txn_01abc", "Cookie", w.h.sessionCookieName()+"="+session.Value)
	body = res.Body.String()
	if res.Code != 200 || !strings.Contains(body, user.Email) || !strings.Contains(body, user.ID) || !strings.Contains(body, `&#34;page&#34;:&#34;pricing&#34;`) || res.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("checkout page: %d %v %s", res.Code, res.Header(), body)
	}
	if body := get("/welcome").Body.String(); !strings.Contains(body, `&#34;page&#34;:&#34;welcome&#34;`) {
		t.Fatalf("welcome page: %s", body)
	}
	// Start free trial signed out: sign in first, remembering to come back.
	res = get("/pricing/start")
	if res.Code != 303 || res.Header().Get("Location") != "/login" {
		t.Fatalf("start signed out: %d %v", res.Code, res.Header())
	}
	returned := ""
	for _, c := range res.Result().Cookies() {
		if c.Name == w.h.returnCookieName() {
			returned = c.Value
		}
	}
	back := httptest.NewRequest("GET", "https://metatrash.com/login", nil)
	back.AddCookie(&http.Cookie{Name: w.h.returnCookieName(), Value: returned})
	if target := w.h.takeReturn(httptest.NewRecorder(), back); target != pricingStart {
		t.Fatalf("return after sign-in: %q", target)
	}
	// Signed in, it goes on to the checkout, which opens by itself.
	cookie := w.h.sessionCookieName() + "=" + session.Value
	res = get("/pricing/start", "Cookie", cookie)
	if res.Code != 303 || res.Header().Get("Location") != "/pricing?start=trial" {
		t.Fatalf("start signed in: %d %v", res.Code, res.Header())
	}
	if body := get("/pricing?start=trial", "Cookie", cookie).Body.String(); !strings.Contains(body, `&#34;start&#34;:true`) {
		t.Fatalf("start not set: %s", body)
	}
	if body := get("/pricing?start=trial").Body.String(); strings.Contains(body, `start&#34;:`) {
		t.Fatalf("start set signed out: %s", body)
	}
	if body := get("/pricing", "Cookie", cookie).Body.String(); strings.Contains(body, `start&#34;:`) {
		t.Fatalf("start set without asking: %s", body)
	}
	// From another site the session cookie stays behind: reload once from here.
	res = get("/checkout?_ptxn=txn_01abc", "Sec-Fetch-Site", "cross-site")
	body = res.Body.String()
	if res.Code != 200 || !strings.Contains(body, `http-equiv="refresh"`) || !strings.Contains(body, "/checkout?_ptxn=txn_01abc") || strings.Contains(body, "metatrash-pricing") {
		t.Fatalf("cross-site reload: %d %s", res.Code, body)
	}
	if body := get("/pricing", "Sec-Fetch-Site", "cross-site", "Cookie", cookie).Body.String(); !strings.Contains(body, user.Email) {
		t.Fatalf("cross-site with cookie: %s", body)
	}
	if body := get("/pricing", "Sec-Fetch-Site", "same-origin").Body.String(); !strings.Contains(body, "metatrash-pricing") {
		t.Fatalf("same-origin reloaded: %s", body)
	}
	r := httptest.NewRequest("POST", "https://metatrash.com/pricing", nil)
	r.RemoteAddr = "192.0.2.14:1"
	post := httptest.NewRecorder()
	w.h.ServeHTTP(post, r)
	if post.Code != 405 {
		t.Fatalf("POST: %d", post.Code)
	}
}
