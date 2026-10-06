package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/validate"
	"trakka/internal/webpush"
)

// pushSendTimeout bounds one whole notifyListChange/RunDueReminderScan
// fan-out to every recipient's subscriptions — generous for a handful of
// household members' devices, short enough that a slow or hanging push
// service can't leak a goroutine indefinitely. Each individual
// webpush.Send call is additionally bounded by its own package-level
// http.Client timeout regardless.
const pushSendTimeout = 30 * time.Second

// subscriptionCleanupTimeout bounds the best-effort delete of a subscription
// the push service reported as permanently gone. It deliberately derives from
// context.Background() rather than reusing sendToUsers' own ctx, which may
// already be at or near pushSendTimeout's deadline by the time an individual
// delivery goroutine gets here — but it still needs its own bound rather than
// running fully unbounded, so a stuck DB call here can't leak the goroutine.
const subscriptionCleanupTimeout = 5 * time.Second

// ---------------------------------------------------------------------------
// Subscription management
// ---------------------------------------------------------------------------

// handlePushVAPIDPublicKey hands the frontend the public half of this
// instance's VAPID identity, so PushManager.subscribe()'s
// applicationServerKey never has to be hard-coded into the frontend or
// duplicated from VAPID_PUBLIC_KEY by hand — see static/js/push.js.
// {"enabled": false} (never a 404) when push isn't configured at all, so
// the frontend's settings toggle can just branch on the field rather than
// on response status.
func (app *Application) handlePushVAPIDPublicKey(w http.ResponseWriter, r *http.Request) {
	if !app.Config.PushEnabled() {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "public_key": app.Config.VAPIDPublicKey})
}

// handlePushSubscribe registers (or refreshes) a Web Push subscription for
// the calling user, in the shape a browser's PushSubscription.toJSON()
// produces: {"endpoint": "...", "keys": {"p256dh": "...", "auth": "..."}}.
func (app *Application) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if !app.Config.PushEnabled() {
		writeError(w, http.StatusServiceUnavailable, "push notifications are not configured on this instance")
		return
	}

	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	// Every real push service endpoint is a plain https URL — internal/validate.URL
	// accepts http too (it exists for item.url, which has no such
	// restriction), so an explicit scheme check is layered on top here, the
	// same defense-in-depth internal/webpush.Send itself re-applies before
	// ever dialing this value.
	cleanEndpoint, err := validate.URL(in.Endpoint)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if cleanEndpoint == "" {
		writeError(w, http.StatusBadRequest, "endpoint is required")
		return
	}
	if !strings.HasPrefix(strings.ToLower(cleanEndpoint), "https://") {
		writeError(w, http.StatusBadRequest, "endpoint must be an https:// URL")
		return
	}
	if in.Keys.P256dh == "" || in.Keys.Auth == "" {
		writeError(w, http.StatusBadRequest, "keys.p256dh and keys.auth are required")
		return
	}

	sub, err := app.DB.CreatePushSubscription(r.Context(), userFromContext(r).ID, cleanEndpoint, in.Keys.P256dh, in.Keys.Auth, r.UserAgent())
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sub)
}

