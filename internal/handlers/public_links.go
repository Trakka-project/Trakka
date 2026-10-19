package handlers

import (
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"trakka/internal/auth"
	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/validate"
)

// Public share links (docs/API.md#public-share-links): a House member can
// make secret links, /public/lists/<token>, that open a list to anyone
// holding one, with no Trakka account, in one of four access modes
// (publicModeAllows): view only, view and add, add without seeing the list
// (a wish list or idea box), or view, add, check, uncheck and edit items. A
// list can have several links, one per audience, each with its own mode,
// optional label and expiry, revoked on its own. They are managed behind
// the session gate (GET/POST /api/v1/lists/{id}/public-links, DELETE
// .../public-links/{linkId}); the visitor's page talks to the three
// /api/v1/public/lists/{token}... routes, which sit outside RequireSession
// next to the calendar feed, the token in the path being the credential.
//
// The token is 32 crypto/rand bytes. Unlike a session, calendar feed or API
// token it is stored as is, not hashed: the House's members can show and
// copy a link from the share modal at any time, which is what the owner
// chose over a link shown once (a database leak or backup then hands out
// working public links — never more than they already open). A leaked link
// is revoked on its own; the list's other links keep working. A disabled,
// expired, revoked or unknown token is a 404 — all four look the same from
// outside — and a request the link's mode doesn't allow is a 403, checked
// before anything about the list or the item is read. Managing links takes
// House membership (authorizeHouseAccess), like sharing the list with a
// person: a share recipient can't open the list to the whole internet.
//
// What a visitor can do, even in full_edit, is narrower than a write share: no
// delete, no url (so an anonymous visitor can never make this server fetch
// a page — see scrape.go), no price, reminder, label or assignee. They see
// only what the list needs to be used: its name and icon, and each item's
// title, quantity, done state, urgency, labels, aisle and due date — never
// the House, its members or the assignees. Visitors' writes are rate
// limited per link, and stop adding once the list holds
// maxPublicListItems items. Logging redacts the token from every path it
// records (logPath), Referrer-Policy: no-referrer keeps it out of the
// Referer header of any link the page opens, and X-Robots-Tag keeps the
// page and its API out of search engines.

// publicLinkTokenBytes is the token's entropy, the same as a session's.
const publicLinkTokenBytes = 32

// maxPublicLinkTokenLength rejects absurd path segments before hashing; a
// real token is 43 characters.
const maxPublicLinkTokenLength = 128

// maxPublicListItems is how many items a list may hold for a visitor of its
// public link to still add one: a link that allows adding, posted somewhere
// public, must not let anyone grow the list (and the database) without
// bound.
const maxPublicListItems = 500

// publicWriteRateWin/publicWriteRateLimit cap visitors' writes per link,
// whoever and wherever they are: plenty for a few people checking items off
// in a shop, too few to flood a list or its members' notifications.
const (
	publicWriteRateWin   = time.Minute
	publicWriteRateLimit = 60
)

// publicRobotsTag keeps public link pages and their API out of search
// engines, should a link ever be posted somewhere a crawler reads.
const publicRobotsTag = "noindex, nofollow, noarchive"

// publicLinkPathPrefix is where a list's public page lives; the token
// follows it.
const publicLinkPathPrefix = "/public/lists/"

// The access modes of a public link (list_public_links.access_mode).
const (
	publicModeRead        = "read"
	publicModeAddOnly     = "add_only"
	publicModeDepositOnly = "deposit_only"
	publicModeFullEdit    = "full_edit"
)

// publicAction is what a visitor's request does, checked against the link's
// mode by resolvePublicLink.
type publicAction int

const (
	// publicOpen opens the page: the list's name and icon, which every mode
	// shows. Whether its items come along is publicView's call.
	publicOpen publicAction = iota
	// publicView reads the list's items.
	publicView
	// publicAdd adds an item.
	publicAdd
	// publicEdit checks, unchecks, renames or changes the quantity of an
	// existing item.
	publicEdit
)

