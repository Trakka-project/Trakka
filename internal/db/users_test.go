package db

import (
	"context"
	"errors"
	"testing"
)

// TestFirstUserBecomesAdmin exercises CreateUser's "very first account on
// this instance is an admin" bootstrap (see the comment on CreateUser) — the
// only way Trakka ever gets an initial administrator, since there is no
// separate seeding mechanism.
func TestFirstUserBecomesAdmin(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	hash := "x"
	first, err := d.CreateUser(ctx, "first@example.com", &hash, nil, nil, "First")
	if err != nil {
		t.Fatalf("creating first user: %v", err)
	}
	if !first.IsAdmin {
		t.Fatal("expected the first user created to be an admin")
	}

	second, err := d.CreateUser(ctx, "second@example.com", &hash, nil, nil, "Second")
	if err != nil {
		t.Fatalf("creating second user: %v", err)
	}
	if second.IsAdmin {
		t.Fatal("expected the second user created to not be an admin")
	}

	// GetUser and GetUserByEmail must agree on the persisted admin flag.
	reloaded, err := d.GetUser(ctx, first.ID)
	if err != nil {
		t.Fatalf("reloading first user: %v", err)
	}
	if !reloaded.IsAdmin {
		t.Fatal("expected GetUser to report the first user as admin")
	}

	byEmail, err := d.GetUserByEmail(ctx, "second@example.com")
	if err != nil {
		t.Fatalf("reloading second user by email: %v", err)
	}
	if byEmail.IsAdmin {
		t.Fatal("expected GetUserByEmail to report the second user as non-admin")
	}
}

// TestKeepLastPagePreference exercises the "keep last page on launch"
// preference (migration 0010): it must default to enabled for a newly
// created user, be updatable via UpdateUserKeepLastPage, and the new value
// must be visible through both GetUser and GetUserByEmail — the two lookup
// paths RequireSession and Authenticate rely on, respectively.
func TestKeepLastPagePreference(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	hash := "x"
	user, err := d.CreateUser(ctx, "keep-last-page@example.com", &hash, nil, nil, "Test")
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}
	if !user.KeepLastPage {
		t.Fatal("expected keep_last_page to default to true for a new user")
	}

	updated, err := d.UpdateUserKeepLastPage(ctx, user.ID, false)
	if err != nil {
		t.Fatalf("disabling keep_last_page: %v", err)
	}
	if updated.KeepLastPage {
		t.Fatal("expected UpdateUserKeepLastPage(false) to report keep_last_page as false")
	}

	reloaded, err := d.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("reloading user: %v", err)
	}
	if reloaded.KeepLastPage {
		t.Fatal("expected GetUser to report the persisted keep_last_page as false")
	}

	byEmail, err := d.GetUserByEmail(ctx, "keep-last-page@example.com")
	if err != nil {
		t.Fatalf("reloading user by email: %v", err)
	}
	if byEmail.KeepLastPage {
		t.Fatal("expected GetUserByEmail to report the persisted keep_last_page as false")
	}

	if _, err := d.UpdateUserKeepLastPage(ctx, 999999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound updating a nonexistent user, got %v", err)
	}
}

// TestUserLanguagePreference exercises the users.language column (migration
// 0017): a new user starts with no explicit preference recorded (an empty
// string — internal/handlers.resolveUserLanguage, not this layer, is what
// falls that back to the instance's DEFAULT_APP_LANGUAGE), and
// UpdateUserLanguage must persist a real choice visible through both GetUser
// and GetUserByEmail.
func TestUserLanguagePreference(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	hash := "x"
	user, err := d.CreateUser(ctx, "language@example.com", &hash, nil, nil, "Test")
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}
	if user.Language != "" {
		t.Fatalf("expected a new user's language to start empty (no explicit preference), got %q", user.Language)
	}

	updated, err := d.UpdateUserLanguage(ctx, user.ID, "en")
	if err != nil {
		t.Fatalf("setting language: %v", err)
	}
	if updated.Language != "en" {
		t.Fatalf("expected UpdateUserLanguage to report language %q, got %q", "en", updated.Language)
	}

	reloaded, err := d.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("reloading user: %v", err)
	}
	if reloaded.Language != "en" {
		t.Fatalf("expected GetUser to report the persisted language %q, got %q", "en", reloaded.Language)
	}

	byEmail, err := d.GetUserByEmail(ctx, "language@example.com")
	if err != nil {
		t.Fatalf("reloading user by email: %v", err)
	}
	if byEmail.Language != "en" {
		t.Fatalf("expected GetUserByEmail to report the persisted language %q, got %q", "en", byEmail.Language)
	}

	if _, err := d.UpdateUserLanguage(ctx, 999999, "fr"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound updating a nonexistent user, got %v", err)
	}
}

