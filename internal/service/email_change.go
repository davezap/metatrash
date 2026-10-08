package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Change email address (0.26.0), on Your account → Security.
//
// Starting a change needs a session that signed in or confirmed within
// stepUpWindow. A six-digit code goes to the new address and waits under the
// session (like the confirmation code); entering it moves the account to the
// new address. The new address gets a notice that it is now the account's
// address, and the old one a notice of the change with what to do if it was
// not the account owner.
//
// An address that already belongs to another account gets a notice instead
// of a code, so the form answers the same whatever addresses have accounts.
// The database's unique email key is the final check.

// emailChangesPerUserDay caps how many changes an account may start a day.
const emailChangesPerUserDay = 5

var errConfirmEmailChange = problem(403, "confirm_required", "Confirm it’s you first. Changing your email address needs a sign-in or confirmation within the last 10 minutes.")

// emailChangeKey is where a session's code for a new address waits.
func emailChangeKey(session string) string {
	return secretDigest("email-change:" + session)
}

// pendingEmailChange is the new address a code was sent to for this session,
// or "" when none is waiting.
func (a *accounts) pendingEmailChange(session string, now time.Time) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.challenges[emailChangeKey(session)]
	if !ok || !c.Ready || !now.Before(c.Expires) {
		return ""
	}
	return c.Email
}

// startEmailChange emails a code to the new address (POST /account/email/change).
func (h *httpAdapter) startEmailChange(ctx context.Context, session, client string, user userAccount, raw string, now time.Time) error {
	a := h.service.accounts
	if fresh, _ := a.sessionFresh(session, now); !fresh {
		return errConfirmEmailChange
	}
	email, err := normalizeEmail(raw)
	if err != nil {
		return invalid("Enter a valid email address.")
	}
	if email == user.Email {
		return invalid("That is already your email address.")
	}
	if err := h.service.rates.take(allowance{"email-change:user:" + user.ID, emailChangesPerUserDay, 86400}); err != nil {
		return err
	}
	emailKey := a.mac("email:" + email)
	if err := h.service.rates.reserve(allowance{"mail:global:day", 100, 86400}, allowance{"mail:ip:" + client, 10, 3600}, allowance{"mail:email:minute:" + emailKey, 1, 60}, allowance{"mail:email:hour:" + emailKey, 3, 3600}); err != nil {
		return err
	}
	taken, err := a.store.Exists(ctx, email)
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	send := a.sendEmailChange
	if taken {
		send = func(ctx context.Context, email, _ string) error {
			if a.sendNotice == nil {
				return errors.New("no mail")
			}
			return a.sendNotice(ctx, email, "Your address already has a Metatrash account", emailChangeTakenMail(a.config.Origin))
		}
	}
	return a.issueCode(ctx, emailChangeKey(session), email, send)
}

// finishEmailChange checks the code and moves the account to the new address
// (POST /account/email/verify). It returns the address it moved to.
func (h *httpAdapter) finishEmailChange(ctx context.Context, session string, user userAccount, code string, now time.Time) (string, error) {
	a, db := h.service.accounts, h.service.ownedDB
	if db == nil {
		return "", problem(503, "unavailable", "Your account is temporarily unavailable.")
	}
	a.mu.Lock()
	a.cleanup(now)
	email, err := a.checkCodeLocked(emailChangeKey(session), code)
	a.mu.Unlock()
	if err != nil {
		return "", err
	}
	if err := db.changeEmail(ctx, user.ID, user.Email, email); err != nil {
		return "", err
	}
	a.mu.Lock()
	// Codes still waiting for either address belong to the old state: a
	// login code for the old address would otherwise create a new account,
	// and a confirmation code was sent to the old address.
	for k, pending := range a.challenges {
		if pending.Email == user.Email || pending.Email == email {
			delete(a.challenges, k)
		}
	}
	a.mu.Unlock()
	log.Printf("account email-change user=%s result=ok", user.ID)
	moved := user
	moved.Email = email
	h.sendSecurityNotice(user, "Your Metatrash email address was changed", emailChangedOldMail(a.config.Origin, user.Email, email, now))
	h.sendSecurityNotice(moved, "This is now your Metatrash email address", emailChangedNewMail(a.config.Origin, user.Email, email, now))
	return email, nil
}

// cancelEmailChange forgets a code waiting for a new address.
func (a *accounts) cancelEmailChange(session string) {
	a.mu.Lock()
	delete(a.challenges, emailChangeKey(session))
	a.mu.Unlock()
}

// changeEmail moves an account from one address to another. It refuses when
// the account's address is no longer from, or another account has to.
func (db *accountDatabase) changeEmail(ctx context.Context, userID, from, to string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if normalized, err := normalizeEmail(to); err != nil || normalized != to {
		return invalid("Enter a valid email address.")
	}
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRowContext(ctx, "SELECT email FROM metatrash_users WHERE user_id = ? FOR UPDATE", userID).Scan(&current); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	if current != from {
		return problem(409, "conflict", "Your email address changed in the meantime. Reload the page.")
	}
	_, err = tx.ExecContext(ctx, "UPDATE metatrash_users SET email = ? WHERE user_id = ?", to, userID)
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return problem(409, "conflict", "That address now has its own Metatrash account, so your address was not changed.")
	}
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	return nil
}
