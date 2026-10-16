package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"trakka/internal/models"
)

// ErrInvalidAssignee is returned by SetItemAssignees when one of the user
// ids is outside the item's list's assignable users (see AssigneeScope).
var ErrInvalidAssignee = errors.New("assignees must be among the list's assignable users")

// AssigneeScope decides who a task can be assigned to — the deployment
// picks it (handlers.Application.assigneeScope, from config.SaaSMode).
type AssigneeScope int

const (
	// AssigneesHouseMembers (SaaS mode): the members of the list's House,
	// nobody else — accounts on the instance don't know each other.
	AssigneesHouseMembers AssigneeScope = iota
	// AssigneesListAudience (self-hosted): everyone who can open the list —
	// the members of its House, the recipients of a share of the list, and
	// those of a share of the Space it is filed in (the three access paths
	// of AccessLevelForList). A self-hosted household often keeps personal
	// lists out of a shared House and shares a Space or a list instead.
	AssigneesListAudience
)

// AssigneeScopeFor is the scope of a deployment, from its SAAS_MODE
// (config.Config.SaaSMode).
func AssigneeScopeFor(saasMode bool) AssigneeScope {
	if saasMode {
		return AssigneesHouseMembers
	}
	return AssigneesListAudience
}

// assignableCTE defines, for the list bound to its one "?", the CTE
// assignable(user_id): that list's assignable users under each scope.
// Moving the list to another Space, or sharing it or its Space with
// someone, changes the AssigneesListAudience set accordingly.
var assignableCTE = map[AssigneeScope]string{
	AssigneesHouseMembers: `WITH target AS (SELECT id, house_id, custom_category_id FROM lists WHERE id = ?),
assignable(user_id) AS (
	SELECT hm.user_id FROM house_members hm JOIN target t ON hm.house_id = t.house_id
)
`,
	AssigneesListAudience: `WITH target AS (SELECT id, house_id, custom_category_id FROM lists WHERE id = ?),
assignable(user_id) AS (
	SELECT hm.user_id FROM house_members hm JOIN target t ON hm.house_id = t.house_id
	UNION
	SELECT ls.shared_with_user_id FROM list_shares ls JOIN target t ON ls.list_id = t.id
	UNION
	SELECT ss.shared_with_user_id FROM space_shares ss JOIN target t ON ss.custom_category_id = t.custom_category_id
)
`,
}

// assigneeOutsideScope is, per scope, the condition under which an
// item_assignees row ia, on an item of list l, no longer falls within its
// scope — the counterpart of assignableCTE that PruneAssignees applies to
// many lists at once.
var assigneeOutsideScope = map[AssigneeScope]string{
	AssigneesHouseMembers: `NOT EXISTS (SELECT 1 FROM house_members hm WHERE hm.house_id = l.house_id AND hm.user_id = ia.user_id)`,
	AssigneesListAudience: `NOT EXISTS (SELECT 1 FROM house_members hm WHERE hm.house_id = l.house_id AND hm.user_id = ia.user_id)
	AND NOT EXISTS (SELECT 1 FROM list_shares ls WHERE ls.list_id = l.id AND ls.shared_with_user_id = ia.user_id)
	AND NOT EXISTS (SELECT 1 FROM space_shares ss WHERE ss.custom_category_id = l.custom_category_id AND ss.shared_with_user_id = ia.user_id)`,
}

func scopeSQL(set map[AssigneeScope]string, scope AssigneeScope) (string, error) {
	q, ok := set[scope]
	if !ok {
		return "", fmt.Errorf("unknown assignee scope %d", scope)
	}
	return q, nil
}

// queryRower is what isAssignable needs of either the connection or a
// transaction.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func isAssignable(ctx context.Context, q queryRower, scope AssigneeScope, listID, userID int64) (bool, error) {
	cte, err := scopeSQL(assignableCTE, scope)
	if err != nil {
		return false, err
	}
	var ok bool
	// #nosec G202 -- cte is one of assignableCTE's constant strings; the ids are bound parameters
	if err := q.QueryRowContext(ctx, cte+`SELECT EXISTS(SELECT 1 FROM assignable WHERE user_id = ?)`, listID, userID).Scan(&ok); err != nil {
		return false, fmt.Errorf("checking whether user %d is assignable on list %d: %w", userID, listID, err)
	}
	return ok, nil
}