// publicModeAllows is, for each access mode, what its visitors may do
// besides opening the page. deposit_only can add without seeing what's
// there; no mode deletes.
var publicModeAllows = map[string]map[publicAction]bool{
	publicModeRead:        {publicView: true},
	publicModeAddOnly:     {publicView: true, publicAdd: true},
	publicModeDepositOnly: {publicAdd: true},
	publicModeFullEdit:    {publicView: true, publicAdd: true, publicEdit: true},
}

// publicLinkAllows reports whether a link in mode may do action.
func publicLinkAllows(mode string, action publicAction) bool {
	return action == publicOpen || publicModeAllows[mode][action]
}

// maxPublicLinksPerList caps how many public links a list may have: one per
// audience is the use, not one per visitor.
const maxPublicLinksPerList = 20

// maxPublicLinkLabelLength is the longest label a link may have, in
// characters (the same bound as migration 38's CHECK).
const maxPublicLinkLabelLength = 50

// publicLinkResponse is one link in GET/POST
// /api/v1/lists/{id}/public-links: its settings, and the link itself (token
// and path), so the share modal can always copy it.
type publicLinkResponse struct {
	ID         int64   `json:"id"`
	Label      *string `json:"label"`
	AccessMode string  `json:"access_mode"`
	IsEnabled  bool    `json:"is_enabled"`
	Expired    bool    `json:"expired"`
	CreatedAt  string  `json:"created_at"`
	ExpiresAt  *string `json:"expires_at"`
	LastUsedAt *string `json:"last_used_at,omitempty"`
	Token      string  `json:"token"`
	Path       string  `json:"path"`
}

func newPublicLinkResponse(l *db.ListPublicLink, now time.Time) publicLinkResponse {
	resp := publicLinkResponse{
		ID:         l.ID,
		AccessMode: l.AccessMode,
		IsEnabled:  l.Enabled,
		Expired:    publicLinkExpired(l, now),
		CreatedAt:  l.CreatedAt,
		Token:      l.Token,
		Path:       publicLinkPathPrefix + l.Token,
	}
	if l.Label != "" {
		resp.Label = &l.Label
	}
	if l.ExpiresAt != "" {
		resp.ExpiresAt = &l.ExpiresAt
	}
	if l.LastUsedAt != "" {
		resp.LastUsedAt = &l.LastUsedAt
	}
	return resp
}

// publicLinkExpired reports whether l's expiry date has passed. An
// unparseable stored date counts as expired: failing closed.
func publicLinkExpired(l *db.ListPublicLink, now time.Time) bool {
	if l.ExpiresAt == "" {
		return false
	}
	expires, err := time.Parse(time.RFC3339, l.ExpiresAt)
	return err != nil || !now.Before(expires)
}

