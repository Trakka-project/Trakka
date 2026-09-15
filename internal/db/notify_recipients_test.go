package db

import (
	"context"
	"sort"
	"testing"
)

// TestListNotificationRecipients exercises all three access sources
// ListNotificationRecipients unions together — House membership, a direct
// List share, and a Space share on the list's parent category — plus the
// excludeUserID exclusion notifyListChange relies on to never notify a user
// about their own action.
func TestListNotificationRecipients(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)

	owner := mustCreateUser(t, ctx, d)
	member := mustCreateUserWithEmail(t, ctx, d, "member@example.com")
	listRecipient := mustCreateUserWithEmail(t, ctx, d, "list-shared@example.com")
	spaceRecipient := mustCreateUserWithEmail(t, ctx, d, "space-shared@example.com")
	stranger := mustCreateUserWithEmail(t, ctx, d, "stranger@example.com")

	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", owner)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	if _, err := d.AddHouseMember(ctx, house.ID, member, "member"); err != nil {
		t.Fatalf("adding house member: %v", err)
	}

	category, err := d.CreateCustomCategory(ctx, owner, "Vacances", "🏖️", "#3366ff", 0)
	if err != nil {
		t.Fatalf("CreateCustomCategory: %v", err)
	}
	list, err := d.CreateList(ctx, "Courses", "shopping", house.ID, &category.ID, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}
	if _, err := d.CreateOrUpdateListShare(ctx, list.ID, listRecipient, "read"); err != nil {
		t.Fatalf("sharing list: %v", err)
	}
	if _, err := d.CreateOrUpdateSpaceShare(ctx, category.ID, spaceRecipient, "write"); err != nil {
		t.Fatalf("sharing space: %v", err)
	}

	// The owner (acting) is excluded; the House member, the List share
	// recipient, and the Space share recipient are all included; the
	// stranger (no relationship to the list at all) is not.
	recipients, err := d.ListNotificationRecipients(ctx, list.ID, owner)
	if err != nil {
		t.Fatalf("ListNotificationRecipients: %v", err)
	}
	sort.Slice(recipients, func(i, j int) bool { return recipients[i] < recipients[j] })

	want := []int64{member, listRecipient, spaceRecipient}
	sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })

	if len(recipients) != len(want) {
		t.Fatalf("recipients = %v, want %v", recipients, want)
	}
	for i := range want {
		if recipients[i] != want[i] {
			t.Fatalf("recipients = %v, want %v", recipients, want)
		}
	}
	for _, id := range recipients {
		if id == owner {
			t.Error("recipients incorrectly included the excluded actor (owner)")
		}
		if id == stranger {
			t.Error("recipients incorrectly included a user with no relationship to the list")
		}
	}

	// excludeUserID = 0 (no real user ever has this id) excludes nobody —
	// the shape RunDueReminderScan uses, since a due-date reminder has no
	// single actor to exclude.
	all, err := d.ListNotificationRecipients(ctx, list.ID, 0)
	if err != nil {
		t.Fatalf("ListNotificationRecipients(excludeUserID=0): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected all 4 related users (owner included) with excludeUserID=0, got %v", all)
	}
}

func TestListNotificationRecipientsNoAccess(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	owner := mustCreateUser(t, ctx, d)
	house, err := d.CreateHouseWithOwner(ctx, "Maison Test", owner)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	list, err := d.CreateList(ctx, "Courses", "shopping", house.ID, nil, "")
	if err != nil {
		t.Fatalf("CreateList: %v", err)
	}

	// Excluding the only House member (its owner) leaves nobody to notify —
	// must come back as an empty slice, not an error.
	recipients, err := d.ListNotificationRecipients(ctx, list.ID, owner)
	if err != nil {
		t.Fatalf("ListNotificationRecipients: %v", err)
	}
	if len(recipients) != 0 {
		t.Fatalf("expected no recipients, got %v", recipients)
	}
}
