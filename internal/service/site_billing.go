package service

import (
	"context"
	"log"
	"time"
)

// Settings → Billing (0.91.0): the account's plan as GET /api/v1/site/account
// shows it under "billing", and
//
//	POST /api/v1/site/account/billing/portal   {}  → {"redirect": Paddle's customer portal}
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
	Storage   int64  `json:"storage"`
	StorageGB int    `json:"storageGb,omitempty"`
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
}

// siteBillingState is the billing part of GET /account; nil when Paddle is
// not configured.
func (h *httpAdapter) siteBillingState(ctx context.Context, user userAccount) (*siteBilling, error) {
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