// handlePushUnsubscribe removes a subscription for the calling user —
// called both on an explicit "disable push" toggle and defensively before a
// fresh subscribe (see static/js/push.js), so a stale endpoint can never
// linger alongside a new one for the same device.
func (app *Application) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Endpoint == "" {
		writeError(w, http.StatusBadRequest, "endpoint is required")
		return
	}

	if err := app.DB.DeletePushSubscription(r.Context(), userFromContext(r).ID, in.Endpoint); errors.Is(err, db.ErrNotFound) {
		// Not finding it is not a failure from the caller's point of view —
		// the end state ("this endpoint is not subscribed") is exactly what
		// was asked for either way, so this is intentionally idempotent
		// rather than a 404.
		w.WriteHeader(http.StatusNoContent)
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handlePushTest sends one push notification to every subscription the
// calling user has registered, as an end-to-end diagnostic: unlike
// notifyListChange/RunDueReminderScan, whose delivery is always a
// best-effort side effect of some other action, this endpoint's entire
// purpose is the delivery itself, so it runs synchronously (bounded by
// pushSendTimeout, same as every other fan-out in this file) rather than in
// a detached goroutine, and reports back how many subscriptions it attempted
// rather than silently succeeding either way. Scoped to the caller's own
// account only — there is no way to test-notify anyone else — so no
// house/list access check applies, only the ordinary RequireSession gate
// every /api/v1/... route already sits behind.
func (app *Application) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if !app.Config.PushEnabled() {
		writeError(w, http.StatusServiceUnavailable, "push notifications are not configured on this instance")
		return
	}

	user := userFromContext(r)
	subs, err := app.DB.ListPushSubscriptionsForUsers(r.Context(), []int64{user.ID})
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	if len(subs) == 0 {
		writeError(w, http.StatusNotFound, "no push subscription registered for your account — enable push notifications in Paramètres first")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), pushSendTimeout)
	defer cancel()
	app.sendToUsers(ctx, []int64{user.ID}, pushPayload{
		Title: "Trakka",
		Body:  "Notification de test — si vous voyez ceci, les notifications push fonctionnent.",
		URL:   "/",
	})

	writeJSON(w, http.StatusOK, map[string]any{"sent_to_subscriptions": len(subs)})
}

// ---------------------------------------------------------------------------
// Delivery
// ---------------------------------------------------------------------------

// pushPayload is the JSON body every notification this app sends carries —
// static/sw.js's 'push' listener reads these fields to call
// self.registration.showNotification. URL is where notificationclick
// navigates to (a deep link of the shape /?list={id} — see
// handleDeepLinkOrRestore in static/js/app.js).
type pushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	// Tag, when set, is the Notification tag static/sw.js shows this under
	// instead of its per-URL default: a later notification with the same
	// tag replaces the earlier one rather than stacking. Due reminders use
	// one tag per item, so two tasks of the same list never hide each
	// other's reminder.
	Tag string `json:"tag,omitempty"`
	// Vibrate is the vibration pattern (alternating vibrate/pause
	// milliseconds, as the Notification API's own `vibrate` option takes
	// it). Never set by callers: sendToUsers fills in
	// notificationVibratePattern for the subscriptions whose owner has
	// models.User.VibrateOnNotification enabled, and leaves it out for the
	// rest, which static/sw.js then shows as silent.
	Vibrate []int `json:"vibrate,omitempty"`
}

// notificationVibratePattern is a short double buzz: 200ms on, 100ms off,
// 200ms on.
var notificationVibratePattern = []int{200, 100, 200}

// marshalPushBodies returns payload's JSON twice: without a vibration
// pattern (quiet) and with notificationVibratePattern (vibrating), so
// sendToUsers marshals once per notification rather than once per device.
func marshalPushBodies(payload pushPayload) (quiet, vibrating []byte, err error) {
	payload.Vibrate = nil
	if quiet, err = json.Marshal(payload); err != nil {
		return nil, nil, err
	}
	payload.Vibrate = notificationVibratePattern
	if vibrating, err = json.Marshal(payload); err != nil {
		return nil, nil, err
	}
	return quiet, vibrating, nil
}

