package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Console views and changes of billing (0.91.0, billing.go):
// `metatrash users comp|uncomp <who>` and `metatrash billing show [<who>]`.

// AdminBilling is one account's plan and what Paddle last said about its
// subscription.
type AdminBilling struct {
	UserID   string `json:"userId"`
	Email    string `json:"email"`
	Username string `json:"username,omitempty"`
	// Plan, Status and StorageBytes are the entitlement (billing.go).
	Plan         string     `json:"plan"`
	Status       string     `json:"status"`
	StorageBytes int64      `json:"storageBytes"`
	CompedAt     *time.Time `json:"compedAt,omitempty"`
	// Subscription is absent for an account that never subscribed.
	Subscription *AdminSubscription `json:"subscription,omitempty"`
}

// AdminSubscription is a row of metatrash_subscriptions.
type AdminSubscription struct {
	SubscriptionID  string    `json:"subscriptionId"`
	CustomerID      string    `json:"customerId"`
	Status          string    `json:"status"`
	StorageGB       int       `json:"storageGb"`
	TrialEndsAt     time.Time `json:"trialEndsAt,omitzero"`
	PeriodEndsAt    time.Time `json:"periodEndsAt,omitzero"`
	NextBilledAt    time.Time `json:"nextBilledAt,omitzero"`
	ScheduledChange string    `json:"scheduledChange,omitempty"`
	ScheduledAt     time.Time `json:"scheduledAt,omitzero"`
	CanceledAt      time.Time `json:"canceledAt,omitzero"`
	// PaddleUpdatedAt is the subscription's updated_at at Paddle; UpdatedAt
	// when this server last wrote the row.
	PaddleUpdatedAt time.Time `json:"paddleUpdatedAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

// AdminPaddleEvent is a Paddle event the webhook recorded.
type AdminPaddleEvent struct {
	EventID        string    `json:"eventId"`
	EventType      string    `json:"eventType"`
	OccurredAt     time.Time `json:"occurredAt"`
	ReceivedAt     time.Time `json:"receivedAt"`
	SubscriptionID string    `json:"subscriptionId,omitempty"`
	UserID         string    `json:"userId,omitempty"`
	Outcome        string    `json:"outcome"`
}

func adminSubscription(row *subscriptionRow) *AdminSubscription {
	if row == nil {
		return nil
	}
	return &AdminSubscription{SubscriptionID: row.SubscriptionID, CustomerID: row.CustomerID, Status: row.Status, StorageGB: row.StorageGB,
		TrialEndsAt: row.TrialEnds, PeriodEndsAt: row.PeriodEnds, NextBilledAt: row.NextBilled, ScheduledChange: row.ScheduledChange,
		ScheduledAt: row.ScheduledAt, CanceledAt: row.CanceledAt, PaddleUpdatedAt: time.UnixMilli(row.PaddleUpdated).UTC(), UpdatedAt: row.Updated}
}

// Billing is one account's plan.
func (d *AdminDB) Billing(ctx context.Context, u AdminUser) (AdminBilling, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var compedAt sql.NullInt64
	if err := d.store.db.QueryRowContext(ctx, "SELECT comped_at FROM metatrash_users WHERE user_id = ?", u.ID).Scan(&compedAt); err != nil {
		return AdminBilling{}, fmt.Errorf("account database unavailable")
	}
	sub, err := subscriptionWhere(ctx, d.store.db, "user_id = ?", u.ID)
	if err != nil {
		return AdminBilling{}, fmt.Errorf("account database unavailable")
	}
	return adminBilling(u.ID, u.Email, u.Username, compedAt, sub), nil
}

func adminBilling(id, email, username string, compedAt sql.NullInt64, sub *subscriptionRow) AdminBilling {
	e := deriveEntitlement(compedAt, sub)
	out := AdminBilling{UserID: id, Email: email, Username: username, Plan: e.Plan, Status: e.Status, StorageBytes: e.StorageBytes, Subscription: adminSubscription(sub)}
	if compedAt.Valid {
		at := time.Unix(compedAt.Int64, 0).UTC()
		out.CompedAt = &at
	}
	return out
}

// BillingAccounts lists every account that is comped or has a subscription,
// newest change first.
func (d *AdminDB) BillingAccounts(ctx context.Context) ([]AdminBilling, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out := []AdminBilling{}
	err := eachRow(ctx, d.store.db, func(scan func(...any) error) error {
		var id, email string
		var username, customer, subscription, status, change sql.NullString
		var compedAt, storage, trial, period, next, scheduledAt, canceled, paddleUpdated, updated sql.NullInt64
		if err := scan(&id, &email, &username, &compedAt, &customer, &subscription, &status, &storage, &trial, &period, &next, &change, &scheduledAt, &canceled, &paddleUpdated, &updated); err != nil {
			return err
		}
		var sub *subscriptionRow
		if subscription.Valid {
			sub = &subscriptionRow{UserID: id, CustomerID: customer.String, SubscriptionID: subscription.String, Status: status.String, StorageGB: int(storage.Int64),
				TrialEnds: fromNullUnix(trial), PeriodEnds: fromNullUnix(period), NextBilled: fromNullUnix(next), ScheduledChange: change.String,
				ScheduledAt: fromNullUnix(scheduledAt), CanceledAt: fromNullUnix(canceled), PaddleUpdated: paddleUpdated.Int64, Updated: fromNullUnix(updated)}
		}
		out = append(out, adminBilling(id, email, username.String, compedAt, sub))
		return nil
	}, `SELECT u.user_id, u.email, u.username, u.comped_at, s.customer_id, s.subscription_id, s.status, s.storage_gb, s.trial_ends_at, s.period_ends_at,
s.next_billed_at, s.scheduled_change, s.scheduled_at, s.canceled_at, s.paddle_updated_at, s.updated_at
FROM metatrash_users u LEFT JOIN metatrash_subscriptions s ON s.user_id = u.user_id
WHERE u.comped_at IS NOT NULL OR s.user_id IS NOT NULL
ORDER BY COALESCE(s.updated_at, u.comped_at) DESC, u.user_id LIMIT 10001`)
	if err != nil {
		return nil, fmt.Errorf("account database unavailable")
	}
	return out, nil
}

// PaddleEvents lists the newest recorded events, or only those with outcome.
func (d *AdminDB) PaddleEvents(ctx context.Context, outcome string, limit int) ([]AdminPaddleEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if limit < 1 || limit > 1000 {
		limit = 50
	}
	where, args := "", []any{}
	if outcome != "" {
		where, args = "WHERE outcome = ?", append(args, outcome)
	}
	out := []AdminPaddleEvent{}
	err := eachRow(ctx, d.store.db, func(scan func(...any) error) error {
		var e AdminPaddleEvent
		var occurred, received int64
		var subscription, user sql.NullString
		if err := scan(&e.EventID, &e.EventType, &occurred, &received, &subscription, &user, &e.Outcome); err != nil {
			return err
		}
		e.OccurredAt, e.ReceivedAt = time.Unix(occurred, 0).UTC(), time.Unix(received, 0).UTC()
		e.SubscriptionID, e.UserID = subscription.String, user.String
		out = append(out, e)
		return nil
	}, "SELECT event_id, event_type, occurred_at, received_at, subscription_id, user_id, outcome FROM metatrash_paddle_events "+where+fmt.Sprintf(" ORDER BY received_at DESC, event_id LIMIT %d", limit), args...)
	if err != nil {
		return nil, fmt.Errorf("account database unavailable")
	}
	return out, nil
}

// SetComped comps an account (a full subscriber with no charge) or ends it.
// changed is false when it already was as asked. A comped account's own
// subscription, if it has one, carries on at Paddle: cancel it there.
func (d *AdminDB) SetComped(ctx context.Context, u AdminUser, on bool, now time.Time) (changed bool, err error) {
	if u.DeletedAt != nil {
		return false, fmt.Errorf("the account was deleted")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	query, args := "UPDATE metatrash_users SET comped_at = ? WHERE user_id = ? AND comped_at IS NULL", []any{now.Unix(), u.ID}
	if !on {
		query, args = "UPDATE metatrash_users SET comped_at = NULL WHERE user_id = ? AND comped_at IS NOT NULL", []any{u.ID}
	}
	result, err := d.store.db.ExecContext(ctx, query, args...)
	var denied *mysql.MySQLError
	if errors.As(err, &denied) && (denied.Number == 1142 || denied.Number == 1143) {
		return false, fmt.Errorf("the database login cannot comp accounts; run deploy/account-grants.sql again (0.91.0 added UPDATE (comped_at))")
	}
	if err != nil {
		return false, fmt.Errorf("account database unavailable")
	}
	n, _ := result.RowsAffected()
	return n > 0, nil
}
