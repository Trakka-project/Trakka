package db

import (
	"context"
	"errors"
	"testing"

	"trakka/internal/models"
)

// assigneesFixture: a House with its owner and two more members (Thomas and
// Sarah), a to-do list in it, plus an outsider with a House of their own.
func assigneesFixture(t *testing.T) (ctx context.Context, d *DB, list *models.List, owner, thomas, sarah, outsider *models.User) {
	t.Helper()
	ctx = context.Background()
	d = openTestDB(t)
	user := func(email, name string) *models.User {
		t.Helper()
		hash := "x"
		u, err := d.CreateUser(ctx, email, &hash, nil, nil, name)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", email, err)
		}
		return u
	}
	owner = user("owner@example.com", "Benjamin")
	thomas = user("thomas@example.com", "Thomas")
	sarah = user("sarah@example.com", "Sarah")
	outsider = user("outsider@example.com", "Outsider")
	house, err := d.CreateHouseWithOwner(ctx, "Maison", owner.ID)
	if err != nil {
		t.Fatalf("CreateHouseWithOwner: %v", err)
	}
	for _, member := range []*models.User{thomas, sarah} {
		if _, err := d.AddHouseMember(ctx, house.ID, member.ID, "member"); err != nil {
			t.Fatalf("AddHouseMember: %v", err)
		}
	}
	if _, err := d.CreateHouseWithOwner(ctx, "Ailleurs", outsider.ID); err != nil {
		t.Fatalf("CreateHouseWithOwner: %v", err)
	}
	list, err = d.CreateList(ctx, "Ménage", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	return ctx, d, list, owner, thomas, sarah, outsider
}

func assigneeIDsOf(item *models.Item) []int64 {
	ids := []int64{}
	for _, a := range item.Assignees {
		ids = append(ids, a.UserID)
	}
	return ids
}

func sameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSetItemAssignees covers the replace semantics: names come back with
// every read, in assignment order; an unchanged set is a no-op that leaves
// updated_at alone; an outsider is refused without writing anything.
func TestSetItemAssignees(t *testing.T) {
	ctx, d, list, owner, thomas, sarah, outsider := assigneesFixture(t)
	item, err := d.CreateItem(ctx, list.ID, "Sortir les poubelles", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if item.Assignees == nil || len(item.Assignees) != 0 {
		t.Fatalf("a new item has an empty, non-nil assignee list, got %#v", item.Assignees)
	}
	backdate(t, ctx, d, item)

	// Duplicates are dropped; the order given is the assignment order.
	got, err := d.SetItemAssignees(ctx, item.ID, []int64{sarah.ID, thomas.ID, sarah.ID}, AssigneesListAudience)
	if err != nil {
		t.Fatalf("SetItemAssignees: %v", err)
	}
	if !sameIDs(assigneeIDsOf(got), []int64{sarah.ID, thomas.ID}) {
		t.Fatalf("assignees = %v, want [sarah thomas]", assigneeIDsOf(got))
	}
	if got.Assignees[0].DisplayName != "Sarah" || got.Assignees[1].DisplayName != "Thomas" {
		t.Fatalf("display names = %+v", got.Assignees)
	}
	if got.UpdatedAt == item.UpdatedAt {
		t.Fatal("assigning should bump updated_at")
	}

	// The same set in another order changes nothing, not even updated_at.
	same, err := d.SetItemAssignees(ctx, item.ID, []int64{thomas.ID, sarah.ID}, AssigneesListAudience)
	if err != nil {
		t.Fatalf("SetItemAssignees: %v", err)
	}
	if !sameIDs(assigneeIDsOf(same), []int64{sarah.ID, thomas.ID}) || same.UpdatedAt != got.UpdatedAt {
		t.Fatalf("re-sending the same assignees: %v (updated_at %s → %s)", assigneeIDsOf(same), got.UpdatedAt, same.UpdatedAt)
	}

	// Removing Sarah and adding the owner keeps Thomas first.
	swapped, err := d.SetItemAssignees(ctx, item.ID, []int64{owner.ID, thomas.ID}, AssigneesListAudience)
	if err != nil {
		t.Fatalf("SetItemAssignees: %v", err)
	}
	if !sameIDs(assigneeIDsOf(swapped), []int64{thomas.ID, owner.ID}) {
		t.Fatalf("assignees = %v, want [thomas owner]", assigneeIDsOf(swapped))
	}

	// Someone who can't open the list: refused whole, nothing written.
	if _, err := d.SetItemAssignees(ctx, item.ID, []int64{sarah.ID, outsider.ID}, AssigneesListAudience); !errors.Is(err, ErrInvalidAssignee) {
		t.Fatalf("assigning an outsider: err = %v, want ErrInvalidAssignee", err)
	}
	unchanged, err := d.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if !sameIDs(assigneeIDsOf(unchanged), []int64{thomas.ID, owner.ID}) {
		t.Fatalf("a refused assignment changed the assignees to %v", assigneeIDsOf(unchanged))
	}

	// ListItemsByList (the list detail read) carries them too.
	items, err := d.ListItemsByList(ctx, list.ID)
	if err != nil || len(items) != 1 || !sameIDs(assigneeIDsOf(items[0]), []int64{thomas.ID, owner.ID}) {
		t.Fatalf("ListItemsByList: %v (err %v)", items, err)
	}

	cleared, err := d.SetItemAssignees(ctx, item.ID, nil, AssigneesListAudience)
	if err != nil || len(cleared.Assignees) != 0 {
		t.Fatalf("unassigning everyone: %v (err %v)", cleared.Assignees, err)
	}
	if _, err := d.SetItemAssignees(ctx, item.ID+1000, []int64{owner.ID}, AssigneesListAudience); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing item: err = %v, want ErrNotFound", err)
	}

	for _, scope := range []AssigneeScope{AssigneesHouseMembers, AssigneesListAudience} {
		ok, err := d.AreAssignable(ctx, list.ID, scope, []int64{owner.ID, sarah.ID})
		if err != nil || !ok {
			t.Fatalf("AreAssignable(members, scope %d) = %v, %v", scope, ok, err)
		}
		if ok, err := d.AreAssignable(ctx, list.ID, scope, []int64{owner.ID, outsider.ID}); err != nil || ok {
			t.Fatalf("AreAssignable(with an outsider, scope %d) = %v, %v", scope, ok, err)
		}
	}
}

