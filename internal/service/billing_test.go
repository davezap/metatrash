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
	default:
		w.WriteHeader(404)
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
		WebhookSecretFile: secretFile, APIKeyFile: keyFile}).settings()
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
