package db

import (
	"context"
	"testing"
)

// TestPushSubscriptionUpsertAndDelete covers CreatePushSubscription's
// upsert-on-(user_id,endpoint) behavior and both delete paths.
func TestPushSubscriptionUpsertAndDelete(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	user := mustCreateUser(t, ctx, d)

	sub, err := d.CreatePushSubscription(ctx, user, "https://push.example.com/ep1", "p256dh-1", "auth-1", "TestAgent/1.0")
	if err != nil {
		t.Fatalf("CreatePushSubscription: %v", err)
	}
	if sub.Endpoint != "https://push.example.com/ep1" {
		t.Fatalf("unexpected endpoint: %q", sub.Endpoint)
	}

	// Re-subscribing the same (user, endpoint) pair refreshes the stored
	// keys in place rather than erroring or creating a second row — a
	// browser occasionally rotates keys for an unchanged endpoint.
	refreshed, err := d.CreatePushSubscription(ctx, user, "https://push.example.com/ep1", "p256dh-2", "auth-2", "TestAgent/2.0")
	if err != nil {
		t.Fatalf("CreatePushSubscription (upsert): %v", err)
	}
	if refreshed.ID != sub.ID {
		t.Fatalf("upsert created a new row (id %d) instead of reusing %d", refreshed.ID, sub.ID)
	}
	if refreshed.P256dh != "p256dh-2" || refreshed.Auth != "auth-2" {
		t.Fatalf("upsert did not refresh keys: %+v", refreshed)
	}

	subs, err := d.ListPushSubscriptionsForUsers(ctx, []int64{user})
	if err != nil {
		t.Fatalf("ListPushSubscriptionsForUsers: %v", err)
	}
	if len(subs) != 1 || subs[0].P256dh != "p256dh-2" {
		t.Fatalf("expected exactly one refreshed subscription, got %+v", subs)
	}

	if err := d.DeletePushSubscription(ctx, user, "https://push.example.com/ep1"); err != nil {
		t.Fatalf("DeletePushSubscription: %v", err)
	}
	if err := d.DeletePushSubscription(ctx, user, "https://push.example.com/ep1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound deleting an already-deleted subscription, got %v", err)
	}
}

// TestListPushSubscriptionsForUsers covers the batch lookup used to fan a
// single notification out to every recipient's devices in one query.
func TestListPushSubscriptionsForUsers(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	userA := mustCreateUser(t, ctx, d)
	userB := mustCreateUserWithEmail(t, ctx, d, "b@example.com")
	userC := mustCreateUserWithEmail(t, ctx, d, "c@example.com")

	if _, err := d.CreatePushSubscription(ctx, userA, "https://push.example.com/a1", "p", "a", ""); err != nil {
		t.Fatalf("subscribing A: %v", err)
	}
	if _, err := d.CreatePushSubscription(ctx, userA, "https://push.example.com/a2", "p", "a", ""); err != nil {
		t.Fatalf("subscribing A's second device: %v", err)
	}
	if _, err := d.CreatePushSubscription(ctx, userB, "https://push.example.com/b1", "p", "a", ""); err != nil {
		t.Fatalf("subscribing B: %v", err)
	}
	// userC deliberately has no subscription — never opted in.

	subs, err := d.ListPushSubscriptionsForUsers(ctx, []int64{userA, userB, userC})
	if err != nil {
		t.Fatalf("ListPushSubscriptionsForUsers: %v", err)
	}
	if len(subs) != 3 {
		t.Fatalf("expected 3 subscriptions across A's two devices and B's one, got %d: %+v", len(subs), subs)
	}

	if empty, err := d.ListPushSubscriptionsForUsers(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("ListPushSubscriptionsForUsers(nil) = %v, %v; want an empty slice, no error", empty, err)
	}
}

func TestDeletePushSubscriptionByID(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	user := mustCreateUser(t, ctx, d)

	sub, err := d.CreatePushSubscription(ctx, user, "https://push.example.com/ep1", "p", "a", "")
	if err != nil {
		t.Fatalf("CreatePushSubscription: %v", err)
	}

	if err := d.DeletePushSubscriptionByID(ctx, sub.ID); err != nil {
		t.Fatalf("DeletePushSubscriptionByID: %v", err)
	}
	// A no-op on an already-gone row, not an error — mirrors how a push
	// service reporting the same subscription gone twice in a row (a race
	// between two concurrent sends) must not surface as a failure.
	if err := d.DeletePushSubscriptionByID(ctx, sub.ID); err != nil {
		t.Fatalf("DeletePushSubscriptionByID (already deleted): %v", err)
	}

	subs, err := d.ListPushSubscriptionsForUsers(ctx, []int64{user})
	if err != nil {
		t.Fatalf("ListPushSubscriptionsForUsers: %v", err)
	}
	if len(subs) != 0 {
		t.Fatalf("expected no subscriptions left, got %+v", subs)
	}
}

// TestListPushSubscriptionsCarriesVibratePreference covers the users JOIN
// in ListPushSubscriptionsForUsers: each subscription reports its own
// owner's vibrate_on_notification, which defaults to on.
func TestListPushSubscriptionsCarriesVibratePreference(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	vibrating := mustCreateUserWithEmail(t, ctx, d, "vibrate@example.com")
	quiet := mustCreateUserWithEmail(t, ctx, d, "quiet@example.com")

	if _, err := d.CreatePushSubscription(ctx, vibrating, "https://push.example.com/v", "p", "a", ""); err != nil {
		t.Fatalf("CreatePushSubscription: %v", err)
	}
	if _, err := d.CreatePushSubscription(ctx, quiet, "https://push.example.com/q", "p", "a", ""); err != nil {
		t.Fatalf("CreatePushSubscription: %v", err)
	}
	if _, err := d.UpdateUserVibrateOnNotification(ctx, quiet, false); err != nil {
		t.Fatalf("UpdateUserVibrateOnNotification: %v", err)
	}

	subs, err := d.ListPushSubscriptionsForUsers(ctx, []int64{vibrating, quiet})
	if err != nil {
		t.Fatalf("ListPushSubscriptionsForUsers: %v", err)
	}
	if len(subs) != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(subs))
	}
	for _, s := range subs {
		if want := s.UserID == vibrating; s.Vibrate != want {
			t.Fatalf("subscription of user %d: Vibrate = %v, want %v", s.UserID, s.Vibrate, want)
		}
	}
}
