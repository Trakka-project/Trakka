package db

import (
	"context"
	"testing"
)

// TestListOverdueTasksForUser checks the summary's selection: not done, due
// before the summary's day however long ago, on a list the user can reach
// through any of the three access sources — whatever the task's own
// reminder setting.
func TestListOverdueTasksForUser(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	owner := mustCreateUserWithEmail(t, ctx, d, "owner@example.com")
	friend := mustCreateUserWithEmail(t, ctx, d, "friend@example.com")
	stranger := mustCreateUserWithEmail(t, ctx, d, "stranger@example.com")
	house, err := d.CreateHouseWithOwner(ctx, "Maison", owner)
	if err != nil {
		t.Fatal(err)
	}
	list, err := d.CreateList(ctx, "Tâches", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateOrUpdateListShare(ctx, list.ID, friend, "read"); err != nil {
		t.Fatal(err)
	}

	create := func(title, due string) int64 {
		t.Helper()
		item, err := d.CreateItem(ctx, list.ID, title, nil, 1, nil, false, 0, nil, &due, nil, nil, false, nil, nil, false)
		if err != nil {
			t.Fatalf("creating %s: %v", title, err)
		}
		return item.ID
	}
	today := "2026-10-06"
	yesterday := create("Payer le loyer", "2026-10-05")
	fourDaysLate := create("Appeler le plombier", "2026-10-02")
	noReminder := create("Sans rappel", "2026-10-05")
	if _, err := d.SetItemReminder(ctx, noReminder, false, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	done := create("Déjà fait", "2026-10-01")
	if _, err := d.conn.ExecContext(ctx, `UPDATE items SET done = 1 WHERE id = ?`, done); err != nil {
		t.Fatal(err)
	}
	create("Aujourd'hui", today)
	create("Demain", "2026-10-07")
	if _, err := d.CreateItem(ctx, list.ID, "Sans date", nil, 1, nil, false, 0, nil, nil, nil, nil, false, nil, nil, false); err != nil {
		t.Fatal(err)
	}

	for _, user := range []int64{owner, friend} {
		tasks, err := d.ListOverdueTasksForUser(ctx, user, today)
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 3 || tasks[0].ItemID != fourDaysLate || tasks[1].ItemID != yesterday || tasks[2].ItemID != noReminder {
			t.Fatalf("user %d got %+v, want items %d, %d and %d, oldest first", user, tasks, fourDaysLate, yesterday, noReminder)
		}
		if tasks[0].ListName != "Tâches" || tasks[0].Title != "Appeler le plombier" || tasks[0].DueDate != "2026-10-02" {
			t.Fatalf("got %+v", tasks[0])
		}
	}
	if tasks, err := d.ListOverdueTasksForUser(ctx, stranger, today); err != nil || len(tasks) != 0 {
		t.Fatalf("a user without access got %+v (%v)", tasks, err)
	}
}

// TestListUsersAwaitingOverdueSummary checks that only users with the summary
// on are listed, until they are marked as handled for that day.
func TestListUsersAwaitingOverdueSummary(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	on := mustCreateUserWithEmail(t, ctx, d, "on@example.com")
	mustCreateUserWithEmail(t, ctx, d, "off@example.com") // off by default
	enabled, summaryTime := true, "07:30"
	if _, err := d.UpdateUserNotificationPreferences(ctx, on, NotificationPreferences{
		OverdueTasksSummaryEnabled: &enabled, OverdueTasksSummaryTime: &summaryTime,
	}); err != nil {
		t.Fatal(err)
	}

	users, err := d.ListUsersAwaitingOverdueSummary(ctx, "2026-10-06")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].UserID != on || users[0].Time != "07:30" {
		t.Fatalf("got %+v, want only user %d at 07:30", users, on)
	}

	if err := d.MarkOverdueSummarySent(ctx, on, "2026-10-06"); err != nil {
		t.Fatal(err)
	}
	if users, _ := d.ListUsersAwaitingOverdueSummary(ctx, "2026-10-06"); len(users) != 0 {
		t.Fatalf("still listed after being marked: %+v", users)
	}
	if users, _ := d.ListUsersAwaitingOverdueSummary(ctx, "2026-10-07"); len(users) != 1 {
		t.Fatalf("not listed again the next day: %+v", users)
	}
}

// TestUpdateUserNotificationPreferences checks the defaults and that a nil
// field leaves the stored value alone.
func TestUpdateUserNotificationPreferences(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	id := mustCreateUser(t, ctx, d)
	user, err := d.GetUser(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !user.RemindersEnabled || user.OverdueTasksSummaryEnabled || user.OverdueTasksSummaryTime != "08:00" ||
		!user.CollaboratorActionsEnabled || !user.ItemAdditionsEnabled || !user.ListSharingEnabled {
		t.Fatalf("defaults: %+v", user)
	}

	off := false
	if user, err = d.UpdateUserNotificationPreferences(ctx, id, NotificationPreferences{RemindersEnabled: &off, ItemAdditionsEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	if user.RemindersEnabled || user.ItemAdditionsEnabled || !user.CollaboratorActionsEnabled || !user.ListSharingEnabled ||
		user.OverdueTasksSummaryEnabled || user.OverdueTasksSummaryTime != "08:00" {
		t.Fatalf("after turning reminders and additions off: %+v", user)
	}
	on, at := true, "06:45"
	if user, err = d.UpdateUserNotificationPreferences(ctx, id, NotificationPreferences{OverdueTasksSummaryEnabled: &on, OverdueTasksSummaryTime: &at, ListSharingEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	if user.RemindersEnabled || user.ItemAdditionsEnabled || user.ListSharingEnabled ||
		!user.OverdueTasksSummaryEnabled || user.OverdueTasksSummaryTime != "06:45" {
		t.Fatalf("after turning the summary on and sharing off: %+v", user)
	}
	if _, err := d.UpdateUserNotificationPreferences(ctx, 9999, NotificationPreferences{RemindersEnabled: &on}); err != ErrNotFound {
		t.Fatalf("unknown user: got %v, want ErrNotFound", err)
	}
}

// TestListNotificationRecipientsFor checks that each notification type
// reaches only the users who kept it on, still excluding the actor.
func TestListNotificationRecipientsFor(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	owner := mustCreateUserWithEmail(t, ctx, d, "owner@example.com")
	friend := mustCreateUserWithEmail(t, ctx, d, "friend@example.com")
	house, err := d.CreateHouseWithOwner(ctx, "Maison", owner)
	if err != nil {
		t.Fatal(err)
	}
	list, err := d.CreateList(ctx, "Tâches", "todo", house.ID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateOrUpdateListShare(ctx, list.ID, friend, "write"); err != nil {
		t.Fatal(err)
	}

	kinds := []NotificationKind{NotifyTaskReminders, NotifyCollaboratorActions, NotifyItemAdditions}
	for _, kind := range kinds {
		if ids, err := d.ListNotificationRecipientsFor(ctx, list.ID, 0, kind); err != nil || len(ids) != 2 {
			t.Fatalf("%s: got %v (%v), want both users", kind, ids, err)
		}
		if ids, err := d.ListNotificationRecipientsFor(ctx, list.ID, owner, kind); err != nil || len(ids) != 1 || ids[0] != friend {
			t.Fatalf("%s excluding the owner: got %v (%v), want only the friend", kind, ids, err)
		}
	}

	off := false
	if _, err := d.UpdateUserNotificationPreferences(ctx, friend, NotificationPreferences{
		RemindersEnabled: &off, CollaboratorActionsEnabled: &off, ItemAdditionsEnabled: &off,
	}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range kinds {
		if ids, err := d.ListNotificationRecipientsFor(ctx, list.ID, 0, kind); err != nil || len(ids) != 1 || ids[0] != owner {
			t.Fatalf("%s with the friend opted out: got %v (%v), want only the owner", kind, ids, err)
		}
	}
	if ids, err := d.ListNotificationRecipientsFor(ctx, list.ID, 0, "unknown"); err != nil || len(ids) != 0 {
		t.Fatalf("unknown kind: got %v (%v), want nobody", ids, err)
	}
	// Price alerts have no switch: the unfiltered query still reaches both.
	if ids, err := d.ListNotificationRecipients(ctx, list.ID, 0); err != nil || len(ids) != 2 {
		t.Fatalf("unfiltered recipients got %v (%v), want both users still", ids, err)
	}
}

func TestUserWantsNotification(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	id := mustCreateUser(t, ctx, d)
	if wants, err := d.UserWantsNotification(ctx, id, NotifyListSharing); err != nil || !wants {
		t.Fatalf("default: got %v (%v), want true", wants, err)
	}
	off := false
	if _, err := d.UpdateUserNotificationPreferences(ctx, id, NotificationPreferences{ListSharingEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	if wants, err := d.UserWantsNotification(ctx, id, NotifyListSharing); err != nil || wants {
		t.Fatalf("turned off: got %v (%v), want false", wants, err)
	}
	if wants, err := d.UserWantsNotification(ctx, 9999, NotifyListSharing); err != nil || wants {
		t.Fatalf("unknown user: got %v (%v), want false", wants, err)
	}
}