// AssignableUsers lists who an item of listID can be assigned to under
// scope, by display name then email — what the assignee pickers offer
// (GET /api/v1/lists/{id}/assignable-users). Empty, never nil.
func (d *DB) AssignableUsers(ctx context.Context, listID int64, scope AssigneeScope) ([]models.AssignableUser, error) {
	cte, err := scopeSQL(assignableCTE, scope)
	if err != nil {
		return nil, err
	}
	// #nosec G202 -- cte is one of assignableCTE's constant strings; the list id is a bound parameter
	rows, err := d.conn.QueryContext(ctx, cte+`SELECT u.id, u.display_name, u.email FROM users u
		WHERE u.id IN (SELECT user_id FROM assignable)
		ORDER BY u.display_name COLLATE NOCASE, u.email COLLATE NOCASE`, listID)
	if err != nil {
		return nil, fmt.Errorf("querying assignable users of list %d: %w", listID, err)
	}
	defer rows.Close()
	users := []models.AssignableUser{}
	for rows.Next() {
		var u models.AssignableUser
		if err := rows.Scan(&u.UserID, &u.DisplayName, &u.Email); err != nil {
			return nil, fmt.Errorf("scanning assignable user row: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating assignable user rows: %w", err)
	}
	return users, nil
}

// AreAssignable reports whether every one of userIDs is an assignable user
// of listID under scope — the check the item handlers run before writing
// anything, so a request naming someone out of scope is refused whole
// rather than half-applied (SetItemAssignees checks again in its own
// transaction).
func (d *DB) AreAssignable(ctx context.Context, listID int64, scope AssigneeScope, userIDs []int64) (bool, error) {
	for _, id := range userIDs {
		ok, err := isAssignable(ctx, d.conn, scope, listID, id)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// SetItemAssignees replaces the set of people item itemID is assigned to
// (models.Item.Assignees, migration 34) with userIDs — empty unassigns it —
// and returns the updated row. Kept as its own method for the same "too
// many positional params" reason as SetItemLabels.
//
// Every id must be an assignable user of the item's list under scope,
// checked inside the same transaction as the writes, so someone who lost
// access meanwhile can't slip in: otherwise nothing is written and
// ErrInvalidAssignee comes back. Duplicates are ignored. The rows of people
// who stay assigned are kept, not rewritten, so the assignment order
// (item_assignees' rowid, see itemAssigneesExpr) survives an edit that only
// adds or removes someone; a new assignee is appended in userIDs' order.
// updated_at is only bumped when the set actually changed, so re-sending
// the same assignees (the edit modal does on every save) is a no-op.
// Returns ErrNotFound if no such item exists.
func (d *DB) SetItemAssignees(ctx context.Context, itemID int64, userIDs []int64, scope AssigneeScope) (*models.Item, error) {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning assignees transaction for item %d: %w", itemID, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	var listID int64
	err = tx.QueryRowContext(ctx, `SELECT list_id FROM items WHERE id = ?`, itemID).Scan(&listID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("looking up the list of item %d: %w", itemID, err)
	}

	wanted := make(map[int64]bool, len(userIDs))
	ordered := make([]int64, 0, len(userIDs))
	for _, id := range userIDs {
		if wanted[id] {
			continue
		}
		ok, err := isAssignable(ctx, tx, scope, listID, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrInvalidAssignee
		}
		wanted[id] = true
		ordered = append(ordered, id)
	}

	current, err := assigneeIDs(ctx, tx, itemID)
	if err != nil {
		return nil, err
	}
	changed := false
	for _, id := range current {
		if wanted[id] {
			delete(wanted, id) // already assigned: nothing to insert
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM item_assignees WHERE item_id = ? AND user_id = ?`, itemID, id); err != nil {
			return nil, fmt.Errorf("unassigning user %d from item %d: %w", id, itemID, err)
		}
		changed = true
	}
	for _, id := range ordered {
		if !wanted[id] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO item_assignees (item_id, user_id) VALUES (?, ?)`, itemID, id); err != nil {
			return nil, fmt.Errorf("assigning user %d to item %d: %w", id, itemID, err)
		}
		changed = true
	}
	if changed {
		if err := touchItem(ctx, tx, itemID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing assignees of item %d: %w", itemID, err)
	}
	return d.GetItem(ctx, itemID)
}

// assigneeIDs lists the users item itemID is assigned to, in assignment
// order.
func assigneeIDs(ctx context.Context, tx *sql.Tx, itemID int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT user_id FROM item_assignees WHERE item_id = ? ORDER BY rowid`, itemID)
	if err != nil {
		return nil, fmt.Errorf("querying assignees of item %d: %w", itemID, err)
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning assignee row: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating assignee rows: %w", err)
	}
	return ids, nil
}

// touchItem bumps itemID's updated_at, so offline mirrors pick up a change
// to its assignees.
func touchItem(ctx context.Context, tx *sql.Tx, itemID int64) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE items SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?`, itemID); err != nil {
		return fmt.Errorf("touching item %d: %w", itemID, err)
	}
	return nil
}

// The lists a PruneAssignees* call looks at — constant conditions on
// lists l, each with at most one bound parameter.
const (
	pruneAllLists    = `1 = 1`
	pruneHouseLists  = `l.house_id = ?`
	pruneOneList     = `l.id = ?`
	pruneSpaceLists  = `l.custom_category_id = ?`
	prunedPairsQuery = `SELECT ia.item_id, ia.user_id FROM item_assignees ia
	JOIN items i ON i.id = ia.item_id JOIN lists l ON l.id = i.list_id WHERE `
)

// PruneAssignees unassigns, on every list, whoever has fallen out of its
// scope — run at startup, so a switch of SAAS_MODE (or a change made while
// a prune failed) never leaves anyone assigned beyond the current rule, and
// after deleting a Space, whose lists the database no longer ties to it.
// Returns how many assignments it removed.
func (d *DB) PruneAssignees(ctx context.Context, scope AssigneeScope) (int, error) {
	return d.pruneAssignees(ctx, scope, pruneAllLists)
}

// PruneHouseAssignees is PruneAssignees for houseID's lists — after a
// member leaves or is removed from it.
func (d *DB) PruneHouseAssignees(ctx context.Context, scope AssigneeScope, houseID int64) (int, error) {
	return d.pruneAssignees(ctx, scope, pruneHouseLists, houseID)
}

// PruneListAssignees is PruneAssignees for one list — after a share of it
// is revoked or it moves to another Space.
func (d *DB) PruneListAssignees(ctx context.Context, scope AssigneeScope, listID int64) (int, error) {
	return d.pruneAssignees(ctx, scope, pruneOneList, listID)
}

// PruneSpaceAssignees is PruneAssignees for the lists filed in categoryID
// — after a share of that Space is revoked.
func (d *DB) PruneSpaceAssignees(ctx context.Context, scope AssigneeScope, categoryID int64) (int, error) {
	return d.pruneAssignees(ctx, scope, pruneSpaceLists, categoryID)
}

// pruneAssignees removes, on the lists listFilter (one of the prune*
// constants above) selects, the assignments outside scope, bumping the
// updated_at of every item it unassigns someone from so offline mirrors
// pick the change up — in one transaction.
func (d *DB) pruneAssignees(ctx context.Context, scope AssigneeScope, listFilter string, args ...any) (int, error) {
	outside, err := scopeSQL(assigneeOutsideScope, scope)
	if err != nil {
		return 0, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("beginning assignee prune: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// #nosec G202 -- listFilter and outside are constant strings; the ids are bound parameters
	stale, err := assignmentsMatching(ctx, tx, prunedPairsQuery+listFilter+` AND `+outside, args...)
	if err != nil {
		return 0, err
	}

	touched := map[int64]bool{}
	for _, p := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM item_assignees WHERE item_id = ? AND user_id = ?`, p.itemID, p.userID); err != nil {
			return 0, fmt.Errorf("unassigning user %d from item %d: %w", p.userID, p.itemID, err)
		}
		if !touched[p.itemID] {
			touched[p.itemID] = true
			if err := touchItem(ctx, tx, p.itemID); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("committing assignee prune: %w", err)
	}
	return len(stale), nil
}

type assignment struct{ itemID, userID int64 }

// assignmentsMatching reads the (item_id, user_id) pairs query selects, in
// full, so the caller can write to item_assignees afterwards.
func assignmentsMatching(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]assignment, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying assignments out of scope: %w", err)
	}
	defer rows.Close()
	var out []assignment
	for rows.Next() {
		var a assignment
		if err := rows.Scan(&a.itemID, &a.userID); err != nil {
			return nil, fmt.Errorf("scanning assignment out of scope: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating assignments out of scope: %w", err)
	}
	return out, nil
}
