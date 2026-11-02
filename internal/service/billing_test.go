package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testWebhookSecret = "pdl_ntfset_01m6dd7pjhh0y7yymvjmse3xcw_TestOnlySecretValue+0123456"

func paddleSign(secret string, body []byte, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + ":"))
	mac.Write(body)
	return "ts=" + ts + ";h1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyPaddleSignature(t *testing.T) {
	now := time.Unix(1793590000, 0)
	body := []byte(`{"event_id":"evt_1"}`)
	good := paddleSign(testWebhookSecret, body, now)
	if !verifyPaddleSignature(good, body, testWebhookSecret, now) {
		t.Fatal("good signature refused")
	}
	// While a secret is rotated Paddle sends one h1 per secret.
	rotated := good + ";h1=" + strings.Repeat("ab", 32)
	if !verifyPaddleSignature("ts="+strconv.FormatInt(now.Unix(), 10)+";h1="+strings.Repeat("ab", 32)+";"+strings.Split(good, ";")[1], body, testWebhookSecret, now) || !verifyPaddleSignature(rotated, body, testWebhookSecret, now) {
		t.Fatal("rotated signatures refused")
	}
	for name, c := range map[string]struct {
		header string
		body   []byte
		at     time.Time
		secret string
	}{
		"other body":   {good, []byte(`{"event_id":"evt_2"}`), now, testWebhookSecret},
		"other secret": {good, body, now, testWebhookSecret + "x"},
		"too old":      {good, body, now.Add(6 * time.Minute), testWebhookSecret},
		"from future":  {good, body, now.Add(-6 * time.Minute), testWebhookSecret},
		"no h1":        {"ts=" + strconv.FormatInt(now.Unix(), 10), body, now, testWebhookSecret},
		"no ts":        {strings.Split(good, ";")[1], body, now, testWebhookSecret},
		"empty":        {"", body, now, testWebhookSecret},
		"no secret":    {good, body, now, ""},
	} {
		if verifyPaddleSignature(c.header, c.body, c.secret, c.at) {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDeriveEntitlement(t *testing.T) {
	trialEnds := time.Unix(1796000000, 0).UTC()
	comped := sql.NullInt64{Int64: 1, Valid: true}
	for name, c := range map[string]struct {
		comped  sql.NullInt64
		sub     *subscriptionRow
		plan    string
		status  string
		storage int64
	}{
		"nobody":     {sql.NullInt64{}, nil, "free", "active", 0},
		"trial":      {sql.NullInt64{}, &subscriptionRow{Status: "trialing", TrialEnds: trialEnds}, "trial", "active", includedStorage},
		"active +3":  {sql.NullInt64{}, &subscriptionRow{Status: "active", StorageGB: 3}, "individual", "active", 4 * includedStorage},
		"past due":   {sql.NullInt64{}, &subscriptionRow{Status: "past_due"}, "individual", "past_due", includedStorage},
		"paused":     {sql.NullInt64{}, &subscriptionRow{Status: "paused"}, "individual", "read_only", includedStorage},
		"cancelled":  {sql.NullInt64{}, &subscriptionRow{Status: "canceled", StorageGB: 2}, "free", "cancelled", 0},
		"comped":     {comped, nil, "comped", "active", includedStorage},
		"comped +2":  {comped, &subscriptionRow{Status: "active", StorageGB: 2}, "comped", "active", 3 * includedStorage},
		"comped old": {comped, &subscriptionRow{Status: "canceled", StorageGB: 2}, "comped", "active", includedStorage},
	} {
		e := deriveEntitlement(c.comped, c.sub)
		if e.Plan != c.plan || e.Status != c.status || e.StorageBytes != c.storage {
			t.Errorf("%s: %+v", name, e)
		}
	}
	if e := deriveEntitlement(sql.NullInt64{}, &subscriptionRow{Status: "trialing", TrialEnds: trialEnds}); !e.TrialEnds.Equal(trialEnds) {
		t.Errorf("trial end: %v", e.TrialEnds)
	}
}

// testSubscription is a subscription as Paddle sends it (the shape of the
// sandbox's first real one).
func testSubscription(id, customer, status, userID string, storage int, updated time.Time) map[string]any {
	items := []any{map[string]any{"status": status, "quantity": 1, "price": map[string]any{"id": testPlanPrice},
		"trial_dates": map[string]any{"starts_at": "2026-11-02T03:32:23.525Z", "ends_at": "2026-12-02T03:32:23.525Z"}}}
	if storage > 0 {
		items = append(items, map[string]any{"status": "active", "quantity": storage, "price": map[string]any{"id": testStoragePrice}, "trial_dates": nil})
	}
	sub := map[string]any{"id": id, "status": status, "customer_id": customer, "items": items,
		"current_billing_period": map[string]any{"starts_at": "2026-11-02T03:32:23.525Z", "ends_at": "2026-12-02T03:32:23.525Z"},
		"next_billed_at":         "2026-12-02T03:32:23.525Z", "scheduled_change": nil, "canceled_at": nil,
		"updated_at": updated.UTC().Format(time.RFC3339Nano), "custom_data": nil}
	if userID != "" {
		sub["custom_data"] = map[string]any{"metatrash_user_id": userID}
	}
	return sub
}

// yearlySubscription turns a testSubscription into one billed yearly.
func yearlySubscription(sub map[string]any) map[string]any {
	sub["billing_cycle"] = map[string]any{"interval": "year", "frequency": 1}
	for _, item := range sub["items"].([]any) {
		price := item.(map[string]any)["price"].(map[string]any)
		switch price["id"] {
		case testPlanPrice:
			price["id"] = testPlanYearlyPrice
		case testStoragePrice:
			price["id"] = testStorageYearlyPrice
		}
	}
	return sub
}

func paddleID(prefix string, n int) string {
	return fmt.Sprintf("%s_%026d", prefix, n)
}

func TestPaddleSubscriptionRow(t *testing.T) {
	settings := &paddleConfig{PlanPriceID: testPlanPrice, StoragePriceID: testStoragePrice}
	updated := time.Date(2026, 11, 2, 3, 32, 23, 531e6, time.UTC)
	b, _ := json.Marshal(testSubscription(paddleID("sub", 1), paddleID("ctm", 1), "trialing", "", 2, updated))
	var sub paddleSubscription
	if err := json.Unmarshal(b, &sub); err != nil {
		t.Fatal(err)
	}
	row, err := sub.row("u1", settings)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "trialing" || row.StorageGB != 2 || row.TrialEnds.Format(time.RFC3339) != "2026-12-02T03:32:23Z" || row.PaddleUpdated != updated.UnixMilli() || row.NextBilled.IsZero() || row.ScheduledChange != "" {
		t.Fatalf("row: %+v", row)
	}
	sub.Status = "active"
	sub.ScheduledChange = &struct {
		Action      string    `json:"action"`
		EffectiveAt time.Time `json:"effective_at"`
	}{"cancel", updated.Add(time.Hour)}
	if row, _ := sub.row("u1", settings); !row.TrialEnds.IsZero() || row.ScheduledChange != "cancel" {
		t.Fatalf("active row: %+v", row)
	}
	if row.Interval != "month" {
		t.Fatalf("monthly interval: %q", row.Interval)
	}
	// Yearly (0.93.0): the yearly storage price counts, and the interval comes
	// from the billing cycle, or from the plan price when that is missing.
	yearlySettings := &paddleConfig{PlanPriceID: testPlanPrice, StoragePriceID: testStoragePrice, PlanYearlyPriceID: testPlanYearlyPrice, StorageYearlyPriceID: testStorageYearlyPrice}
	b, _ = json.Marshal(yearlySubscription(testSubscription(paddleID("sub", 2), paddleID("ctm", 2), "trialing", "", 3, updated)))
	var year paddleSubscription
	_ = json.Unmarshal(b, &year)
	if row, err := year.row("u2", yearlySettings); err != nil || row.Interval != "year" || row.StorageGB != 3 || row.TrialEnds.IsZero() {
		t.Fatalf("yearly row: %+v %v", row, err)
	}
	year.BillingCycle = nil
	if got := year.interval(yearlySettings); got != "year" {
		t.Fatalf("yearly from the plan price: %q", got)
	}
	if got := yearlySettings.storagePriceFor("year"); got != testStorageYearlyPrice {
		t.Fatalf("yearly storage price: %q", got)
	}
	if got := settings.storagePriceFor("year"); got != "" {
		t.Fatalf("yearly storage price without yearly prices: %q", got)
	}
	for _, bad := range []func(*paddleSubscription){
		func(s *paddleSubscription) { s.ID = "sub_short" },
		func(s *paddleSubscription) { s.CustomerID = "" },
		func(s *paddleSubscription) { s.Status = "deleted" },
		func(s *paddleSubscription) { s.UpdatedAt = time.Time{} },
	} {
		c := sub
		bad(&c)
		if _, err := c.row("u1", settings); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

// fakePaddle answers the two API calls the server makes.
type fakePaddle struct {
	mu            sync.Mutex
	subscriptions map[string]map[string]any
	portals       []string
	auth          []string
	// changes are the PATCHes: "path mode price=quantity,...".
	changes []string
	// refuse makes the next PATCH fail with this Paddle error code.
	refuse string
}

func (f *fakePaddle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization")+" "+r.Header.Get("Paddle-Version"))
	switch {
	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/subscriptions/"):
		sub, ok := f.subscriptions[strings.TrimPrefix(r.URL.Path, "/subscriptions/")]
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"entity_not_found"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": sub})
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/portal-sessions"):
		var body struct {
			SubscriptionIDs []string `json:"subscription_ids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		customer := strings.Split(r.URL.Path, "/")[2]
		f.portals = append(f.portals, customer+" "+strings.Join(body.SubscriptionIDs, ","))
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": "cpls_1", "urls": map[string]any{
			"general": map[string]any{"overview": "https://sandbox-customer-portal.paddle.com/cpl_1?token=pga_x"}, "subscriptions": []any{}}}})
	case r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/subscriptions/"):
		var body struct {
			Items []paddleItem `json:"items"`
			Mode  string       `json:"proration_billing_mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id, preview := strings.TrimPrefix(r.URL.Path, "/subscriptions/"), false
		if strings.HasSuffix(id, "/preview") {
			id, preview = strings.TrimSuffix(id, "/preview"), true
		}
		list := []string{}
		storage := 0
		for _, item := range body.Items {
			list = append(list, fmt.Sprintf("%s=%d", item.PriceID, item.Quantity))
			if item.PriceID == testStoragePrice || item.PriceID == testStorageYearlyPrice {
				storage = item.Quantity
			}
		}
		f.changes = append(f.changes, r.URL.Path+" "+body.Mode+" "+strings.Join(list, ","))
		sub, ok := f.subscriptions[id]
		if !ok || f.refuse != "" {
			code := f.refuse
			if !ok {
				code = "entity_not_found"
			}
			f.refuse = ""
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code}})
			return
		}
		user := ""
		if c, ok := sub["custom_data"].(map[string]any); ok {
			user, _ = c["metatrash_user_id"].(string)
		}
		changed := testSubscription(id, sub["customer_id"].(string), sub["status"].(string), user, storage, time.Now())
		if c, ok := sub["billing_cycle"].(map[string]any); ok && c["interval"] == "year" {
			changed = yearlySubscription(changed)
		}
		total := fmt.Sprint(1000 + 100*storage)
		if preview {
			delete(changed, "id")
			changed["immediate_transaction"] = map[string]any{"details": map[string]any{"totals": map[string]any{"grand_total": "123", "currency_code": "NZD"}}}
			changed["next_transaction"] = map[string]any{"billing_period": map[string]any{"starts_at": "2026-12-02T03:32:23.525Z"}, "details": map[string]any{"totals": map[string]any{"grand_total": total, "currency_code": "NZD"}}}
			changed["recurring_transaction_details"] = map[string]any{"totals": map[string]any{"grand_total": total, "currency_code": "NZD"}}
		} else {
			f.subscriptions[id] = changed
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": changed})
	default:
		w.WriteHeader(404)
	}
}

func TestMinimumExtraGB(t *testing.T) {
	for used, want := range map[int64]int{0: 0, includedStorage: 0, includedStorage + 1: 1, 2 * includedStorage: 1, 2*includedStorage + 1: 2, 101 * includedStorage: 100} {
		if got := minimumExtraGB(used); got != want {
			t.Errorf("minimumExtraGB(%d) = %d, want %d", used, got, want)
		}
	}
}

func TestStorageRefusal(t *testing.T) {
	for code, want := range map[string]string{
		"subscription_trialing_items_update_invalid_options": "free trial",
		"subscription_payment_declined":                      "payment",
		"subscription_locked_renewal":                        "busy",
		"forbidden":                                          "key",
		"something_else":                                     "refused",
	} {
		err := storageRefusal(&paddleError{Status: 400, Code: code}, false)
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", code, err)
		}
	}
	if err := storageRefusal(fmt.Errorf("Paddle could not be reached"), false); !strings.Contains(err.Error(), "reached") {
		t.Errorf("unreachable: %v", err)
	}
}

func TestChangeItemsMode(t *testing.T) {
	fake := &fakePaddle{subscriptions: map[string]map[string]any{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	id := paddleID("sub", 9)
	fake.subscriptions[id] = testSubscription(id, paddleID("ctm", 9), "trialing", "", 0, time.Now())
	api := &paddleAPI{base: server.URL, key: "k", client: server.Client()}
	items := []paddleItem{{testPlanPrice, 1}, {testStoragePrice, 3}}
	p, err := api.changeItems(context.Background(), id, true, items, true)
	if err != nil || p.NextTransaction == nil || p.NextTransaction.Details.Totals.GrandTotal != "1300" {
		t.Fatalf("preview: %+v %v", p, err)
	}
	if _, err := api.changeItems(context.Background(), id, false, items, false); err != nil {
		t.Fatal(err)
	}
	if want := []string{
		"/subscriptions/" + id + "/preview do_not_bill " + testPlanPrice + "=1," + testStoragePrice + "=3",
		"/subscriptions/" + id + " prorated_immediately " + testPlanPrice + "=1," + testStoragePrice + "=3",
	}; strings.Join(fake.changes, "\n") != strings.Join(want, "\n") {
		t.Fatalf("changes:\n%s", strings.Join(fake.changes, "\n"))
	}
	fake.refuse = "subscription_trialing_items_update_invalid_options"
	if _, err := api.changeItems(context.Background(), id, true, items, false); paddleErrorCode(err) != fake.refuse && paddleErrorCode(err) != "subscription_trialing_items_update_invalid_options" {
		t.Fatalf("refusal: %v", err)
	}
}

func TestBillingAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	fake := &fakePaddle{subscriptions: map[string]map[string]any{}}
	api := httptest.NewServer(fake)
	defer api.Close()
	dir := t.TempDir()
	secretFile, keyFile := filepath.Join(dir, "webhook"), filepath.Join(dir, "api")
	_ = os.WriteFile(secretFile, []byte(testWebhookSecret+"\n"), 0600)
	_ = os.WriteFile(keyFile, []byte("pdl_sdbx_apikey_01abcdefghijklmnopqrstuvwxyz_TestOnlyKey\n"), 0600)
	settings, err := (&paddleConfig{Enabled: true, Environment: "sandbox", ClientToken: "test_ebbff7d957dece400c2e9677157", PlanPriceID: testPlanPrice, StoragePriceID: testStoragePrice,
		PlanYearlyPriceID: testPlanYearlyPrice, StorageYearlyPriceID: testStorageYearlyPrice, WebhookSecretFile: secretFile, APIKeyFile: keyFile}).settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.apiBase = api.URL
	post := func(body []byte, signature string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://metatrash.com/paddle/webhook", strings.NewReader(string(body)))
		r.RemoteAddr = "192.0.2.30:1"
		r.Header.Set("Content-Type", "application/json")
		if signature != "" {
			r.Header.Set("Paddle-Signature", signature)
		}
		res := httptest.NewRecorder()
		w.h.ServeHTTP(res, r)
		return res
	}
	eventN := 0
	send := func(eventType string, data any) string {
		t.Helper()
		eventN++
		body, _ := json.Marshal(map[string]any{"event_id": paddleID("evt", int(time.Now().UnixNano()%1e12)*100+eventN), "event_type": eventType,
			"occurred_at": time.Now().UTC().Format(time.RFC3339Nano), "notification_id": "ntf_1", "data": data})
		res := post(body, paddleSign(testWebhookSecret, body, time.Now()))
		if res.Code != 200 {
			t.Fatalf("%s: %d %s", eventType, res.Code, res.Body)
		}
		var out struct{ Outcome string }
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out.Outcome
	}

	// Off without paddle, and without the webhook secret.
	if res := post([]byte(`{}`), ""); res.Code != 404 {
		t.Fatalf("off: %d", res.Code)
	}
	w.s.accounts.paddle = settings
	t.Cleanup(func() { w.s.accounts.paddle = nil })
	if res := post([]byte(`{}`), ""); res.Code != 401 {
		t.Fatalf("unsigned: %d", res.Code)
	}
	if res := post([]byte(`{}`), paddleSign("pdl_ntfset_other", []byte(`{}`), time.Now())); res.Code != 401 {
		t.Fatalf("wrong secret: %d", res.Code)
	}
	if res := post([]byte(`not json`), paddleSign(testWebhookSecret, []byte(`not json`), time.Now())); res.Code != 400 {
		t.Fatalf("not an event: %d", res.Code)
	}
	r := httptest.NewRequest("GET", "https://metatrash.com/paddle/webhook", nil)
	r.RemoteAddr = "192.0.2.30:1"
	get := httptest.NewRecorder()
	w.h.ServeHTTP(get, r)
	if get.Code != 405 {
		t.Fatalf("GET: %d", get.Code)
	}

	payer := w.user("payer")
	n := int(time.Now().UnixNano() % 1e9)
	subID, customer := paddleID("sub", n), paddleID("ctm", n)
	t0 := time.Now().Add(-time.Hour)
	// A checkout by a signed-in person: the subscription carries their account.
	created := testSubscription(subID, customer, "trialing", payer.ID, 0, t0)
	body, _ := json.Marshal(map[string]any{"event_id": paddleID("evt", n), "event_type": "subscription.created", "occurred_at": t0.Format(time.RFC3339Nano), "data": created})
	if res := post(body, paddleSign(testWebhookSecret, body, time.Now())); res.Code != 200 || !strings.Contains(res.Body.String(), `"applied"`) {
		t.Fatalf("created: %d %s", res.Code, res.Body)
	}
	// Paddle sends an event again until it hears 200: applied once.
	if res := post(body, paddleSign(testWebhookSecret, body, time.Now())); res.Code != 200 || !strings.Contains(res.Body.String(), `"duplicate"`) {
		t.Fatalf("again: %d %s", res.Code, res.Body)
	}
	e, err := w.s.accounts.entitlement(ctx, payer.ID)
	if err != nil || e.Plan != "trial" || e.StorageBytes != includedStorage || e.TrialEnds.Format(time.RFC3339) != "2026-12-02T03:32:23Z" {
		t.Fatalf("trial: %+v %v", e, err)
	}
	// Settings shows it.
	session := w.session(payer)
	var acct struct {
		Billing siteBilling `json:"billing"`
	}
	_ = json.Unmarshal(siteCall(w, "GET", "/api/v1/site/account", "", session, "").Body.Bytes(), &acct)
	if b := acct.Billing; b.Plan != "trial" || b.TrialEnds != "2026-12-02T03:32:23Z" || b.Renews == "" || !b.Portal || !b.Test || b.Pricing != "" || b.Storage != includedStorage {
		t.Fatalf("settings: %+v", b)
	}
	// The pricing page offers Billing, not a second checkout.
	if build := pricingBuildOnce(); build != nil {
		res := httptest.NewRecorder()
		pr := httptest.NewRequest("GET", "https://metatrash.com/pricing?start=trial", nil)
		pr.RemoteAddr = "192.0.2.30:1"
		pr.Header.Set("Cookie", w.h.sessionCookieName()+"="+session.Value)
		w.h.ServeHTTP(res, pr)
		if !strings.Contains(res.Body.String(), `&#34;subscribed&#34;:true`) || strings.Contains(res.Body.String(), `&#34;start&#34;:true`) {
			t.Fatalf("pricing subscribed: %s", res.Body)
		}
	}

	// Active with 2 GB of extra storage; then an older description arrives late.
	active := testSubscription(subID, customer, "active", "", 2, t0.Add(2*time.Minute))
	if out := send("subscription.updated", active); out != "applied" {
		t.Fatalf("updated: %s", out)
	}
	if out := send("subscription.trialing", testSubscription(subID, customer, "trialing", payer.ID, 0, t0.Add(time.Minute))); out != "older" {
		t.Fatalf("late: %s", out)
	}
	if e, _ := w.s.accounts.entitlement(ctx, payer.ID); e.Plan != "individual" || e.StorageBytes != 3*includedStorage {
		t.Fatalf("active: %+v", e)
	}
	// A subscription nobody can be found for is recorded and left alone.
	stranger := paddleID("sub", n+1)
	if out := send("subscription.created", testSubscription(stranger, paddleID("ctm", n+1), "trialing", "", 0, t0)); out != "unlinked" {
		t.Fatalf("stranger: %s", out)
	}
	if out := send("subscription.created", testSubscription(stranger, paddleID("ctm", n+1), "trialing", "0123456789abcdef0123456789abcdef", 0, t0)); out != "unlinked" {
		t.Fatalf("unknown account: %s", out)
	}
	// Events with no use here are recorded too, so Paddle stops sending them.
	if out := send("customer.updated", map[string]any{"id": customer}); out != "ignored" {
		t.Fatalf("customer: %s", out)
	}

	// A second person whose subscription.created never arrived: the completed
	// transaction names them, and the subscription is read from Paddle.
	second := w.user("second")
	sub2, cust2 := paddleID("sub", n+2), paddleID("ctm", n+2)
	fake.mu.Lock()
	fake.subscriptions[sub2] = testSubscription(sub2, cust2, "trialing", "", 0, t0)
	fake.mu.Unlock()
	if out := send("transaction.completed", map[string]any{"id": "txn_1", "status": "completed", "customer_id": cust2, "subscription_id": sub2, "custom_data": map[string]any{"metatrash_user_id": second.ID}}); out != "applied" {
		t.Fatalf("transaction: %s", out)
	}
	if out := send("transaction.completed", map[string]any{"id": "txn_2", "status": "completed", "customer_id": cust2, "subscription_id": sub2, "custom_data": map[string]any{"metatrash_user_id": second.ID}}); out != "ignored" {
		t.Fatalf("transaction again: %s", out)
	}
	if e, _ := w.s.accounts.entitlement(ctx, second.ID); e.Plan != "trial" {
		t.Fatalf("second: %+v", e)
	}
	// A later subscription for the same Paddle customer without custom data
	// (made in the portal) belongs to the same account.
	sub3 := paddleID("sub", n+3)
	if out := send("subscription.created", testSubscription(sub3, cust2, "active", "", 0, t0.Add(time.Hour))); out != "applied" {
		t.Fatalf("same customer: %s", out)
	}
	// The old one being cancelled does not replace the live one.
	if out := send("subscription.canceled", testSubscription(sub2, cust2, "canceled", second.ID, 0, t0.Add(2*time.Hour))); out != "kept" {
		t.Fatalf("old cancelled: %s", out)
	}
	if e, _ := w.s.accounts.entitlement(ctx, second.ID); e.Plan != "individual" || e.Subscription.SubscriptionID != sub3 {
		t.Fatalf("second live: %+v", e.Subscription)
	}

	// The daily check reads Paddle again: the payer cancelled and the
	// webhook missed it.
	cancelled := testSubscription(subID, customer, "canceled", payer.ID, 0, time.Now())
	cancelled["canceled_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	fake.mu.Lock()
	fake.subscriptions[subID] = cancelled
	fake.subscriptions[sub3] = testSubscription(sub3, cust2, "active", "", 0, t0.Add(time.Hour))
	fake.mu.Unlock()
	checked, changed := w.s.checkBilling(ctx, time.Now())
	if checked < 2 || changed < 1 {
		t.Fatalf("check: %d checked, %d changed", checked, changed)
	}
	if e, _ := w.s.accounts.entitlement(ctx, payer.ID); e.Plan != "free" || e.Status != "cancelled" {
		t.Fatalf("after check: %+v", e)
	}
	_ = json.Unmarshal(siteCall(w, "GET", "/api/v1/site/account", "", session, "").Body.Bytes(), &acct)
	if b := acct.Billing; b.Plan != "free" || b.CanceledAt == "" || b.Pricing != "/pricing" {
		t.Fatalf("settings after cancel: %+v", b)
	}
	for _, auth := range fake.auth {
		if !strings.HasPrefix(auth, "Bearer pdl_sdbx_apikey_") || !strings.HasSuffix(auth, " 1") {
			t.Fatalf("API call without key or version: %q", auth)
		}
	}

	// The customer portal needs a recent sign-in, and opens for the subscription.
	secondSession := w.session(second)
	if res := accountPost(w, secondSession, "/billing/portal", `{}`); res.Code != 403 || !strings.Contains(res.Body.String(), "step_up_needed") {
		t.Fatalf("portal, not fresh: %d %s", res.Code, res.Body)
	}
	fresh := w.freshSession(second)
	res := accountPost(w, fresh, "/billing/portal", `{}`)
	if res.Code != 200 || !strings.Contains(res.Body.String(), "https://sandbox-customer-portal.paddle.com/cpl_1") {
		t.Fatalf("portal: %d %s", res.Code, res.Body)
	}
	if fake.portals[len(fake.portals)-1] != cust2+" "+sub3 {
		t.Fatalf("portal for: %v", fake.portals)
	}
	nobody := w.user("nobody")
	if res := accountPost(w, w.freshSession(nobody), "/billing/portal", `{}`); res.Code != 404 {
		t.Fatalf("portal without subscription: %d %s", res.Code, res.Body)
	}

	// Extra storage: a preview needs no recent sign-in, a change does.
	if res := accountPost(w, secondSession, "/billing/storage", `{"storageGb": 2, "preview": true}`); res.Code != 200 ||
		!strings.Contains(res.Body.String(), `"now":{"amount":"123","currency":"NZD"}`) || !strings.Contains(res.Body.String(), `"monthly":{"amount":"1200","currency":"NZD"}`) {
		t.Fatalf("storage preview: %d %s", res.Code, res.Body)
	}
	if last := fake.changes[len(fake.changes)-1]; last != "/subscriptions/"+sub3+"/preview prorated_immediately "+testPlanPrice+"=1,"+testStoragePrice+"=2" {
		t.Fatalf("preview sent: %s", last)
	}
	if res := accountPost(w, secondSession, "/billing/storage", `{"storageGb": 2}`); res.Code != 403 || !strings.Contains(res.Body.String(), "step_up_needed") {
		t.Fatalf("storage, not fresh: %d %s", res.Code, res.Body)
	}
	for _, bad := range []string{`{"storageGb": 101}`, `{"storageGb": -1}`, `{}`, `{"storageGb": "2"}`} {
		if res := accountPost(w, fresh, "/billing/storage", bad); res.Code != 400 {
			t.Fatalf("storage %s: %d %s", bad, res.Code, res.Body)
		}
	}
	if res := accountPost(w, fresh, "/billing/storage", `{"storageGb": 2}`); res.Code != 200 || !strings.Contains(res.Body.String(), "2 GB of extra storage") {
		t.Fatalf("storage: %d %s", res.Code, res.Body)
	}
	if e, _ := w.s.accounts.entitlement(ctx, second.ID); e.Subscription.StorageGB != 2 || e.StorageBytes != 3*includedStorage {
		t.Fatalf("after storage: %+v", e)
	}
	if res := accountPost(w, fresh, "/billing/storage", `{"storageGb": 2}`); res.Code != 200 || !strings.Contains(res.Body.String(), "already") {
		t.Fatalf("storage again: %d %s", res.Code, res.Body)
	}
	fake.mu.Lock()
	fake.refuse = "subscription_payment_declined"
	fake.mu.Unlock()
	if res := accountPost(w, fresh, "/billing/storage", `{"storageGb": 5}`); res.Code != 409 || !strings.Contains(res.Body.String(), "payment") {
		t.Fatalf("declined: %d %s", res.Code, res.Body)
	}
	if e, _ := w.s.accounts.entitlement(ctx, second.ID); e.Subscription.StorageGB != 2 {
		t.Fatalf("after declined: %+v", e.Subscription)
	}
	var secondAcct struct {
		Billing siteBilling `json:"billing"`
	}
	_ = json.Unmarshal(siteCall(w, "GET", "/api/v1/site/account", "", secondSession, "").Body.Bytes(), &secondAcct)
	if b := secondAcct.Billing; !b.StorageChange || b.StorageMaxGB != storageMaxGB || b.StorageMinGB != 0 || b.StorageGB != 2 {
		t.Fatalf("settings storage: %+v", b)
	}
	if res := accountPost(w, fresh, "/billing/storage", `{"storageGb": 0}`); res.Code != 200 || !strings.Contains(res.Body.String(), "removed") {
		t.Fatalf("storage off: %d %s", res.Code, res.Body)
	}
	if last := fake.changes[len(fake.changes)-1]; last != "/subscriptions/"+sub3+" prorated_immediately "+testPlanPrice+"=1" {
		t.Fatalf("removal sent: %s", last)
	}
	if res := accountPost(w, w.freshSession(nobody), "/billing/storage", `{"storageGb": 1}`); res.Code != 404 {
		t.Fatalf("storage without subscription: %d %s", res.Code, res.Body)
	}

	// Yearly (0.93.0): stored as yearly, shown as yearly, and storage is added
	// at the yearly storage price.
	yearly := w.user("yearly")
	subY, custY := paddleID("sub", n+7), paddleID("ctm", n+7)
	fake.subscriptions[subY] = yearlySubscription(testSubscription(subY, custY, "active", yearly.ID, 0, t0))
	if out := send("subscription.created", yearlySubscription(testSubscription(subY, custY, "active", yearly.ID, 0, t0))); out != "applied" {
		t.Fatalf("yearly created: %s", out)
	}
	if e, _ := w.s.accounts.entitlement(ctx, yearly.ID); e.Plan != "individual" || e.Subscription.Interval != "year" {
		t.Fatalf("yearly: %+v", e.Subscription)
	}
	yearlyFresh := w.freshSession(yearly)
	var yearlyAcct struct {
		Billing siteBilling `json:"billing"`
	}
	_ = json.Unmarshal(siteCall(w, "GET", "/api/v1/site/account", "", yearlyFresh, "").Body.Bytes(), &yearlyAcct)
	if yearlyAcct.Billing.Interval != "year" {
		t.Fatalf("yearly billing: %+v", yearlyAcct.Billing)
	}
	if res := accountPost(w, yearlyFresh, "/billing/storage", `{"storageGb": 3, "preview": true}`); res.Code != 200 || !strings.Contains(res.Body.String(), `"interval":"year"`) {
		t.Fatalf("yearly preview: %d %s", res.Code, res.Body)
	}
	if res := accountPost(w, yearlyFresh, "/billing/storage", `{"storageGb": 3}`); res.Code != 200 {
		t.Fatalf("yearly storage: %d %s", res.Code, res.Body)
	}
	if last := fake.changes[len(fake.changes)-1]; last != "/subscriptions/"+subY+" prorated_immediately "+testPlanYearlyPrice+"=1,"+testStorageYearlyPrice+"=3" {
		t.Fatalf("yearly storage sent: %s", last)
	}
	if e, _ := w.s.accounts.entitlement(ctx, yearly.ID); e.Subscription.StorageGB != 3 || e.Subscription.Interval != "year" {
		t.Fatalf("after yearly storage: %+v", e.Subscription)
	}
	if b, _ := (&AdminDB{store: w.db}).BillingAccounts(ctx); len(b) == 0 {
		t.Fatal("no billing accounts")
	} else {
		for _, a := range b {
			if a.UserID == yearly.ID && (a.Subscription == nil || a.Subscription.Interval != "year") {
				t.Fatalf("admin yearly: %+v", a.Subscription)
			}
		}
	}

	// Comping, from the console.
	admin := &AdminDB{store: w.db}
	who, err := admin.FindUser(ctx, nobody.ID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := admin.SetComped(ctx, who, true, time.Now()); err != nil || !changed {
		t.Fatalf("comp: %v %v", changed, err)
	}
	if changed, _ := admin.SetComped(ctx, who, true, time.Now()); changed {
		t.Fatal("comped twice")
	}
	if e, _ := w.s.accounts.entitlement(ctx, nobody.ID); e.Plan != "comped" || e.StorageBytes != includedStorage {
		t.Fatalf("comped: %+v", e)
	}
	list, err := admin.BillingAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, b := range list {
		seen[b.UserID] = b.Plan
	}
	if seen[nobody.ID] != "comped" || seen[payer.ID] != "free" || seen[second.ID] != "individual" {
		t.Fatalf("billing list: %v", seen)
	}
	if events, err := admin.PaddleEvents(ctx, "unlinked", 100); err != nil || len(events) < 2 {
		t.Fatalf("unlinked events: %v %v", events, err)
	}
	if changed, _ := admin.SetComped(ctx, who, false, time.Now()); !changed {
		t.Fatal("uncomp")
	}
	if b, _ := admin.Billing(ctx, who); b.Plan != "free" || b.CompedAt != nil {
		t.Fatalf("uncomped: %+v", b)
	}
}