// sendToUsers delivers payload to every subscription belonging to any of
// userIDs, concurrently (a household has at most a handful of members, each
// with at most a few devices, so no worker-pool bound is needed) and
// best-effort: one recipient's failed/unreachable push never blocks or
// fails another's, and this function itself never returns an error — a
// push notification is always a secondary effect of some other action that
// has already succeeded by the time this is called (see notifyListChange
// and RunDueReminderScan below), and must never be able to make that
// action look like it failed. A subscription the push service reports as
// permanently gone (webpush.ErrSubscriptionGone — 404/410) is deleted so
// future notifications stop trying it; any other failure is just logged.
// Each subscription gets the quiet or the vibrating body per its owner's
// preference (see marshalPushBodies).
func (app *Application) sendToUsers(ctx context.Context, userIDs []int64, payload pushPayload) {
	if app.pushHook != nil {
		if len(userIDs) > 0 {
			app.pushHook(userIDs, payload)
		}
		return
	}
	if !app.Config.PushEnabled() || len(userIDs) == 0 {
		return
	}
	quietBody, vibratingBody, err := marshalPushBodies(payload)
	if err != nil {
		app.Logger.Error("marshaling push payload", "error", err)
		return
	}

	subs, err := app.DB.ListPushSubscriptionsForUsers(ctx, userIDs)
	if err != nil {
		app.Logger.Error("listing push subscriptions", "error", err)
		return
	}

	keys := webpush.VAPIDKeyPair{PublicKeyB64: app.Config.VAPIDPublicKey, PrivateKeyB64: app.Config.VAPIDPrivateKey}

	var wg sync.WaitGroup
	for _, sub := range subs {
		wg.Add(1)
		go func(sub *models.PushSubscription) { // #nosec G118 -- the context.Background() inside is deliberately given its own subscriptionCleanupTimeout rather than reusing ctx, which may already be at or past its own pushSendTimeout deadline by the time this cleanup runs; see the subscriptionCleanupTimeout doc comment
			defer wg.Done()
			body := quietBody
			if sub.Vibrate {
				body = vibratingBody
			}
			err := webpush.Send(ctx, webpush.Subscription{Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth}, keys, app.Config.VAPIDSubject, body)
			if errors.Is(err, webpush.ErrSubscriptionGone) {
				delCtx, delCancel := context.WithTimeout(context.Background(), subscriptionCleanupTimeout)
				defer delCancel()
				if delErr := app.DB.DeletePushSubscriptionByID(delCtx, sub.ID); delErr != nil {
					app.Logger.Error("deleting gone push subscription", "subscription_id", sub.ID, "error", delErr)
				}
				return
			}
			if err != nil {
				app.Logger.Debug("sending push notification failed", "subscription_id", sub.ID, "error", err)
			}
		}(sub)
	}
	wg.Wait()
}

// listChange is what someone did to an item that notifyListChange tells the
// list's other users about.
type listChange int

const (
	itemAdded listChange = iota
	itemChecked
	itemUnchecked
)

// doneChange is the listChange of an item whose done flag just flipped to
// checked (true) or back (false).
func doneChange(checked bool) listChange {
	if checked {
		return itemChecked
	}
	return itemUnchecked
}

// notifyListChange fires a push notification to every other user with
// access to list (see db.ListNotificationRecipients — House members, plus
// anyone the list or its parent Space has been shared with) when actor adds,
// checks off or unchecks an item, per this feature's "Use Case 1" — each
// recipient only if they want that type (models.User.ItemAdditionsEnabled
// for an addition, CollaboratorActionsEnabled for a check or uncheck). Called from
// handleItemsCreate/handleItemsUpdate/handleItemsPatch (items.go) in a
// detached goroutine on its own bounded context — never r.Context(), which
// is canceled the moment the response is written, the same reasoning
// scrapeProductInfo (scrape.go) already follows for its own background
// work — so a slow push delivery can never delay the response to the
// request that triggered it.
//
// The notification text is always composed in French, regardless of the
// recipient's own language preference: although users.language (see
// models.User.Language) now exists, wiring push notification text through
// per-recipient localization is out of scope here — the same reasoning that
// already leaves templates/login.html French-only, per CLAUDE.md's "UI
// language" convention.
func (app *Application) notifyListChange(list *models.List, actor *models.User, itemTitle string, change listChange) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pushSendTimeout)
		defer cancel()
		app.deliverListChange(ctx, list, actor, itemTitle, change)
	}()
}

// deliverListChange is notifyListChange's work, run synchronously.
func (app *Application) deliverListChange(ctx context.Context, list *models.List, actor *models.User, itemTitle string, change listChange) {
	kind, verb := db.NotifyItemAdditions, "a ajouté un article"
	switch change {
	case itemChecked:
		kind, verb = db.NotifyCollaboratorActions, "a coché un article"
	case itemUnchecked:
		kind, verb = db.NotifyCollaboratorActions, "a décoché un article"
	}
	recipients, err := app.DB.ListNotificationRecipientsFor(ctx, list.ID, actor.ID, kind)
	if err != nil {
		app.Logger.Error("listing notification recipients", "list_id", list.ID, "error", err)
		return
	}
	if len(recipients) == 0 {
		return
	}
	app.sendToUsers(ctx, recipients, pushPayload{
		Title: list.Name,
		Body:  fmt.Sprintf("%s %s (« %s ») dans %s", displayName(actor), verb, itemTitle, list.Name),
		URL:   fmt.Sprintf("/?list=%d", list.ID),
	})
}

