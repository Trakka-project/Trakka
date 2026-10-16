package db

import (
	"context"
	"errors"
	"testing"

	"trakka/internal/models"
)

// scopeFixture is the self-hosted case the assignee scopes exist for:
// Benjamin keeps a House of his own ("Perso") with a personal list nobody
// else sees (Homelab), files a chores list in his Space "Maison", shared
// with Camille, and shares a groceries list directly with Lucas (read
// only). Zoé is on the instance with no access to any of it.
type scopeFixture struct {
	ctx                                context.Context
	d                                  *DB
	house                              *models.House
	space                              *models.CustomCategory
	homelab, chores, groceries         *models.List
	benjamin, camille, lucas, zoe      *models.User
	choresItem, groceriesItem, homeJob *models.Item
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	f := &scopeFixture{ctx: context.Background(), d: openTestDB(t)}
	ctx, d := f.ctx, f.d
	user := func(email, name string) *models.User {
		t.Helper()
		hash := "x"
		u, err := d.CreateUser(ctx, email, &hash, nil, nil, name)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", email, err)
		}
		return u
	}
	f.benjamin = user("benjamin@example.com", "Benjamin")
	f.camille = user("camille@example.com", "Camille")
	f.lucas = user("lucas@example.com", "Lucas")
	f.zoe = user("zoe@example.com", "Zoé")

	var err error
	if f.house, err = d.CreateHouseWithOwner(ctx, "Perso", f.benjamin.ID); err != nil {
		t.Fatalf("CreateHouseWithOwner: %v", err)
	}
	if f.space, err = d.CreateCustomCategory(ctx, f.benjamin.ID, "Maison", "", "", 0); err != nil {
		t.Fatalf("CreateCustomCategory: %v", err)
	}
	if _, err := d.CreateOrUpdateSpaceShare(ctx, f.space.ID, f.camille.ID, "write"); err != nil {
		t.Fatalf("CreateOrUpdateSpaceShare: %v", err)
	}
	list := func(name string, space *int64) *models.List {
		t.Helper()
		l, err := d.CreateList(ctx, name, "todo", f.house.ID, space, "")
		if err != nil {
			t.Fatalf("CreateList(%s): %v", name, err)
		}
		return l
	}
	f.homelab = list("Homelab", nil)
	f.chores = list("Ménage", &f.space.ID)
	f.groceries = list("Courses", nil)
	if _, err := d.CreateOrUpdateListShare(ctx, f.groceries.ID, f.lucas.ID, "read"); err != nil {
		t.Fatalf("CreateOrUpdateListShare: %v", err)
	}
	item := func(l *models.List, title string) *models.Item {
		t.Helper()
		it, err := d.CreateItem(ctx, l.ID, title, nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("CreateItem(%s): %v", title, err)
		}
		return it
	}
	f.homeJob = item(f.homelab, "Mettre à jour le NAS")
	f.choresItem = item(f.chores, "Passer l'aspirateur")
	f.groceriesItem = item(f.groceries, "Acheter du pain")
	return f
}

func (f *scopeFixture) assignable(t *testing.T, l *models.List, scope AssigneeScope) []int64 {
	t.Helper()
	users, err := f.d.AssignableUsers(f.ctx, l.ID, scope)
	if err != nil {
		t.Fatalf("AssignableUsers(%s): %v", l.Name, err)
	}
	ids := []int64{}
	for _, u := range users {
		ids = append(ids, u.UserID)
	}
	return ids
}

