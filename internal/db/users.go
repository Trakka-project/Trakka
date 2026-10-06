package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"trakka/internal/models"
)

// CreateUser inserts a new user and returns the persisted, API-facing row.
// Exactly one of passwordHash or (oidcSubject, oidcIssuer) is expected to
// be non-nil, per the users table's CHECK constraint. Returns
// ErrDuplicateEmail if the email is already registered.
//
// The very first account ever created (local or OIDC-provisioned) is made
// an admin automatically: Trakka has no separate seeding mechanism or CLI
// (it is env-var-configured only, see CLAUDE.md), so this is the only way
// an instance ever gets an initial administrator. The count-then-insert
// check runs inside a transaction so two concurrent registrations against a
// brand new database can't both see count == 0 and both become admin.
func (d *DB) CreateUser(ctx context.Context, email string, passwordHash, oidcSubject, oidcIssuer *string, displayName string) (*models.User, error) {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning user creation transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return nil, fmt.Errorf("counting users: %w", err)
	}
	isAdmin := count == 0

	res, err := tx.ExecContext(ctx,
		`INSERT INTO users (email, password_hash, oidc_subject, oidc_issuer, display_name, is_admin) VALUES (?, ?, ?, ?, ?, ?)`,
		email, passwordHash, oidcSubject, oidcIssuer, displayName, boolToInt(isAdmin))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrDuplicateEmail
		}
		return nil, fmt.Errorf("inserting user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("reading inserted user id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing user creation: %w", err)
	}
	return d.GetUser(ctx, id)
}

// userSelectColumns is the column list scanUser expects, in order: every
// public models.User field.
const userSelectColumns = `id, email, display_name, created_at, is_admin, keep_last_page, language,
		 reminder_default_offset_days, reminder_default_time, reminder_default_at_due_time,
		 vibrate_on_notification, reminders_enabled, overdue_tasks_summary_enabled, overdue_tasks_summary_time,
		 collaborator_actions_enabled, item_additions_enabled, list_sharing_enabled`

// userCredentialColumns is userSelectColumns plus the credentials
// getUserWithCredentials scans into models.UserWithCredentials.
const userCredentialColumns = userSelectColumns + `, password_hash, oidc_subject, oidc_issuer`

// scanUser scans one row of userSelectColumns into u, followed by extra
// destinations for a query that selects more columns after them.
func scanUser(row rowScanner, u *models.User, extra ...any) error {
	var isAdmin, keepLastPage, atDueTime, vibrate, reminders, overdueSummary, collaborators, additions, sharing int
	dest := append([]any{&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt, &isAdmin, &keepLastPage, &u.Language,
		&u.ReminderDefaultOffsetDays, &u.ReminderDefaultTime, &atDueTime, &vibrate,
		&reminders, &overdueSummary, &u.OverdueTasksSummaryTime,
		&collaborators, &additions, &sharing}, extra...)
	if err := row.Scan(dest...); err != nil {
		return err
	}
	u.IsAdmin = isAdmin != 0
	u.KeepLastPage = keepLastPage != 0
	u.ReminderDefaultAtDueTime = atDueTime != 0
	u.VibrateOnNotification = vibrate != 0
	u.RemindersEnabled = reminders != 0
	u.OverdueTasksSummaryEnabled = overdueSummary != 0
	u.CollaboratorActionsEnabled = collaborators != 0
	u.ItemAdditionsEnabled = additions != 0
	u.ListSharingEnabled = sharing != 0
	return nil
}

// GetUser fetches a single user's public fields by id. Returns ErrNotFound
// if no such user exists.
func (d *DB) GetUser(ctx context.Context, id int64) (*models.User, error) {
	u := &models.User{}
	err := scanUser(d.conn.QueryRowContext(ctx, `SELECT `+userSelectColumns+` FROM users WHERE id = ?`, id), u)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying user %d: %w", id, err)
	}
	return u, nil
}

// UpdateUserKeepLastPage sets whether the frontend should reopen on the
// user's last-visited dashboard tab/list (see models.User.KeepLastPage).
// Returns ErrNotFound if no such user exists.
func (d *DB) UpdateUserKeepLastPage(ctx context.Context, id int64, keepLastPage bool) (*models.User, error) {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE users SET keep_last_page = ? WHERE id = ?`, boolToInt(keepLastPage), id)
	if err != nil {
		return nil, fmt.Errorf("updating user %d keep_last_page: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reading rows affected updating user %d: %w", id, err)
	}
	if affected == 0 {
		return nil, ErrNotFound
	}
	return d.GetUser(ctx, id)
}

// UpdateUserVibrateOnNotification sets whether the user's Web Push
// notifications vibrate the device (see models.User.VibrateOnNotification).
// Returns ErrNotFound if no such user exists.
func (d *DB) UpdateUserVibrateOnNotification(ctx context.Context, id int64, vibrate bool) (*models.User, error) {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE users SET vibrate_on_notification = ? WHERE id = ?`, boolToInt(vibrate), id)
	if err != nil {
		return nil, fmt.Errorf("updating user %d vibrate_on_notification: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reading rows affected updating user %d vibrate_on_notification: %w", id, err)
	}
	if affected == 0 {
		return nil, ErrNotFound
	}
	return d.GetUser(ctx, id)
}

