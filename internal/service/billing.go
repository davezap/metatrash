package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Billing state from Paddle (0.91.0), phase 3 of the payment rollout plan,
// second part. Paddle is the source of truth: a subscription is made in
// Paddle's checkout (paddle.go) and changed in Paddle's customer portal, and
// Paddle tells this server what it looks like.
//
//	POST /paddle/webhook  Paddle's notification destination. Each event is
//	                      signed with the destination's secret key
//	                      (Paddle-Signature) and applied once
//	                      (metatrash_paddle_events).
//	daily check           with an API key, every subscription not long
//	                      cancelled is read from Paddle again, which catches
//	                      an event that never arrived.
//	Settings → Billing    the plan, trial end and storage, and a button to
//	                      Paddle's customer portal (card, invoices, cancel).
//
// The state is one row per account (metatrash_subscriptions). Which account a
// subscription belongs to comes from the checkout's custom data
// (metatrash_user_id, set by the pricing page for a signed-in person), then
// from a row already holding the subscription or the Paddle customer. A
// subscription none of those name is recorded as unlinked and left alone.
//
// entitlement() is the one place that says what an account may have. Nothing
// is limited by it yet: phase 4 of the plan gates writing and space creation
// on it.

const (
	// includedStorage comes with every plan; each extra storage unit adds the same.
	includedStorage        = int64(1) << 30
	paddleSignatureMaxAge  = 5 * time.Minute
	paddleWebhookMaxBytes  = 1 << 20
	paddleEventsKept       = 90 * 24 * time.Hour
	billingCheckEvery      = 24 * time.Hour
	billingCheckFirstAfter = 10 * time.Minute
	// A cancelled subscription is still read in the daily check for this
	// long, in case it is brought back.
	billingCheckCancelled = 30 * 24 * time.Hour
)

var paddleSubscriptionPattern = regexp.MustCompile(`^sub_[a-z0-9]{26}$`)
var paddleCustomerPattern = regexp.MustCompile(`^ctm_[a-z0-9]{26}$`)
var paddleEventPattern = regexp.MustCompile(`^evt_[a-z0-9]{26}$`)
var paddleEventTypePattern = regexp.MustCompile(`^[a-z_]{1,32}\.[a-z_]{1,32}$`)

var paddleStatuses = map[string]bool{"trialing": true, "active": true, "past_due": true, "paused": true, "canceled": true}

// subscriptionRow is one row of metatrash_subscriptions. A zero time is NULL.
type subscriptionRow struct {
	UserID         string
	CustomerID     string
	SubscriptionID string
	Status         string
	StorageGB      int
	// Interval is how often Paddle bills: "month" or "year" (0.93.0).
	Interval        string
	TrialEnds       time.Time
	PeriodEnds      time.Time
	NextBilled      time.Time
	ScheduledChange string
	ScheduledAt     time.Time
	CanceledAt      time.Time
	// PaddleUpdated is the subscription's updated_at at Paddle, Unix ms.
	PaddleUpdated int64
	Updated       time.Time
}

// entitlement is what an account may have.
type entitlement struct {
	// Plan is free, trial, individual, comped or enterprise (not yet).
	Plan string
	// Status is active, past_due, read_only or cancelled.
	Status string
	// StorageBytes is the account's storage allowance; 0 on the free plan.
	StorageBytes int64
	TrialEnds    time.Time
	Comped       bool
	Subscription *subscriptionRow
}

// Paid says the account has a plan with storage of its own (phase 4 lets
// it create spaces and write).
func (e entitlement) Paid() bool { return e.Plan != "free" }

