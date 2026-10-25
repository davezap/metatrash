package service

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDatabaseWebStore(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	store := dbWebStore{w.db.db}
	now := time.Now().UTC().Truncate(time.Second)
	page, text := webObject{Hash: webHash([]byte("page")), ContentType: "text/html", Bytes: 4, Uploaded: now}, webObject{Hash: webHash([]byte("md")), ContentType: "text/markdown", Bytes: 2, Uploaded: now}
	for _, o := range []webObject{page, text, page} {
		if err := store.saveObject(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	later := page
	later.Uploaded = now.Add(time.Hour)
	if err := store.saveObject(ctx, later); err != nil {
		t.Fatal(err)
	}
	if o, ok, err := store.object(ctx, page.Hash); err != nil || !ok || !o.Uploaded.Equal(later.Uploaded) || o.Bytes != 4 {
		t.Fatalf("object %+v %v %v", o, ok, err)
	}
	address := "https://example.org/leave?a=1&b=ā"
	ref := webRef{ID: "0123456789abcdef", UserID: w.owner.ID, URL: address, Original: page.Hash, Text: text.Hash, Kind: webHTML, Ext: "html", Title: "Leave — guide", Lines: 12, ETag: `"v1"`, Created: now, Expires: now.Add(webRefDays * 24 * time.Hour)}
	if err := store.saveRef(ctx, ref); err != nil {
		t.Fatal(err)
	}
	image := webRef{ID: "fedcba9876543210", UserID: w.owner.ID, URL: "https://example.org/pic.png", Original: page.Hash, Kind: webKindImage, Ext: "png", Created: now, Expires: now.Add(time.Hour)}
	if err := store.saveRef(ctx, image); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := store.latestRef(ctx, w.owner.ID, address, now); err != nil || !ok || got.ID != ref.ID || got.Title != ref.Title || got.Text != text.Hash || got.ETag != `"v1"` {
		t.Fatalf("latest %+v %v %v", got, ok, err)
	}
	if _, ok, _ := store.latestRef(ctx, w.member.ID, address, now); ok {
		t.Error("another user found the reference by URL")
	}
	if _, ok, _ := store.ref(ctx, w.member.ID, ref.ID, now); ok {
		t.Error("another user found the reference")
	}
	if got, ok, err := store.ref(ctx, w.owner.ID, image.ID, now); err != nil || !ok || got.Text != "" || got.Kind != webKindImage {
		t.Fatalf("image ref %+v %v %v", got, ok, err)
	}
	// Two hours on, the image reference has expired; objects stay while
	// references use them or the bucket still holds them.
	if err := store.expire(ctx, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.ref(ctx, w.owner.ID, image.ID, now); ok {
		t.Error("expired reference kept")
	}
	if err := store.expire(ctx, now.Add(40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{page.Hash, text.Hash} {
		if _, ok, _ := store.object(ctx, hash); ok {
			t.Errorf("object %s kept after its references and files went", hash)
		}
	}
}

func TestDatabaseSchedulesAndTimezone(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	store := dbJobStore{w.db.db}
	now := time.Now().UTC().Truncate(time.Second)
	row := actionSchedule{SpaceID: w.ownedID, Folder: "hr/", Action: "digest", Every: "day 06:00", NextRun: now.Add(-time.Minute)}
	if err := store.addSchedule(ctx, row); err != nil {
		t.Fatal(err)
	}
	due, err := store.dueSchedules(ctx, now, 10)
	if err != nil || len(due) != 1 || due[0] != row {
		t.Fatalf("due %+v %v", due, err)
	}
	if ok, err := store.advanceSchedule(ctx, row, now.Add(time.Hour), now); err != nil || !ok {
		t.Fatalf("advance %v %v", ok, err)
	}
	if ok, _ := store.advanceSchedule(ctx, row, now.Add(time.Hour), now); ok {
		t.Error("the same run was taken twice")
	}
	row.Every, row.NextRun = "hour", now.Add(2*time.Hour)
	if err := store.setSchedule(ctx, row); err != nil {
		t.Fatal(err)
	}
	rows, err := store.schedules(ctx, w.ownedID)
	if err != nil || len(rows) != 1 || rows[0].Every != "hour" || !rows[0].NextRun.Equal(row.NextRun) || !rows[0].LastRun.Equal(now) {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if due, _ := store.dueSchedules(ctx, now, 10); len(due) != 0 {
		t.Errorf("due again: %+v", due)
	}
	if err := store.removeSchedule(ctx, row); err != nil {
		t.Fatal(err)
	}
	if rows, _ := store.schedules(ctx, w.ownedID); len(rows) != 0 {
		t.Errorf("not removed: %+v", rows)
	}

	// The time zone: the owner's form, then the runner reads it.
	session := w.session(w.owner)
	csrf := w.s.accounts.mac("space-timezone:" + session.Value)
	if res := oauthCall(w.h, "POST", "/account/spaces/timezone", url.Values{"csrf": {csrf}, "space": {w.ownedID}, "timezone": {"Mars/Olympus"}}, session); res.Code != 400 {
		t.Fatalf("bad zone: %d", res.Code)
	}
	if res := oauthCall(w.h, "POST", "/account/spaces/timezone", url.Values{"csrf": {csrf}, "space": {w.joinID}, "timezone": {"Australia/Perth"}}, session); res.Code != 404 {
		t.Fatalf("a space of someone else's: %d", res.Code)
	}
	if res := oauthCall(w.h, "POST", "/account/spaces/timezone", url.Values{"csrf": {csrf}, "space": {w.ownedID}, "timezone": {"Australia/Perth"}}, session); res.Code != 303 {
		t.Fatalf("set zone: %d %s", res.Code, res.Body.String())
	}
	if got := w.s.dbSpaceZone(ctx, w.ownedID); got != "Australia/Perth" {
		t.Fatalf("zone %q", got)
	}
	account := oauthCall(w.h, "GET", "/account", nil, session)
	if body := account.Body.String(); !strings.Contains(body, `value="Australia/Perth"`) || !strings.Contains(body, `list="timezones"`) {
		t.Error("the account page lacks the zone")
	}
	if res := oauthCall(w.h, "POST", "/account/spaces/timezone", url.Values{"csrf": {csrf}, "space": {w.ownedID}, "timezone": {""}}, session); res.Code != 303 {
		t.Fatalf("clear zone: %d", res.Code)
	}
	if got := w.s.dbSpaceZone(ctx, w.ownedID); got != "" {
		t.Fatalf("zone %q after clearing", got)
	}
	// A new space takes the browser's zone.
	if _, err := w.db.db.ExecContext(ctx, "UPDATE metatrash_users SET max_private_spaces = 10 WHERE user_id = ?", w.owner.ID); err != nil {
		t.Fatal(err)
	}
	name := randomName(t, "Zone ")
	spaceCSRF := w.s.accounts.mac("space-create:" + session.Value)
	if res := oauthCall(w.h, "POST", "/account/spaces", url.Values{"csrf": {spaceCSRF}, "name": {name}, "timezone": {"Europe/London"}}, session); res.Code != 303 {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	zones, err := w.db.spaceTimezones(ctx, w.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, zone := range zones {
		found = found || zone == "Europe/London"
	}
	if !found {
		t.Errorf("new space zones %v", zones)
	}
}
