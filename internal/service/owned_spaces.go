package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ownedSpace contains immutable identity/routing metadata. Owner authority comes
// from OwnerID alone; there is no independently editable owner membership.
type ownedSpace struct {
	ID, OwnerID, Name, Slug, State string
	// Visibility is "private" (owner and members) or "web" (also readable by
	// anyone in the website's read-only explorer). Agent access is the same
	// for both.
	Visibility string
	CreatedAt  time.Time
}

const (
	spacePrivate = "private"
	spaceWeb     = "web"
)

const ownedSpaceColumns = "space_id, owner_user_id, name, slug, provisioning_state, created_at, visibility"
const ownedSpaceREADME = "# Private space\n\nThis space belongs to its human owner. Human browsing is read-only.\nThe owner can invite people through Your account. Agents reach this space only\nthrough apps that the owner or a member connected with OAuth and chose this\nspace for; the owner decides whether members' apps may write.\n\nAgents: start by reading .metatrash.json in the space root. It describes\nthe space and its folders; a folder may have its own .metatrash.json.\n"

func (db *accountDatabase) checkOwnedSpaceSchema(ctx context.Context) error {
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'metatrash_spaces' AND engine = 'InnoDB'").Scan(&count); err != nil || count != 1 {
		return fmt.Errorf("owned-space InnoDB table required; follow docs/deployment.md")
	}
	for _, index := range []struct{ name, columns string }{
		{"PRIMARY", "space_id"},
		{"metatrash_spaces_owner_slug", "owner_user_id,slug"},
	} {
		var columns sql.NullString
		var nonUnique, prefixes int
		if err := db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ','), COALESCE(SUM(non_unique), 0), COUNT(sub_part) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'metatrash_spaces' AND index_name = ?", index.name).Scan(&columns, &nonUnique, &prefixes); err != nil || columns.String != index.columns || nonUnique != 0 || prefixes != 0 {
			return fmt.Errorf("owned-space primary and owner/slug unique indexes required")
		}
	}
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.key_column_usage WHERE constraint_schema = DATABASE() AND table_name = 'metatrash_spaces' AND constraint_name = 'metatrash_spaces_owner' AND column_name = 'owner_user_id' AND referenced_table_name = 'metatrash_users' AND referenced_column_name = 'user_id'").Scan(&count); err != nil || count != 1 {
		return fmt.Errorf("owned-space owner foreign key required")
	}
	return nil
}

// slugFold maps common accented Latin letters, including te reo Māori
// macrons, to ASCII when a slug is derived from a space name.
var slugFold = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a", 'æ': "ae",
	'ç': "c", 'ð': "d", 'đ': "d",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ī': "i",
	'ł': "l", 'ñ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ō': "o", 'œ': "oe",
	'ß': "ss", 'þ': "th",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ū': "u",
	'ý': "y", 'ÿ': "y",
}

// deriveSpaceSlug makes a space's URL slug from its name: lowercase ASCII
// letters and digits, folded accents, apostrophes dropped, and every other run
// of characters as one hyphen. Long results are cut to 48 characters at a word
// break where possible. The slug column stays separate from the name, so the
// stored slug never changes if this rule does.
func deriveSpaceSlug(name string) (string, error) {
	var b strings.Builder
	gap := false
	for _, c := range strings.ToLower(strings.TrimSpace(name)) {
		part := ""
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			part = string(c)
		case c == '\'' || c == '’':
			continue
		default:
			part = slugFold[c]
		}
		if part == "" {
			gap = b.Len() > 0
			continue
		}
		if gap {
			b.WriteByte('-')
			gap = false
		}
		b.WriteString(part)
	}
	slug := b.String()
	if len(slug) > 48 {
		slug = slug[:48]
		if i := strings.LastIndexByte(slug, '-'); i >= 24 {
			slug = slug[:i]
		}
		slug = strings.TrimRight(slug, "-")
	}
	if slug == "" {
		return "", invalid("Use at least one letter or number in the name. It becomes the space's address.")
	}
	return slug, nil
}

func normalizeSpaceName(name, slug string) (string, string, error) {
	name = strings.TrimSpace(name)
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 120 {
		return "", "", invalid("Use a space name of 1–120 characters.")
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return "", "", invalid("Space names cannot contain control characters.")
		}
	}
	if len(slug) < 1 || len(slug) > 48 || !usernamePattern.MatchString(slug) {
		return "", "", invalid("Use a slug of 1–48 letters or numbers, with single hyphens between them.")
	}
	return name, slug, nil
}

