package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const reservedUsernames = " public admin administrator api mcp login logout account accounts spaces assets health healthz static docs support help www mail root system metatrash settings signup register robots sitemap favicon "

func normalizeUsername(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 3 || len(value) > 32 || !usernamePattern.MatchString(value) {
		return "", invalid("Use 3–32 letters or numbers, with single hyphens between them.")
	}
	if strings.Contains(reservedUsernames, " "+value+" ") {
		return "", invalid("That username is reserved. Please choose another.")
	}
	return value, nil
}

// The conditional write and unique index arbitrate both same-account and
// same-name races. A selected name can never be replaced through this API.
func (s *accountDatabase) ChooseUsername(ctx context.Context, id, value string) error {
	username, err := normalizeUsername(value)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := s.db.ExecContext(ctx, "UPDATE metatrash_users SET username = ? WHERE user_id = ? AND username IS NULL", username, id)
	if err != nil {
		var duplicate *mysql.MySQLError
		if errors.As(err, &duplicate) && duplicate.Number == 1062 {
			return problem(409, "conflict", "That username is already taken. Please choose another.")
		}
		return errors.New("account database unavailable")
	}
	count, err := result.RowsAffected()
	if err != nil {
		return errors.New("cannot confirm username selection")
	}
	if count != 1 {
		return problem(409, "conflict", "A username has already been selected. Reload your account page.")
	}
	return nil
}