// TestListAllUsers exercises the admin "Utilisateurs" panel's data source:
// every account on the instance, in creation order, regardless of who's
// asking (authorization is the handler layer's job, not this method's).
func TestListAllUsers(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	hash := "x"
	first, err := d.CreateUser(ctx, "first@example.com", &hash, nil, nil, "First")
	if err != nil {
		t.Fatalf("creating first user: %v", err)
	}
	second, err := d.CreateUser(ctx, "second@example.com", &hash, nil, nil, "Second")
	if err != nil {
		t.Fatalf("creating second user: %v", err)
	}

	users, err := d.ListAllUsers(ctx)
	if err != nil {
		t.Fatalf("listing all users: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
	if users[0].ID != first.ID || users[1].ID != second.ID {
		t.Fatalf("expected users ordered by id (first then second), got %d then %d", users[0].ID, users[1].ID)
	}
	if !users[0].IsAdmin {
		t.Fatal("expected the first-created user to be reported as admin")
	}
	if users[1].IsAdmin {
		t.Fatal("expected the second-created user to be reported as non-admin")
	}
}

// TestSetUserAdminAndCountAdmins exercises the admin-role toggle and the
// admin-count helper the handler layer uses to refuse demoting/deleting the
// last remaining admin.
func TestSetUserAdminAndCountAdmins(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	hash := "x"
	first, err := d.CreateUser(ctx, "first@example.com", &hash, nil, nil, "First") // becomes admin
	if err != nil {
		t.Fatalf("creating first user: %v", err)
	}
	second, err := d.CreateUser(ctx, "second@example.com", &hash, nil, nil, "Second")
	if err != nil {
		t.Fatalf("creating second user: %v", err)
	}

	count, err := d.CountAdmins(ctx)
	if err != nil {
		t.Fatalf("counting admins: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 admin initially, got %d", count)
	}

	promoted, err := d.SetUserAdmin(ctx, second.ID, true)
	if err != nil {
		t.Fatalf("promoting second user: %v", err)
	}
	if !promoted.IsAdmin {
		t.Fatal("expected the promoted user to be reported as admin")
	}
	count, err = d.CountAdmins(ctx)
	if err != nil {
		t.Fatalf("counting admins after promotion: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 admins after promotion, got %d", count)
	}

	demoted, err := d.SetUserAdmin(ctx, first.ID, false)
	if err != nil {
		t.Fatalf("demoting first user: %v", err)
	}
	if demoted.IsAdmin {
		t.Fatal("expected the demoted user to be reported as non-admin")
	}

	if _, err := d.SetUserAdmin(ctx, 999999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound promoting a nonexistent user, got %v", err)
	}
}

// TestDeleteUser exercises account deletion and confirms its cascade
// removes dependent rows (house_members here — the same ON DELETE CASCADE
// chain removes sessions, custom_categories, shares, etc., per
// db.DeleteUser's own doc comment).
func TestDeleteUser(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	hash := "x"
	user, err := d.CreateUser(ctx, "delete-me@example.com", &hash, nil, nil, "Delete Me")
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}
	house, err := d.CreateHouseWithOwner(ctx, "Test House", user.ID)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}

	if err := d.DeleteUser(ctx, user.ID); err != nil {
		t.Fatalf("deleting user: %v", err)
	}

	if _, err := d.GetUser(ctx, user.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound reloading a deleted user, got %v", err)
	}
	ok, err := d.UserCanAccessHouse(ctx, user.ID, house.ID)
	if err != nil {
		t.Fatalf("checking house access after user deletion: %v", err)
	}
	if ok {
		t.Fatal("expected the deleted user's house_members row to have been cascade-deleted")
	}

	if err := d.DeleteUser(ctx, 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound deleting a nonexistent user, got %v", err)
	}
}