// scanOwnedSpace reads ownedSpaceColumns, then any extra columns into extra.
func scanOwnedSpace(row accountScanner, extra ...any) (ownedSpace, error) {
	var space ownedSpace
	var created string
	if err := row.Scan(append([]any{&space.ID, &space.OwnerID, &space.Name, &space.Slug, &space.State, &created, &space.Visibility}, extra...)...); err != nil {
		return space, err
	}
	name, slug, err := normalizeSpaceName(space.Name, space.Slug)
	if err != nil || name != space.Name || slug != space.Slug || !idPattern.MatchString(space.ID) || !idPattern.MatchString(space.OwnerID) || (space.Visibility != spacePrivate && space.Visibility != spaceWeb) || (space.State != "provisioning" && space.State != "ready") {
		return space, fmt.Errorf("invalid stored space metadata")
	}
	space.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil || space.CreatedAt.IsZero() {
		return space, fmt.Errorf("invalid space creation time")
	}
	return space, nil
}

// Lock the owner's row before counting all reservations, including interrupted
// provisioning. READ COMMITTED ensures the count sees the previous lock holder's
// commit. A retry of the same owner/slug/name reuses its immutable ID.
func (db *accountDatabase) reserveOwnedSpace(ctx context.Context, ownerID, id, name, slug string) (ownedSpace, error) {
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ownedSpace{}, fmt.Errorf("space database unavailable")
	}
	defer tx.Rollback()
	owner, err := scanAccount(tx.QueryRowContext(ctx, "SELECT user_id, email, created_at, max_private_spaces, username FROM metatrash_users WHERE user_id = ? FOR UPDATE", ownerID), true)
	if err != nil {
		return ownedSpace{}, fmt.Errorf("space owner unavailable")
	}
	if owner.Username == "" {
		return ownedSpace{}, invalid("Choose a public username before creating a space.")
	}
	existing, err := scanOwnedSpace(tx.QueryRowContext(ctx, "SELECT "+ownedSpaceColumns+" FROM metatrash_spaces WHERE owner_user_id = ? AND slug = ?", ownerID, slug))
	if err == nil {
		if existing.Name != name {
			return ownedSpace{}, problem(409, "conflict", "That space slug is already in use.")
		}
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return ownedSpace{}, fmt.Errorf("cannot read space reservation")
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_spaces WHERE owner_user_id = ?", ownerID).Scan(&count); err != nil {
		return ownedSpace{}, fmt.Errorf("cannot check owned-space allowance")
	}
	if count >= owner.MaxPrivateSpaces {
		return ownedSpace{}, problem(409, "space_limit", "Owned-space allowance reached, including spaces still being prepared.")
	}
	space := ownedSpace{ID: id, OwnerID: ownerID, Name: name, Slug: slug, State: "provisioning", CreatedAt: time.Now().UTC()}
	_, err = tx.ExecContext(ctx, "INSERT INTO metatrash_spaces ("+ownedSpaceColumns+") VALUES (?, ?, ?, ?, ?, ?, 'private')", space.ID, space.OwnerID, space.Name, space.Slug, space.State, space.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return ownedSpace{}, fmt.Errorf("cannot reserve owned space")
	}
	if err := tx.Commit(); err != nil {
		return ownedSpace{}, fmt.Errorf("cannot confirm space reservation; retry with the same name and slug")
	}
	return space, nil
}

