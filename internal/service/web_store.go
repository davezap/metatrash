package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// The downloads cache (schema v18). Each download is stored once in the
// bucket, under web/<sha256 of its bytes>, and recorded in
// metatrash_web_objects. A reference (metatrash_web_refs) ties one user's
// fetch of a URL to its original and, for a page, its Markdown text; agents
// name it as web/<ref>.<ext>. References belong to the user whose agent made
// them (whoever paid for the job): another user's reference is simply not
// found. The bucket's lifecycle rule deletes files 30 days after upload, so
// references expire a day before their oldest file and rows are deleted as
// they expire.

const (
	// webBucketDays is the bucket's lifecycle rule: files go 30 days after
	// upload. A file older than webReuseDays is uploaded again rather than
	// reused, and references expire webRefDays after the upload.
	webBucketDays = 30
	webReuseDays  = 27
	webRefDays    = 29
)

type webObject struct {
	Hash, ContentType string
	Bytes             int64
	Uploaded          time.Time
}

func (o webObject) key() string { return "web/" + o.Hash }

func webHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// webRef is one reference. Original and Text are object hashes; Text is ""
// when the download has no text of its own (an image, a PDF) and equals
// Original for a text file. Kind and Ext describe the original.
type webRef struct {
	ID, UserID, URL, Original, Text string
	Kind, Ext, Title                string
	Lines                           int
	ETag, LastModified              string
	Created, Expires                time.Time
}

// webRefStore keeps references and objects; tests use memory.
type webRefStore interface {
	// latestRef is the user's newest unexpired reference to a URL.
	latestRef(ctx context.Context, userID, url string, now time.Time) (webRef, bool, error)
	// ref is one of the user's unexpired references.
	ref(ctx context.Context, userID, id string, now time.Time) (webRef, bool, error)
	saveRef(ctx context.Context, r webRef) error
	object(ctx context.Context, hash string) (webObject, bool, error)
	// saveObject records an upload: a new object, or a new upload time.
	saveObject(ctx context.Context, o webObject) error
	// expire deletes expired references, then objects the bucket has deleted
	// that no reference uses.
	expire(ctx context.Context, now time.Time) error
}

type dbWebStore struct{ db *sql.DB }

const webRefColumns = "ref_id, user_id, url, original_hash, text_hash, kind, extension, title, text_lines, etag, last_modified, created_at, expires_at"

func (db *accountDatabase) checkWebSchema(ctx context.Context) error {
	bad := fmt.Errorf("account schema v21 required (web tools, schedules, virtual file refresh, deleted spaces and accounts); follow docs/deployment.md")
	for _, query := range []string{
		"SELECT " + webRefColumns + " FROM metatrash_web_refs LIMIT 0",
		"SELECT object_hash, content_type, bytes, uploaded_at FROM metatrash_web_objects LIMIT 0",
		"SELECT " + scheduleColumns + " FROM metatrash_action_schedules LIMIT 0",
		"SELECT timezone FROM metatrash_spaces LIMIT 0",
		"SELECT " + refreshColumns + " FROM metatrash_virtual_refresh LIMIT 0",
		"SELECT deleted_at FROM metatrash_spaces LIMIT 0",
		"SELECT deletion_requested_at, deleted_at FROM metatrash_users LIMIT 0",
	} {
		rows, err := db.db.QueryContext(ctx, query)
		if err != nil {
			return bad
		}
		rows.Close()
	}
	return nil
}

func scanWebRef(scan func(...any) error) (webRef, error) {
	var r webRef
	var text sql.NullString
	var created, expires int64
	err := scan(&r.ID, &r.UserID, &r.URL, &r.Original, &text, &r.Kind, &r.Ext, &r.Title, &r.Lines, &r.ETag, &r.LastModified, &created, &expires)
	r.Text = text.String
	r.Created, r.Expires = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC()
	return r, err
}

func (s dbWebStore) oneRef(ctx context.Context, query string, args ...any) (webRef, bool, error) {
	r, err := scanWebRef(s.db.QueryRowContext(ctx, "SELECT "+webRefColumns+" FROM metatrash_web_refs "+query, args...).Scan)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return webRef{}, false, nil
	case err != nil:
		return webRef{}, false, fmt.Errorf("cannot read web references")
	}
	return r, true, nil
}

func (s dbWebStore) latestRef(ctx context.Context, userID, url string, now time.Time) (webRef, bool, error) {
	return s.oneRef(ctx, "WHERE user_id = ? AND url_hash = ? AND url = ? AND expires_at > ? ORDER BY created_at DESC, ref_id LIMIT 1", userID, webHash([]byte(url)), url, now.Unix())
}

func (s dbWebStore) ref(ctx context.Context, userID, id string, now time.Time) (webRef, bool, error) {
	return s.oneRef(ctx, "WHERE ref_id = ? AND user_id = ? AND expires_at > ?", id, userID, now.Unix())
}

func (s dbWebStore) saveRef(ctx context.Context, r webRef) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO metatrash_web_refs ("+webRefColumns+", url_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		r.ID, r.UserID, r.URL, r.Original, nullText(r.Text), r.Kind, r.Ext, r.Title, r.Lines, r.ETag, r.LastModified, r.Created.Unix(), r.Expires.Unix(), webHash([]byte(r.URL)))
	if err != nil {
		return fmt.Errorf("cannot save the web reference")
	}
	return nil
}

func (s dbWebStore) object(ctx context.Context, hash string) (webObject, bool, error) {
	o := webObject{Hash: hash}
	var uploaded int64
	err := s.db.QueryRowContext(ctx, "SELECT content_type, bytes, uploaded_at FROM metatrash_web_objects WHERE object_hash = ?", hash).Scan(&o.ContentType, &o.Bytes, &uploaded)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return o, false, nil
	case err != nil:
		return o, false, fmt.Errorf("cannot read web objects")
	}
	o.Uploaded = time.Unix(uploaded, 0).UTC()
	return o, true, nil
}

func (s dbWebStore) saveObject(ctx context.Context, o webObject) error {
	result, err := s.db.ExecContext(ctx, "UPDATE metatrash_web_objects SET uploaded_at = ? WHERE object_hash = ?", o.Uploaded.Unix(), o.Hash)
	if err == nil {
		if n, _ := result.RowsAffected(); n == 1 {
			return nil
		}
		_, err = s.db.ExecContext(ctx, "INSERT INTO metatrash_web_objects (object_hash, content_type, bytes, uploaded_at) VALUES (?, ?, ?, ?)", o.Hash, o.ContentType, o.Bytes, o.Uploaded.Unix())
		if err != nil {
			// Another job recorded the same bytes in between, or the time was
			// already this second's.
			if _, found, readErr := s.object(ctx, o.Hash); readErr == nil && found {
				return nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("cannot record the web download")
	}
	return nil
}

func (s dbWebStore) expire(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM metatrash_web_refs WHERE expires_at <= ?", now.Unix()); err != nil {
		return fmt.Errorf("cannot expire web references")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM metatrash_web_objects WHERE uploaded_at <= ?
		AND NOT EXISTS (SELECT 1 FROM metatrash_web_refs r WHERE r.original_hash = metatrash_web_objects.object_hash OR r.text_hash = metatrash_web_objects.object_hash)`,
		now.Add(-webBucketDays*24*time.Hour).Unix())
	if err != nil {
		return fmt.Errorf("cannot expire web objects")
	}
	return nil
}