// deriveEntitlement turns the stored state into an entitlement. compedAt is
// metatrash_users.comped_at; sub is the account's row or nil.
func deriveEntitlement(compedAt sql.NullInt64, sub *subscriptionRow) entitlement {
	e := entitlement{Plan: "free", Status: "active", Subscription: sub}
	extra := int64(0)
	if sub != nil {
		if sub.Status != "canceled" && sub.StorageGB > 0 {
			extra = int64(sub.StorageGB) * includedStorage
		}
		switch sub.Status {
		case "trialing":
			e.Plan, e.TrialEnds = "trial", sub.TrialEnds
		case "active":
			e.Plan = "individual"
		case "past_due":
			e.Plan, e.Status = "individual", "past_due"
		case "paused":
			e.Plan, e.Status = "individual", "read_only"
		case "canceled":
			e.Status = "cancelled"
		}
	}
	if compedAt.Valid {
		e.Plan, e.Status, e.Comped, e.TrialEnds = "comped", "active", true, time.Time{}
	}
	if e.Paid() {
		e.StorageBytes = includedStorage + extra
	}
	return e
}

// entitlement reads an account's entitlement. Every check of what an account
// may do asks this.
func (a *accounts) entitlement(ctx context.Context, userID string) (entitlement, error) {
	db := a.billingDB()
	if db == nil {
		return entitlement{}, fmt.Errorf("the account database is not configured")
	}
	return db.entitlementFor(ctx, userID)
}

// billingDB is the account database, or nil (accounts kept in a file, tests).
func (a *accounts) billingDB() *accountDatabase {
	if a == nil {
		return nil
	}
	return a.sessionDB()
}

// --- Database ---------------------------------------------------------------

const subscriptionColumns = "user_id, customer_id, subscription_id, status, storage_gb, trial_ends_at, period_ends_at, next_billed_at, scheduled_change, scheduled_at, canceled_at, paddle_updated_at, updated_at, billing_interval"

func (db *accountDatabase) checkBillingSchema(ctx context.Context) error {
	for _, query := range []string{
		"SELECT " + subscriptionColumns + " FROM metatrash_subscriptions LIMIT 0",
		"SELECT event_id, event_type, occurred_at, received_at, subscription_id, user_id, outcome FROM metatrash_paddle_events LIMIT 0",
		"SELECT comped_at FROM metatrash_users LIMIT 0",
	} {
		rows, err := db.db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("account schema v25 required (billing state); follow docs/deployment.md")
		}
		rows.Close()
	}
	return nil
}

func nullUnix(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.Unix(), Valid: true}
}

func fromNullUnix(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(v.Int64, 0).UTC()
}