// notifyInvitation tells the person invited to a House, or with whom a List
// or Space was just shared (invitation kind db.InvitationKind*, targetID),
// about it — if email belongs to an account here that wants it
// (models.User.ListSharingEnabled). The invitation itself takes effect the
// next time they open the app (db.MaterializePendingInvitations), which the
// notification's tap does. Detached, like notifyListChange: the inviting
// request's reply must not depend on whether the address has an account
// here, not even through its timing (docs/AUDIT.md, L-06).
func (app *Application) notifyInvitation(kind string, targetID int64, email string, inviter *models.User) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pushSendTimeout)
		defer cancel()
		app.deliverInvitation(ctx, kind, targetID, email, inviter)
	}()
}

// deliverInvitation is notifyInvitation's work, run synchronously.
func (app *Application) deliverInvitation(ctx context.Context, kind string, targetID int64, email string, inviter *models.User) {
	invitee, err := app.DB.GetUserByEmail(ctx, email)
	if errors.Is(err, db.ErrNotFound) {
		return // no account yet: nobody to notify
	}
	if err != nil {
		app.Logger.Error("looking up an invitee", "error", err)
		return
	}
	if wants, err := app.DB.UserWantsNotification(ctx, invitee.ID, db.NotifyListSharing); err != nil {
		app.Logger.Error("reading an invitee's notification preference", "user_id", invitee.ID, "error", err)
		return
	} else if !wants {
		return
	}

	who := displayName(inviter)
	var payload pushPayload
	switch kind {
	case db.InvitationKindHouse:
		house, err := app.DB.GetHouse(ctx, targetID)
		if err != nil {
			app.Logger.Error("loading the house of an invitation", "house_id", targetID, "error", err)
			return
		}
		payload = pushPayload{Title: "Invitation", Body: fmt.Sprintf("%s vous a invité à rejoindre la maison « %s »", who, house.Name), URL: "/"}
	case db.InvitationKindSpace:
		space, err := app.DB.GetCustomCategoryForUser(ctx, targetID, inviter.ID)
		if err != nil {
			app.Logger.Error("loading the space of a share", "custom_category_id", targetID, "error", err)
			return
		}
		payload = pushPayload{Title: "Nouveau partage", Body: fmt.Sprintf("%s a partagé l'espace « %s » avec vous", who, space.Name), URL: "/"}
	case db.InvitationKindList:
		list, err := app.DB.GetList(ctx, targetID)
		if err != nil {
			app.Logger.Error("loading the list of a share", "list_id", targetID, "error", err)
			return
		}
		payload = pushPayload{Title: "Nouveau partage", Body: fmt.Sprintf("%s a partagé la liste « %s » avec vous", who, list.Name), URL: fmt.Sprintf("/?list=%d", list.ID)}
	default:
		return
	}
	app.sendToUsers(ctx, []int64{invitee.ID}, payload)
}

// displayName is how a notification names user: their display name, else
// their email address.
func displayName(user *models.User) string {
	if user.DisplayName != "" {
		return user.DisplayName
	}
	return user.Email
}

// ---------------------------------------------------------------------------
// Task due-date reminders ("Use Case 2")
// ---------------------------------------------------------------------------

// dueReminderScanTimeout bounds one whole periodic scan across every
// eligible item — generous for the modest item counts this app targets
// (CLAUDE.md's <20MB RAM footprint implies a small-household scale
// throughout), while still guaranteeing the scan can't hang the process
// indefinitely if something goes wrong partway through.
const dueReminderScanTimeout = 5 * time.Minute

