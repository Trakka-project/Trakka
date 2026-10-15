package db

import (
	"context"
	"fmt"
	"strings"

	"trakka/internal/models"
)

// UserSearchLimit caps how many suggestions SearchUsers returns: the
// sharing autocomplete shows a short dropdown, never a directory.
const UserSearchLimit = 10

// userSearchMaxTerms bounds how many whitespace-separated terms of a query
// SearchUsers turns into LIKE clauses, so a pasted paragraph can't build an
// arbitrarily long WHERE clause.
const userSearchMaxTerms = 5

// UserSearchScope selects what SearchUsers may match, and among whom — see
// config.Config.SaaSMode for which deployment uses which.
type UserSearchScope int

const (
	// UserSearchInstance (self-hosted) matches every account on the
	// instance by display name or email address.
	UserSearchInstance UserSearchScope = iota
	// UserSearchContactsByEmail (SaaS mode) matches by email address only,
	// and only among the caller's existing contacts (see contactsCTE).
	UserSearchContactsByEmail
)

// likeEscaper escapes LIKE's own wildcards (and the escape character
// itself) so a "%" or "_" typed into the search box matches literally.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func likeContains(term string) string {
	return "%" + likeEscaper.Replace(term) + "%"
}

// contactsCTE defines contacts(user_id): everyone the caller (bound to
// every "?" below) already shares something with, in either direction —
// co-members of their houses, recipients of shares on their houses' lists
// or on their own spaces, and the members/owner behind any list or space
// shared with them. In SaaS mode this is the whole population the
// autocomplete can reveal: a stranger's address can still be invited by
// typing it in full (the invite/share endpoints never disclose whether it
// has an account, see docs/AUDIT.md L-06), but it is never suggested.
const contactsCTE = `WITH my_houses AS (SELECT house_id FROM house_members WHERE user_id = ?),
contacts(user_id) AS (
	SELECT hm.user_id FROM house_members hm WHERE hm.house_id IN (SELECT house_id FROM my_houses)
	UNION
	SELECT ls.shared_with_user_id FROM list_shares ls JOIN lists l ON l.id = ls.list_id
	 WHERE l.house_id IN (SELECT house_id FROM my_houses)
	UNION
	SELECT ss.shared_with_user_id FROM space_shares ss JOIN custom_categories c ON c.id = ss.custom_category_id
	 WHERE c.user_id = ?
	UNION
	SELECT hm.user_id FROM list_shares ls JOIN lists l ON l.id = ls.list_id JOIN house_members hm ON hm.house_id = l.house_id
	 WHERE ls.shared_with_user_id = ?
	UNION
	SELECT c.user_id FROM space_shares ss JOIN custom_categories c ON c.id = ss.custom_category_id
	 WHERE ss.shared_with_user_id = ?
)
`

// contactsCTEParams is how many times contactsCTE binds the caller's id.
const contactsCTEParams = 4

// SearchUsers returns up to UserSearchLimit accounts other than callerID
// matching query, for the sharing autocomplete.
//
// With UserSearchInstance, every whitespace-separated term of query must
// appear somewhere in the account's display name or email address,
// case-insensitively — so "jean", "dupont", "dupont jean" and "jean@exa"
// all find "Jean Dupont <jean@example.com>". Trakka stores one display_name
// rather than separate first/last names, and matching each term anywhere in
// it is what makes both "Prénom Nom" and "Nom Prénom" work.
//
// With UserSearchContactsByEmail, query is matched against the email
// address alone (anywhere in it, case-insensitively), only among the
// caller's contacts, and the display name is never read: an email-only
// result is all SaaS mode ever exposes.
func (d *DB) SearchUsers(ctx context.Context, query string, callerID int64, scope UserSearchScope) ([]models.UserSearchResult, error) {
	results := []models.UserSearchResult{}
	query = strings.TrimSpace(query)
	if query == "" {
		return results, nil
	}

	var q string
	var args []any
	switch scope {
	case UserSearchContactsByEmail:
		for range contactsCTEParams {
			args = append(args, callerID)
		}
		args = append(args, callerID, likeContains(query), UserSearchLimit)
		q = contactsCTE + `SELECT id, email FROM users
			WHERE id != ? AND id IN (SELECT user_id FROM contacts) AND email LIKE ? ESCAPE '\'
			ORDER BY email COLLATE NOCASE LIMIT ?`
	case UserSearchInstance:
		terms := strings.Fields(query)
		if len(terms) > userSearchMaxTerms {
			terms = terms[:userSearchMaxTerms]
		}
		// Only this fixed clause is repeated per term; every term itself is
		// bound as a parameter.
		clauses := make([]string, 0, len(terms))
		args = append(args, callerID)
		for _, term := range terms {
			clauses = append(clauses, `(display_name LIKE ? ESCAPE '\' OR email LIKE ? ESCAPE '\')`)
			args = append(args, likeContains(term), likeContains(term))
		}
		args = append(args, UserSearchLimit)
		// #nosec G202 -- the concatenated clauses are constant strings; the search terms are bound parameters
		q = `SELECT id, email, display_name FROM users WHERE id != ? AND ` + strings.Join(clauses, " AND ") +
			` ORDER BY display_name COLLATE NOCASE, email COLLATE NOCASE LIMIT ?`
	default:
		return nil, fmt.Errorf("unknown user search scope %d", scope)
	}

	rows, err := d.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("searching users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r models.UserSearchResult
		dest := []any{&r.ID, &r.Email}
		if scope == UserSearchInstance {
			dest = append(dest, &r.DisplayName)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scanning user search row: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating user search rows: %w", err)
	}
	return results, nil
}
