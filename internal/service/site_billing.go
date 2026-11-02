package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// Settings → Billing (0.91.0): the account's plan as GET /api/v1/site/account
// shows it under "billing", and
//
//	POST /api/v1/site/account/billing/portal   {}  → {"redirect": Paddle's customer portal}
//	POST /api/v1/site/account/billing/storage  {"storageGb": n, "preview": true}  → {"preview": what Paddle would charge}
//	POST /api/v1/site/account/billing/storage  {"storageGb": n}  → the change made (0.92.0)
//
// The portal is where a subscriber changes their card, reads invoices and
// cancels. Its address signs the person in to Paddle, so it is made when
// asked for, never stored, and only after a sign-in or confirmation within
// the last 10 minutes.

// siteBilling is the account's plan. Times are RFC 3339.
type siteBilling struct {
	// Plan is free, trial, individual or comped; Status active, past_due,
	// read_only or cancelled (billing.go, entitlement).
	Plan   string `json:"plan"`
	Status string `json:"status"`
	// Storage is the allowance in bytes; StorageGB the extra storage bought.
	Storage   int64 `json:"storage"`
	StorageGB int   `json:"storageGb,omitempty"`
	// Interval is how often Paddle bills a subscription: month or year (0.93.0).
	Interval  string `json:"interval,omitempty"`
	TrialEnds string `json:"trialEnds,omitempty"`
	// Renews is when Paddle next bills; Ends when a scheduled cancellation
	// takes effect; Paused when a scheduled pause does.
	Renews     string `json:"renews,omitempty"`
	Ends       string `json:"ends,omitempty"`
	Pauses     string `json:"pauses,omitempty"`
	CanceledAt string `json:"canceledAt,omitempty"`
	// Portal: Settings can open Paddle's customer portal for this account.
	Portal bool `json:"portal,omitempty"`
	// Pricing is where to start a trial, for an account without a plan.
	Pricing string `json:"pricing,omitempty"`
	// Test: Paddle's sandbox, where no real money moves.
	Test bool `json:"test,omitempty"`
	// StorageChange: Settings can change the extra storage (0.92.0), from
	// StorageMinGB (what the spaces already use) to StorageMaxGB.
	StorageChange bool `json:"storageChange,omitempty"`
	StorageMinGB  int  `json:"storageMinGb,omitempty"`
	StorageMaxGB  int  `json:"storageMaxGb,omitempty"`
}

// storageMaxGB is the most extra storage Settings sells (the Paddle price's
// quantity limit is 100).
const storageMaxGB = 100

// minimumExtraGB is the least extra storage that still holds used bytes.
func minimumExtraGB(used int64) int {
	if used <= includedStorage {
		return 0
	}
	return int((used - includedStorage + includedStorage - 1) / includedStorage)
}

// siteMoney is an amount in the currency's smallest unit (cents), for the
// browser to format.
type siteMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func moneyOf(t paddleTotal) *siteMoney {
	if t.CurrencyCode == "" || t.GrandTotal == "" {
		return nil
	}
	for _, c := range t.GrandTotal {
		if c < '0' || c > '9' {
			return nil
		}
	}
	return &siteMoney{Amount: t.GrandTotal, Currency: t.CurrencyCode}
}

// siteStoragePreview is what a change of extra storage would cost.
type siteStoragePreview struct {
	StorageGB int `json:"storageGb"`
	// Now is charged at once (prorated; nothing during a trial or for a
	// reduction, which Paddle credits to the next bill instead).
	Now *siteMoney `json:"now,omitempty"`
	// Next is the next bill and when it is; Monthly the bill from then on,
	// each month or each year as Interval says (the name is from 0.92.0).
	Next     *siteMoney `json:"next,omitempty"`
	NextAt   string     `json:"nextAt,omitempty"`
	Monthly  *siteMoney `json:"monthly,omitempty"`
	Interval string     `json:"interval,omitempty"`
	Trial    bool       `json:"trial,omitempty"`
}

