// Package models defines the data types shared between the db and handlers
// layers.
package models

// House groups related lists together (e.g. shared by the members of a
// household). Every List belongs to exactly one House.
type House struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// List represents a shopping or to-do list.
type List struct {
	ID      int64  `json:"id"`
	HouseID int64  `json:"house_id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	// CustomCategoryID optionally attaches this list to one of its owner's
	// CustomCategory "spaces" (nil means unattached, the default). Any house
	// member may associate/dissociate it via POST/PUT /api/v1/lists, but
	// the id must reference a category owned by the caller making that
	// request — see internal/handlers.handleListsCreate/handleListsUpdate.
	CustomCategoryID *int64 `json:"custom_category_id,omitempty"`
	// CustomCategory is the embedded category row for CustomCategoryID,
	// populated only by reads that join it in (GET /api/v1/lists and
	// GET /api/v1/lists/{id}) — nil whenever CustomCategoryID is nil, and
	// never itself accepted on a create/update request.
	CustomCategory *CustomCategory `json:"custom_category,omitempty"`
	// Icon is a short freeform string (typically an emoji) the frontend
	// renders next to the list's name; "" means no icon was set, in which
	// case the frontend falls back to a fixed icon for the list's Type (see
	// LIST_TYPE_BADGE_META in static/js/app.js).
	Icon      string  `json:"icon,omitempty"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	Items     []*Item `json:"items,omitempty"`
	// AccessSource is a response-only field (never persisted) populated
	// exclusively by db.ListSharedListsForUser to say how the requesting
	// user reached a list they aren't a House member of: "list_share" (a
	// direct List share) or "space_share" (via the list's parent Space
	// being shared with them). Empty on every other read (GetList,
	// ListListsForUser's ordinary House-scoped listing, ...), since those
	// aren't about a sharing relationship. The frontend uses it to show the
	// 👥 "shared with you" indicator (see static/js/shares.js).
	AccessSource string `json:"access_source,omitempty"`
	// AccessPermission mirrors AccessSource: the "read"/"write" level the
	// requesting user actually holds via that share. Always empty wherever
	// AccessSource is.
	AccessPermission string `json:"access_permission,omitempty"`
	// IsPinnedToDashboard is a response-only field, populated only by
	// db.ListSharedListsForUser (never GetList/ListListsForUser's ordinary
	// House-scoped read), reporting whether the requesting user has chosen —
	// via PATCH /api/v1/lists/{id}/share/pin, or PATCH
	// /api/v1/custom-categories/{id}/share/pin on the list's parent Space —
	// to have this shared list show up alongside their own House's lists on
	// the dashboard, rather than only in the "Partagé avec moi" tab. Always
	// false wherever AccessSource is empty. True either when a list_shares
	// row for this list is itself pinned (AccessSource "list_share", or
	// "space_share" for a list individually pinned via the auto-created
	// list_shares row SetListSharePinned creates on demand — see its
	// comment), or when AccessSource is "space_share" and the list's parent
	// Space itself is pinned as a whole (space_shares.is_pinned_to_dashboard)
	// — pinning a whole Space is what lets every list reachable through it
	// show up pinned without pinning each one individually.
	IsPinnedToDashboard bool `json:"is_pinned_to_dashboard,omitempty"`
	// TotalAmount is a response-only field (never persisted), populated by
	// every read that goes through internal/db's listSelect (GetList,
	// ListListsForUser, ListPinnedHouseSpaceLists): the sum of
	// price * quantity across every still-unchecked (done = false) item in
	// this list that has a price set — the list's "reste à dépenser", the
	// same figure the list detail view's own finance summary shows (see
	// updateFinanceSummary/lineTotal in static/js/list_view.js), just
	// computed once in SQL instead of per-card from a full item fetch (see
	// the dashboard/Espaces card's green total badge in
	// static/js/app.js's urlBadges). A done item never contributes,
	// regardless of price. nil when no unchecked item in the list has a
	// price at all, which is how the frontend decides whether to show the
	// badge in the first place; otherwise always present, including a
	// genuine 0.
	TotalAmount *float64 `json:"total_amount,omitempty"`
}