// TestAssigneesSurviveNextOccurrence: a recurring task checked off and
// brought back for its next occurrence is the same row, so it comes back
// assigned to the same people.
func TestAssigneesSurviveNextOccurrence(t *testing.T) {
	ctx, d, list, _, thomas, sarah, _ := assigneesFixture(t)
	due, rule := "2026-10-12", "FREQ=WEEKLY"
	item, err := d.CreateItem(ctx, list.ID, "Arroser les plantes", nil, 1, nil, false, 0, nil, &due, &rule, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if _, err := d.SetItemAssignees(ctx, item.ID, []int64{thomas.ID, sarah.ID}, AssigneesListAudience); err != nil {
		t.Fatalf("SetItemAssignees: %v", err)
	}
	// Checked off: done, with its next occurrence scheduled.
	if _, err := d.UpdateItem(ctx, item.ID, item.Title, nil, 1, nil, false, nil, true, 0, nil, &due, &rule, nil, false, nil, nil, false); err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}
	next := "2026-10-19"
	if _, err := d.SetItemSchedule(ctx, item.ID, nil, &next); err != nil {
		t.Fatalf("SetItemSchedule: %v", err)
	}
	activated, err := d.ActivateNextOccurrence(ctx, item.ID, next)
	if err != nil || !activated {
		t.Fatalf("ActivateNextOccurrence = %v, %v", activated, err)
	}
	back, err := d.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if back.Done || stringOrEmpty(back.DueDate) != next {
		t.Fatalf("next occurrence: done=%v due=%v", back.Done, back.DueDate)
	}
	if !sameIDs(assigneeIDsOf(back), []int64{thomas.ID, sarah.ID}) {
		t.Fatalf("next occurrence assignees = %v, want [thomas sarah]", assigneeIDsOf(back))
	}
}

