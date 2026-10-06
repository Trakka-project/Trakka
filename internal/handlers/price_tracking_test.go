package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
)

// waitUntil polls cond until it holds, for what a detached goroutine
// (notifyPriceChange) writes without anything to wait on.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func priceNotificationsOf(t *testing.T, app *Application, userID int64) []*models.PriceNotification {
	t.Helper()
	n, err := app.DB.ListPriceNotifications(context.Background(), userID, 50)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// trackedItemFixture is the owner of a house with a shopping list, a
// second member ("friend") of that house, and an item on the list with a
// url and a price of 100 — scraper-found (price_auto) when auto is true,
// typed in otherwise.
func trackedItemFixture(t *testing.T, app *Application, auto bool) (owner, friend *models.User, item *models.Item) {
	t.Helper()
	ctx := context.Background()
	owner, list := itemTestFixture(t, app)
	friend = mustCreateTestUser(t, app, "friend@example.com")
	if _, err := app.DB.AddHouseMember(ctx, list.HouseID, friend.ID, "member"); err != nil {
		t.Fatal(err)
	}
	url := "https://shop.example.com/product"
	price := 100.0
	item, err := app.DB.CreateItem(ctx, list.ID, "Casque Sony WH-1000XM5", &url, 1, &price, auto, 0, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return owner, friend, item
}

// TestApplyObservedPriceFollowsAutoPrice: a scraper-found price follows its
// page — a drop and an increase are both applied, recorded as the item's
// last movement and in its history, and announced to whoever wants that
// direction (drops on and increases off by default; the friend flips both).
func TestApplyObservedPriceFollowsAutoPrice(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	ctx := context.Background()
	owner, friend, item := trackedItemFixture(t, app, true)
	setPrefs(t, app, friend.ID, db.NotificationPreferences{PriceDropAlertsEnabled: off, PriceIncreaseAlertsEnabled: on})

	if err := app.applyObservedPrice(ctx, item, 80, false); err != nil {
		t.Fatal(err)
	}
	drop := pushes.next(t)
	if drop.payload.Title != "📉 Baisse de prix" || len(drop.userIDs) != 1 || drop.userIDs[0] != owner.ID {
		t.Fatalf("drop push = %+v, want one to the owner", drop)
	}
	reloaded, err := app.DB.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *reloaded.Price != 80 || !reloaded.PriceAuto || reloaded.PreviousPrice == nil || *reloaded.PreviousPrice != 100 || reloaded.PriceChangedAt == nil {
		t.Fatalf("after the drop: %+v", reloaded)
	}
	if n := priceNotificationsOf(t, app, owner.ID); len(n) != 1 || n[0].Kind != "drop" || n[0].OldPrice != 100 || n[0].NewPrice != 80 || n[0].ReadAt != nil {
		t.Fatalf("owner's inbox after the drop: %+v", n)
	}
	if n := priceNotificationsOf(t, app, friend.ID); len(n) != 0 {
		t.Fatalf("friend has drops off but got %+v", n)
	}

	// The same price again is no change at all.
	if err := app.applyObservedPrice(ctx, reloaded, 80, false); err != nil {
		t.Fatal(err)
	}

	if err := app.applyObservedPrice(ctx, reloaded, 90, false); err != nil {
		t.Fatal(err)
	}
	increase := pushes.next(t)
	if increase.payload.Title != "📈 Hausse de prix" || len(increase.userIDs) != 1 || increase.userIDs[0] != friend.ID {
		t.Fatalf("increase push = %+v, want one to the friend", increase)
	}
	if n := priceNotificationsOf(t, app, friend.ID); len(n) != 1 || n[0].Kind != "increase" {
		t.Fatalf("friend's inbox after the increase: %+v", n)
	}
	if extra := pushes.all(); len(extra) != 0 {
		t.Fatalf("unexpected extra pushes: %+v", extra)
	}

	history, err := app.DB.ListPriceHistory(ctx, item.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Price != 80 || history[1].Price != 90 {
		t.Fatalf("history = %+v, want 80 then 90", history)
	}

	// A manual price edit is not a movement at the store: it clears it.
	reloaded, _ = app.DB.GetItem(ctx, item.ID)
	manual := 75.0
	edited, err := app.DB.UpdateItem(ctx, reloaded.ID, reloaded.Title, reloaded.URL, reloaded.Quantity, &manual, false, reloaded.ImageURL,
		false, reloaded.Position, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if edited.PreviousPrice != nil || edited.PriceChangedAt != nil {
		t.Fatalf("manual edit kept the movement: %+v", edited)
	}
	// An edit leaving price and url alone keeps it.
	if err := app.applyObservedPrice(ctx, &models.Item{ID: item.ID, ListID: item.ListID, Title: item.Title, URL: item.URL, Price: &manual, PriceAuto: true}, 70, false); err != nil {
		t.Fatal(err)
	}
	pushes.next(t)
	moved, _ := app.DB.GetItem(ctx, item.ID)
	renamed, err := app.DB.UpdateItem(ctx, moved.ID, "Renommé", moved.URL, 2, moved.Price, moved.PriceAuto, moved.ImageURL,
		false, moved.Position, nil, nil, nil, nil, false, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.PreviousPrice == nil || *renamed.PreviousPrice != 75 {
		t.Fatalf("unrelated edit cleared the movement: %+v", renamed)
	}
}

// TestApplyObservedPriceKeepsManualPrice: a typed-in price is never
// overwritten; a lower one on the page is proposed once, and not again
// after being rejected.
func TestApplyObservedPriceKeepsManualPrice(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	ctx := context.Background()
	owner, _, item := trackedItemFixture(t, app, false)

	if err := app.applyObservedPrice(ctx, item, 120, false); err != nil {
		t.Fatal(err)
	}
	if err := app.applyObservedPrice(ctx, item, 90, false); err != nil {
		t.Fatal(err)
	}
	deal := pushes.next(t)
	if deal.payload.Title != "💡 Meilleur prix trouvé" || deal.userIDs[0] != owner.ID {
		t.Fatalf("deal push = %+v", deal)
	}
	reloaded, _ := app.DB.GetItem(ctx, item.ID)
	if *reloaded.Price != 100 || reloaded.PriceAuto || reloaded.PreviousPrice != nil {
		t.Fatalf("manual price was touched: %+v", reloaded)
	}
	alert, err := app.DB.GetPendingPriceAlertForItem(ctx, item.ID)
	if err != nil || alert.FoundPrice != 90 || alert.SourceURL != *item.URL {
		t.Fatalf("pending alert = %+v, %v", alert, err)
	}
	if n := priceNotificationsOf(t, app, owner.ID); len(n) != 1 || n[0].Kind != "deal" || n[0].SourceURL == nil {
		t.Fatalf("owner's inbox: %+v", n)
	}

	if _, err := app.DB.RejectPriceAlert(ctx, alert.ID); err != nil {
		t.Fatal(err)
	}
	if err := app.applyObservedPrice(ctx, item, 90, false); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.GetPendingPriceAlertForItem(ctx, item.ID); err == nil {
		t.Fatal("a rejected deal was proposed again")
	}

	history, _ := app.DB.ListPriceHistory(ctx, item.ID, 10)
	if len(history) != 2 || history[0].Price != 120 || history[1].Price != 90 {
		t.Fatalf("history = %+v, want 120 then 90", history)
	}
}

// TestIncreaseInInboxFollowsCurrentSetting: with push not configured at all,
// a price increase still reaches the inbox of whoever has "Notifier en cas
// de hausse de prix" on — including one detected while it was still off,
// like the ▲ indicator the list already shows for it — and nobody's while
// it is off, or while price alerts are turned off altogether.
func TestIncreaseInInboxFollowsCurrentSetting(t *testing.T) {
	app := newTestApplication(t) // no VAPID keys, no push hook: push is off
	ctx := context.Background()
	owner, friend, item := trackedItemFixture(t, app, true)
	setPrefs(t, app, friend.ID, db.NotificationPreferences{PriceIncreaseAlertsEnabled: on})

	if err := app.applyObservedPrice(ctx, item, 120, false); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the increase in the friend's inbox", func() bool {
		n := priceNotificationsOf(t, app, friend.ID)
		return len(n) == 1 && n[0].Kind == "increase" && n[0].OldPrice == 100 && n[0].NewPrice == 120
	})
	if n := priceNotificationsOf(t, app, owner.ID); len(n) != 0 {
		t.Fatalf("owner has increases off (the default) but sees %+v", n)
	}

	setPrefs(t, app, owner.ID, db.NotificationPreferences{PriceIncreaseAlertsEnabled: on})
	if n := priceNotificationsOf(t, app, owner.ID); len(n) != 1 || n[0].Kind != "increase" {
		t.Fatalf("owner turned increases on, inbox = %+v", n)
	}
	setPrefs(t, app, owner.ID, db.NotificationPreferences{PriceAlertsEnabled: off})
	if n := priceNotificationsOf(t, app, owner.ID); len(n) != 0 {
		t.Fatalf("owner turned price alerts off, inbox = %+v", n)
	}
}

// TestAcceptingDealSwitchesURL: accepting a price found elsewhere moves the
// item to that price and that url, which the next checks then follow.
func TestAcceptingDealSwitchesURL(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	ctx := context.Background()
	owner, _, item := trackedItemFixture(t, app, false)
	dealURL := "https://www.dealabs.com/bons-plans/casque-sony-wh-1000xm5-3420521"

	if err := app.proposeBetterPrice(ctx, item, 70, dealURL); err != nil {
		t.Fatal(err)
	}
	pushes.next(t)
	alert, err := app.DB.GetPendingPriceAlertForItem(ctx, item.ID)
	if err != nil || !alert.ChangesURL {
		t.Fatalf("pending alert = %+v, %v; want one that changes the url", alert, err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/price-alerts/"+strconv.FormatInt(alert.ID, 10), strings.NewReader(`{"status":"accepted"}`))
	req.SetPathValue("id", strconv.FormatInt(alert.ID, 10))
	app.handlePriceAlertsUpdate(rec, withUser(req, owner))
	if rec.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body.String())
	}
	accepted, _ := app.DB.GetItem(ctx, item.ID)
	if *accepted.Price != 70 || !accepted.PriceAuto || *accepted.URL != dealURL || accepted.PreviousPrice != nil {
		t.Fatalf("after accepting: %+v", accepted)
	}
	if history, _ := app.DB.ListPriceHistory(ctx, item.ID, 10); len(history) != 1 || history[0].Price != 70 {
		t.Fatalf("history after accepting = %+v", history)
	}

	// The deal page is now the item's page: its price is followed.
	if err := app.applyObservedPrice(ctx, accepted, 65, false); err != nil {
		t.Fatal(err)
	}
	if p := pushes.next(t); p.payload.Title != "📉 Baisse de prix" {
		t.Fatalf("push = %+v", p)
	}
	if followed, _ := app.DB.GetItem(ctx, item.ID); *followed.Price != 65 || *followed.PreviousPrice != 70 {
		t.Fatalf("after the deal page moved: %+v", followed)
	}
}

// TestAcceptRefusesUnsafeSourceURL: a source_url that isn't a plain http(s)
// url never becomes an item's url.
func TestAcceptRefusesUnsafeSourceURL(t *testing.T) {
	app := newTestApplication(t)
	ctx := context.Background()
	owner, _, item := trackedItemFixture(t, app, false)
	if _, err := app.DB.CreatePriceAlertIfNonePending(ctx, item.ID, 100, 70, "javascript:alert(1)"); err != nil {
		t.Fatal(err)
	}
	alert, err := app.DB.GetPendingPriceAlertForItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/price-alerts/"+strconv.FormatInt(alert.ID, 10), strings.NewReader(`{"status":"accepted"}`))
	req.SetPathValue("id", strconv.FormatInt(alert.ID, 10))
	app.handlePriceAlertsUpdate(rec, withUser(req, owner))
	if rec.Code != http.StatusConflict {
		t.Fatalf("accept: %d %s, want 409", rec.Code, rec.Body.String())
	}
	if unchanged, _ := app.DB.GetItem(ctx, item.ID); *unchanged.URL != *item.URL || *unchanged.Price != 100 {
		t.Fatalf("item changed: %+v", unchanged)
	}
}

// TestDealExpiredAnnouncedOnce: an expired deal is announced the first time a
// check sees it, not on every later one.
func TestDealExpiredAnnouncedOnce(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	ctx := context.Background()
	owner, _, item := trackedItemFixture(t, app, true)

	if err := app.announceDealExpired(ctx, item); err != nil {
		t.Fatal(err)
	}
	if p := pushes.next(t); p.payload.Title != "⌛ Bon plan expiré" {
		t.Fatalf("push = %+v", p)
	}
	if n := priceNotificationsOf(t, app, owner.ID); len(n) != 1 || n[0].Kind != "expired" {
		t.Fatalf("inbox = %+v", n)
	}
	if err := app.announceDealExpired(ctx, item); err != nil {
		t.Fatal(err)
	}
	if extra := pushes.all(); len(extra) != 0 {
		t.Fatalf("announced twice: %+v", extra)
	}
}

// TestTargetPriceReachedPushesOnce: a scan move that reaches the item's
// target price pushes "Bonne affaire" only, not a second "Baisse de prix"
// about the same change — which still lands in the inbox.
func TestTargetPriceReachedPushesOnce(t *testing.T) {
	app := newTestApplication(t)
	pushes := recordPushes(app)
	ctx := context.Background()
	owner, _, item := trackedItemFixture(t, app, true)
	target := 85.0
	item.TargetPrice, item.AlertOnPriceDrop = &target, true

	if err := app.applyObservedPrice(ctx, item, 80, false); err != nil {
		t.Fatal(err)
	}
	if p := pushes.next(t); p.payload.Title != "🔥 Bonne affaire !" {
		t.Fatalf("push = %+v", p)
	}
	waitUntil(t, "the drop in the inbox", func() bool { return len(priceNotificationsOf(t, app, owner.ID)) == 1 })
	if extra := pushes.all(); len(extra) != 0 {
		t.Fatalf("unexpected extra pushes: %+v", extra)
	}
}

// TestPriceAlertPreferences: the master switch silences every price alert
// kind, the per-direction switches only theirs.
func TestPriceAlertPreferences(t *testing.T) {
	app := newTestApplication(t)
	ctx := context.Background()
	owner, friend, item := trackedItemFixture(t, app, true)
	setPrefs(t, app, friend.ID, db.NotificationPreferences{PriceAlertsEnabled: off, PriceIncreaseAlertsEnabled: on})

	want := map[db.NotificationKind][]int64{
		db.NotifyPriceDrops:     {owner.ID},
		db.NotifyPriceIncreases: {},
		db.NotifyPriceTargets:   {owner.ID},
	}
	for kind, ids := range want {
		got, err := app.DB.ListNotificationRecipientsFor(ctx, item.ListID, 0, kind)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(ids) || (len(ids) == 1 && got[0] != ids[0]) {
			t.Errorf("%s recipients = %v, want %v", kind, got, ids)
		}
	}
}

func TestHandlePriceNotifications(t *testing.T) {
	app := newTestApplication(t)
	ctx := context.Background()
	owner, friend, item := trackedItemFixture(t, app, true)
	for _, u := range []*models.User{owner, friend} {
		setPrefs(t, app, u.ID, db.NotificationPreferences{PriceIncreaseAlertsEnabled: on})
	}
	for _, kind := range []string{db.PriceNotificationDrop, db.PriceNotificationIncrease} {
		if err := app.DB.CreatePriceNotifications(ctx, []int64{owner.ID, friend.ID}, item.ID, kind, 100, 90, nil); err != nil {
			t.Fatal(err)
		}
	}

	list := func(user *models.User) []models.PriceNotification {
		t.Helper()
		rec := httptest.NewRecorder()
		app.handlePriceNotificationsIndex(rec, withUser(httptest.NewRequest(http.MethodGet, "/api/v1/price-notifications", nil), user))
		if rec.Code != http.StatusOK {
			t.Fatalf("index: %d %s", rec.Code, rec.Body.String())
		}
		var got []models.PriceNotification
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	markRead := func(user *models.User, body string) {
		t.Helper()
		rec := httptest.NewRecorder()
		app.handlePriceNotificationsRead(rec, withUser(httptest.NewRequest(http.MethodPost, "/api/v1/price-notifications/read", strings.NewReader(body)), user))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("read %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}

	ownerList := list(owner)
	if len(ownerList) != 2 || ownerList[0].ItemTitle != item.Title || ownerList[0].Kind != "increase" {
		t.Fatalf("owner's notifications: %+v", ownerList)
	}

	// The friend can't mark the owner's notification read.
	markRead(friend, `{"ids":[`+strconv.FormatInt(ownerList[0].ID, 10)+`]}`)
	markRead(owner, `{"ids":[`+strconv.FormatInt(ownerList[1].ID, 10)+`]}`)
	ownerList = list(owner)
	if ownerList[0].ReadAt != nil || ownerList[1].ReadAt == nil {
		t.Fatalf("after marking one read: %+v", ownerList)
	}
	markRead(owner, `{}`)
	for _, n := range list(owner) {
		if n.ReadAt == nil {
			t.Fatalf("still unread after marking all read: %+v", n)
		}
	}
	for _, n := range list(friend) {
		if n.ReadAt != nil {
			t.Fatalf("the owner's mark-all touched the friend's: %+v", n)
		}
	}

	// Leaving the house hides what was queued about its lists.
	if err := app.DB.RemoveHouseMember(ctx, mustListHouseID(t, app, item.ListID), friend.ID); err != nil {
		t.Fatal(err)
	}
	if got := list(friend); len(got) != 0 {
		t.Fatalf("former member still sees %+v", got)
	}
}

func mustListHouseID(t *testing.T, app *Application, listID int64) int64 {
	t.Helper()
	list, err := app.DB.GetList(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	return list.HouseID
}

func TestHandleItemsPriceHistory(t *testing.T) {
	app := newTestApplication(t)
	ctx := context.Background()
	owner, _, item := trackedItemFixture(t, app, true)
	stranger := mustCreateTestUser(t, app, "stranger@example.com")
	for _, p := range []float64{100, 100, 95} {
		if _, err := app.DB.RecordPriceObservation(ctx, item.ID, p); err != nil {
			t.Fatal(err)
		}
	}

	get := func(user *models.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/items/"+strconv.FormatInt(item.ID, 10)+"/price-history", nil)
		req.SetPathValue("id", strconv.FormatInt(item.ID, 10))
		rec := httptest.NewRecorder()
		app.handleItemsPriceHistory(rec, withUser(req, user))
		return rec
	}
	if rec := get(stranger); rec.Code == http.StatusOK {
		t.Fatalf("a stranger read the history: %s", rec.Body.String())
	}
	rec := get(owner)
	var history []models.PricePoint
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(history) != 2 || history[0].Price != 100 || history[1].Price != 95 {
		t.Fatalf("history = %+v, want 100 then 95", history)
	}
}

func TestHandleMeUpdatePriceAlertPreferences(t *testing.T) {
	app := newTestApplication(t)
	user := mustCreateTestUser(t, app, "prices@example.com")
	if !user.PriceAlertsEnabled || !user.PriceDropAlertsEnabled || user.PriceIncreaseAlertsEnabled || !user.PriceChangeIndicatorsEnabled {
		t.Fatalf("defaults: %+v", user)
	}

	rec := httptest.NewRecorder()
	app.handleMeUpdate(rec, withUser(httptest.NewRequest(http.MethodPatch, "/api/v1/me", strings.NewReader(
		`{"price_alerts_enabled":false,"price_increase_alerts_enabled":true,"price_change_indicators_enabled":false}`)), user))
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var got models.User
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PriceAlertsEnabled || !got.PriceDropAlertsEnabled || !got.PriceIncreaseAlertsEnabled || got.PriceChangeIndicatorsEnabled {
		t.Fatalf("after PATCH: %+v", got)
	}
}