func scanSubscription(scan func(...any) error) (*subscriptionRow, error) {
	var row subscriptionRow
	var trial, period, next, scheduledAt, canceled sql.NullInt64
	var change sql.NullString
	var updated int64
	if err := scan(&row.UserID, &row.CustomerID, &row.SubscriptionID, &row.Status, &row.StorageGB, &trial, &period, &next, &change, &scheduledAt, &canceled, &row.PaddleUpdated, &updated, &row.Interval); err != nil {
		return nil, err
	}
	row.TrialEnds, row.PeriodEnds, row.NextBilled = fromNullUnix(trial), fromNullUnix(period), fromNullUnix(next)
	row.ScheduledChange, row.ScheduledAt, row.CanceledAt = change.String, fromNullUnix(scheduledAt), fromNullUnix(canceled)
	row.Updated = time.Unix(updated, 0).UTC()
	return &row, nil
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// subscriptionWhere reads one row, or nil when there is none.
func subscriptionWhere(ctx context.Context, q queryRower, where string, args ...any) (*subscriptionRow, error) {
	row, err := scanSubscription(q.QueryRowContext(ctx, "SELECT "+subscriptionColumns+" FROM metatrash_subscriptions WHERE "+where, args...).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return row, err
}

func (db *accountDatabase) entitlementFor(ctx context.Context, userID string) (entitlement, error) {
	var compedAt sql.NullInt64
	if err := db.db.QueryRowContext(ctx, "SELECT comped_at FROM metatrash_users WHERE user_id = ?", userID).Scan(&compedAt); err != nil {
		return entitlement{}, fmt.Errorf("cannot read the account's plan: %w", err)
	}
	sub, err := subscriptionWhere(ctx, db.db, "user_id = ?", userID)
	if err != nil {
		return entitlement{}, fmt.Errorf("cannot read the account's subscription: %w", err)
	}
	return deriveEntitlement(compedAt, sub), nil
}

// paddleEventSeen reports whether an event was applied before.
func (db *accountDatabase) paddleEventSeen(ctx context.Context, eventID string) (bool, error) {
	var n int
	err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_paddle_events WHERE event_id = ?", eventID).Scan(&n)
	return n > 0, err
}

// liveUser reports whether userID names an account that is not deleted.
func liveUser(ctx context.Context, q queryRower, userID string) (bool, error) {
	if !idPattern.MatchString(userID) {
		return false, nil
	}
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_users WHERE user_id = ? AND deleted_at IS NULL", userID).Scan(&n)
	return n > 0, err
}

// saveSubscription writes what Paddle said about a subscription for an
// account, in tx. It answers what it did: applied, older (a newer
// description is stored already), kept (the account has another live
// subscription and this one is cancelled) or conflict (the subscription is
// stored for another account).
func saveSubscription(ctx context.Context, tx *sql.Tx, row subscriptionRow, now time.Time) (string, error) {
	held, err := subscriptionWhere(ctx, tx, "subscription_id = ? FOR UPDATE", row.SubscriptionID)
	if err != nil {
		return "", err
	}
	if held != nil && held.UserID != row.UserID {
		log.Printf("billing: subscription %s is stored for account %s, not %s; not changed", row.SubscriptionID, held.UserID, row.UserID)
		return "conflict", nil
	}
	existing, err := subscriptionWhere(ctx, tx, "user_id = ? FOR UPDATE", row.UserID)
	if err != nil {
		return "", err
	}
	args := []any{row.CustomerID, row.SubscriptionID, row.Status, row.StorageGB, nullUnix(row.TrialEnds), nullUnix(row.PeriodEnds), nullUnix(row.NextBilled),
		sql.NullString{String: row.ScheduledChange, Valid: row.ScheduledChange != ""}, nullUnix(row.ScheduledAt), nullUnix(row.CanceledAt), row.PaddleUpdated, now.Unix(), billingInterval(row.Interval)}
	if existing == nil {
		_, err := tx.ExecContext(ctx, "INSERT INTO metatrash_subscriptions ("+subscriptionColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", append([]any{row.UserID}, args...)...)
		if err != nil {
			return "", err
		}
		return "applied", nil
	}
	if existing.SubscriptionID == row.SubscriptionID {
		if row.PaddleUpdated < existing.PaddleUpdated {
			return "older", nil
		}
	} else {
		if existing.Status != "canceled" && row.Status == "canceled" {
			return "kept", nil
		}
		if existing.Status != "canceled" {
			// Two live subscriptions for one account: the newer one is kept.
			// Paddle still bills the other; it needs cancelling there.
			log.Printf("billing: account %s has a second live subscription %s; %s (%s) is no longer followed, cancel it in Paddle", row.UserID, row.SubscriptionID, existing.SubscriptionID, existing.Status)
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE metatrash_subscriptions SET customer_id = ?, subscription_id = ?, status = ?, storage_gb = ?, trial_ends_at = ?, period_ends_at = ?,
next_billed_at = ?, scheduled_change = ?, scheduled_at = ?, canceled_at = ?, paddle_updated_at = ?, updated_at = ?, billing_interval = ? WHERE user_id = ?`, append(args, row.UserID)...)
	if err != nil {
		return "", err
	}
	return "applied", nil
}

// billingInterval is interval as stored: "year", or "month" for anything else.
func billingInterval(interval string) string {
	if interval == "year" {
		return "year"
	}
	return "month"
}

func recordPaddleEvent(ctx context.Context, tx *sql.Tx, ev paddleEvent, subscriptionID, userID, outcome string, now time.Time) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO metatrash_paddle_events (event_id, event_type, occurred_at, received_at, subscription_id, user_id, outcome) VALUES (?, ?, ?, ?, ?, ?, ?)",
		ev.EventID, ev.EventType, ev.OccurredAt.Unix(), now.Unix(), sql.NullString{String: subscriptionID, Valid: subscriptionID != ""}, sql.NullString{String: userID, Valid: userID != ""}, outcome)
	return err
}

// --- Paddle's payloads --------------------------------------------------------

type paddleEvent struct {
	EventID    string          `json:"event_id"`
	EventType  string          `json:"event_type"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

type paddleCustomData struct {
	UserID string `json:"metatrash_user_id"`
}

// customUser is the account the checkout named, or "".
func customUser(raw json.RawMessage) string {
	var data paddleCustomData
	if len(raw) == 0 || json.Unmarshal(raw, &data) != nil || !idPattern.MatchString(data.UserID) {
		return ""
	}
	return data.UserID
}

type paddleSubscription struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	CustomerID string          `json:"customer_id"`
	CustomData json.RawMessage `json:"custom_data"`
	Items      []struct {
		Status   string `json:"status"`
		Quantity int    `json:"quantity"`
		Price    struct {
			ID string `json:"id"`
		} `json:"price"`
		TrialDates *struct {
			EndsAt *time.Time `json:"ends_at"`
		} `json:"trial_dates"`
	} `json:"items"`
	CurrentBillingPeriod *struct {
		EndsAt time.Time `json:"ends_at"`
	} `json:"current_billing_period"`
	NextBilledAt    *time.Time `json:"next_billed_at"`
	ScheduledChange *struct {
		Action      string    `json:"action"`
		EffectiveAt time.Time `json:"effective_at"`
	} `json:"scheduled_change"`
	CanceledAt   *time.Time `json:"canceled_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	BillingCycle *struct {
		Interval  string `json:"interval"`
		Frequency int    `json:"frequency"`
	} `json:"billing_cycle"`
}

// interval is how often Paddle bills the subscription, "month" or "year":
// from its billing cycle, else from which plan price it has.
func (sub paddleSubscription) interval(settings *paddleConfig) string {
	if c := sub.BillingCycle; c != nil && c.Frequency == 1 && (c.Interval == "month" || c.Interval == "year") {
		return c.Interval
	}
	for _, item := range sub.Items {
		if interval, ok := settings.planInterval(item.Price.ID); ok && item.Status != "inactive" {
			return interval
		}
	}
	return "month"
}

type paddleTransaction struct {
	ID             string          `json:"id"`
	Status         string          `json:"status"`
	CustomerID     string          `json:"customer_id"`
	SubscriptionID string          `json:"subscription_id"`
	CustomData     json.RawMessage `json:"custom_data"`
}

// row turns a subscription as Paddle describes it into a row for userID.
func (sub paddleSubscription) row(userID string, settings *paddleConfig) (subscriptionRow, error) {
	if !paddleSubscriptionPattern.MatchString(sub.ID) || !paddleCustomerPattern.MatchString(sub.CustomerID) || !paddleStatuses[sub.Status] || sub.UpdatedAt.IsZero() {
		return subscriptionRow{}, fmt.Errorf("not a subscription this server understands")
	}
	row := subscriptionRow{UserID: userID, CustomerID: sub.CustomerID, SubscriptionID: sub.ID, Status: sub.Status, PaddleUpdated: sub.UpdatedAt.UnixMilli(), Interval: sub.interval(settings)}
	for _, item := range sub.Items {
		if item.Status == "inactive" {
			continue
		}
		_, plan := settings.planInterval(item.Price.ID)
		switch {
		case settings.isStoragePrice(item.Price.ID):
			if item.Quantity > 0 && item.Quantity <= 10000 {
				row.StorageGB += item.Quantity
			}
		case plan:
			if item.TrialDates != nil && item.TrialDates.EndsAt != nil {
				row.TrialEnds = item.TrialDates.EndsAt.UTC()
			}
		}
	}
	if sub.CurrentBillingPeriod != nil {
		row.PeriodEnds = sub.CurrentBillingPeriod.EndsAt.UTC()
	}
	if sub.NextBilledAt != nil {
		row.NextBilled = sub.NextBilledAt.UTC()
	}
	if c := sub.ScheduledChange; c != nil && (c.Action == "cancel" || c.Action == "pause" || c.Action == "resume") {
		row.ScheduledChange, row.ScheduledAt = c.Action, c.EffectiveAt.UTC()
	}
	if sub.CanceledAt != nil {
		row.CanceledAt = sub.CanceledAt.UTC()
	}
	if row.Status != "trialing" {
		row.TrialEnds = time.Time{}
	}
	return row, nil
}

// --- The webhook ----------------------------------------------------------------

// verifyPaddleSignature checks a Paddle-Signature header ("ts=...;h1=...",
// with more than one h1 while a secret is being rotated) against the body.
func verifyPaddleSignature(header string, body []byte, secret string, now time.Time) bool {
	ts := ""
	var signatures []string
	for _, part := range strings.Split(header, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "ts":
			ts = value
		case "h1":
			signatures = append(signatures, value)
		}
	}
	seconds, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(signatures) == 0 || secret == "" {
		return false
	}
	if age := now.Sub(time.Unix(seconds, 0)); age > paddleSignatureMaxAge || age < -paddleSignatureMaxAge {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(ts + ":"))
	_, _ = mac.Write(body)
	want := mac.Sum(nil)
	for _, signature := range signatures {
		if got, err := hex.DecodeString(signature); err == nil && hmac.Equal(got, want) {
			return true
		}
	}
	return false
}

// servePaddleWebhook is POST /paddle/webhook. It answers 200 once an event
// is applied or recorded (also for events it has no use for, so Paddle stops
// sending them), and 503 when the database fails, so Paddle tries again.
func (h *httpAdapter) servePaddleWebhook(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/paddle/webhook" {
		return false
	}
	a := h.service.accounts
	if a == nil || a.paddle == nil || a.paddle.webhookSecret == "" || a.billingDB() == nil {
		sendError(w, missing())
		return true
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		sendError(w, problem(405, "invalid_request", "Use POST."))
		return true
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, paddleWebhookMaxBytes))
	if err != nil {
		sendError(w, problem(413, "payload_too_large", "Request exceeds the byte limit."))
		return true
	}
	now := time.Now()
	if !verifyPaddleSignature(r.Header.Get("Paddle-Signature"), body, a.paddle.webhookSecret, now) {
		log.Printf("billing: webhook with a signature that does not match from %s", h.client(r))
		sendError(w, problem(401, "unauthorized", "The signature does not match."))
		return true
	}
	var ev paddleEvent
	if err := json.Unmarshal(body, &ev); err != nil || !paddleEventPattern.MatchString(ev.EventID) || !paddleEventTypePattern.MatchString(ev.EventType) || ev.OccurredAt.IsZero() {
		sendError(w, invalid("Not a Paddle event."))
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	outcome, err := h.service.applyPaddleEvent(ctx, ev, now)
	if err != nil {
		log.Printf("billing: event %s (%s): %v", ev.EventID, ev.EventType, err)
		sendError(w, problem(503, "unavailable", "The event could not be applied just now."))
		return true
	}
	if outcome != "applied" && outcome != "duplicate" && outcome != "ignored" {
		log.Printf("billing: event %s (%s): %s", ev.EventID, ev.EventType, outcome)
	}
	sendJSON(w, 200, map[string]string{"outcome": outcome})
	return true
}

// applyPaddleEvent applies one verified event once.
func (s *Service) applyPaddleEvent(ctx context.Context, ev paddleEvent, now time.Time) (string, error) {
	a := s.accounts
	db := a.billingDB()
	if db == nil {
		return "", fmt.Errorf("the account database is not configured")
	}
	if seen, err := db.paddleEventSeen(ctx, ev.EventID); err != nil || seen {
		if seen {
			return "duplicate", nil
		}
		return "", err
	}
	var sub *paddleSubscription
	override := ""
	switch {
	case strings.HasPrefix(ev.EventType, "subscription."):
		var value paddleSubscription
		if err := json.Unmarshal(ev.Data, &value); err != nil {
			return s.recordOnly(ctx, ev, "", "", "invalid", now)
		}
		sub = &value
	case ev.EventType == "transaction.completed":
		// A backstop for the subscription events: the checkout's custom data
		// is on the transaction too. Only used when no row holds the
		// subscription yet, and only with an API key to read it.
		var txn paddleTransaction
		if err := json.Unmarshal(ev.Data, &txn); err != nil || !paddleSubscriptionPattern.MatchString(txn.SubscriptionID) {
			return s.recordOnly(ctx, ev, "", "", "ignored", now)
		}
		held, err := subscriptionWhere(ctx, db.db, "subscription_id = ?", txn.SubscriptionID)
		if err != nil {
			return "", err
		}
		user := customUser(txn.CustomData)
		if held != nil || user == "" {
			return s.recordOnly(ctx, ev, txn.SubscriptionID, "", "ignored", now)
		}
		api := a.paddle.api()
		if api == nil {
			return s.recordOnly(ctx, ev, txn.SubscriptionID, user, "unlinked", now)
		}
		value, err := api.subscription(ctx, txn.SubscriptionID)
		if err != nil {
			return "", err
		}
		sub, override = &value, user
	default:
		return s.recordOnly(ctx, ev, "", "", "ignored", now)
	}
	return s.applySubscription(ctx, &ev, *sub, override, now)
}

func (s *Service) recordOnly(ctx context.Context, ev paddleEvent, subscriptionID, userID, outcome string, now time.Time) (string, error) {
	tx, err := s.accounts.billingDB().db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err := recordPaddleEvent(ctx, tx, ev, subscriptionID, userID, outcome, now); err != nil {
		if isDuplicateKey(err) {
			return "duplicate", nil
		}
		return "", err
	}
	return outcome, tx.Commit()
}

// applySubscription saves a subscription for the account it belongs to and,
// for an event, records the event in the same transaction. user, when set,
// is the account it belongs to (the daily check, or a transaction's custom
// data); otherwise the subscription's custom data, then a row already
// holding the subscription or its customer, decide.
func (s *Service) applySubscription(ctx context.Context, ev *paddleEvent, sub paddleSubscription, user string, now time.Time) (string, error) {
	settings := s.accounts.paddle
	tx, err := s.accounts.billingDB().db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	outcome := ""
	if user == "" {
		user = customUser(sub.CustomData)
	}
	if user != "" {
		ok, err := liveUser(ctx, tx, user)
		if err != nil {
			return "", err
		}
		if !ok {
			user = ""
		}
	}
	if user == "" && paddleSubscriptionPattern.MatchString(sub.ID) {
		if held, err := subscriptionWhere(ctx, tx, "subscription_id = ?", sub.ID); err != nil {
			return "", err
		} else if held != nil {
			user = held.UserID
		}
	}
	if user == "" && paddleCustomerPattern.MatchString(sub.CustomerID) {
		if held, err := subscriptionWhere(ctx, tx, "customer_id = ? ORDER BY updated_at DESC LIMIT 1", sub.CustomerID); err != nil {
			return "", err
		} else if held != nil {
			user = held.UserID
		}
	}
	if user == "" {
		outcome = "unlinked"
	} else if row, err := sub.row(user, settings); err != nil {
		outcome = "invalid"
	} else if outcome, err = saveSubscription(ctx, tx, row, now); err != nil {
		return "", err
	}
	if ev != nil {
		subscriptionID := ""
		if paddleSubscriptionPattern.MatchString(sub.ID) {
			subscriptionID = sub.ID
		}
		if err := recordPaddleEvent(ctx, tx, *ev, subscriptionID, user, outcome, now); err != nil {
			if isDuplicateKey(err) {
				return "duplicate", nil
			}
			return "", err
		}
	}
	return outcome, tx.Commit()
}

// isDuplicateKey reports MariaDB/MySQL error 1062.
func isDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

// --- Paddle's API ------------------------------------------------------------------

// paddleAPI calls Paddle's API with the server-side key.
type paddleAPI struct {
	base   string
	key    string
	client *http.Client
}

// api is nil without an API key.
func (c *paddleConfig) api() *paddleAPI {
	if c == nil || c.apiKey == "" {
		return nil
	}
	base := c.apiBase
	if base == "" {
		base = "https://api.paddle.com"
		if c.Environment == "sandbox" {
			base = "https://sandbox-api.paddle.com"
		}
	}
	return &paddleAPI{base: base, key: c.apiKey, client: &http.Client{Timeout: 20 * time.Second}}
}

func (p *paddleAPI) call(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.key)
	req.Header.Set("Paddle-Version", "1")
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("Paddle could not be reached")
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("Paddle's answer could not be read")
	}
	if res.StatusCode >= 300 {
		var problem struct {
			Error struct {
				Code   string `json:"code"`
				Detail string `json:"detail"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &problem)
		return &paddleError{Status: res.StatusCode, Code: problem.Error.Code}
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &envelope); err != nil || len(envelope.Data) == 0 {
		return fmt.Errorf("Paddle's answer is not what was expected")
	}
	return json.Unmarshal(envelope.Data, out)
}

func (p *paddleAPI) subscription(ctx context.Context, id string) (paddleSubscription, error) {
	var sub paddleSubscription
	if !paddleSubscriptionPattern.MatchString(id) {
		return sub, fmt.Errorf("not a subscription ID")
	}
	err := p.call(ctx, http.MethodGet, "/subscriptions/"+url.PathEscape(id), nil, &sub)
	return sub, err
}

// paddleError is a refusal from Paddle's API; Code is Paddle's error code
// (such as subscription_trialing_items_update_invalid_options).
type paddleError struct {
	Status int
	Code   string
}

func (e *paddleError) Error() string { return fmt.Sprintf("Paddle answered %d %s", e.Status, e.Code) }

// paddleErrorCode is err's Paddle error code, or "".
func paddleErrorCode(err error) string {
	var p *paddleError
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

// paddleItem is an item to keep on a subscription.
type paddleItem struct {
	PriceID  string `json:"price_id"`
	Quantity int    `json:"quantity"`
}

// paddleTotal is an amount in the currency's smallest unit, as Paddle gives it.
type paddleTotal struct {
	GrandTotal   string `json:"grand_total"`
	CurrencyCode string `json:"currency_code"`
}

// paddleUpdate is a subscription after a change, or a preview of one.
type paddleUpdate struct {
	paddleSubscription
	ImmediateTransaction *struct {
		Details struct {
			Totals paddleTotal `json:"totals"`
		} `json:"details"`
	} `json:"immediate_transaction"`
	NextTransaction *struct {
		BillingPeriod struct {
			StartsAt time.Time `json:"starts_at"`
		} `json:"billing_period"`
		Details struct {
			Totals paddleTotal `json:"totals"`
		} `json:"details"`
	} `json:"next_transaction"`
	RecurringTransactionDetails *struct {
		Totals paddleTotal `json:"totals"`
	} `json:"recurring_transaction_details"`
}

// changeItems sets a subscription's items (the complete list), or with
// preview only asks Paddle what it would charge. Paddle refuses billing for
// item changes during a trial, so a trialing subscription is changed with
// do_not_bill: the new items start when the trial ends. Otherwise the change
// is prorated and charged (or credited) at once; a failed payment leaves the
// subscription as it was (prevent_change, Paddle's default).
func (p *paddleAPI) changeItems(ctx context.Context, id string, trialing bool, items []paddleItem, preview bool) (paddleUpdate, error) {
	var out paddleUpdate
	if !paddleSubscriptionPattern.MatchString(id) {
		return out, fmt.Errorf("not a subscription ID")
	}
	mode := "prorated_immediately"
	if trialing {
		mode = "do_not_bill"
	}
	path := "/subscriptions/" + url.PathEscape(id)
	if preview {
		path += "/preview"
	}
	err := p.call(ctx, http.MethodPatch, path, map[string]any{"items": items, "proration_billing_mode": mode, "on_payment_failure": "prevent_change"}, &out)
	return out, err
}

// portal makes an authenticated customer portal session and answers its
// address: the overview, or the subscription's own page when there is one.
func (p *paddleAPI) portal(ctx context.Context, customerID, subscriptionID string) (string, error) {
	if !paddleCustomerPattern.MatchString(customerID) {
		return "", fmt.Errorf("not a customer ID")
	}
	body := map[string]any{}
	if paddleSubscriptionPattern.MatchString(subscriptionID) {
		body["subscription_ids"] = []string{subscriptionID}
	}
	var session struct {
		URLs struct {
			General struct {
				Overview string `json:"overview"`
			} `json:"general"`
		} `json:"urls"`
	}
	if err := p.call(ctx, http.MethodPost, "/customers/"+url.PathEscape(customerID)+"/portal-sessions", body, &session); err != nil {
		return "", err
	}
	address := session.URLs.General.Overview
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || !(parsed.Host == "customer-portal.paddle.com" || parsed.Host == "sandbox-customer-portal.paddle.com") {
		return "", fmt.Errorf("Paddle's portal address is not what was expected")
	}
	return address, nil
}

// --- The daily check ---------------------------------------------------------------

type billingChecker struct {
	once sync.Once
	wg   sync.WaitGroup
	// checked, when set, hears of each finished check (tests).
	checked func(checked, changed int)
}

// startBillingCheck starts the daily check once, when Paddle is configured.
// Without an API key it only removes old event records.
func (s *Service) startBillingCheck() {
	if s.accounts == nil || s.accounts.paddle == nil {
		return
	}
	s.billing.once.Do(func() {
		s.billing.wg.Add(1)
		go func() {
			defer s.billing.wg.Done()
			timer := time.NewTimer(billingCheckFirstAfter)
			defer timer.Stop()
			for {
				select {
				case <-s.stop:
					return
				case <-timer.C:
					ctx, cancel := s.backgroundContext(30 * time.Minute)
					s.checkBilling(ctx, time.Now())
					cancel()
					timer.Reset(billingCheckEvery)
				}
			}
		}()
	})
}

// checkBilling reads each subscription not long cancelled from Paddle again
// and applies it, and removes event records older than 90 days.
func (s *Service) checkBilling(ctx context.Context, now time.Time) (checked, changed int) {
	db := s.accounts.billingDB()
	if db == nil {
		return 0, 0
	}
	if _, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_paddle_events WHERE received_at < ?", now.Add(-paddleEventsKept).Unix()); err != nil {
		log.Printf("billing check: old events: %v", err)
	}
	api := s.accounts.paddle.api()
	if api == nil {
		return 0, 0
	}
	var rows []*subscriptionRow
	err := eachRow(ctx, db.db, func(scan func(...any) error) error {
		row, err := scanSubscription(scan)
		if err == nil {
			rows = append(rows, row)
		}
		return err
	}, "SELECT "+subscriptionColumns+" FROM metatrash_subscriptions WHERE status <> 'canceled' OR canceled_at IS NULL OR canceled_at > ? LIMIT 100000", now.Add(-billingCheckCancelled).Unix())
	if err != nil {
		log.Printf("billing check: %v", err)
		return 0, 0
	}
	failed := 0
	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		sub, err := api.subscription(ctx, row.SubscriptionID)
		if err != nil {
			failed++
			log.Printf("billing check: %s: %v", row.SubscriptionID, err)
			continue
		}
		checked++
		before := *row
		if _, err := s.applySubscription(ctx, nil, sub, row.UserID, now); err != nil {
			log.Printf("billing check: %s: %v", row.SubscriptionID, err)
			continue
		}
		after, err := subscriptionWhere(ctx, db.db, "user_id = ?", row.UserID)
		if err == nil && after != nil && (after.Status != before.Status || after.StorageGB != before.StorageGB || after.Interval != before.Interval || after.PaddleUpdated != before.PaddleUpdated) {
			changed++
		}
	}
	if changed > 0 || failed > 0 {
		log.Printf("billing check: %d subscriptions read from Paddle, %d changed, %d failed", checked, changed, failed)
	}
	if s.billing.checked != nil {
		s.billing.checked(checked, changed)
	}
	return checked, changed
}