// Item represents a single entry within a List.
type Item struct {
	ID       int64    `json:"id"`
	ListID   int64    `json:"list_id"`
	Title    string   `json:"title"`
	URL      *string  `json:"url,omitempty"`
	Quantity int      `json:"quantity"`
	Price    *float64 `json:"price,omitempty"`
	// PriceAuto is true when Price was filled in automatically by
	// internal/scraper's background lookup rather than typed in by a user.
	// It is always false when Price is nil, and is reset to false the
	// moment a user sets or clears Price manually (see
	// internal/handlers/items.go) — it exists purely so the frontend can
	// show a "detected automatically" badge next to the price.
	PriceAuto bool `json:"price_auto"`
	// ImageURL is the item's product image, either filled in by
	// internal/scraper's background lookup (the same one that fills in
	// Price) or left nil if none was found. Unlike Price/PriceAuto there is
	// no manual "set your own image" input anywhere in the API — it is
	// scraper-only, and is cleared back to nil whenever URL changes to
	// something new (see internal/handlers/items.go), since an image
	// scraped for the previous URL no longer describes the current one.
	ImageURL *string `json:"image_url,omitempty"`
	Done     bool    `json:"done"`
	Position int     `json:"position"`
	// TargetMonth is the month (YYYY-MM) an item's purchase is planned for,
	// used by the "Budget & Prévisions Achats" planning view to group and
	// total upcoming spending. Nil means the item isn't scheduled.
	TargetMonth *string `json:"target_month,omitempty"`
	// DueDate (YYYY-MM-DD) is the date this item (or, for a recurring item,
	// its current occurrence) is due. Nil means no due date has been set
	// yet. For a recurring item it moves to the next occurrence when
	// internal/handlers.RunNextOccurrenceScan brings a completed item back
	// (see NextDueDate); while the item sits done it keeps the date of the
	// occurrence that was completed.
	DueDate *string `json:"due_date,omitempty"`
	// DueTime (HH:MM, 24h, internal/validate.TimeOfDay) is an optional
	// wall-clock time for DueDate, interpreted in the instance's
	// APP_TIMEZONE. Always nil when DueDate is nil. The "at the exact due
	// time" reminder mode (ReminderAtDueTime) fires at DueDate + DueTime.
	DueTime *string `json:"due_time,omitempty"`
	// NextDueDate (YYYY-MM-DD) is only ever set on a done recurring item:
	// checking one off leaves it done (see
	// internal/handlers.applyRecurrenceLifecycle) with this set to its next
	// occurrence, the first one strictly after today. Once that occurrence
	// starts (the start of its day, or its reminder moment if that comes
	// first), internal/handlers.RunNextOccurrenceScan un-checks the item and
	// moves NextDueDate into DueDate. Nil on a done recurring item means its
	// series has ended (RecurrenceEndDate reached). Server-computed: never
	// accepted on a request.
	NextDueDate *string `json:"next_due_date,omitempty"`
	// IsRecurring is true exactly when RecurrenceRule is set — it is never
	// set independently, purely a convenience so callers don't have to
	// check RecurrenceRule for non-nilness themselves.
	IsRecurring bool `json:"is_recurring"`
	// RecurrenceRule is the item's rule in the canonical iCalendar RRULE
	// subset of internal/recurrence ("FREQ=WEEKLY", "FREQ=DAILY;INTERVAL=3",
	// "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR", ...; legacy spellings are
	// normalized on write, see internal/validate.Recurrence), or nil if the
	// item doesn't repeat. Completing a recurring item doesn't delete or clone it: it
	// stays done with NextDueDate set, then comes back for that occurrence,
	// so the same row is reused indefinitely rather than accumulating one
	// row per occurrence.
	RecurrenceRule *string `json:"recurrence_rule,omitempty"`
	// RecurrenceEndDate (YYYY-MM-DD), if set, is the last date this item
	// should recur on: once the next computed occurrence would fall after
	// it, the item stops advancing and simply stays done, like a
	// non-recurring item. Nil means the recurrence never ends on its own.
	RecurrenceEndDate *string `json:"recurrence_end_date,omitempty"`
	// IsUrgent flags an item that needs attention right away (e.g. "out of
	// toilet paper"). It's a plain user-set toggle, independent of
	// TargetMonth/RecurrenceRule — unlike IsRecurring it has no derived
	// relationship to another field. The frontend sorts an unfinished urgent
	// item to the top of its list and surfaces it in the cross-list
	// "Achats & Tâches Urgentes" dashboard widget (static/js/urgent.js).
	IsUrgent bool `json:"is_urgent"`
	// RecurrenceLeadMinutes optionally overrode the instance-wide
	// NOTIF_RECURRING_TASK_LEAD_TIME for a recurring item. Superseded by
	// ReminderEnabled/ReminderOffsetDays/ReminderTime below, which apply to
	// any item with a DueDate rather than only a recurring one — kept here
	// purely for API backward compatibility (still stored and echoed back),
	// but no longer read by any scan (see internal/handlers.RunDueReminderScan).
	RecurrenceLeadMinutes *int `json:"recurrence_lead_minutes,omitempty"`
	// ReminderEnabled gates whether this item's DueDate (if any) should ever
	// produce a reminder push at all — meaningless unless DueDate is also
	// set, the same "gate + payload" relationship AlertOnPriceDrop has to
	// TargetPrice. Defaults to true (a Google-Tasks-style "any due-dated
	// task reminds you unless you turn it off" default).
	ReminderEnabled bool `json:"reminder_enabled"`
	// ReminderOffsetDays is how many whole days before DueDate the reminder
	// fires (0 = the same day, 1 = the day before, ...). Always resolved to
	// a concrete value by internal/handlers whenever ReminderEnabled ends up
	// true — see internal/handlers.resolveReminderDefaults — from either an
	// explicit per-item override or, if the request asked to use "the
	// default", the acting user's own current
	// User.ReminderDefaultOffsetDays at write time. Meaningless while
	// ReminderEnabled is false, in which case it's typically nil.
	ReminderOffsetDays *int `json:"reminder_offset_days,omitempty"`
	// ReminderTime is the wall-clock time of day (HH:MM, 24h,
	// internal/validate.TimeOfDay) the reminder fires at on its computed
	// day, resolved the same way as ReminderOffsetDays.
	ReminderTime *string `json:"reminder_time,omitempty"`
	// ReminderAtDueTime selects the "at the exact due time" reminder mode:
	// when the item also has a DueTime, the reminder fires at DueDate +
	// DueTime and ReminderOffsetDays/ReminderTime are ignored. Without a
	// DueTime those two still apply, as the fallback. Resolved at write time
	// like the other reminder fields (see
	// internal/handlers.resolveReminderDefaults).
	ReminderAtDueTime bool `json:"reminder_at_due_time"`
	// NotificationSentAt (ISO-8601 UTC) is when the due reminder for the
	// item's *current* due date/time was pushed, nil while it is still
	// pending. Read-only: internal/db only reports it while
	// due_reminder_sent_for still matches the current due date/time, so
	// moving the task or starting its next occurrence resets it to nil.
	NotificationSentAt *string `json:"notification_sent_at,omitempty"`
	// TargetPrice is a user-set threshold (see AlertOnPriceDrop): once
	// Price drops to or below it, internal/handlers.checkPriceDropAlert
	// fires an in-app toast (via PriceAlertTriggered below) and a push
	// notification. Nil means no threshold is set, independent of whether
	// AlertOnPriceDrop is on — the two are stored separately so a user can
	// type a target price before deciding whether to actually enable the
	// alert for it.
	TargetPrice *float64 `json:"target_price,omitempty"`
	// AlertOnPriceDrop opts an item into the target-price notification
	// above. A plain user-set toggle, independent of every other field
	// here — the same relationship IsUrgent has to the rest of Item.
	AlertOnPriceDrop bool   `json:"alert_on_price_drop"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	// PriceStatus is a transient, response-only field set by
	// internal/handlers.scrapePrice after a create/update/patch — never
	// persisted, and never populated by a plain GET (it's the zero value
	// there, which omitempty drops from the JSON entirely). One of
	// "found" (a price is present, manual or freshly scraped), "pending"
	// (a background lookup is still running and will land later via
	// db.UpdateItemPriceIfMissing), or "none" (no url to scrape, or
	// nothing found within the request's bounded wait).
	PriceStatus string `json:"price_status,omitempty"`
	// PriceAlertTriggered is a transient, response-only field (like
	// PriceStatus — never persisted, never set by a plain GET) reporting
	// whether *this exact request* just crossed the item's target-price
	// threshold (see internal/handlers.checkPriceDropAlert): a manual price
	// edit that lands at or below TargetPrice, or a synchronous scrape
	// result that resolves within the request's bounded wait. The frontend
	// uses it to show an immediate toast without waiting for the separate
	// push notification, which fires regardless (including for a price
	// drop the background scraper finds after the response has already
	// been sent, when this field can't be set).
	PriceAlertTriggered bool `json:"price_alert_triggered,omitempty"`
	// Labels is a freeform, user-managed set of short tags (e.g. "Bio",
	// "Promo") attached via the label management bottom sheet (see
	// static/js/list_view.js's openLabelManageSheet) — independent of
	// TargetMonth/RecurrenceRule/IsUrgent, the same "plain, unrelated toggle"
	// relationship those already have to each other. Cleaned and deduplicated
	// by internal/validate.Labels before being persisted (see
	// internal/db.SetItemLabels); never nil in a response — an item with no
	// labels reports an empty array, matching internal/db's own "labels
	// column defaults to '[]', never NULL" convention (see migration 18).
	Labels []string `json:"labels"`
}

// ValidListTypes enumerates the allowed values for List.Type. `shopping`
// (one-off purchases), `groceries` (day-to-day shopping runs) and
// `recurring_shopping` (subscriptions/recurring purchases) are all
// purchase-oriented list types with different item-form fields shown by the
// frontend (see applyListTypeVisibility in static/js/list_view.js) — the
// same lists.type column just distinguishes which fields make sense for a
// given list, it's not a separate concept from `shopping`. `custom` (a
// freeform list — baby name ideas, notes, an inventory, ...) is the odd one
// out: every item field beyond the required `title` is already optional at
// this layer (Quantity defaults to 1, Price/URL/DueDate/TargetMonth/Done all
// zero-value cleanly), so nothing here needs to special-case it — the
// frontend is what actually hides the checkbox/finance UI and simplifies the
// item form down to a single text field for it (see applyListTypeVisibility
// and FIELD_VISIBILITY_BY_TYPE's `done` flag in static/js/list_view.js).
var ValidListTypes = map[string]bool{
	"todo":               true,
	"shopping":           true,
	"groceries":          true,
	"recurring_shopping": true,
	"custom":             true,
}

// User is the API-facing account shape. It never carries a password hash
// or OIDC identity — those live only in UserWithCredentials, which is
// returned solely to internal/auth and must never be passed to writeJSON.
type User struct {
	ID          int64  `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	// IsAdmin grants access to the /api/v1/admin/... endpoints and the
	// "Paramètres du Système" panel (see internal/handlers/admin.go). It is
	// never settable through the registration/profile API — the only way to
	// become an admin is being the very first account created on a fresh
	// instance (internal/db.CreateUser).
	IsAdmin   bool   `json:"is_admin"`
	CreatedAt string `json:"created_at"`
	// KeepLastPage controls whether the frontend reopens on the last
	// dashboard tab/list the user had open (static/js/settings.js) instead
	// of always landing on the dashboard. Settable via PATCH /api/v1/me.
	KeepLastPage bool `json:"keep_last_page"`
	// Language is the account's own UI-language preference ("fr" or "en"),
	// set from the "Langue" section of the "Paramètres" modal
	// (static/js/i18n.js/settings.js) and settable via PATCH /api/v1/me.
	// Never empty in an API response: internal/handlers.resolveUserLanguage
	// fills in the instance's DEFAULT_APP_LANGUAGE (internal/config) before
	// this struct is ever marshaled to JSON, whenever the account itself has
	// no explicit preference recorded — internal/db.GetUser can still read
	// the underlying users.language column as "" for such an account.
	Language string `json:"language"`
	// ReminderDefaultOffsetDays/ReminderDefaultTime are this account's own
	// default for "when should I be reminded before a task's due date",
	// applied to a new/edited item whenever its reminder is left as "use the
	// default" rather than given an explicit per-item override (see
	// Item.ReminderOffsetDays/ReminderTime). Expressed as a number of whole
	// days before the due date plus a wall-clock time of day (HH:MM, 24h)
	// rather than a plain duration, so it can represent "the same day at
	// 09:00" (0, "09:00", the default below), "the evening before at 20:00"
	// (1, "20:00"), or any custom combination — the frontend derives which
	// named preset to show from these two values rather than a separate
	// mode field. Settable via PATCH /api/v1/me.
	ReminderDefaultOffsetDays int    `json:"reminder_default_offset_days"`
	ReminderDefaultTime       string `json:"reminder_default_time"`
	// ReminderDefaultAtDueTime is the "at the exact due time" preset: a task
	// left on "use the default" gets Item.ReminderAtDueTime = true, and
	// ReminderDefaultOffsetDays/ReminderDefaultTime become the fallback for
	// a task that has no due time. Settable via PATCH /api/v1/me together
	// with the other two.
	ReminderDefaultAtDueTime bool `json:"reminder_default_at_due_time"`
	// VibrateOnNotification controls whether this account's Web Push
	// notifications vibrate the device: internal/handlers.sendToUsers only
	// adds a `vibrate` pattern to the payload when it is set, and
	// static/sw.js shows a notification without one as silent. Settable via
	// PATCH /api/v1/me.
	VibrateOnNotification bool `json:"vibrate_on_notification"`
}