// UpdateUserLanguage sets the caller's own UI-language preference (see
// models.User.Language) to lang, which the caller (internal/handlers) must
// already have validated against validate.SupportedLanguages — this method
// doesn't re-validate, mirroring UpdateUserKeepLastPage's own division of
// responsibility. Returns ErrNotFound if no such user exists.
func (d *DB) UpdateUserLanguage(ctx context.Context, id int64, lang string) (*models.User, error) {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE users SET language = ? WHERE id = ?`, lang, id)
	if err != nil {
		return nil, fmt.Errorf("updating user %d language: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reading rows affected updating user %d language: %w", id, err)
	}
	if affected == 0 {
		return nil, ErrNotFound
	}
	return d.GetUser(ctx, id)
}

// UpdateUserReminderDefaults sets the caller's own default due-date-reminder
// timing (see models.User.ReminderDefaultOffsetDays/ReminderDefaultTime/
// ReminderDefaultAtDueTime), applied to any item whose own reminder is left
// as "use the default" (see internal/handlers' reminder-resolution logic in
// items.go). offsetDays and timeOfDay must already be validated by the
// caller (offsetDays >= 0, timeOfDay via internal/validate.TimeOfDay) —
// this method doesn't re-validate, mirroring UpdateUserLanguage's own
// division of responsibility. Returns ErrNotFound if no such user exists.
func (d *DB) UpdateUserReminderDefaults(ctx context.Context, id int64, offsetDays int, timeOfDay string, atDueTime bool) (*models.User, error) {
	res, err := d.conn.ExecContext(ctx,
		`UPDATE users SET reminder_default_offset_days = ?, reminder_default_time = ?, reminder_default_at_due_time = ? WHERE id = ?`,
		offsetDays, timeOfDay, boolToInt(atDueTime), id)
	if err != nil {
		return nil, fmt.Errorf("updating user %d reminder defaults: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reading rows affected updating user %d reminder defaults: %w", id, err)
	}
	if affected == 0 {
		return nil, ErrNotFound
	}
	return d.GetUser(ctx, id)
}

// NotificationPreferences is a partial update of the notification-type
// preferences on models.User: a nil field is left as it is. Each value must
// already be validated by the caller (OverdueTasksSummaryTime via
// internal/validate.TimeOfDay, non-empty).
type NotificationPreferences struct {
	RemindersEnabled           *bool
	OverdueTasksSummaryEnabled *bool
	OverdueTasksSummaryTime    *string
	CollaboratorActionsEnabled *bool
	ItemAdditionsEnabled       *bool
	ListSharingEnabled         *bool
}

// Any reports whether prefs changes anything at all.
func (prefs NotificationPreferences) Any() bool {
	return prefs.RemindersEnabled != nil || prefs.OverdueTasksSummaryEnabled != nil || prefs.OverdueTasksSummaryTime != nil ||
		prefs.CollaboratorActionsEnabled != nil || prefs.ItemAdditionsEnabled != nil || prefs.ListSharingEnabled != nil
}

// nullableBoolToInt is boolToInt for an optional value, nil staying nil
// (SQL NULL) so COALESCE keeps the stored column.
func nullableBoolToInt(b *bool) any {
	if b == nil {
		return nil
	}
	return boolToInt(*b)
}

// UpdateUserNotificationPreferences applies prefs to the user's own
// notification-type preferences (see models.User.RemindersEnabled and the
// fields after it). Returns ErrNotFound if no such user exists.
func (d *DB) UpdateUserNotificationPreferences(ctx context.Context, id int64, prefs NotificationPreferences) (*models.User, error) {
	var summaryTime any
	if prefs.OverdueTasksSummaryTime != nil {
		summaryTime = *prefs.OverdueTasksSummaryTime
	}
	res, err := d.conn.ExecContext(ctx,
		`UPDATE users SET
		   reminders_enabled = COALESCE(?, reminders_enabled),
		   overdue_tasks_summary_enabled = COALESCE(?, overdue_tasks_summary_enabled),
		   overdue_tasks_summary_time = COALESCE(?, overdue_tasks_summary_time),
		   collaborator_actions_enabled = COALESCE(?, collaborator_actions_enabled),
		   item_additions_enabled = COALESCE(?, item_additions_enabled),
		   list_sharing_enabled = COALESCE(?, list_sharing_enabled)
		 WHERE id = ?`,
		nullableBoolToInt(prefs.RemindersEnabled), nullableBoolToInt(prefs.OverdueTasksSummaryEnabled), summaryTime,
		nullableBoolToInt(prefs.CollaboratorActionsEnabled), nullableBoolToInt(prefs.ItemAdditionsEnabled),
		nullableBoolToInt(prefs.ListSharingEnabled), id)
	if err != nil {
		return nil, fmt.Errorf("updating user %d notification preferences: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reading rows affected updating user %d notification preferences: %w", id, err)
	}
	if affected == 0 {
		return nil, ErrNotFound
	}
	return d.GetUser(ctx, id)
}

// GetUserByEmail fetches a user (with credentials, for authentication) by
// email, compared case-insensitively. Returns ErrNotFound if no such user
// exists.
func (d *DB) GetUserByEmail(ctx context.Context, email string) (*models.UserWithCredentials, error) {
	return d.getUserWithCredentials(ctx, `SELECT `+userCredentialColumns+` FROM users WHERE email = ?`, email)
}

// GetUserByOIDCSubject fetches a user (with credentials) by their OIDC
// identity (issuer + subject). Returns ErrNotFound if no such user exists.
func (d *DB) GetUserByOIDCSubject(ctx context.Context, issuer, subject string) (*models.UserWithCredentials, error) {
	return d.getUserWithCredentials(ctx, `SELECT `+userCredentialColumns+` FROM users WHERE oidc_issuer = ? AND oidc_subject = ?`, issuer, subject)
}

// ListAllUsers returns every account on the instance, ordered by id — for
// the admin "Utilisateurs" panel (GET /api/v1/admin/users), the one place
// in the app that needs to see every user rather than the caller's own
// profile. Deliberately not scoped or paginated: Trakka targets small,
// self-hosted households/groups, not a multi-tenant SaaS with thousands of
// accounts, so a single unpaginated query stays proportionate.
func (d *DB) ListAllUsers(ctx context.Context) ([]*models.User, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+userSelectColumns+` FROM users ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying all users: %w", err)
	}
	defer rows.Close()

	users := []*models.User{}
	for rows.Next() {
		u := &models.User{}
		if err := scanUser(rows, u); err != nil {
			return nil, fmt.Errorf("scanning user row: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating user rows: %w", err)
	}
	return users, nil
}

