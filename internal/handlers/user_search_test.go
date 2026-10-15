package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"trakka/internal/models"
)

// mustCreateNamedUser is mustCreateTestUser with a display name distinct
// from the email address, which is what the name search needs to prove
// anything.
func mustCreateNamedUser(t *testing.T, app *Application, email, displayName string) *models.User {
	t.Helper()
	hash := "x"
	user, err := app.DB.CreateUser(context.Background(), email, &hash, nil, nil, displayName)
	if err != nil {
		t.Fatalf("creating user %s: %v", email, err)
	}
	return user
}

// searchUsers calls GET /api/v1/users/search as caller and returns the
// status and the raw body.
func searchUsers(t *testing.T, app *Application, caller *models.User, q string) (int, string) {
	t.Helper()
	req := withUser(httptest.NewRequest(http.MethodGet, "/api/v1/users/search?q="+url.QueryEscape(q), nil), caller)
	rec := httptest.NewRecorder()
	app.handleUsersSearch(rec, req)
	return rec.Code, rec.Body.String()
}

// searchEmails runs a search expected to succeed and returns the matched
// email addresses, sorted.
func searchEmails(t *testing.T, app *Application, caller *models.User, q string) []string {
	t.Helper()
	code, body := searchUsers(t, app, caller, q)
	if code != http.StatusOK {
		t.Fatalf("search %q: got %d, want 200: %s", q, code, body)
	}
	var results []models.UserSearchResult
	if err := json.Unmarshal([]byte(body), &results); err != nil {
		t.Fatalf("search %q: decoding %s: %v", q, body, err)
	}
	emails := make([]string, 0, len(results))
	for _, r := range results {
		emails = append(emails, r.Email)
	}
	sort.Strings(emails)
	return emails
}

func assertEmails(t *testing.T, q string, got []string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("search %q = %v, want %v", q, got, want)
	}
}

// userSearchFixture builds an instance where caller shares a house with
// jean, has shared a list with marie, and is a stranger to paul.
func userSearchFixture(t *testing.T, app *Application) (caller, jean, marie, paul *models.User) {
	t.Helper()
	ctx := context.Background()
	caller = mustCreateNamedUser(t, app, "alice@example.com", "Alice Martin")
	jean = mustCreateNamedUser(t, app, "jean.dupont@example.com", "Jean Dupont")
	marie = mustCreateNamedUser(t, app, "marie@example.org", "Marie Durand")
	paul = mustCreateNamedUser(t, app, "paul@example.net", "Paul Dupuis")

	house, err := app.DB.CreateHouseWithOwner(ctx, "Maison", caller.ID)
	if err != nil {
		t.Fatalf("creating house: %v", err)
	}
	if _, err := app.DB.AddHouseMember(ctx, house.ID, jean.ID, "member"); err != nil {
		t.Fatalf("adding jean: %v", err)
	}
	list, err := app.DB.CreateList(ctx, "Courses", "shopping", house.ID, nil, "")
	if err != nil {
		t.Fatalf("creating list: %v", err)
	}
	if _, err := app.DB.CreateOrUpdateListShare(ctx, list.ID, marie.ID, "read"); err != nil {
		t.Fatalf("sharing list with marie: %v", err)
	}
	return caller, jean, marie, paul
}

// TestUserSearchSelfHostedMatchesNamesAndEmails covers saas_mode: false:
// any account on the instance is found by first name, last name (in either
// order), or any part of the email address, case-insensitively — and the
// caller never finds themselves.
func TestUserSearchSelfHostedMatchesNamesAndEmails(t *testing.T) {
	app := newTestApplication(t)
	app.Config.SaaSMode = false
	caller, _, _, _ := userSearchFixture(t, app)

	tests := []struct {
		q    string
		want []string
	}{
		{"jean", []string{"jean.dupont@example.com"}},                    // first name
		{"DUPONT", []string{"jean.dupont@example.com"}},                  // last name, any case
		{"dupont jean", []string{"jean.dupont@example.com"}},             // "Nom Prénom"
		{"Dup", []string{"jean.dupont@example.com", "paul@example.net"}}, // prefix shared by two last names
		{"example.org", []string{"marie@example.org"}},                   // email domain
		{"paul@", []string{"paul@example.net"}},                          // email local part
		{"jean marie", nil},                                              // every term must match the same account
		{"alice", nil},                                                   // never the caller
		{"martin", nil},                                                  // never the caller, by name either
		{"%", nil},                                                       // too short
		{"%%", nil},                                                      // LIKE wildcards match literally
		{"_a", nil},                                                      // ditto
		{"j", nil},                                                       // below the minimum length
		{"   ", nil},                                                     // blank
		{"example", []string{"jean.dupont@example.com", "marie@example.org", "paul@example.net"}}, // everyone but the caller
	}
	for _, tt := range tests {
		assertEmails(t, tt.q, searchEmails(t, app, caller, tt.q), tt.want...)
	}

	// Self-hosted suggestions carry the display name for the dropdown.
	_, body := searchUsers(t, app, caller, "jean")
	if !strings.Contains(body, `"display_name":"Jean Dupont"`) {
		t.Errorf("self-hosted result should carry display_name, got %s", body)
	}
}