func (f *scopeFixture) assigneesOf(t *testing.T, item *models.Item) []int64 {
	t.Helper()
	got, err := f.d.GetItem(f.ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	return assigneeIDsOf(got)
}

// TestAssignableUsersByScope: self-hosted, a list's assignable users are
// everyone who can open it — its House, its Space's share recipients, its
// own share recipients (read-only ones too); in SaaS mode, its House only.
// Nobody else, ever.
func TestAssignableUsersByScope(t *testing.T) {
	f := newScopeFixture(t)
	b, c, l := f.benjamin.ID, f.camille.ID, f.lucas.ID
	cases := []struct {
		list  *models.List
		scope AssigneeScope
		want  []int64
	}{
		// Users come back by display name: Benjamin, Camille, Lucas.
		{f.homelab, AssigneesListAudience, []int64{b}},
		{f.chores, AssigneesListAudience, []int64{b, c}},
		{f.groceries, AssigneesListAudience, []int64{b, l}},
		{f.homelab, AssigneesHouseMembers, []int64{b}},
		{f.chores, AssigneesHouseMembers, []int64{b}},
		{f.groceries, AssigneesHouseMembers, []int64{b}},
	}
	for _, tc := range cases {
		if got := f.assignable(t, tc.list, tc.scope); !sameIDs(got, tc.want) {
			t.Errorf("AssignableUsers(%s, scope %d) = %v, want %v", tc.list.Name, tc.scope, got, tc.want)
		}
		ok, err := f.d.AreAssignable(f.ctx, tc.list.ID, tc.scope, tc.want)
		if err != nil || !ok {
			t.Errorf("AreAssignable(%s, scope %d, %v) = %v, %v", tc.list.Name, tc.scope, tc.want, ok, err)
		}
		if ok, err := f.d.AreAssignable(f.ctx, tc.list.ID, tc.scope, []int64{b, f.zoe.ID}); err != nil || ok {
			t.Errorf("AreAssignable(%s, scope %d, with Zoé) = %v, %v", tc.list.Name, tc.scope, ok, err)
		}
	}
	users, err := f.d.AssignableUsers(f.ctx, f.chores.ID, AssigneesListAudience)
	if err != nil || users[1].DisplayName != "Camille" || users[1].Email != "camille@example.com" {
		t.Fatalf("AssignableUsers carries names and emails: %+v (err %v)", users, err)
	}

	// SetItemAssignees applies the same rule.
	if _, err := f.d.SetItemAssignees(f.ctx, f.choresItem.ID, []int64{c}, AssigneesHouseMembers); !errors.Is(err, ErrInvalidAssignee) {
		t.Fatalf("SaaS mode, assigning a Space share recipient: err = %v, want ErrInvalidAssignee", err)
	}
	if _, err := f.d.SetItemAssignees(f.ctx, f.homeJob.ID, []int64{c}, AssigneesListAudience); !errors.Is(err, ErrInvalidAssignee) {
		t.Fatalf("assigning Camille on the unshared Homelab: err = %v, want ErrInvalidAssignee", err)
	}
	if _, err := f.d.SetItemAssignees(f.ctx, f.choresItem.ID, []int64{b, c}, AssigneesListAudience); err != nil {
		t.Fatalf("self-hosted, assigning a Space share recipient: %v", err)
	}
	if _, err := f.d.SetItemAssignees(f.ctx, f.groceriesItem.ID, []int64{l}, AssigneesListAudience); err != nil {
		t.Fatalf("self-hosted, assigning a list share recipient: %v", err)
	}
}

// TestPruneAssigneesFollowsAccess: whoever loses access to a list is
// unassigned from its tasks by the matching prune — the list moved out of
// its Space, the Space share or the list share revoked — and a switch to
// SaaS mode prunes everyone outside the House. Nobody who still has
// access is touched.
func TestPruneAssigneesFollowsAccess(t *testing.T) {
	f := newScopeFixture(t)
	ctx, d := f.ctx, f.d
	b, c, l := f.benjamin.ID, f.camille.ID, f.lucas.ID
	assign := func(item *models.Item, ids ...int64) {
		t.Helper()
		if _, err := d.SetItemAssignees(ctx, item.ID, ids, AssigneesListAudience); err != nil {
			t.Fatalf("SetItemAssignees(%s): %v", item.Title, err)
		}
	}
	pruned := func(what string, n int, err error, want int) {
		t.Helper()
		if err != nil || n != want {
			t.Fatalf("%s: pruned %d (err %v), want %d", what, n, err, want)
		}
	}
	assign(f.choresItem, b, c)
	assign(f.groceriesItem, l)

	// Nothing to prune while everyone still has access.
	n, err := d.PruneAssignees(ctx, AssigneesListAudience)
	pruned("PruneAssignees with no change", n, err, 0)

	// The chores list moves out of the Space: Camille goes, Benjamin stays,
	// and updated_at moves so offline mirrors notice.
	backdate(t, ctx, d, f.choresItem)
	if _, err := d.UpdateList(ctx, f.chores.ID, f.chores.Name, f.chores.Type, nil, ""); err != nil {
		t.Fatalf("UpdateList: %v", err)
	}
	n, err = d.PruneListAssignees(ctx, AssigneesListAudience, f.chores.ID)
	pruned("PruneListAssignees after leaving the Space", n, err, 1)
	if got := f.assigneesOf(t, f.choresItem); !sameIDs(got, []int64{b}) {
		t.Fatalf("after the list left the Space: assignees = %v, want [benjamin]", got)
	}
	if moved, _ := d.GetItem(ctx, f.choresItem.ID); moved.UpdatedAt == f.choresItem.UpdatedAt {
		t.Fatal("a prune should bump updated_at")
	}

	// Back in the Space and assigned again; then the Space share goes.
	if _, err := d.UpdateList(ctx, f.chores.ID, f.chores.Name, f.chores.Type, &f.space.ID, ""); err != nil {
		t.Fatalf("UpdateList: %v", err)
	}
	assign(f.choresItem, b, c)
	if err := d.RevokeSpaceShare(ctx, f.space.ID, c); err != nil {
		t.Fatalf("RevokeSpaceShare: %v", err)
	}
	n, err = d.PruneSpaceAssignees(ctx, AssigneesListAudience, f.space.ID)
	pruned("PruneSpaceAssignees after the Space share was revoked", n, err, 1)
	if got := f.assigneesOf(t, f.choresItem); !sameIDs(got, []int64{b}) {
		t.Fatalf("after the Space share was revoked: assignees = %v, want [benjamin]", got)
	}

	// The list share goes: Lucas too.
	if err := d.RevokeListShare(ctx, f.groceries.ID, l); err != nil {
		t.Fatalf("RevokeListShare: %v", err)
	}
	n, err = d.PruneListAssignees(ctx, AssigneesListAudience, f.groceries.ID)
	pruned("PruneListAssignees after the list share was revoked", n, err, 1)
	if got := f.assigneesOf(t, f.groceriesItem); len(got) != 0 {
		t.Fatalf("after the list share was revoked: assignees = %v, want none", got)
	}

	// Switching the instance to SaaS mode: the startup prune keeps the
	// House's members and drops a share recipient.
	if _, err := d.CreateOrUpdateListShare(ctx, f.groceries.ID, l, "write"); err != nil {
		t.Fatalf("CreateOrUpdateListShare: %v", err)
	}
	assign(f.groceriesItem, b, l)
	n, err = d.PruneAssignees(ctx, AssigneesHouseMembers)
	pruned("PruneAssignees in SaaS mode", n, err, 1)
	if got := f.assigneesOf(t, f.groceriesItem); !sameIDs(got, []int64{b}) {
		t.Fatalf("after switching to SaaS mode: assignees = %v, want [benjamin]", got)
	}
}

// TestPruneHouseAssigneesKeepsShareAccess: self-hosted, a member who leaves
// a House but still holds a share of one of its lists stays assigned
// there; in SaaS mode, the House is all that counts.
func TestPruneHouseAssigneesKeepsShareAccess(t *testing.T) {
	for _, tc := range []struct {
		scope AssigneeScope
		want  int // assignments pruned
	}{{AssigneesListAudience, 0}, {AssigneesHouseMembers, 1}} {
		f := newScopeFixture(t)
		ctx, d := f.ctx, f.d
		if _, err := d.AddHouseMember(ctx, f.house.ID, f.zoe.ID, "member"); err != nil {
			t.Fatalf("AddHouseMember: %v", err)
		}
		if _, err := d.CreateOrUpdateListShare(ctx, f.groceries.ID, f.zoe.ID, "write"); err != nil {
			t.Fatalf("CreateOrUpdateListShare: %v", err)
		}
		if _, err := d.SetItemAssignees(ctx, f.groceriesItem.ID, []int64{f.zoe.ID}, tc.scope); err != nil {
			t.Fatalf("SetItemAssignees: %v", err)
		}
		if err := d.RemoveHouseMember(ctx, f.house.ID, f.zoe.ID); err != nil {
			t.Fatalf("RemoveHouseMember: %v", err)
		}
		if n, err := d.PruneHouseAssignees(ctx, tc.scope, f.house.ID); err != nil || n != tc.want {
			t.Fatalf("scope %d: PruneHouseAssignees = %d, %v; want %d", tc.scope, n, err, tc.want)
		}
	}
}

func TestUnknownAssigneeScope(t *testing.T) {
	f := newScopeFixture(t)
	if _, err := f.d.AssignableUsers(f.ctx, f.chores.ID, AssigneeScope(42)); err == nil {
		t.Fatal("AssignableUsers with an unknown scope should fail")
	}
	if _, err := f.d.PruneAssignees(f.ctx, AssigneeScope(42)); err == nil {
		t.Fatal("PruneAssignees with an unknown scope should fail")
	}
	if AssigneeScopeFor(true) != AssigneesHouseMembers || AssigneeScopeFor(false) != AssigneesListAudience {
		t.Fatal("AssigneeScopeFor: SaaS mode is the House-only scope, self-hosted the list audience")
	}
}
