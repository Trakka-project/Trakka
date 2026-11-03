package fluv

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// What a purchase may name in a user's Fluv — their accounts and expense
// categories — read from Fluv's GET …/integrations/trakka/metadata with the
// link token, so that the list modal and the purchase sheet offer menus
// rather than ids to copy (docs/FLUV.md). Like every exchange with Fluv, it
// only goes to the pinned addresses, never resolving the host name; the
// token never leaves the server. What Fluv answers is checked and cut down
// before the page sees it: ids of Fluv's shape only, names without control
// or bidi characters, bounded lengths and counts.

// metadataPath is the metadata endpoint, next to purchasesPath on the same
// Fluv.
const metadataPath = "/api/v1/integrations/trakka/metadata"

const (
	// metadataTTL is how long an answer is reused, and metadataFailureTTL
	// a failure: opening the list modal or the purchase sheet again doesn't
	// ask Fluv each time (whose Trakka routes are rate limited).
	metadataTTL        = 2 * time.Minute
	metadataFailureTTL = 20 * time.Second
	// maxMetadataBody bounds the answer read: names and ids only.
	maxMetadataBody       = 256 << 10
	maxMetadataAccounts   = 200
	maxMetadataCategories = 500
	maxMetadataName       = 80 // runes
	maxMetadataEmoji      = 8  // runes: a ZWJ sequence with its selectors
)

// MetadataAccount is one of the user's Fluv accounts a purchase may go to
// (Fluv leaves loans out).
type MetadataAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind is Fluv's kind of account (checking, cash…), "" when unknown.
	Kind string `json:"kind,omitempty"`
}

// MetadataCategory is one of the user's Fluv expense categories.
type MetadataCategory struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Emoji string `json:"emoji,omitempty"`
}

// Metadata is what GET /api/v1/integrations/fluv/metadata answers. Fluv
// failing — unreachable, an older Fluv without the endpoint (404), a token
// refused — is OK false with a code, like TestResult: the page then keeps
// its text fields.
type Metadata struct {
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Code   string `json:"code,omitempty"`
	// DefaultAccountID is the account linked to Trakka, where a purchase
	// that names none goes.
	DefaultAccountID string             `json:"default_account_id,omitempty"`
	Accounts         []MetadataAccount  `json:"accounts"`
	Categories       []MetadataCategory `json:"categories"`
}

// fluvMetadata is Fluv's answer, as handleTrakkaMetadata writes it.
type fluvMetadata struct {
	DefaultAccountID string             `json:"default_account_id"`
	Accounts         []MetadataAccount  `json:"accounts"`
	Categories       []MetadataCategory `json:"categories"`
}

type metadataKey struct {
	userID int64
	lang   string
}

type metadataEntry struct {
	m       Metadata
	expires time.Time
}

// metadataLang is the language category names are asked in: Trakka's
// interface languages only, French by default.
func metadataLang(lang string) string {
	if strings.EqualFold(strings.TrimSpace(lang), "en") {
		return "en"
	}
	return "fr"
}

// metadataURL is the metadata endpoint of the Fluv whose purchase endpoint
// is endpoint (parseEndpoint made sure it ends with purchasesPath).
func metadataURL(endpoint *url.URL) string {
	u := *endpoint
	u.Path = strings.TrimSuffix(u.Path, purchasesPath) + metadataPath
	return u.String()
}

// Metadata returns userID's Fluv accounts and expense categories, category
// names in lang ("fr" or "en"), from a cache of a couple of minutes.
// Returns db.ErrNotFound if there is no connection.
func (s *Service) Metadata(ctx context.Context, userID int64, lang string) (Metadata, error) {
	key := metadataKey{userID: userID, lang: metadataLang(lang)}
	if m, ok := s.cachedMetadata(key); ok {
		return m, nil
	}
	integ, err := s.db.GetFluvIntegration(ctx, userID)
	if err != nil {
		return Metadata{}, err
	}
	m := Metadata{Accounts: []MetadataAccount{}, Categories: []MetadataCategory{}}
	t, token, code := s.prepare(integ)
	if code != "" {
		m.Code = code
	} else {
		var raw fluvMetadata
		resp, err := getJSON(ctx, newClient(t, s.policy), metadataURL(t.endpoint), token, key.lang, maxMetadataBody, &raw)
		switch {
		case err != nil:
			m.Code = networkErrorCode(err)
		case resp.status != http.StatusOK:
			m.Status, m.Code = resp.status, statusCode(resp)
		case resp.code != "":
			m.Status, m.Code = resp.status, resp.code
		default:
			m = raw.clean()
		}
	}
	if !m.OK {
		s.logger.Warn("Fluv metadata not read", "user_id", userID, "status", m.Status, "code", m.Code)
	}
	// A request the caller gave up on says nothing about Fluv.
	if ctx.Err() == nil || m.OK {
		s.storeMetadata(key, m)
	}
	return m, nil
}

func (s *Service) cachedMetadata(key metadataKey) (Metadata, bool) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	e, ok := s.metaCache[key]
	if !ok || !s.now().Before(e.expires) {
		return Metadata{}, false
	}
	return e.m, true
}

func (s *Service) storeMetadata(key metadataKey, m Metadata) {
	now := s.now()
	ttl := metadataTTL
	if !m.OK {
		ttl = metadataFailureTTL
	}
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	for k, e := range s.metaCache {
		if !now.Before(e.expires) {
			delete(s.metaCache, k)
		}
	}
	s.metaCache[key] = metadataEntry{m: m, expires: now.Add(ttl)}
}

// forgetMetadata drops userID's cached metadata: their connection changed.
func (s *Service) forgetMetadata(userID int64) {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	for k := range s.metaCache {
		if k.userID == userID {
			delete(s.metaCache, k)
		}
	}
}

// clean keeps what Fluv answered that fits, dropping the rest rather than
// refusing the whole answer: well-formed, distinct ids, bounded names, at most maxMetadataAccounts and maxMetadataCategories.
func (raw fluvMetadata) clean() Metadata {
	m := Metadata{OK: true, Accounts: []MetadataAccount{}, Categories: []MetadataCategory{}}
	if id := strings.ToLower(raw.DefaultAccountID); validFluvID(id) {
		m.DefaultAccountID = id
	}
	seen := map[string]bool{}
	for _, a := range raw.Accounts {
		id := strings.ToLower(a.ID)
		if len(m.Accounts) == maxMetadataAccounts || !validFluvID(id) || seen[id] {
			continue
		}
		seen[id] = true
		m.Accounts = append(m.Accounts, MetadataAccount{ID: id, Name: cleanLabel(a.Name, maxMetadataName), Kind: cleanCode(a.Kind)})
	}
	for _, c := range raw.Categories {
		id := strings.ToLower(c.ID)
		if len(m.Categories) == maxMetadataCategories || !validFluvID(id) || seen[id] {
			continue
		}
		seen[id] = true
		m.Categories = append(m.Categories, MetadataCategory{
			ID: id, Name: cleanLabel(c.Name, maxMetadataName), Emoji: cleanLabel(c.Emoji, maxMetadataEmoji),
		})
	}
	return m
}

// cleanLabel trims s to at most max runes, without control characters,
// invalid UTF-8 or the bidi controls that could make a name read as
// something else.
func cleanLabel(s string, max int) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if n == max {
			break
		}
		if r == utf8.RuneError || unicode.IsControl(r) || isBidiControl(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}

func isBidiControl(r rune) bool {
	return r == '\u061c' || r == '\u200e' || r == '\u200f' || (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069')
}