// UserWithCredentials is returned by db lookups used for authentication
// (GetUserByEmail, GetUserByOIDCSubject). Never pass this to writeJSON.
type UserWithCredentials struct {
	User
	PasswordHash *string
	OIDCSubject  *string
	OIDCIssuer   *string
}

// Session is an opaque server-side session. ID holds the hex-encoded
// SHA-256 of the raw cookie token, never the raw token itself, and is
// never API-serialized.
type Session struct {
	ID        string
	UserID    int64
	ExpiresAt string
	CreatedAt string
}

// HouseMember links a User to a House with a role. Email/DisplayName are
// populated by a JOIN for the member-roster endpoint only.
type HouseMember struct {
	HouseID     int64  `json:"house_id"`
	UserID      int64  `json:"user_id"`
	Role        string `json:"role"`
	CreatedAt   string `json:"created_at"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	// Pending marks a roster entry that is an outstanding invitation rather
	// than an actual membership: UserID is 0, and the entry becomes a real
	// member once the invited person next signs in (see
	// db.MaterializePendingInvitations). Response-only, like Item.PriceStatus
	// — never stored, never accepted in a request body.
	Pending bool `json:"pending,omitempty"`
}

// PendingInvitation is an invitation addressed to an email address that has
// not yet been turned into a real membership or share — either because the
// address has no account here at all, or because its owner has not signed in
// since being invited.
//
// Invitations are deliberately keyed by email rather than by user id: that is
// what lets the invite endpoints answer identically whether or not the
// address is registered, closing the account-enumeration oracle they used to
// expose (see docs/AUDIT.md, finding L-06). Kind is one of db.InvitationKind*, and
// TargetID is a house, list, or custom_categories id accordingly. Permission
// is "read"/"write" for a list or space share, and "" for a house invitation,
// where membership carries a role instead.
type PendingInvitation struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	TargetID   int64  `json:"target_id"`
	Email      string `json:"email"`
	Permission string `json:"permission,omitempty"`
	InvitedBy  int64  `json:"invited_by"`
	CreatedAt  string `json:"created_at"`
}

// ValidHouseRoles enumerates the allowed values for HouseMember.Role.
var ValidHouseRoles = map[string]bool{
	"owner":  true,
	"member": true,
}

// PriceAlert records a lower price internal/scraper found for an item
// versus its current price, awaiting a user decision (see
// internal/handlers/price_alerts.go). It is only ever created by the
// periodic background scan or an on-demand check, never directly through
// the API. OriginalPrice is a snapshot of the item's price at the moment
// the alert was created, not re-read live from the item, so a notification
// always reflects what the comparison was actually made against even if
// the item's price changes in the meantime.
type PriceAlert struct {
	ID     int64 `json:"id"`
	ItemID int64 `json:"item_id"`
	// ItemTitle/ListID are populated by a JOIN for display and
	// authorization purposes only (mirroring HouseMember's
	// Email/DisplayName) — never written back to the database.
	ItemTitle     string  `json:"item_title"`
	ListID        int64   `json:"list_id"`
	OriginalPrice float64 `json:"original_price"`
	FoundPrice    float64 `json:"found_price"`
	SourceURL     string  `json:"source_url"`
	Status        string  `json:"status"`
	CreatedAt     string  `json:"created_at"`
}

// ValidPriceAlertStatuses enumerates the allowed values for
// PriceAlert.Status, and the "status" filter accepted by
// GET /api/v1/price-alerts.
var ValidPriceAlertStatuses = map[string]bool{
	"pending":  true,
	"accepted": true,
	"rejected": true,
}

// CustomCategory is a personal "space"/category a user can attach to any
// list (via List.CustomCategoryID), purely for their own organization
// (e.g. "Vacances", "Anniversaire de Léo") — orthogonal to the fixed
// List.Type enum, and not shared the way a house is: it belongs to exactly
// one user (UserID). Other members of a house the category's list belongs
// to can still see it (it's embedded in GET /api/v1/lists) but only its
// owner can rename, restyle, reorder, or delete it — see
// internal/handlers/categories.go.
type CustomCategory struct {
	ID     int64  `json:"id"`
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
	// Icon is a short freeform string (typically an emoji) the frontend
	// renders next to the category name; "" means no icon was set.
	Icon string `json:"icon,omitempty"`
	// Color is a hex color (validated by internal/validate.Color) used as
	// an accent color wherever the category is shown; "" means no color
	// was set.
	Color     string `json:"color,omitempty"`
	Position  int    `json:"position"`
	CreatedAt string `json:"created_at"`
	// AccessSource is the Space-level equivalent of List.AccessSource:
	// populated exclusively by db.ListSpacesVisibleToUser to say how the
	// requesting user can see a Space they don't own — "space_share" (the
	// owner granted them a space_shares row directly) or "house_member"
	// (nobody shared anything; at least one of the Space's tagged lists
	// belongs to a House the requesting user is a member of — see
	// db.spaceAccessibleViaHouse). Empty on every other read, including a
	// category the caller owns.
	AccessSource string `json:"access_source,omitempty"`
	// AccessPermission is a response-only field (never persisted) populated
	// exclusively by db.ListSpacesVisibleToUser to say what level of access
	// ("read" or "write") the requesting user holds on this Space — mirrors
	// the space_shares grant's own permission for AccessSource
	// "space_share", or is always "write" for AccessSource "house_member"
	// (House membership has always implied full read/write access, see
	// db.AccessLevelForList) — empty on every other read (a category the
	// caller owns has no "access permission" distinct from ownership).
	AccessPermission string `json:"access_permission,omitempty"`
	// IsPinnedToDashboard is the Space-level equivalent of
	// List.IsPinnedToDashboard: whether the viewer has chosen to have this
	// Space (and every list reachable through it) show up on their own
	// dashboard/Espaces tab — via PATCH
	// /api/v1/custom-categories/{id}/share/pin (handleSpaceSharePin), which
	// records the choice in space_shares.is_pinned_to_dashboard for an
	// AccessSource "space_share" recipient, or in a dedicated
	// space_house_pins row for an AccessSource "house_member" one (see
	// db.SetSpaceHousePinned) since there's no space_shares row to flip a
	// flag on in that case. Populated only by db.ListSpacesVisibleToUser;
	// always false for a category the caller owns themselves.
	IsPinnedToDashboard bool `json:"is_pinned_to_dashboard,omitempty"`
}

// SpacePinStatus is handleSpaceSharePin's response when a Space is pinned or
// unpinned through House-membership-based access (db.SetSpaceHousePinned)
// rather than an explicit space_shares grant — there's no SpaceShare row to
// return in that case (no Permission, no CreatedAt, no roster entry to
// speak of), just the resulting pin state and how the caller reached it.
type SpacePinStatus struct {
	CustomCategoryID    int64  `json:"custom_category_id"`
	IsPinnedToDashboard bool   `json:"is_pinned_to_dashboard"`
	AccessSource        string `json:"access_source"`
}

// SpaceShare grants one other user ("SharedWithUserID") read or write
// access to every List attached to a CustomCategory ("space"), independent
// of whether they belong to those lists' Houses — see
// internal/handlers/shares.go and db.AccessLevelForList. Only the category's
// own owner may create/revoke one (the same person who can rename/delete
// the category itself).
type SpaceShare struct {
	ID               int64  `json:"id"`
	CustomCategoryID int64  `json:"custom_category_id"`
	SharedWithUserID int64  `json:"shared_with_user_id"`
	Permission       string `json:"permission"`
	CreatedAt        string `json:"created_at"`
	// Email/DisplayName are populated by a JOIN for the roster endpoint
	// only (mirrors HouseMember.Email/DisplayName above).
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	// IsPinnedToDashboard records whether the recipient (SharedWithUserID)
	// has chosen to have this whole Space — and every list reachable
	// through it — show up pinned on their own dashboard/Espaces tab; see
	// PATCH /api/v1/custom-categories/{id}/share/pin (handleSpaceSharePin)
	// and CustomCategory.IsPinnedToDashboard above.
	IsPinnedToDashboard bool `json:"is_pinned_to_dashboard"`
	// Pending marks a roster entry that is still an outstanding invitation
	// rather than a granted share — SharedWithUserID is 0 and only Email is
	// meaningful. See HouseMember.Pending.
	Pending bool `json:"pending,omitempty"`
}

// ListShare grants one other user ("SharedWithUserID") read or write access
// to a single List, independent of House membership — see
// internal/handlers/shares.go and db.AccessLevelForList. Only an actual
// member of the list's House may create/revoke one (see
// handleListShareCreate), so access granted through a share can never
// itself be used to extend further access.
type ListShare struct {
	ID               int64  `json:"id"`
	ListID           int64  `json:"list_id"`
	SharedWithUserID int64  `json:"shared_with_user_id"`
	Permission       string `json:"permission"`
	CreatedAt        string `json:"created_at"`
	// Email/DisplayName are populated by a JOIN for the roster endpoint
	// only (mirrors HouseMember.Email/DisplayName above).
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	// IsPinnedToDashboard records whether the recipient (SharedWithUserID)
	// has chosen to have this shared list show up on their own dashboard —
	// see PATCH /api/v1/lists/{id}/share/pin (handleListSharePin) and
	// List.IsPinnedToDashboard above.
	IsPinnedToDashboard bool `json:"is_pinned_to_dashboard"`
	// Pending marks a roster entry that is still an outstanding invitation
	// rather than a granted share — SharedWithUserID is 0 and only Email is
	// meaningful. See HouseMember.Pending.
	Pending bool `json:"pending,omitempty"`
}

// ValidSharePermissions enumerates the allowed values for
// SpaceShare.Permission/ListShare.Permission.
var ValidSharePermissions = map[string]bool{
	"read":  true,
	"write": true,
}

// PushSubscription is what a browser's PushManager.subscribe() handed this
// app, recorded against the user who granted it — see internal/webpush for
// how Endpoint/P256dh/Auth are used to encrypt and address a push, and
// internal/handlers/push.go for the subscribe/unsubscribe endpoints. Never
// round-tripped back out via a GET endpoint beyond the confirmation
// POST /api/v1/push/subscribe itself returns — there is no reason for a
// client to ever read its own P256dh/Auth back, so those two are not even
// JSON-tagged for output.
type PushSubscription struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"-"`
	Endpoint  string `json:"endpoint"`
	P256dh    string `json:"-"`
	Auth      string `json:"-"`
	UserAgent string `json:"-"`
	CreatedAt string `json:"created_at"`
	// Vibrate is the owning user's User.VibrateOnNotification, filled in
	// only by internal/db.ListPushSubscriptionsForUsers (the one read
	// internal/handlers.sendToUsers delivers from), so each device gets the
	// payload variant its owner asked for. Never serialized.
	Vibrate bool `json:"-"`
}

// BackupRun is one finished encrypted-backup attempt (see internal/backup),
// manual or scheduled — the admin console's "Sauvegardes" history rows and
// the raw material for its failure/staleness alerts. Timestamps use the same
// ISO-8601-UTC-with-milliseconds text format as every other *_at column, so
// they stay lexicographically comparable in SQL.
type BackupRun struct {
	ID         int64  `json:"id"`
	Source     string `json:"source"` // "manual" or "scheduled"
	Status     string `json:"status"` // "success" or "failed"
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	FileName   string `json:"file_name"`
	SizeBytes  int64  `json:"size_bytes"`
	Error      string `json:"error"`
}