// backdate moves item's updated_at into the past (and into item), so a
// test can tell a write that bumps it from one that doesn't — timestamps
// only have millisecond precision.
func backdate(t *testing.T, ctx context.Context, d *DB, item *models.Item) {
	t.Helper()
	item.UpdatedAt = "2026-01-01T00:00:00.000Z"
	if _, err := d.conn.ExecContext(ctx, `UPDATE items SET updated_at = ? WHERE id = ?`, item.UpdatedAt, item.ID); err != nil {
		t.Fatalf("backdating item %d: %v", item.ID, err)
	}
}

func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// TestLeavingAHouseUnassigns: once a member is removed from a House,
// PruneHouseAssignees unassigns them from that House's tasks only — not
// from another House they're still in, and without touching the other
// assignees. Deleting the account or the item drops the rows through the
// foreign keys.
func TestLeavingAHouseUnassigns(t *testing.T) {
	ctx, d, list, owner, thomas, sarah, _ := assigneesFixture(t)
	chore, err := d.CreateItem(ctx, list.ID, "Passer l'aspirateur", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if _, err := d.SetItemAssignees(ctx, chore.ID, []int64{thomas.ID, sarah.ID}, AssigneesListAudience); err != nil {
		t.Fatalf("SetItemAssignees: %v", err)
	}
	// Thomas also belongs to a second House, with a task of his there.
	other, err := d.CreateHouseWithOwner(ctx, "Chalet", thomas.ID)
	if err != nil {
		t.Fatalf("CreateHouseWithOwner: %v", err)
	}
	otherList, err := d.CreateList(ctx, "Chalet", "todo", other.ID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	otherChore, err := d.CreateItem(ctx, otherList.ID, "Fermer les volets", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if _, err := d.SetItemAssignees(ctx, otherChore.ID, []int64{thomas.ID}, AssigneesListAudience); err != nil {
		t.Fatalf("SetItemAssignees: %v", err)
	}
	backdate(t, ctx, d, chore)

	if err := d.RemoveHouseMember(ctx, list.HouseID, thomas.ID); err != nil {
		t.Fatalf("RemoveHouseMember: %v", err)
	}
	if n, err := d.PruneHouseAssignees(ctx, AssigneesListAudience, list.HouseID); err != nil || n != 1 {
		t.Fatalf("PruneHouseAssignees = %d, %v; want 1 assignment removed", n, err)
	}
	after, err := d.GetItem(ctx, chore.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if !sameIDs(assigneeIDsOf(after), []int64{sarah.ID}) {
		t.Fatalf("after Thomas left: assignees = %v, want [sarah]", assigneeIDsOf(after))
	}
	if after.UpdatedAt == chore.UpdatedAt {
		t.Fatal("unassigning should bump updated_at, so offline mirrors pick it up")
	}
	stillThere, err := d.GetItem(ctx, otherChore.ID)
	if err != nil || !sameIDs(assigneeIDsOf(stillThere), []int64{thomas.ID}) {
		t.Fatalf("Thomas' task in his other House: %v (err %v)", assigneeIDsOf(stillThere), err)
	}
	// And he can't be assigned in the House he left any more.
	if _, err := d.SetItemAssignees(ctx, chore.ID, []int64{thomas.ID}, AssigneesListAudience); !errors.Is(err, ErrInvalidAssignee) {
		t.Fatalf("assigning a former member: err = %v, want ErrInvalidAssignee", err)
	}
	if err := d.RemoveHouseMember(ctx, list.HouseID, thomas.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing a non-member: err = %v, want ErrNotFound", err)
	}

	// An account deletion drops its assignments.
	if err := d.DeleteUser(ctx, sarah.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	if after, err := d.GetItem(ctx, chore.ID); err != nil || len(after.Assignees) != 0 {
		t.Fatalf("after Sarah's account was deleted: %v (err %v)", after.Assignees, err)
	}
	// So does an item deletion.
	if _, err := d.SetItemAssignees(ctx, chore.ID, []int64{owner.ID}, AssigneesListAudience); err != nil {
		t.Fatalf("SetItemAssignees: %v", err)
	}
	if err := d.DeleteItem(ctx, chore.ID); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	var rows int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_assignees WHERE item_id = ?`, chore.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("assignee rows left for a deleted item: %d (err %v)", rows, err)
	}
}
