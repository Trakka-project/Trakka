package db

import (
	"context"
	"testing"
)

// TestCustomCategoryCRUD exercises create/list/update/delete and the
// per-user ownership scoping every lookup/mutation relies on.
func TestCustomCategoryCRUD(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	owner := mustCreateUser(t, ctx, d)
	other := mustCreateUserWithEmail(t, ctx, d, "other@example.com")

	category, err := d.CreateCustomCategory(ctx, owner, "Vacances", "🏖️", "#3366ff", 0)
	if err != nil {
		t.Fatalf("CreateCustomCategory: %v", err)
	}
	if category.UserID != owner {
		t.Fatalf("expected category owned by %d, got %d", owner, category.UserID)
	}

	list, err := d.ListCustomCategoriesForUser(ctx, owner)
	if err != nil {
		t.Fatalf("ListCustomCategoriesForUser: %v", err)
	}
	if len(list) != 1 || list[0].ID != category.ID {
		t.Fatalf("expected exactly the created category, got %+v", list)
	}

	otherList, err := d.ListCustomCategoriesForUser(ctx, other)
	if err != nil {
		t.Fatalf("ListCustomCategoriesForUser (other user): %v", err)
	}
	if len(otherList) != 0 {
		t.Fatalf("expected no categories for a different user, got %+v", otherList)
	}

	if _, err := d.GetCustomCategoryForUser(ctx, category.ID, other); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound looking up another user's category, got %v", err)
	}

	updated, err := d.UpdateCustomCategoryForUser(ctx, category.ID, owner, "Été", "☀️", "#ffcc00", 1)
	if err != nil {
		t.Fatalf("UpdateCustomCategoryForUser: %v", err)
	}
	if updated.Name != "Été" || updated.Icon != "☀️" || updated.Color != "#ffcc00" || updated.Position != 1 {
		t.Fatalf("unexpected updated category %+v", updated)
	}

	if _, err := d.UpdateCustomCategoryForUser(ctx, category.ID, other, "Hacked", "", "", 0); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound updating another user's category, got %v", err)
	}

	if err := d.DeleteCustomCategoryForUser(ctx, category.ID, other); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound deleting another user's category, got %v", err)
	}
	if err := d.DeleteCustomCategoryForUser(ctx, category.ID, owner); err != nil {
		t.Fatalf("DeleteCustomCategoryForUser: %v", err)
	}
	if _, err := d.GetCustomCategory(ctx, category.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

// TestListCustomCategoryAssociation exercises attaching/embedding/detaching
// a custom category on a list: GetList/ListListsForUser should embed the
// category whenever custom_category_id is set, and deleting the category
// should unassign it from the list (ON DELETE SET NULL) rather than
// deleting the list.
func TestListCustomCategoryAssociation(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	owner := mustCreateUser(t, ctx, d)
	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", owner)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	category, err := d.CreateCustomCategory(ctx, owner, "Vacances", "🏖️", "#3366ff", 0)
	if err != nil {
		t.Fatalf("CreateCustomCategory: %v", err)
	}

	list, err := d.CreateList(ctx, "Courses de vacances", "shopping", house.ID, &category.ID, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	if list.CustomCategoryID == nil || *list.CustomCategoryID != category.ID {
		t.Fatalf("expected list.CustomCategoryID to be %d, got %v", category.ID, list.CustomCategoryID)
	}
	if list.CustomCategory == nil || list.CustomCategory.Name != "Vacances" {
		t.Fatalf("expected embedded category named Vacances, got %+v", list.CustomCategory)
	}

	fetched, err := d.GetList(ctx, list.ID)
	if err != nil {
		t.Fatalf("GetList: %v", err)
	}
	if fetched.CustomCategory == nil || fetched.CustomCategory.ID != category.ID {
		t.Fatalf("expected GetList to embed the category, got %+v", fetched.CustomCategory)
	}

	forUser, err := d.ListListsForUser(ctx, owner, "", 0)
	if err != nil {
		t.Fatalf("ListListsForUser: %v", err)
	}
	if len(forUser) != 1 || forUser[0].CustomCategory == nil || forUser[0].CustomCategory.ID != category.ID {
		t.Fatalf("expected ListListsForUser to embed the category, got %+v", forUser)
	}

	// Dissociating via UpdateList (nil) clears both the id and the embed.
	dissociated, err := d.UpdateList(ctx, list.ID, list.Name, list.Type, nil, list.Icon)
	if err != nil {
		t.Fatalf("UpdateList (dissociate): %v", err)
	}
	if dissociated.CustomCategoryID != nil || dissociated.CustomCategory != nil {
		t.Fatalf("expected list to be dissociated, got %+v", dissociated)
	}

	// Re-associate, then delete the category: the list must survive with
	// custom_category_id reset to NULL (ON DELETE SET NULL), not cascade.
	reassociated, err := d.UpdateList(ctx, list.ID, list.Name, list.Type, &category.ID, list.Icon)
	if err != nil {
		t.Fatalf("UpdateList (re-associate): %v", err)
	}
	if reassociated.CustomCategoryID == nil {
		t.Fatalf("expected list to be re-associated, got %+v", reassociated)
	}
	if err := d.DeleteCustomCategoryForUser(ctx, category.ID, owner); err != nil {
		t.Fatalf("DeleteCustomCategoryForUser: %v", err)
	}
	afterDelete, err := d.GetList(ctx, list.ID)
	if err != nil {
		t.Fatalf("GetList after category deletion: %v", err)
	}
	if afterDelete.CustomCategoryID != nil || afterDelete.CustomCategory != nil {
		t.Fatalf("expected custom_category_id to be cleared after deleting the category, got %+v", afterDelete)
	}
}

// TestListAllCustomCategoriesWithOwners exercises the admin "Espaces"
// panel's data source: every Space across every user, with its owner's
// identity and a correct per-category count of the lists tagged with it.
func TestListAllCustomCategoriesWithOwners(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	owner := mustCreateUser(t, ctx, d)
	house, err := d.CreateHouseWithOwner(ctx, "Test House", owner)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	category, err := d.CreateCustomCategory(ctx, owner, "Vacances", "🏖️", "#3366ff", 0)
	if err != nil {
		t.Fatalf("CreateCustomCategory: %v", err)
	}
	if _, err := d.CreateList(ctx, "List A", "shopping", house.ID, &category.ID, ""); err != nil {
		t.Fatalf("creating list A: %v", err)
	}
	if _, err := d.CreateList(ctx, "List B", "shopping", house.ID, &category.ID, ""); err != nil {
		t.Fatalf("creating list B: %v", err)
	}
	// An untagged list must not count toward this category.
	if _, err := d.CreateList(ctx, "List C", "shopping", house.ID, nil, ""); err != nil {
		t.Fatalf("creating list C: %v", err)
	}

	rows, err := d.ListAllCustomCategoriesWithOwners(ctx)
	if err != nil {
		t.Fatalf("ListAllCustomCategoriesWithOwners: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 category, got %d", len(rows))
	}
	got := rows[0]
	if got.ID != category.ID {
		t.Fatalf("expected category %d, got %d", category.ID, got.ID)
	}
	owned, err := d.GetUser(ctx, owner)
	if err != nil {
		t.Fatalf("loading owner: %v", err)
	}
	if got.OwnerEmail != owned.Email || got.OwnerDisplayName != owned.DisplayName {
		t.Fatalf("expected owner %q/%q, got %q/%q", owned.Email, owned.DisplayName, got.OwnerEmail, got.OwnerDisplayName)
	}
	if got.ListCount != 2 {
		t.Fatalf("expected list_count 2, got %d", got.ListCount)
	}
}

// TestDeleteCustomCategoryAdmin exercises the admin override delete (not
// scoped to an owner), and confirms it leaves a referencing list intact
// with custom_category_id reset to NULL — the same ON DELETE SET NULL
// behavior the owner-scoped delete already has.
func TestDeleteCustomCategoryAdmin(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	owner := mustCreateUser(t, ctx, d)
	house, err := d.CreateHouseWithOwner(ctx, "Test House", owner)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	category, err := d.CreateCustomCategory(ctx, owner, "Vacances", "🏖️", "#3366ff", 0)
	if err != nil {
		t.Fatalf("CreateCustomCategory: %v", err)
	}
	list, err := d.CreateList(ctx, "List A", "shopping", house.ID, &category.ID, "")
	if err != nil {
		t.Fatalf("creating list: %v", err)
	}

	// An admin (a different user than the owner) can delete it directly.
	if err := d.DeleteCustomCategory(ctx, category.ID); err != nil {
		t.Fatalf("DeleteCustomCategory: %v", err)
	}
	if _, err := d.GetCustomCategory(ctx, category.ID); err == nil {
		t.Fatal("expected the category to be gone")
	}
	afterDelete, err := d.GetList(ctx, list.ID)
	if err != nil {
		t.Fatalf("GetList after category deletion: %v", err)
	}
	if afterDelete.CustomCategoryID != nil {
		t.Fatalf("expected custom_category_id to be cleared, got %+v", afterDelete.CustomCategoryID)
	}

	if err := d.DeleteCustomCategory(ctx, 999999); err == nil {
		t.Fatal("expected an error deleting a nonexistent category")
	}
}

func mustCreateUserWithEmail(t *testing.T, ctx context.Context, d *DB, email string) int64 {
	t.Helper()
	hash := "x"
	user, err := d.CreateUser(ctx, email, &hash, nil, nil, "Someone")
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}
	return user.ID
}