// RunDueReminderScan checks every not-done item with a due date and an
// active reminder (see db.ListItemsForDueReminderScan) and sends a push to
// every user with access to its list once the current time has reached that
// item's own (already-resolved) reminder moment — see reminderMoment:
// its due date and due time, reminder_at_due_time, reminder_offset_days and
// reminder_time, interpreted in app.location(). Applies to any item with a
// due date, not only a recurring one — see models.Item.ReminderEnabled/
// ReminderOffsetDays/ReminderTime/ReminderAtDueTime and the
// reminder-resolution logic in items.go for how those get set. Runs
// RunNextOccurrenceScan first, so a recurring task whose next occurrence
// starts at its reminder moment is back (not done, due on its new date) by
// the time this pass looks for it. Called on a timer from
// cmd/server/main.go; also safe to call directly for an immediate,
// whole-catalog scan. Best-effort throughout, mirroring
// RunPriceAlertScan: one item's failure (a bad due date, a db error
// resolving its list/recipients) is logged and never stops the rest of the
// scan.
func (app *Application) RunDueReminderScan(ctx context.Context) {
	if !app.Config.PushEnabled() {
		return
	}

	app.RunNextOccurrenceScan(ctx)

	scanCtx, cancel := context.WithTimeout(ctx, dueReminderScanTimeout)
	defer cancel()

	candidates, err := app.DB.ListItemsForDueReminderScan(scanCtx)
	if err != nil {
		app.Logger.Error("listing items for due reminder scan", "error", err)
		return
	}

	// Debug rather than Info: the scan runs every minute by default, since
	// an "at the exact due time" reminder can't wait half an hour.
	app.Logger.Debug("running due-date reminder scan", "item_count", len(candidates))
	now := time.Now()
	for _, c := range candidates {
		if scanCtx.Err() != nil {
			return
		}
		if err := app.checkItemForDueReminder(scanCtx, c, now); err != nil {
			app.Logger.Error("due reminder scan check failed", "item_id", c.ItemID, "error", err)
		}
	}
}

// checkItemForDueReminder evaluates one candidate: if now has already
// reached its computed reminder moment, it notifies every user with access
// to its list and records that the reminder was sent for this exact due
// date/time (db.MarkDueReminderSent) so the next scan tick doesn't repeat
// it — see that method's own comment for why storing the due date/time key
// itself, rather than a plain boolean, is what makes this automatically
// re-arm once the item's due date or time next changes.
func (app *Application) checkItemForDueReminder(ctx context.Context, c *db.DueReminderCandidate, now time.Time) error {
	loc := app.location()
	reminderAt, err := reminderMoment(c.DueDate, c.DueTime, c.AtDueTime, c.OffsetDays, c.TimeOfDay, loc)
	if err != nil {
		return err
	}
	if now.Before(reminderAt) {
		return nil // not due soon enough yet
	}

	list, err := app.DB.GetList(ctx, c.ListID)
	if errors.Is(err, db.ErrNotFound) {
		// The list was deleted after this item was fetched by the scan —
		// nothing to notify about, and ON DELETE CASCADE means the item
		// itself is already gone too.
		return nil
	}
	if err != nil {
		return fmt.Errorf("loading list %d: %w", c.ListID, err)
	}

	// Every House member, plus every share recipient, is notified here —
	// unlike notifyListChange there is no single "actor" to exclude, since a
	// due-date reminder is not the result of anyone's own action — except
	// those who turned task reminders off (models.User.RemindersEnabled).
	// The reminder is still marked sent below either way, so turning them
	// back on doesn't deliver every reminder missed meanwhile.
	recipients, err := app.DB.ListNotificationRecipientsFor(ctx, list.ID, 0, db.NotifyTaskReminders)
	if err != nil {
		return fmt.Errorf("listing notification recipients: %w", err)
	}
	// Nobody left to notify (everyone turned reminders off, or — not
	// expected in practice — the house has no members left): nothing to
	// send, but the reminder still counts as handled.
	if len(recipients) > 0 {
		app.sendToUsers(ctx, recipients, pushPayload{
			Title: c.Title,
			Body:  fmt.Sprintf("🔔 %s — %s", describeDueFR(c.DueDate, c.DueTime, now.In(loc)), list.Name),
			URL:   fmt.Sprintf("/?list=%d", list.ID),
			Tag:   fmt.Sprintf("trakka-reminder-%d", c.ItemID),
		})
	}

	if err := app.DB.MarkDueReminderSent(ctx, c.ItemID, c.Key()); err != nil {
		return fmt.Errorf("marking reminder sent: %w", err)
	}
	return nil
}

