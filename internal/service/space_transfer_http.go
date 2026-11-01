package service

import (
	"context"
	"time"
)

// transferMailsPerOwnerDay caps offer emails per owner across all spaces.
const transferMailsPerOwnerDay = 5

// emailTransferOffer tells the member about the offer and returns a notice
// key. The offer stands whatever happens to the email. SMTP errors may
// contain addresses or credentials; never log them.
func (h *httpAdapter) emailTransferOffer(ctx context.Context, owner userAccount, spaceID string) string {
	a := h.service.accounts
	db := h.service.ownedDB
	if a.sendNotice == nil {
		return "transfer-offered-unsent"
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	var email, username, name, slug string
	var expires int64
	err := db.db.QueryRowContext(queryCtx, `SELECT u.email, u.username, s.name, s.slug, t.expires_at FROM metatrash_space_transfers t JOIN metatrash_spaces s ON s.space_id = t.space_id JOIN metatrash_users u ON u.user_id = t.to_user_id WHERE t.space_id = ? AND t.from_user_id = ? AND s.owner_user_id = ?`, spaceID, owner.ID, owner.ID).Scan(&email, &username, &name, &slug, &expires)
	cancel()
	if err != nil {
		return "transfer-offered-unsent"
	}
	if err := h.service.rates.reserve(
		allowance{"transfer-mail:owner:" + owner.ID, transferMailsPerOwnerDay, 86400},
		allowance{"transfer-mail:" + spaceID + ":" + email, 1, 3600},
		allowance{"mail:global:day", 100, 86400},
	); err != nil {
		return "transfer-offered-unsent"
	}
	select {
	case a.mailSlots <- struct{}{}:
		defer func() { <-a.mailSlots }()
	default:
		return "transfer-offered-unsent"
	}
	body := transferOfferMail(a.config.Origin, owner.Username, name, owner.Username+"/"+slug, username+"/"+slug, time.Unix(expires, 0))
	if err := a.sendNotice(ctx, email, "A Metatrash space has been offered to you", body); err != nil {
		return "transfer-offered-unsent"
	}
	return "transfer-offered"
}

// notifyTransferAccepted emails the previous owner. Best effort.
func (h *httpAdapter) notifyTransferAccepted(t acceptedTransfer) {
	body := transferAcceptedMail(h.service.accounts.config.Origin, t.To.Username, t.Name, t.From.Username+"/"+t.Slug, t.To.Username+"/"+t.Slug, time.Now())
	h.sendSecurityNotice(t.From, "Your Metatrash space has a new owner", body)
}

// transferSection fills the owner's Transfer ownership part of the sharing
// page: the pending offer, and the members it could go to.
func (h *httpAdapter) transferSection(ctx context.Context, page *humanSharingPage, ownerID string) error {
	for _, m := range page.Members {
		if m.Status == "active" && m.Username != "" {
			page.Candidates = append(page.Candidates, m)
		}
	}
	var err error
	page.Transfer, err = h.service.ownedDB.pendingTransfer(ctx, page.ID, ownerID, time.Now())
	return err
}