// validPublicLinkToken reports whether token can be a public link token at
// all: base64url (auth.RandomToken's alphabet), of a sane length.
func validPublicLinkToken(token string) bool {
	if token == "" || len(token) > maxPublicLinkTokenLength {
		return false
	}
	for _, c := range token {
		ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

// publicWriteLimiter is the per-link visitor write bucket, lazily built like
// the other limiters (see Application).
func (app *Application) publicWriteLimiter() *rateLimiter {
	app.publicWriteLimiterOnce.Do(func() { app.publicWriteLimiterVal = newRateLimiter(publicWriteRateWin) })
	return app.publicWriteLimiterVal
}

// loadListForPublicLink reads {id} and checks the caller may manage that
// list's public link: a real House member of it (see the file comment).
func (app *Application) loadListForPublicLink(w http.ResponseWriter, r *http.Request) (*models.List, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	list, err := app.DB.GetList(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return nil, false
	} else if err != nil {
		app.serverError(w, r, err)
		return nil, false
	}
	if !app.authorizeHouseAccess(w, r, list.HouseID) {
		return nil, false
	}
	return list, true
}

// handleListPublicLinksIndex answers GET /api/v1/lists/{id}/public-links:
// every public link of the list, oldest first — expired and suspended ones
// included (flagged), so they can be seen and revoked.
func (app *Application) handleListPublicLinksIndex(w http.ResponseWriter, r *http.Request) {
	list, ok := app.loadListForPublicLink(w, r)
	if !ok {
		return
	}
	links, err := app.DB.ListListPublicLinks(r.Context(), list.ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	now := time.Now()
	resp := make([]publicLinkResponse, 0, len(links))
	for _, l := range links {
		resp = append(resp, newPublicLinkResponse(l, now))
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleListPublicLinksCreate answers POST /api/v1/lists/{id}/public-links:
// a new link next to the list's others — access_mode (required), label
// (optional, at most 50 characters) and expires_at (absent, null or "" for
// never). 409 once the list has maxPublicLinksPerList links.
func (app *Application) handleListPublicLinksCreate(w http.ResponseWriter, r *http.Request) {
	list, ok := app.loadListForPublicLink(w, r)
	if !ok {
		return
	}
	var in struct {
		AccessMode string  `json:"access_mode"`
		Label      *string `json:"label"`
		ExpiresAt  *string `json:"expires_at"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if _, ok := publicModeAllows[in.AccessMode]; !ok {
		writeError(w, http.StatusBadRequest, `access_mode must be "read", "add_only", "deposit_only" or "full_edit"`)
		return
	}
	var label *string
	if in.Label != nil {
		cleaned := validate.Text(*in.Label)
		if !validate.MaxLen(cleaned, maxPublicLinkLabelLength) {
			writeError(w, http.StatusBadRequest, "label is too long (50 characters at most)")
			return
		}
		label = nullableString(cleaned)
	}
	now := time.Now()
	var expiresAt *string
	if in.ExpiresAt != nil && strings.TrimSpace(*in.ExpiresAt) != "" {
		expires, err := time.Parse(time.RFC3339, strings.TrimSpace(*in.ExpiresAt))
		if err != nil {
			writeError(w, http.StatusBadRequest, "expires_at must be an RFC 3339 date-time")
			return
		}
		if !expires.After(now) {
			writeError(w, http.StatusBadRequest, "expires_at must be in the future")
			return
		}
		formatted := expires.UTC().Format(time.RFC3339)
		expiresAt = &formatted
	}

	count, err := app.DB.CountListPublicLinks(r.Context(), list.ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if count >= maxPublicLinksPerList {
		writeError(w, http.StatusConflict, "this list already has the maximum number of public links (20)")
		return
	}
	token, err := auth.RandomToken(publicLinkTokenBytes)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	link, err := app.DB.CreateListPublicLink(r.Context(), list.ID, token, label, in.AccessMode, expiresAt, userFromContext(r).ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newPublicLinkResponse(link, now))
}

// handleListPublicLinksDelete answers DELETE
// /api/v1/lists/{id}/public-links/{linkId}: that link stops working for
// good; the list's other links keep working. 404 for a link the list
// doesn't have (another list's included).
func (app *Application) handleListPublicLinksDelete(w http.ResponseWriter, r *http.Request) {
	list, ok := app.loadListForPublicLink(w, r)
	if !ok {
		return
	}
	linkID, err := strconv.ParseInt(r.PathValue("linkId"), 10, 64)
	if err != nil || linkID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid link id")
		return
	}
	if err := app.DB.DeleteListPublicLink(r.Context(), list.ID, linkID); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "public link not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// resolvePublicLink authenticates a request to /api/v1/public/lists/{token}
// by its token, writing the error response and returning false when the
// link can't open the list — 404 unknown, disabled or expired, 403 when the
// link's mode doesn't allow action, 429 past the link's write budget (adds
// and edits).
func (app *Application) resolvePublicLink(w http.ResponseWriter, r *http.Request, action publicAction) (*db.ListPublicLink, *models.List, bool) {
	w.Header().Set("X-Robots-Tag", publicRobotsTag)
	token := r.PathValue("token")
	if !validPublicLinkToken(token) {
		writeError(w, http.StatusNotFound, "shared list not found")
		return nil, nil, false
	}
	link, err := app.DB.UseListPublicLink(r.Context(), token)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "shared list not found")
		return nil, nil, false
	}
	if err != nil {
		app.serverError(w, r, err)
		return nil, nil, false
	}
	if !link.Enabled || publicLinkExpired(link, time.Now()) {
		writeError(w, http.StatusNotFound, "shared list not found")
		return nil, nil, false
	}
	list, err := app.DB.GetList(r.Context(), link.ListID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "shared list not found")
		return nil, nil, false
	}
	if err != nil {
		app.serverError(w, r, err)
		return nil, nil, false
	}
	if !publicLinkAllows(link.AccessMode, action) {
		writeError(w, http.StatusForbidden, publicActionRefusal[action])
		return nil, nil, false
	}
	if action == publicAdd || action == publicEdit {
		if !app.publicWriteLimiter().allow(strconv.FormatInt(link.ID, 10), publicWriteRateLimit) {
			writeError(w, http.StatusTooManyRequests, "too many changes through this link, try again in a minute")
			return nil, nil, false
		}
	}
	return link, list, true
}

// publicActionRefusal is the 403 message for an action a link's mode
// doesn't allow.
var publicActionRefusal = map[publicAction]string{
	publicView: "this link doesn't show the list's items",
	publicAdd:  "this link doesn't allow adding items",
	publicEdit: "this link doesn't allow changing items",
}

// publicList is the shape of GET /api/v1/public/lists/{token}.
type publicList struct {
	Name       string        `json:"name"`
	Icon       string        `json:"icon,omitempty"`
	Type       string        `json:"type"`
	AccessMode string        `json:"access_mode"`
	ExpiresAt  *string       `json:"expires_at"`
	Items      []*publicItem `json:"items"`
}

// publicItem is the part of an item a public link shows (see the file
// comment for why not the whole models.Item).
type publicItem struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	Quantity int      `json:"quantity"`
	Done     bool     `json:"done"`
	IsUrgent bool     `json:"is_urgent"`
	Labels   []string `json:"labels"`
	Aisle    *string  `json:"aisle,omitempty"`
	DueDate  *string  `json:"due_date,omitempty"`
	DueTime  *string  `json:"due_time,omitempty"`
}

func newPublicItem(item *models.Item) *publicItem {
	labels := item.Labels
	if labels == nil {
		labels = []string{}
	}
	return &publicItem{
		ID:       item.ID,
		Title:    item.Title,
		Quantity: item.Quantity,
		Done:     item.Done,
		IsUrgent: item.IsUrgent,
		Labels:   labels,
		Aisle:    item.Aisle,
		DueDate:  item.DueDate,
		DueTime:  item.DueTime,
	}
}

// handlePublicListShow answers GET /api/v1/public/lists/{token}: the list
// and its items, as a visitor of its public link sees them — for a
// deposit_only link, the list's name and icon with no items at all (the
// items aren't even read).
func (app *Application) handlePublicListShow(w http.ResponseWriter, r *http.Request) {
	link, list, ok := app.resolvePublicLink(w, r, publicOpen)
	if !ok {
		return
	}
	var items []*models.Item
	if publicLinkAllows(link.AccessMode, publicView) {
		var err error
		items, err = app.DB.ListItemsByList(r.Context(), list.ID)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
	}
	resp := publicList{Name: list.Name, Icon: list.Icon, Type: list.Type, AccessMode: link.AccessMode, Items: make([]*publicItem, 0, len(items))}
	if link.ExpiresAt != "" {
		resp.ExpiresAt = &link.ExpiresAt
	}
	for _, item := range items {
		resp.Items = append(resp.Items, newPublicItem(item))
	}
	writeJSON(w, http.StatusOK, resp)
}

// publicDeposit is what a deposit_only visitor gets back for an addition:
// that it was taken, never the item — which, on a Courses list, may be an
// existing one updated, whose state the visitor isn't shown.
type publicDeposit struct {
	Added bool `json:"added"`
}

// handlePublicItemCreate answers POST /api/v1/public/lists/{token}/items: a
// visitor of an add_only, deposit_only or full_edit link adds an item — a
// title and a quantity, nothing more. On a Courses list, adding a product
// already there updates that item instead (200, like an authenticated add —
// see addToQuickShoppingList). A deposit_only visitor always gets 201 and
// publicDeposit, merged or not, so the reply can't tell what the list holds.
func (app *Application) handlePublicItemCreate(w http.ResponseWriter, r *http.Request) {
	link, list, ok := app.resolvePublicLink(w, r, publicAdd)
	if !ok {
		return
	}
	var in struct {
		Title    string `json:"title"`
		Quantity int    `json:"quantity"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Title = validate.Text(in.Title)
	if in.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if !validate.MaxLen(in.Title, validate.MaxTitleLen) {
		writeError(w, http.StatusBadRequest, "title is too long")
		return
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	if in.Quantity > maxItemQuantity {
		writeError(w, http.StatusBadRequest, "quantity is too large")
		return
	}
	count, err := app.DB.CountListItems(r.Context(), list.ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if count >= maxPublicListItems {
		writeError(w, http.StatusConflict, "this list is full")
		return
	}

	create := func() (*models.Item, error) {
		return app.DB.CreateItem(r.Context(), list.ID, in.Title, nil, in.Quantity, nil, false, 0,
			nil, nil, nil, nil, false, nil, nil, false)
	}
	var item *models.Item
	if isQuickShoppingList(list) {
		aisle := app.resolveItemAisle(r.Context(), list, in.Title, "", "")
		var merged bool
		item, merged, err = app.addToQuickShoppingList(r.Context(), list, in.Title, in.Quantity, aisle, false, false, create)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
		app.recordProductAdded(r.Context(), list, item, false, "")
		if merged {
			app.notifyListChange(list, nil, item.Title, itemAdded)
			app.writePublicAddition(w, link, http.StatusOK, item)
			return
		}
	} else {
		item, err = create()
		if err != nil {
			app.serverError(w, r, err)
			return
		}
	}
	// What an authenticated create that says nothing about reminders writes
	// (see handleItemsCreate): CreateItem's column default would otherwise
	// leave the reminder on.
	item, err = app.DB.SetItemReminder(r.Context(), item.ID, false, nil, nil, false)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.notifyListChange(list, nil, item.Title, itemAdded)
	app.writePublicAddition(w, link, http.StatusCreated, item)
}

// writePublicAddition answers a visitor's addition: the item, or only
// publicDeposit (always 201) when the link doesn't show the list.
func (app *Application) writePublicAddition(w http.ResponseWriter, link *db.ListPublicLink, status int, item *models.Item) {
	if !publicLinkAllows(link.AccessMode, publicView) {
		writeJSON(w, http.StatusCreated, publicDeposit{Added: true})
		return
	}
	writeJSON(w, status, newPublicItem(item))
}

// handlePublicItemPatch answers PATCH
// /api/v1/public/lists/{token}/items/{itemId}: a visitor of a full_edit link
// checks or unchecks an item, or changes its title or quantity. Checking an
// item off has the same effects as for a member (handleItemsPatch): a
// recurring item moves to its next occurrence, a Courses purchase is
// counted, the list's users are notified.
func (app *Application) handlePublicItemPatch(w http.ResponseWriter, r *http.Request) {
	_, list, ok := app.resolvePublicLink(w, r, publicEdit)
	if !ok {
		return
	}
	itemID, err := strconv.ParseInt(r.PathValue("itemId"), 10, 64)
	if err != nil || itemID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid item id")
		return
	}
	var in struct {
		Title    *string `json:"title"`
		Quantity *int    `json:"quantity"`
		Done     *bool   `json:"done"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Title == nil && in.Quantity == nil && in.Done == nil {
		writeError(w, http.StatusBadRequest, "nothing to update: send title, quantity or done")
		return
	}

	item, err := app.DB.GetItem(r.Context(), itemID)
	// Another list's item is as unknown here as one that doesn't exist.
	if errors.Is(err, db.ErrNotFound) || (err == nil && item.ListID != list.ID) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	before := *item

	if in.Title != nil {
		title := validate.Text(*in.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, "title cannot be empty")
			return
		}
		if !validate.MaxLen(title, validate.MaxTitleLen) {
			writeError(w, http.StatusBadRequest, "title is too long")
			return
		}
		item.Title = title
	}
	if in.Quantity != nil {
		if *in.Quantity <= 0 {
			writeError(w, http.StatusBadRequest, "quantity must be positive")
			return
		}
		if *in.Quantity > maxItemQuantity {
			writeError(w, http.StatusBadRequest, "quantity is too large")
			return
		}
		item.Quantity = *in.Quantity
	}
	if in.Done != nil {
		item.Done = *in.Done
	}
	item.IsPlanned, item.ActualPrice = purchaseColumns(item.Done, item.IsPlanned, item.ActualPrice)

	justCompleted := !before.Done && item.Done
	justUnchecked := before.Done && !item.Done
	applyRecurrenceLifecycle(item, &before, app.today())

	updated, err := app.DB.UpdateItem(r.Context(), item.ID, item.Title, item.URL, item.Quantity, item.Price, item.PriceAuto, item.ImageURL, item.Done, item.Position,
		item.TargetMonth, item.DueDate, item.RecurrenceRule, item.RecurrenceEndDate, item.IsUrgent, item.RecurrenceLeadMinutes,
		item.TargetPrice, item.AlertOnPriceDrop)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	// The same "only write when something changed" gates as handleItemsPatch.
	if stringValue(item.DueTime) != stringValue(before.DueTime) || stringValue(item.NextDueDate) != stringValue(before.NextDueDate) {
		updated, err = app.DB.SetItemSchedule(r.Context(), updated.ID, item.DueTime, item.NextDueDate)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
	}
	if item.IsPlanned != before.IsPlanned || !sameFloat(item.ActualPrice, before.ActualPrice) {
		updated, err = app.DB.SetItemPurchase(r.Context(), updated.ID, item.IsPlanned, item.ActualPrice)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
	}
	app.updateProductCatalog(r.Context(), updated, false, justCompleted)
	if justCompleted || justUnchecked {
		app.notifyListChange(list, nil, updated.Title, doneChange(justCompleted))
	}
	writeJSON(w, http.StatusOK, newPublicItem(updated))
}

// handlePublicListPage answers GET /public/lists/{token}: the visitor's
// page (static/public-list.html), which reads the token from its own URL
// and talks to the routes above. The page is the same for every token, and
// says so itself when the API answers 404 — but never cached under its
// token-bearing URL, nor indexed.
func (app *Application) handlePublicListPage(w http.ResponseWriter, r *http.Request) {
	if !validPublicLinkToken(r.PathValue("token")) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Robots-Tag", publicRobotsTag)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Join(app.StaticDir, "public-list.html"))
}

// logPath is r's path as the logs record it: the token of a public link
// path replaced by "{token}", since the token is the link's only credential.
func logPath(r *http.Request) string {
	path := r.URL.Path
	for _, prefix := range []string{"/api/v1/public/lists/", publicLinkPathPrefix} {
		rest, ok := strings.CutPrefix(path, prefix)
		if !ok {
			continue
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			return prefix + "{token}" + rest[i:]
		}
		return prefix + "{token}"
	}
	return path
}
