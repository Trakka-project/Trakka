package db

import (
	"context"
	"errors"
	"testing"
)

func TestAPITokens(t *testing.T) {
	ctx := context.Background()
	d := openTestDB(t)
	hash := "x"
	alice, err := d.CreateUser(ctx, "alice@example.com", &hash, nil, nil, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := d.CreateUser(ctx, "bob@example.com", &hash, nil, nil, "Bob")
	if err != nil {
		t.Fatal(err)
	}

	ha, err := d.CreateAPIToken(ctx, alice.ID, "Home Assistant", "hash-ha")
	if err != nil {
		t.Fatal(err)
	}
	if ha.Name != "Home Assistant" || ha.CreatedAt == "" || ha.LastUsedAt != "" {
		t.Fatalf("unexpected new token %+v", ha)
	}
	if _, err := d.CreateAPIToken(ctx, alice.ID, "Script", "hash-script"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateAPIToken(ctx, bob.ID, "Bob's", "hash-ha"); err == nil {
		t.Fatal("a token hash must be unique")
	}

	tokens, err := d.ListAPITokens(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0].Name != "Home Assistant" || tokens[1].Name != "Script" {
		t.Fatalf("want Alice's two tokens oldest first, got %+v", tokens)
	}
	if n, err := d.CountAPITokens(ctx, alice.ID); err != nil || n != 2 {
		t.Fatalf("CountAPITokens = %d, %v", n, err)
	}

	// Using a token resolves its user and records the use.
	userID, err := d.UseAPIToken(ctx, "hash-ha")
	if err != nil || userID != alice.ID {
		t.Fatalf("UseAPIToken = %d, %v", userID, err)
	}
	tokens, _ = d.ListAPITokens(ctx, alice.ID)
	firstUse := tokens[0].LastUsedAt
	if firstUse == "" {
		t.Fatal("last_used_at not recorded")
	}
	// ...but not again within the minute: a poll every few seconds must not
	// write on every request.
	if _, err := d.UseAPIToken(ctx, "hash-ha"); err != nil {
		t.Fatal(err)
	}
	tokens, _ = d.ListAPITokens(ctx, alice.ID)
	if tokens[0].LastUsedAt != firstUse {
		t.Errorf("last_used_at rewritten within a minute: %q -> %q", firstUse, tokens[0].LastUsedAt)
	}
	// Once it's older than that, the next use moves it.
	if _, err := d.conn.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = '2020-01-01T00:00:00.000Z' WHERE id = ?`, ha.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UseAPIToken(ctx, "hash-ha"); err != nil {
		t.Fatal(err)
	}
	tokens, _ = d.ListAPITokens(ctx, alice.ID)
	if tokens[0].LastUsedAt == "2020-01-01T00:00:00.000Z" {
		t.Error("a stale last_used_at was not refreshed")
	}

	if _, err := d.UseAPIToken(ctx, "unknown"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown hash: got %v, want ErrNotFound", err)
	}

	// Only the owner can revoke a token.
	if err := d.DeleteAPIToken(ctx, bob.ID, ha.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking another user's token: got %v, want ErrNotFound", err)
	}
	if err := d.DeleteAPIToken(ctx, alice.ID, ha.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UseAPIToken(ctx, "hash-ha"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked token still resolves: %v", err)
	}
	if err := d.DeleteAPIToken(ctx, alice.ID, ha.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second revoke: got %v, want ErrNotFound", err)
	}

	// Deleting the account takes its tokens with it.
	if err := d.DeleteUser(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.UseAPIToken(ctx, "hash-script"); !errors.Is(err, ErrNotFound) {
		t.Errorf("token of a deleted user still resolves: %v", err)
	}
}