// Creation for the authenticated account handler. ownerID must
// come from the resolved human session, never a submitted form or agent input.
// Only the human account form exposes creation; agent adapters cannot call it.
func (s *Service) createOwnedSpace(ctx context.Context, ownerID, name, slug string) (ownedSpace, error) {
	name, slug, err := normalizeSpaceName(name, slug)
	if err != nil {
		return ownedSpace{}, err
	}
	if !idPattern.MatchString(ownerID) || s.ownedDB == nil {
		return ownedSpace{}, fmt.Errorf("owned spaces unavailable")
	}
	id, err := randomHex(16)
	if err != nil {
		return ownedSpace{}, err
	}
	if _, exists := s.config.Spaces[id]; exists {
		return ownedSpace{}, fmt.Errorf("space identity collision; retry")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	space, err := s.ownedDB.reserveOwnedSpace(ctx, ownerID, id, name, slug)
	if err != nil {
		return ownedSpace{}, err
	}
	if err := s.prepareOwnedSpace(ctx, space); err != nil {
		return ownedSpace{}, fmt.Errorf("space preparation incomplete; retry with the same name and slug")
	}
	space.State = "ready"
	return space, nil
}

func (s *Service) ownedRepository(id string) *repository {
	s.ownedMu.RLock()
	defer s.ownedMu.RUnlock()
	return s.ownedRepos[id]
}

// The existing write queue serializes retries/provisioning with other Git writes.
// Readers see a repository only after both Git and the database are ready.
func (s *Service) prepareOwnedSpace(ctx context.Context, space ownedSpace) error {
	_, err := s.queued(ctx, func() (any, error) {
		if s.ownedRepository(space.ID) != nil {
			return nil, nil
		}
		// Refresh after queueing, including an earlier attempt whose ready commit
		// succeeded but whose database response was lost.
		current, err := scanOwnedSpace(s.ownedDB.db.QueryRowContext(ctx, "SELECT "+ownedSpaceColumns+" FROM metatrash_spaces WHERE space_id = ? AND owner_user_id = ?", space.ID, space.OwnerID))
		if err != nil {
			return nil, fmt.Errorf("cannot read space preparation state")
		}
		space = current
		path := filepath.Join(s.ownedRoot, space.ID+".git")
		if space.State == "ready" {
			// Never recreate a missing ready repository: it may contain user history.
			if _, err := os.Lstat(path); err != nil {
				return nil, fmt.Errorf("ready owned repository missing")
			}
		}
		repo, err := provision(ctx, path, s.config.Defaults.Storage, []byte(ownedSpaceREADME), space.Name, true)
		if err != nil {
			return nil, err
		}
		repo.owner = space.OwnerID
		if space.State == "provisioning" {
			result, err := s.ownedDB.db.ExecContext(ctx, "UPDATE metatrash_spaces SET provisioning_state = 'ready' WHERE space_id = ? AND owner_user_id = ? AND provisioning_state = 'provisioning'", space.ID, space.OwnerID)
			if err != nil {
				return nil, fmt.Errorf("cannot mark space ready")
			}
			count, err := result.RowsAffected()
			if err != nil || count != 1 {
				return nil, fmt.Errorf("cannot confirm ready space")
			}
		}
		s.ownedMu.Lock()
		s.ownedRepos[space.ID] = repo
		s.ownedMu.Unlock()
		return nil, nil
	})
	return err
}

// Called before HTTP serving. Pending failures stay reserved and hidden; ready
// repository failures stop startup rather than silently replacing stored content.
func (s *Service) loadOwnedSpaces(ctx context.Context, db *accountDatabase) error {
	if err := os.MkdirAll(s.ownedRoot, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(s.ownedRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid owned repository root")
	}
	rows, err := db.db.QueryContext(ctx, "SELECT "+ownedSpaceColumns+" FROM metatrash_spaces")
	if err != nil {
		return fmt.Errorf("cannot load owned spaces")
	}
	var spaces []ownedSpace
	for rows.Next() {
		space, err := scanOwnedSpace(rows)
		if err != nil {
			rows.Close()
			return err
		}
		if _, exists := s.config.Spaces[space.ID]; exists {
			rows.Close()
			return fmt.Errorf("owned space conflicts with configured space ID")
		}
		spaces = append(spaces, space)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return fmt.Errorf("cannot load owned spaces")
	}
	s.ownedDB = db
	for _, space := range spaces {
		attempt, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := s.prepareOwnedSpace(attempt, space)
		cancel()
		if err != nil {
			if space.State == "ready" {
				return fmt.Errorf("cannot open owned space %s; restore its repository before restarting", space.ID)
			}
			log.Printf("owned space %s still provisioning; reservation retained", space.ID)
		}
	}
	return nil
}

// setSpaceVisibility lets an owner make a ready space readable on the web, or
// private again. Only the website's explorer changes; agents still need an
// approved app connection.
func (db *accountDatabase) setSpaceVisibility(ctx context.Context, ownerID, spaceID, visibility string) error {
	if !idPattern.MatchString(ownerID) || !idPattern.MatchString(spaceID) || (visibility != spacePrivate && visibility != spaceWeb) {
		return invalid("Invalid space setting.")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := db.db.ExecContext(ctx, "UPDATE metatrash_spaces SET visibility = ? WHERE space_id = ? AND owner_user_id = ? AND provisioning_state = 'ready'", visibility, spaceID, ownerID)
	if err != nil {
		return fmt.Errorf("cannot update space visibility")
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("cannot update space visibility")
	} else if n == 0 {
		// Unchanged rows also count as 0 in MariaDB; check the space exists.
		var current string
		err := db.db.QueryRowContext(ctx, "SELECT visibility FROM metatrash_spaces WHERE space_id = ? AND owner_user_id = ? AND provisioning_state = 'ready'", spaceID, ownerID).Scan(&current)
		if err == sql.ErrNoRows {
			return missing()
		}
		if err != nil {
			return fmt.Errorf("cannot update space visibility")
		}
	}
	return nil
}
