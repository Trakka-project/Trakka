package db

import (
	"context"
	"errors"
	"testing"
)

// publicLinksFixture: a House owner with two lists.
func publicLinksFixture(t *testing.T) (d *DB, userID, listA, listB int64) {
	t.Helper()
	d = openTestDB(t)
	ctx := context.Background()
	hash := "x"
	user, err := d.CreateUser(ctx, "owner@example.com", &hash, nil, nil, "Owner")
	if err != nil {
		t.Fatal(err)
	}
	house, err := d.CreateHouseWithOwner(ctx, "Maison", user.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, err := d.CreateList(ctx, "Courses", "shopping", house.ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.CreateList(ctx, "Tâches", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return d, user.ID, a.ID, b.ID
}

func TestListPublicLinksAreIndependent(t *testing.T) {
	d, user, listA, listB := publicLinksFixture(t)
	ctx := context.Background()

	if links, err := d.ListListPublicLinks(ctx, listA); err != nil || len(links) != 0 {
		t.Fatalf("a new list has no link, got %+v (%v)", links, err)
	}

	family, guests := "Famille", "Invités"
	expires := "2099-01-01T00:00:00Z"
	l1, err := d.CreateListPublicLink(ctx, listA, "token-1", &family, "full_edit", nil, user)
	if err != nil {
		t.Fatal(err)
	}
	l2, err := d.CreateListPublicLink(ctx, listA, "token-2", &guests, "deposit_only", &expires, user)
	if err != nil {
		t.Fatal(err)
	}
	l3, err := d.CreateListPublicLink(ctx, listA, "token-3", nil, "read", nil, user)
	if err != nil {
		t.Fatal(err)
	}
	other, err := d.CreateListPublicLink(ctx, listB, "token-b", nil, "read", nil, user)
	if err != nil {
		t.Fatal(err)
	}
	if l1.Label != "Famille" || l1.AccessMode != "full_edit" || !l1.Enabled || l2.ExpiresAt != expires || l3.Label != "" || l1.ID == l2.ID {
		t.Fatalf("unexpected links %+v %+v %+v", l1, l2, l3)
	}
	if _, err := d.CreateListPublicLink(ctx, listA, "token-1", nil, "read", nil, user); err == nil {
		t.Fatal("two links can't share a token")
	}

	links, err := d.ListListPublicLinks(ctx, listA)
	if err != nil || len(links) != 3 || links[0].ID != l1.ID || links[2].ID != l3.ID {
		t.Fatalf("list A's links, oldest first: %+v (%v)", links, err)
	}
	if n, err := d.CountListPublicLinks(ctx, listA); err != nil || n != 3 {
		t.Fatalf("CountListPublicLinks = %d (%v), want 3", n, err)
	}

	// Each token resolves to its own link, and records its own use.
	used, err := d.UseListPublicLink(ctx, "token-2")
	if err != nil || used.ID != l2.ID || used.ListID != listA || used.AccessMode != "deposit_only" {
		t.Fatalf("token-2: %+v (%v)", used, err)
	}
	if got, _ := d.GetListPublicLink(ctx, listA, l2.ID); got.LastUsedAt == "" {
		t.Fatal("a use must be recorded on that link")
	}
	if got, _ := d.GetListPublicLink(ctx, listA, l1.ID); got.LastUsedAt != "" {
		t.Fatal("using one link must not touch the others")
	}

	// A link is only reachable through its own list.
	if _, err := d.GetListPublicLink(ctx, listB, l1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another list's link: got %v, want ErrNotFound", err)
	}
	if err := d.DeleteListPublicLink(ctx, listB, l1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting another list's link: got %v, want ErrNotFound", err)
	}

	// Deleting one link leaves the others working.
	if err := d.DeleteListPublicLink(ctx, listA, l2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UseListPublicLink(ctx, "token-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the deleted link must be gone, got %v", err)
	}
	for _, token := range []string{"token-1", "token-3", "token-b"} {
		if _, err := d.UseListPublicLink(ctx, token); err != nil {
			t.Fatalf("%s must still work: %v", token, err)
		}
	}
	if err := d.DeleteListPublicLink(ctx, listA, l2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting it again: got %v, want ErrNotFound", err)
	}

	// Deleting a list deletes all its links, and only its.
	if err := d.DeleteList(ctx, listA); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"token-1", "token-3"} {
		if _, err := d.UseListPublicLink(ctx, token); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s must be gone with its list, got %v", token, err)
		}
	}
	if got, err := d.UseListPublicLink(ctx, "token-b"); err != nil || got.ID != other.ID {
		t.Fatalf("list B's link must be untouched: %+v (%v)", got, err)
	}
}

func TestDisabledPublicLinkRecordsNoUse(t *testing.T) {
	d, user, listA, _ := publicLinksFixture(t)
	ctx := context.Background()
	link, err := d.CreateListPublicLink(ctx, listA, "token-off", nil, "read", nil, user)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.conn.Exec(`UPDATE list_public_links SET is_enabled = 0 WHERE id = ?`, link.ID); err != nil {
		t.Fatal(err)
	}
	got, err := d.UseListPublicLink(ctx, "token-off")
	if err != nil || got.Enabled {
		t.Fatalf("a disabled link still resolves, disabled: %+v (%v)", got, err)
	}
	if again, _ := d.GetListPublicLink(ctx, listA, link.ID); again.LastUsedAt != "" {
		t.Fatal("a disabled link must record no use")
	}
}

func TestCountListItems(t *testing.T) {
	d, _, listA, _ := publicLinksFixture(t)
	ctx := context.Background()
	for _, title := range []string{"a", "b", "c"} {
		if _, err := d.CreateItem(ctx, listA, title, nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := d.CountListItems(ctx, listA); err != nil || n != 3 {
		t.Fatalf("CountListItems = %d, %v; want 3", n, err)
	}
}