// reminderMoment computes when an item's due reminder fires, in loc: at
// dueDate + dueTime in the "at the exact due time" mode when the item has a
// due time, otherwise offsetDays whole days before dueDate at timeOfDay
// (the fallback the write path always resolves — see
// resolveReminderDefaults). The single place that instant is computed:
// checkItemForDueReminder sends at it, and occurrenceStart brings a
// recurring task back no later than it.
func reminderMoment(dueDate, dueTime string, atDueTime bool, offsetDays int, timeOfDay string, loc *time.Location) (time.Time, error) {
	due, err := time.ParseInLocation(dateLayout, dueDate, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing due date %q: %w", dueDate, err)
	}
	if atDueTime && dueTime != "" {
		hour, minute, err := parseTimeOfDay(dueTime)
		if err != nil {
			return time.Time{}, fmt.Errorf("parsing due time %q: %w", dueTime, err)
		}
		return time.Date(due.Year(), due.Month(), due.Day(), hour, minute, 0, 0, loc), nil
	}
	hour, minute, err := parseTimeOfDay(timeOfDay)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing reminder time %q: %w", timeOfDay, err)
	}
	return time.Date(due.Year(), due.Month(), due.Day()-offsetDays, hour, minute, 0, 0, loc), nil
}

var (
	frenchWeekdays = [...]string{"dim.", "lun.", "mar.", "mer.", "jeu.", "ven.", "sam."}
	frenchMonths   = [...]string{"janv.", "févr.", "mars", "avr.", "mai", "juin", "juil.", "août", "sept.", "oct.", "nov.", "déc."}
)

// describeDueFR phrases a due date (and optional HH:MM due time) relative
// to now for a reminder's body, in French like every server-composed
// notification (see notifyListChange's doc comment): "Échéance aujourd'hui
// à 18:00", "Échéance demain", "Échéance le dim. 11 oct.", or "Échéance
// dépassée (lun. 5 oct.)" when a scan catches up on a reminder whose due
// date has already passed. The short day/month forms match what the
// frontend's Intl.DateTimeFormat shows on the task's own badge.
func describeDueFR(dueDate, dueTime string, now time.Time) string {
	due, err := time.Parse(dateLayout, dueDate)
	if err != nil {
		return "Échéance le " + dueDate
	}
	// Calendar-day difference computed on UTC midnights: no DST shift, so
	// a 23- or 25-hour local day can't round the count off by one.
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	days := int(due.Sub(today).Hours() / 24)
	label := shortDateFR(due, now)

	var phrase string
	switch {
	case days < 0:
		return "Échéance dépassée (" + label + ")"
	case days == 0:
		phrase = "Échéance aujourd'hui"
	case days == 1:
		phrase = "Échéance demain"
	default:
		phrase = "Échéance le " + label
	}
	if dueTime != "" {
		phrase += " à " + dueTime
	}
	return phrase
}

// shortDateFR is due's short French form, "dim. 11 oct.", with the year
// only when it isn't now's.
func shortDateFR(due, now time.Time) string {
	label := fmt.Sprintf("%s %d %s", frenchWeekdays[due.Weekday()], due.Day(), frenchMonths[due.Month()-1])
	if due.Year() != now.Year() {
		label += fmt.Sprintf(" %d", due.Year())
	}
	return label
}

// parseTimeOfDay parses an HH:MM (24h) string — already validated by
// internal/validate.TimeOfDay before it was ever stored — into its hour and
// minute components.
func parseTimeOfDay(raw string) (hour, minute int, err error) {
	t, err := time.Parse("15:04", raw)
	if err != nil {
		return 0, 0, err
	}
	return t.Hour(), t.Minute(), nil
}
