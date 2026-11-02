package service

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// Console views of storage (0.89.0, storage.go): `metatrash spaces usage`
// and `metatrash users usage`. They read metatrash_space_usage, which the
// running service keeps; a space it has not measured yet shows no numbers.

// AdminSpaceUsage is what one owned space takes up.
type AdminSpaceUsage struct {
	ID      string `json:"id"`
	OwnerID string `json:"ownerId"`
	Owner   string `json:"owner"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	State   string `json:"state"`
	// Bytes is GitBytes + FileBytes. Measured is false until the service
	// measured the space; FilesMeasuredAt is nil until the bucket was listed.
	Bytes           int64      `json:"bytes"`
	GitBytes        int64      `json:"gitBytes"`
	FileBytes       int64      `json:"fileBytes"`
	FileObjects     int        `json:"fileObjects"`
	Measured        bool       `json:"measured"`
	GitMeasuredAt   *time.Time `json:"gitMeasuredAt,omitempty"`
	FilesMeasuredAt *time.Time `json:"filesMeasuredAt,omitempty"`
}

// AdminUserUsage is one account's storage: the sum over the spaces it owns
// (ready ones; deleted ones until they are purged are DeletedBytes).
type AdminUserUsage struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Username    string `json:"username,omitempty"`
	Spaces      int    `json:"spaces"`
	Unmeasured  int    `json:"unmeasured"`
	Bytes       int64  `json:"bytes"`
	GitBytes    int64  `json:"gitBytes"`
	FileBytes   int64  `json:"fileBytes"`
	FileObjects int    `json:"fileObjects"`
	// DeletedSpaces and DeletedBytes are the account's deleted spaces kept
	// for 30 days, not in Bytes.
	DeletedSpaces int   `json:"deletedSpaces,omitempty"`
	DeletedBytes  int64 `json:"deletedBytes,omitempty"`
	// OldestMeasuredAt is the earliest measurement in Bytes.
	OldestMeasuredAt *time.Time `json:"oldestMeasuredAt,omitempty"`
}

// SpaceUsage lists every owned space that is not purged, or one owner's,
// largest first.
func (d *AdminDB) SpaceUsage(ctx context.Context, ownerID string) ([]AdminSpaceUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	where := "WHERE s.provisioning_state IN ('ready', 'deleted')"
	var args []any
	if ownerID != "" {
		where += " AND s.owner_user_id = ?"
		args = append(args, ownerID)
	}
	out := []AdminSpaceUsage{}
	err := eachRow(ctx, d.store.db, func(scan func(...any) error) error {
		var s AdminSpaceUsage
		var git, files, gitAt, filesAt sql.NullInt64
		var objects sql.NullInt32
		if err := scan(&s.ID, &s.OwnerID, &s.Owner, &s.Slug, &s.Name, &s.State, &git, &files, &objects, &gitAt, &filesAt); err != nil {
			return err
		}
		if gitAt.Valid {
			s.Measured = true
			at := time.Unix(gitAt.Int64, 0).UTC()
			s.GitMeasuredAt = &at
			s.GitBytes, s.FileBytes, s.FileObjects = git.Int64, files.Int64, int(objects.Int32)
			s.Bytes = s.GitBytes + s.FileBytes
		}
		if filesAt.Valid {
			at := time.Unix(filesAt.Int64, 0).UTC()
			s.FilesMeasuredAt = &at
		}
		out = append(out, s)
		return nil
	}, `SELECT s.space_id, s.owner_user_id, COALESCE(o.username, o.email), s.slug, s.name, s.provisioning_state,
	u.git_bytes, u.file_bytes, u.file_objects, u.git_measured_at, u.files_measured_at
FROM metatrash_spaces s JOIN metatrash_users o ON o.user_id = s.owner_user_id
LEFT JOIN metatrash_space_usage u ON u.space_id = s.space_id `+where+` LIMIT 10001`, args...)
	if err != nil {
		return nil, fmt.Errorf("cannot read storage usage")
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Owner+"/"+out[i].Slug < out[j].Owner+"/"+out[j].Slug
	})
	return out, nil
}

// UserUsage totals storage per account, for every account that is not
// deleted (or the one given), largest first.
func (d *AdminDB) UserUsage(ctx context.Context, only *AdminUser) ([]AdminUserUsage, error) {
	var users []AdminUser
	if only != nil {
		users = []AdminUser{*only}
	} else {
		var err error
		if users, err = d.Users(ctx); err != nil {
			return nil, err
		}
	}
	ownerID := ""
	if only != nil {
		ownerID = only.ID
	}
	spaces, err := d.SpaceUsage(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	byOwner := map[string]*AdminUserUsage{}
	out := []AdminUserUsage{}
	for _, u := range users {
		if u.DeletedAt != nil {
			continue
		}
		out = append(out, AdminUserUsage{ID: u.ID, Email: u.Email, Username: u.Username})
	}
	for i := range out {
		byOwner[out[i].ID] = &out[i]
	}
	for _, s := range spaces {
		a := byOwner[s.OwnerID]
		if a == nil {
			continue
		}
		if s.State == "deleted" {
			a.DeletedSpaces++
			a.DeletedBytes += s.Bytes
			continue
		}
		a.Spaces++
		if !s.Measured {
			a.Unmeasured++
			continue
		}
		a.Bytes += s.Bytes
		a.GitBytes += s.GitBytes
		a.FileBytes += s.FileBytes
		a.FileObjects += s.FileObjects
		at := *s.GitMeasuredAt
		if s.FilesMeasuredAt != nil && s.FilesMeasuredAt.Before(at) {
			at = *s.FilesMeasuredAt
		}
		if a.OldestMeasuredAt == nil || at.Before(*a.OldestMeasuredAt) {
			a.OldestMeasuredAt = &at
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Email < out[j].Email
	})
	return out, nil
}