// CountAdmins returns how many accounts currently have is_admin = 1 — used
// by the admin users handlers to refuse demoting or deleting the very last
// admin, which (per CreateUser's own doc comment) would permanently lock
// every future admin action out of the instance, since there is no separate
// seeding mechanism or CLI to grant the role back.
func (d *DB) CountAdmins(ctx context.Context) (int, error) {
	var count int
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE is_admin = 1`).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting admins: %w", err)
	}
	return count, nil
}

// CountOIDCAdmins returns how many admins are linked to an identity from the
// given OIDC issuer, i.e. could still sign in with local login disabled.
func (d *DB) CountOIDCAdmins(ctx context.Context, issuer string) (int, error) {
	var count int
	if err := d.conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE is_admin = 1 AND oidc_subject IS NOT NULL AND oidc_subject != '' AND oidc_issuer = ?`,
		issuer,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting oidc admins: %w", err)
	}
	return count, nil
}

// SetUserAdmin grants or revokes the system-wide admin role for a single
// account. Callers (internal/handlers) are responsible for refusing to
// demote the last remaining admin — this method just performs the write.
// Returns ErrNotFound if no such user exists.
func (d *DB) SetUserAdmin(ctx context.Context, id int64, isAdmin bool) (*models.User, error) {
	res, err := d.conn.ExecContext(ctx, `UPDATE users SET is_admin = ? WHERE id = ?`, boolToInt(isAdmin), id)
	if err != nil {
		return nil, fmt.Errorf("updating user %d is_admin: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("reading rows affected updating user %d is_admin: %w", id, err)
	}
	if affected == 0 {
		return nil, ErrNotFound
	}
	return d.GetUser(ctx, id)
}

// DeleteUser permanently removes an account. Every table referencing
// users(id) does so with ON DELETE CASCADE (sessions, house_members,
// custom_categories, list_shares/space_shares as the recipient,
// space_house_pins, push_subscriptions, pending_invitations as the
// inviter — see internal/db/migrations), so this single DELETE is enough to
// clean up everything the account owned or was granted; a house left
// without any remaining member becomes an orphaned row, the same
// pre-existing, harmless situation ensureDefaultHouse's seed row already is
// (see CLAUDE.md's Houses section) — not a new problem this introduces.
// Callers (internal/handlers) are responsible for refusing to delete the
// caller's own account or the last remaining admin. Returns ErrNotFound if
// no such user exists.
func (d *DB) DeleteUser(ctx context.Context, id int64) error {
	res, err := d.conn.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting user %d: %w", id, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("reading rows affected deleting user %d: %w", id, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// getUserWithCredentials runs query, which selects userCredentialColumns,
// and scans its one row.
func (d *DB) getUserWithCredentials(ctx context.Context, query string, args ...any) (*models.UserWithCredentials, error) {
	u := &models.UserWithCredentials{}
	err := scanUser(d.conn.QueryRowContext(ctx, query, args...), &u.User, &u.PasswordHash, &u.OIDCSubject, &u.OIDCIssuer)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("querying user: %w", err)
	}
	return u, nil
}
