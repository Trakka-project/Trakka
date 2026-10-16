package handlers

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"trakka/internal/models"
)

// assignFixture: a House (owner + Thomas + Sarah) with a to-do list, plus
// a write-share recipient of that list who isn't a House member, and an
// outsider with a House of their own.
type assignFixture struct {
	app                                *Application
	list                               *models.List
	owner, thomas, sarah, guest, stray *models.User
}

func newAssignFixture(t *testing.T) *assignFixture {
	t.Helper()
	app := newTestApplication(t)
	ctx := context.Background()
	f := &assignFixture{app: app}
	user := func(email, name string) *models.User {
		t.Helper()
		hash := "x"
		u, err := app.DB.CreateUser(ctx, email, &hash, nil, nil, name)
		if err != nil {
			t.Fatalf("CreateUser(%s): %v", email, err)
		}
		return u
	}
	f.owner = user("owner@example.com", "Benjamin")
	f.thomas = user("thomas@example.com", "Thomas")
	f.sarah = user("sarah@example.com", "Sarah")
	f.guest = user("guest@example.com", "Invité")
	f.stray = user("stray@example.com", "Ailleurs")
	house, err := app.DB.CreateHouseWithOwner(ctx, "Maison", f.owner.ID)
	if err != nil {
		t.Fatalf("CreateHouseWithOwner: %v", err)
	}
	for _, member := range []*models.User{f.thomas, f.sarah} {
		if _, err := app.DB.AddHouseMember(ctx, house.ID, member.ID, "member"); err != nil {
			t.Fatalf("AddHouseMember: %v", err)
		}
	}
	if _, err := app.DB.CreateHouseWithOwner(ctx, "Ailleurs", f.stray.ID); err != nil {
		t.Fatalf("CreateHouseWithOwner: %v", err)
	}
	f.list, err = app.DB.CreateList(ctx, "Ménage", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	if _, err := app.DB.CreateOrUpdateListShare(ctx, f.list.ID, f.guest.ID, "write"); err != nil {
		t.Fatalf("CreateOrUpdateListShare: %v", err)
	}
	return f
}

func idJSON(ids ...int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func (f *assignFixture) create(t *testing.T, as *models.User, body string, wantStatus int) *models.Item {
	t.Helper()
	rec := callAs(as, f.app.handleItemsCreate, http.MethodPost, "/api/v1/items",
		`{"list_id":`+strconv.FormatInt(f.list.ID, 10)+`,`+body+`}`)
	if wantStatus != http.StatusCreated {
		if rec.Code != wantStatus {
			t.Fatalf("POST %s: status %d, want %d (%s)", body, rec.Code, wantStatus, rec.Body.String())
		}
		return nil
	}
	var item models.Item
	decodeInto(t, rec, http.StatusCreated, &item)
	return &item
}

func (f *assignFixture) write(t *testing.T, as *models.User, method string, id int64, body string, wantStatus int) *models.Item {
	t.Helper()
	handler := f.app.handleItemsPatch
	if method == http.MethodPut {
		handler = f.app.handleItemsUpdate
	}
	idStr := strconv.FormatInt(id, 10)
	rec := callAs(as, handler, method, "/api/v1/items/"+idStr, body, "id", idStr)
	if wantStatus != http.StatusOK {
		if rec.Code != wantStatus {
			t.Fatalf("%s %s: status %d, want %d (%s)", method, body, rec.Code, wantStatus, rec.Body.String())
		}
		return nil
	}
	var item models.Item
	decodeInto(t, rec, http.StatusOK, &item)
	return &item
}

func assignedTo(item *models.Item) []int64 {
	ids := []int64{}
	for _, a := range item.Assignees {
		ids = append(ids, a.UserID)
	}
	return ids
}

func wantAssignees(t *testing.T, item *models.Item, want ...*models.User) {
	t.Helper()
	got := assignedTo(item)
	ok := len(got) == len(want)
	for i := 0; ok && i < len(want); i++ {
		ok = got[i] == want[i].ID && item.Assignees[i].DisplayName == want[i].DisplayName
	}
	if !ok {
		t.Fatalf("%q assigned to %+v, want %d people: %v", item.Title, item.Assignees, len(want), want)
	}
}

// TestItemAssigneesAPI covers assignee_ids on POST/PUT/PATCH in SaaS mode,
// the strict scope: who may send them, who may be named, and the
// absent/present conventions (the same in both modes).
func TestItemAssigneesAPI(t *testing.T) {
	f := newAssignFixture(t)
	f.app.Config.SaaSMode = true

	// Created assigned to two people; the response and the list read both
	// carry their names. Duplicates are dropped.
	chore := f.create(t, f.owner, `"title":"Sortir les poubelles","assignee_ids":`+idJSON(f.thomas.ID, f.sarah.ID, f.thomas.ID), http.StatusCreated)
	wantAssignees(t, chore, f.thomas, f.sarah)
	listID := strconv.FormatInt(f.list.ID, 10)
	var shown models.List
	decodeInto(t, callAs(f.owner, f.app.handleListsShow, http.MethodGet, "/api/v1/lists/"+listID, "", "id", listID), http.StatusOK, &shown)
	if len(shown.Items) != 1 {
		t.Fatalf("list read: %d items", len(shown.Items))
	}
	wantAssignees(t, shown.Items[0], f.thomas, f.sarah)

	// An item created without assignees has an empty array, never null.
	plain := f.create(t, f.owner, `"title":"Arroser"`, http.StatusCreated)
	if plain.Assignees == nil || len(plain.Assignees) != 0 {
		t.Fatalf("unassigned item: %#v", plain.Assignees)
	}

	// Refused before anything is written: a list-share recipient may add
	// items but not assign them (403), nobody may name someone outside the
	// House — not even that recipient (400) — and malformed ids are a 400.
	f.create(t, f.guest, `"title":"Refusé","assignee_ids":`+idJSON(f.thomas.ID), http.StatusForbidden)
	f.create(t, f.owner, `"title":"Refusé","assignee_ids":`+idJSON(f.thomas.ID, f.stray.ID), http.StatusBadRequest)
	f.create(t, f.owner, `"title":"Refusé","assignee_ids":`+idJSON(f.guest.ID), http.StatusBadRequest)
	f.create(t, f.owner, `"title":"Refusé","assignee_ids":[0]`, http.StatusBadRequest)
	f.create(t, f.owner, `"title":"Refusé","assignee_ids":"thomas"`, http.StatusBadRequest)
	items, err := f.app.DB.ListItemsByList(context.Background(), f.list.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("refused creations left %d items (err %v), want 2", len(items), err)
	}
	f.create(t, f.guest, `"title":"Ajouté par l'invité"`, http.StatusCreated)

	// PATCH: absent keeps them (a plain check-off), present replaces them,
	// [] unassigns.
	if got := f.write(t, f.sarah, http.MethodPatch, chore.ID, `{"done":true}`, http.StatusOK); !got.Done {
		t.Fatal("check-off failed")
	} else {
		wantAssignees(t, got, f.thomas, f.sarah)
	}
	wantAssignees(t, f.write(t, f.sarah, http.MethodPatch, chore.ID, `{"assignee_ids":`+idJSON(f.sarah.ID, f.owner.ID)+`}`, http.StatusOK), f.sarah, f.owner)
	f.write(t, f.owner, http.MethodPatch, chore.ID, `{"title":"Changé","assignee_ids":`+idJSON(f.stray.ID)+`}`, http.StatusBadRequest)
	if got, _ := f.app.DB.GetItem(context.Background(), chore.ID); got.Title == "Changé" {
		t.Fatal("a refused assignment must leave the rest of the PATCH unapplied")
	}

	// The share recipient can still edit an assigned task, and re-send its
	// current assignees (no change, no authorization needed), but not
	// change them.
	wantAssignees(t, f.write(t, f.guest, http.MethodPatch, chore.ID, `{"title":"Sortir les poubelles (jaune)"}`, http.StatusOK), f.sarah, f.owner)
	wantAssignees(t, f.write(t, f.guest, http.MethodPatch, chore.ID, `{"assignee_ids":`+idJSON(f.owner.ID, f.sarah.ID)+`}`, http.StatusOK), f.sarah, f.owner)
	f.write(t, f.guest, http.MethodPatch, chore.ID, `{"assignee_ids":[]}`, http.StatusForbidden)

	// PUT: a full replace that leaves absent assignees as they are (the
	// share recipient's PUT included); present, they're replaced.
	wantAssignees(t, f.write(t, f.guest, http.MethodPut, chore.ID, `{"title":"Sortir les poubelles"}`, http.StatusOK), f.sarah, f.owner)
	wantAssignees(t, f.write(t, f.owner, http.MethodPut, chore.ID, `{"title":"Sortir les poubelles","assignee_ids":`+idJSON(f.thomas.ID)+`}`, http.StatusOK), f.thomas)
	f.write(t, f.guest, http.MethodPut, chore.ID, `{"title":"Sortir les poubelles","assignee_ids":`+idJSON(f.sarah.ID)+`}`, http.StatusForbidden)
	cleared := f.write(t, f.owner, http.MethodPatch, chore.ID, `{"assignee_ids":[]}`, http.StatusOK)
	if len(cleared.Assignees) != 0 {
		t.Fatalf("assignee_ids [] left %+v", cleared.Assignees)
	}
}

// TestAssignedRecurringTaskKeepsAssignees is the spec's recurrence rule:
// checking off a recurring assigned task schedules its next occurrence,
// and that occurrence comes back assigned to the same people.
func TestAssignedRecurringTaskKeepsAssignees(t *testing.T) {
	f := newAssignFixture(t)
	ctx := context.Background()
	today := f.app.today()
	chore := f.create(t, f.owner, `"title":"Arroser les plantes","due_date":"`+today+`","recurrence_rule":"FREQ=DAILY","assignee_ids":`+idJSON(f.thomas.ID, f.sarah.ID), http.StatusCreated)
	wantAssignees(t, chore, f.thomas, f.sarah)

	// Thomas checks it off: done, next occurrence tomorrow, still both.
	done := f.write(t, f.thomas, http.MethodPatch, chore.ID, `{"done":true}`, http.StatusOK)
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	if !done.Done || done.NextDueDate == nil || *done.NextDueDate != tomorrow {
		t.Fatalf("after check-off: done=%v next=%v, want next %s", done.Done, done.NextDueDate, tomorrow)
	}
	wantAssignees(t, done, f.thomas, f.sarah)

	// Its next occurrence has started (moved into the past so the scan
	// sees it): it comes back active, on that date, assigned the same way.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	if _, err := f.app.DB.SetItemSchedule(ctx, chore.ID, nil, &yesterday); err != nil {
		t.Fatalf("SetItemSchedule: %v", err)
	}
	f.app.RunNextOccurrenceScan(ctx)
	back, err := f.app.DB.GetItem(ctx, chore.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if back.Done || back.NextDueDate != nil || back.DueDate == nil || *back.DueDate != yesterday {
		t.Fatalf("next occurrence: done=%v due=%v next=%v, want active, due %s", back.Done, back.DueDate, back.NextDueDate, yesterday)
	}
	wantAssignees(t, back, f.thomas, f.sarah)

	// Reassigned in between, the next occurrence follows the new choice.
	wantAssignees(t, f.write(t, f.owner, http.MethodPatch, chore.ID, `{"done":true,"assignee_ids":`+idJSON(f.sarah.ID)+`}`, http.StatusOK), f.sarah)
	if _, err := f.app.DB.SetItemSchedule(ctx, chore.ID, nil, &yesterday); err != nil {
		t.Fatalf("SetItemSchedule: %v", err)
	}
	f.app.RunNextOccurrenceScan(ctx)
	if back, err = f.app.DB.GetItem(ctx, chore.ID); err != nil || back.Done {
		t.Fatalf("second occurrence: %+v (err %v)", back, err)
	}
	wantAssignees(t, back, f.sarah)
}

// TestLeavingTheHouseUnassignsTasks: a member who leaves (or is removed)
// is unassigned from the House's tasks, through the members endpoint.
func TestLeavingTheHouseUnassignsTasks(t *testing.T) {
	f := newAssignFixture(t)
	chore := f.create(t, f.owner, `"title":"Vider le lave-vaisselle","assignee_ids":`+idJSON(f.thomas.ID, f.sarah.ID), http.StatusCreated)
	houseID := strconv.FormatInt(f.list.HouseID, 10)
	thomasID := strconv.FormatInt(f.thomas.ID, 10)
	rec := callAs(f.thomas, f.app.handleHouseMembersRemove, http.MethodDelete, "/api/v1/houses/"+houseID+"/members/"+thomasID, "", "id", houseID, "userId", thomasID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("leaving the house: status %d (%s)", rec.Code, rec.Body.String())
	}
	after, err := f.app.DB.GetItem(context.Background(), chore.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	wantAssignees(t, after, f.sarah)
	f.write(t, f.owner, http.MethodPatch, chore.ID, `{"assignee_ids":`+idJSON(f.thomas.ID)+`}`, http.StatusBadRequest)
}

func TestCleanAssigneeIDs(t *testing.T) {
	got, err := cleanAssigneeIDs([]int64{3, 1, 3, 2, 1})
	if err != nil || len(got) != 3 || got[0] != 3 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("cleanAssigneeIDs = %v, %v; want [3 1 2]", got, err)
	}
	if got, err := cleanAssigneeIDs(nil); err != nil || len(got) != 0 {
		t.Fatalf("cleanAssigneeIDs(nil) = %v, %v", got, err)
	}
	if _, err := cleanAssigneeIDs([]int64{1, -2}); err == nil {
		t.Fatal("a negative id must be refused")
	}
	tooMany := make([]int64, models.MaxItemAssignees+1)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	if _, err := cleanAssigneeIDs(tooMany); err == nil {
		t.Fatal("more than MaxItemAssignees people must be refused")
	}
	if _, err := cleanAssigneeIDs(append(tooMany[:models.MaxItemAssignees:models.MaxItemAssignees], 1)); err != nil {
		t.Fatalf("a duplicate past the cap is not one more person: %v", err)
	}
}

// TestItemAssigneesSelfHosted is the self-hosted scope: whoever can open
// the list may be assigned — its House, the recipients of a share of the
// list (read-only ones too) and of its Space — and whoever may edit its
// items may assign them. Someone with no access stays out, and losing
// access (the list moved out of the Space, a Space or list share revoked,
// the Space deleted) unassigns.
func TestItemAssigneesSelfHosted(t *testing.T) {
	f := newAssignFixture(t)
	ctx := context.Background()
	reader := mustUser(t, f.app, "reader@example.com", "Lecteur")
	if _, err := f.app.DB.CreateOrUpdateListShare(ctx, f.list.ID, reader.ID, "read"); err != nil {
		t.Fatalf("CreateOrUpdateListShare: %v", err)
	}
	camille := mustUser(t, f.app, "camille@example.com", "Camille")
	space, err := f.app.DB.CreateCustomCategory(ctx, f.owner.ID, "Maison", "", "", 0)
	if err != nil {
		t.Fatalf("CreateCustomCategory: %v", err)
	}
	if _, err := f.app.DB.CreateOrUpdateSpaceShare(ctx, space.ID, camille.ID, "write"); err != nil {
		t.Fatalf("CreateOrUpdateSpaceShare: %v", err)
	}

	// Not yet in the Space: Camille can't be assigned.
	f.create(t, f.owner, `"title":"Refusé","assignee_ids":`+idJSON(camille.ID), http.StatusBadRequest)
	listID := strconv.FormatInt(f.list.ID, 10)
	moveTo := func(spaceJSON string) {
		t.Helper()
		rec := callAs(f.owner, f.app.handleListsUpdate, http.MethodPut, "/api/v1/lists/"+listID,
			`{"name":"Ménage","type":"todo","custom_category_id":`+spaceJSON+`}`, "id", listID)
		if rec.Code != http.StatusOK {
			t.Fatalf("moving the list to space %s: status %d (%s)", spaceJSON, rec.Code, rec.Body.String())
		}
	}
	moveTo(strconv.FormatInt(space.ID, 10))

	// The list-share recipient assigns a House member, and the owner
	// assigns the share recipients and the Space's recipient.
	byGuest := f.create(t, f.guest, `"title":"Arroser","assignee_ids":`+idJSON(f.thomas.ID), http.StatusCreated)
	wantAssignees(t, byGuest, f.thomas)
	chore := f.create(t, f.owner, `"title":"Sortir les poubelles","assignee_ids":`+idJSON(f.guest.ID, reader.ID, camille.ID, f.sarah.ID), http.StatusCreated)
	wantAssignees(t, chore, f.guest, reader, camille, f.sarah)
	wantAssignees(t, f.write(t, camille, http.MethodPatch, chore.ID, `{"assignee_ids":`+idJSON(camille.ID, f.guest.ID, reader.ID, f.sarah.ID)+`}`, http.StatusOK), f.guest, reader, camille, f.sarah)

	// Still nobody without access, and a read-only recipient can't write.
	f.create(t, f.guest, `"title":"Refusé","assignee_ids":`+idJSON(f.stray.ID), http.StatusBadRequest)
	f.write(t, reader, http.MethodPatch, chore.ID, `{"assignee_ids":[]}`, http.StatusForbidden)

	// Out of the Space, Camille is unassigned; the guest goes with the
	// revoked share; the others stay.
	moveTo("null")
	after, err := f.app.DB.GetItem(ctx, chore.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	wantAssignees(t, after, f.guest, reader, f.sarah)

	// Back in the Space and assigned again, Camille goes with a revoked
	// Space share, and again with a deleted Space.
	spaceID, camilleID := strconv.FormatInt(space.ID, 10), strconv.FormatInt(camille.ID, 10)
	reassignCamille := func() {
		t.Helper()
		moveTo(spaceID)
		wantAssignees(t, f.write(t, f.owner, http.MethodPatch, chore.ID, `{"assignee_ids":`+idJSON(f.guest.ID, reader.ID, f.sarah.ID, camille.ID)+`}`, http.StatusOK), f.guest, reader, f.sarah, camille)
	}
	reassignCamille()
	rec := callAs(f.owner, f.app.handleSpaceShareRevoke, http.MethodDelete, "/api/v1/custom-categories/"+spaceID+"/share/"+camilleID, "", "id", spaceID, "userId", camilleID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoking the Space share: status %d (%s)", rec.Code, rec.Body.String())
	}
	if after, err = f.app.DB.GetItem(ctx, chore.ID); err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	wantAssignees(t, after, f.guest, reader, f.sarah)
	if _, err := f.app.DB.CreateOrUpdateSpaceShare(ctx, space.ID, camille.ID, "write"); err != nil {
		t.Fatalf("CreateOrUpdateSpaceShare: %v", err)
	}
	reassignCamille()
	rec = callAs(f.owner, f.app.handleCustomCategoriesDelete, http.MethodDelete, "/api/v1/custom-categories/"+spaceID, "", "id", spaceID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("deleting the Space: status %d (%s)", rec.Code, rec.Body.String())
	}
	if after, err = f.app.DB.GetItem(ctx, chore.ID); err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	wantAssignees(t, after, f.guest, reader, f.sarah)

	guestID := strconv.FormatInt(f.guest.ID, 10)
	rec = callAs(f.owner, f.app.handleListShareRevoke, http.MethodDelete, "/api/v1/lists/"+listID+"/share/"+guestID, "", "id", listID, "userId", guestID)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoking the share: status %d (%s)", rec.Code, rec.Body.String())
	}
	if after, err = f.app.DB.GetItem(ctx, chore.ID); err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	wantAssignees(t, after, reader, f.sarah)
}

// TestAssignableUsersEndpoint covers GET /api/v1/lists/{id}/assignable-users
// in both modes: who is listed, and who may assign.
func TestAssignableUsersEndpoint(t *testing.T) {
	f := newAssignFixture(t)
	reader := mustUser(t, f.app, "reader@example.com", "Lecteur")
	if _, err := f.app.DB.CreateOrUpdateListShare(context.Background(), f.list.ID, reader.ID, "read"); err != nil {
		t.Fatalf("CreateOrUpdateListShare: %v", err)
	}
	listID := strconv.FormatInt(f.list.ID, 10)
	get := func(as *models.User, wantStatus int) models.AssignableUsers {
		t.Helper()
		var out models.AssignableUsers
		rec := callAs(as, f.app.handleListAssignableUsers, http.MethodGet, "/api/v1/lists/"+listID+"/assignable-users", "", "id", listID)
		if wantStatus != http.StatusOK {
			if rec.Code != wantStatus {
				t.Fatalf("status %d, want %d (%s)", rec.Code, wantStatus, rec.Body.String())
			}
			return out
		}
		decodeInto(t, rec, http.StatusOK, &out)
		if out.Users == nil {
			t.Fatal("users must be an array, never null")
		}
		return out
	}
	names := func(out models.AssignableUsers) string {
		parts := []string{}
		for _, u := range out.Users {
			parts = append(parts, u.DisplayName)
		}
		return strings.Join(parts, ",")
	}

	// Self-hosted: everyone who can open the list, by name, to everyone
	// who can open it; assigning takes write access.
	all := "Benjamin,Invité,Lecteur,Sarah,Thomas"
	for _, tc := range []struct {
		as        *models.User
		canAssign bool
	}{{f.owner, true}, {f.guest, true}, {reader, false}} {
		out := get(tc.as, http.StatusOK)
		if names(out) != all || out.CanAssign != tc.canAssign {
			t.Errorf("self-hosted, as %s: can_assign=%v users=%s; want %v, %s", tc.as.DisplayName, out.CanAssign, names(out), tc.canAssign, all)
		}
	}
	if out := get(f.owner, http.StatusOK); out.Users[0].Email != "owner@example.com" {
		t.Errorf("users carry their email: %+v", out.Users[0])
	}
	get(f.stray, http.StatusForbidden)

	// SaaS mode: the House only, and only to its members.
	f.app.Config.SaaSMode = true
	if out := get(f.thomas, http.StatusOK); names(out) != "Benjamin,Sarah,Thomas" || !out.CanAssign {
		t.Errorf("SaaS mode, as a member: can_assign=%v users=%s", out.CanAssign, names(out))
	}
	if out := get(f.guest, http.StatusOK); len(out.Users) != 0 || out.CanAssign {
		t.Errorf("SaaS mode, as a share recipient: can_assign=%v users=%s; want nothing", out.CanAssign, names(out))
	}
	get(f.stray, http.StatusForbidden)
}

func mustUser(t *testing.T, app *Application, email, name string) *models.User {
	t.Helper()
	hash := "x"
	u, err := app.DB.CreateUser(context.Background(), email, &hash, nil, nil, name)
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", email, err)
	}
	return u
}