// TestUserSearchSaaSModeMatchesEmailOnlyAmongContacts covers saas_mode:
// true: names are never searched nor returned, and only the caller's
// existing contacts (house co-members, share recipients) can be suggested —
// a stranger stays invisible even by their exact address, so the search is
// no account-existence oracle (docs/AUDIT.md L-06).
func TestUserSearchSaaSModeMatchesEmailOnlyAmongContacts(t *testing.T) {
	app := newTestApplication(t)
	app.Config.SaaSMode = true
	caller, _, _, paul := userSearchFixture(t, app)

	tests := []struct {
		q    string
		want []string
	}{
		{"jean.dupont@", []string{"jean.dupont@example.com"}}, // house co-member, by email
		{"MARIE@EXAMPLE.ORG", []string{"marie@example.org"}},  // list-share recipient, any case
		{"example", []string{"jean.dupont@example.com", "marie@example.org"}},
		{"Dupont", []string{"jean.dupont@example.com"}}, // matches the email's "dupont", not the name
		{"Jean Dupont", nil},       // a display name is never searched
		{"Durand", nil},            // Marie's last name only appears in her name
		{"paul@example.net", nil},  // stranger: not even by exact address
		{"alice@example.com", nil}, // never the caller
	}
	for _, tt := range tests {
		assertEmails(t, tt.q, searchEmails(t, app, caller, tt.q), tt.want...)
	}

	// SaaS suggestions never carry a display name.
	_, body := searchUsers(t, app, caller, "jean")
	if strings.Contains(body, "display_name") || strings.Contains(body, "Jean Dupont") {
		t.Errorf("SaaS result must not carry display_name, got %s", body)
	}

	// Contacts work in both directions: paul becomes visible to the caller
	// once the caller is a recipient of a space paul owns, and the caller
	// becomes visible to paul.
	ctx := context.Background()
	space, err := app.DB.CreateCustomCategory(ctx, paul.ID, "Vacances", "", "", 0)
	if err != nil {
		t.Fatalf("creating paul's space: %v", err)
	}
	if _, err := app.DB.CreateOrUpdateSpaceShare(ctx, space.ID, caller.ID, "read"); err != nil {
		t.Fatalf("sharing paul's space with caller: %v", err)
	}
	assertEmails(t, "paul@", searchEmails(t, app, caller, "paul@"), "paul@example.net")
	assertEmails(t, "alice@", searchEmails(t, app, paul, "alice@"), "alice@example.com")
}

// TestUserSearchRateLimited checks the per-account bucket: past the limit
// the caller gets 429, while another account is unaffected.
func TestUserSearchRateLimited(t *testing.T) {
	app := newTestApplication(t)
	caller, jean, _, _ := userSearchFixture(t, app)

	for i := range userSearchRateLimit {
		if code, body := searchUsers(t, app, caller, "jean"); code != http.StatusOK {
			t.Fatalf("search #%d: got %d, want 200: %s", i+1, code, body)
		}
	}
	if code, _ := searchUsers(t, app, caller, "jean"); code != http.StatusTooManyRequests {
		t.Fatalf("search past the limit: got %d, want 429", code)
	}
	if code, body := searchUsers(t, app, jean, "alice"); code != http.StatusOK {
		t.Fatalf("another account's search: got %d, want 200: %s", code, body)
	}
}

func TestUserSearchRejectsOverlongQuery(t *testing.T) {
	app := newTestApplication(t)
	caller, _, _, _ := userSearchFixture(t, app)
	if code, _ := searchUsers(t, app, caller, strings.Repeat("a", userSearchMaxBytes+1)); code != http.StatusBadRequest {
		t.Fatalf("overlong query: got %d, want 400", code)
	}
}

// TestConfigExposesSaaSMode checks GET /api/v1/config reports the mode the
// frontend adapts the share field to.
func TestConfigExposesSaaSMode(t *testing.T) {
	for _, saas := range []bool{false, true} {
		app := newTestApplication(t)
		app.Config.SaaSMode = saas
		caller := mustCreateTestUser(t, app, "alice@example.com")

		rec := httptest.NewRecorder()
		app.handleConfig(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/v1/config", nil), caller))
		if rec.Code != http.StatusOK {
			t.Fatalf("saas=%v: got %d, want 200", saas, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("saas=%v: Cache-Control = %q, want no-store", saas, got)
		}
		var cfg instanceConfig
		if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
			t.Fatalf("saas=%v: decoding %s: %v", saas, rec.Body.String(), err)
		}
		if cfg.SaaSMode != saas {
			t.Errorf("saas_mode = %v, want %v", cfg.SaaSMode, saas)
		}
	}
}