// siteBillingState is the billing part of GET /account; nil when Paddle is
// not configured.
// used is what the account's spaces take up, nil when unknown.
func (h *httpAdapter) siteBillingState(ctx context.Context, user userAccount, used *int64) (*siteBilling, error) {
	a := h.service.accounts
	if a == nil || a.paddle == nil {
		return nil, nil
	}
	e, err := a.entitlement(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	out := &siteBilling{Plan: e.Plan, Status: e.Status, Storage: e.StorageBytes, TrialEnds: siteTime(e.TrialEnds), Test: a.paddle.Environment == "sandbox"}
	if sub := e.Subscription; sub != nil {
		out.Interval = billingInterval(sub.Interval)
		if sub.Status != "canceled" {
			out.StorageGB = sub.StorageGB
			if sub.ScheduledChange == "cancel" {
				out.Ends = siteTime(sub.ScheduledAt)
			} else {
				out.Renews = siteTime(sub.NextBilled)
			}
			if sub.ScheduledChange == "pause" {
				out.Pauses = siteTime(sub.ScheduledAt)
			}
		} else {
			out.CanceledAt = siteTime(sub.CanceledAt)
		}
		out.Portal = a.paddle.api() != nil
		if out.Portal && (sub.Status == "active" || sub.Status == "trialing") && !e.Comped {
			out.StorageChange, out.StorageMaxGB = true, storageMaxGB
			if used != nil {
				out.StorageMinGB = min(minimumExtraGB(*used), storageMaxGB)
			}
		}
	}
	if !e.Paid() || (e.Subscription != nil && e.Subscription.Status == "canceled" && !e.Comped) {
		out.Pricing = h.basePath + "/pricing"
	}
	return out, nil
}

// siteBillingPortal is POST /account/billing/portal.
func (h *httpAdapter) siteBillingPortal(ctx context.Context, user userAccount, session string) (siteNotice, error) {
	a := h.service.accounts
	if err := h.service.rates.take(allowance{"billing-portal:user:" + user.ID, 10, 600}); err != nil {
		return siteNotice{}, err
	}
	api := a.paddle.api()
	if api == nil || a.billingDB() == nil {
		return siteNotice{}, problem(503, "unavailable", "Billing can't be managed from here on this server yet.")
	}
	if fresh, _ := a.sessionFresh(session, time.Now()); !fresh {
		return siteNotice{}, errConfirmFirst
	}
	sub, err := subscriptionWhere(ctx, a.billingDB().db, "user_id = ?", user.ID)
	if err != nil {
		return siteNotice{}, err
	}
	if sub == nil {
		return siteNotice{}, problem(404, "not_found", "You have no subscription to manage. Start a trial on the pricing page.")
	}
	address, err := api.portal(ctx, sub.CustomerID, sub.SubscriptionID)
	if err != nil {
		log.Printf("billing: portal for %s: %v", user.ID, err)
		return siteNotice{}, problem(503, "unavailable", "Paddle's billing page can't be opened just now. Try again shortly.")
	}
	return siteNotice{Redirect: address}, nil
}

// siteBillingStorage is POST /account/billing/storage: the extra storage
// becomes storageGb GB (0 removes it), or with preview Paddle only says what
// that would cost. A change needs a recent sign-in, as it charges the card.
func (h *httpAdapter) siteBillingStorage(ctx context.Context, user userAccount, session string, body siteAccountBody) (siteNotice, error) {
	a := h.service.accounts
	if err := h.service.rates.take(allowance{"billing-storage:user:" + user.ID, 30, 600}); err != nil {
		return siteNotice{}, err
	}
	api := a.paddle.api()
	if api == nil || a.billingDB() == nil {
		return siteNotice{}, problem(503, "unavailable", "Storage can't be changed from here on this server yet.")
	}
	if body.StorageGB == nil || *body.StorageGB < 0 || *body.StorageGB > storageMaxGB {
		return siteNotice{}, invalid(fmt.Sprintf("Choose between 0 and %d GB of extra storage.", storageMaxGB))
	}
	want := *body.StorageGB
	if !body.Preview {
		if fresh, _ := a.sessionFresh(session, time.Now()); !fresh {
			return siteNotice{}, errConfirmFirst
		}
	}
	e, err := a.entitlement(ctx, user.ID)
	if err != nil {
		return siteNotice{}, err
	}
	row := e.Subscription
	if row == nil || row.Status == "canceled" {
		return siteNotice{}, problem(404, "not_found", "You have no subscription to add storage to. Start a trial on the pricing page.")
	}
	if e.Comped {
		return siteNotice{}, problem(409, "conflict", "Your plan is free of charge and its storage is set for you. Ask us if you need more.")
	}
	switch row.Status {
	case "past_due":
		return siteNotice{}, problem(409, "conflict", "Your last payment didn't go through. Update your card in Manage subscription first.")
	case "paused":
		return siteNotice{}, problem(409, "conflict", "Your subscription is paused. Resume it in Manage subscription first.")
	}
	if want < row.StorageGB {
		used, err := h.service.accountStorage(ctx, user.ID)
		if err != nil {
			return siteNotice{}, problem(503, "unavailable", "What your spaces use can't be measured just now, so storage can't be reduced. Try again shortly.")
		}
		if least := minimumExtraGB(used.Total); want < least {
			return siteNotice{}, problem(409, "conflict", fmt.Sprintf("Your spaces use %s, so you need at least %d GB extra. Delete files or spaces first to go lower.", formatGB(used.Total), least))
		}
	}
	// Paddle's current items, so nothing else on the subscription changes.
	sub, err := api.subscription(ctx, row.SubscriptionID)
	if err != nil {
		log.Printf("billing: storage for %s: %v", user.ID, err)
		return siteNotice{}, problem(503, "unavailable", "Paddle can't be reached just now. Try again shortly.")
	}
	if sub.Status != "active" && sub.Status != "trialing" {
		return siteNotice{}, problem(409, "conflict", "Your subscription can't be changed just now. Open Manage subscription to see why.")
	}
	// Storage is billed on the subscription's own cycle: Paddle refuses items
	// on different cycles.
	interval := sub.interval(a.paddle)
	storagePrice := a.paddle.storagePriceFor(interval)
	if storagePrice == "" {
		return siteNotice{}, problem(409, "conflict", "Extra storage for yearly plans isn't set up on this server yet. Ask us if you need more.")
	}
	items, had := []paddleItem{}, 0
	for _, item := range sub.Items {
		if item.Status == "inactive" || item.Price.ID == "" {
			continue
		}
		if a.paddle.isStoragePrice(item.Price.ID) {
			had += item.Quantity
			continue
		}
		items = append(items, paddleItem{PriceID: item.Price.ID, Quantity: item.Quantity})
	}
	if want == had {
		return siteNotice{Notice: fmt.Sprintf("You already have %d GB of extra storage.", want)}, nil
	}
	if want > 0 {
		items = append(items, paddleItem{PriceID: storagePrice, Quantity: want})
	}
	trialing := sub.Status == "trialing"
	result, err := api.changeItems(ctx, sub.ID, trialing, items, body.Preview)
	if err != nil {
		log.Printf("billing: storage %d GB for %s: %v", want, user.ID, err)
		return siteNotice{}, storageRefusal(err, trialing)
	}
	if body.Preview {
		p := &siteStoragePreview{StorageGB: want, Trial: trialing, Interval: interval}
		if t := result.ImmediateTransaction; t != nil {
			p.Now = moneyOf(t.Details.Totals)
		}
		if t := result.NextTransaction; t != nil {
			p.Next, p.NextAt = moneyOf(t.Details.Totals), siteTime(t.BillingPeriod.StartsAt)
		}
		if t := result.RecurringTransactionDetails; t != nil {
			p.Monthly = moneyOf(t.Totals)
		}
		return siteNotice{Preview: p}, nil
	}
	// Store the change now rather than wait for the webhook (which then finds
	// it applied already).
	if _, err := h.service.applySubscription(ctx, nil, result.paddleSubscription, user.ID, time.Now()); err != nil {
		log.Printf("billing: storage for %s: changed at Paddle, not stored yet: %v", user.ID, err)
	}
	if want == 0 {
		return siteNotice{Notice: "Extra storage removed."}, nil
	}
	if trialing {
		return siteNotice{Notice: fmt.Sprintf("You now have %d GB of extra storage, free until your trial ends.", want)}, nil
	}
	return siteNotice{Notice: fmt.Sprintf("You now have %d GB of extra storage.", want)}, nil
}

// storageRefusal says why Paddle refused a storage change.
func storageRefusal(err error, trialing bool) error {
	code := paddleErrorCode(err)
	switch {
	case code == "subscription_trialing_items_update_invalid_options" || trialing && strings.HasPrefix(code, "subscription_trialing"):
		return problem(409, "conflict", "Paddle doesn't allow this change during the free trial. Try again once your trial ends.")
	case strings.Contains(code, "payment") || strings.Contains(code, "declined") || strings.Contains(code, "charge"):
		return problem(409, "conflict", "Paddle couldn't take the payment, so nothing changed. Check your card in Manage subscription.")
	case code == "subscription_locked_pending_changes" || code == "subscription_locked_processing" || code == "subscription_locked_renewal":
		return problem(409, "conflict", "Paddle is busy with your subscription just now. Try again in a few minutes.")
	case code == "forbidden":
		return problem(503, "unavailable", "This server's Paddle key can't change subscriptions yet.")
	case code != "":
		return problem(409, "conflict", "Paddle refused the change. Try again shortly, or open Manage subscription.")
	}
	return problem(503, "unavailable", "Paddle can't be reached just now. Try again shortly.")
}

// formatGB is bytes as GB with one decimal.
func formatGB(n int64) string {
	return fmt.Sprintf("%.1f GB", float64(n)/float64(includedStorage))
}
