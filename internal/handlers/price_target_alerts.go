package handlers

import (
	"context"
	"fmt"

	"trakka/internal/db"
	"trakka/internal/models"
)

// priceAlertCondition reports whether item's own user-set "notify me when
// the price drops" threshold currently holds: alerting is opted in
// (AlertOnPriceDrop), a threshold is set (TargetPrice), the item has a
// price at all, and that price is at or below the threshold. This is a
// pure function of the item's current fields, independent of any request —
// see checkPriceDropAlert for how it's used to detect a false→true
// transition rather than re-notifying on every read.
func priceAlertCondition(item *models.Item) bool {
	return item.AlertOnPriceDrop && item.TargetPrice != nil && item.Price != nil && *item.Price <= *item.TargetPrice
}

// checkPriceDropAlert is the single entry point every price-mutating code
// path calls after persisting a new price (or a changed target_price/
// alert_on_price_drop) for item: handleItemsCreate/Update/Patch
// (items.go), the background scraper's initial price fill-in
// (scrapeProductInfo, scrape.go), and accepting an auto-detected
// price_alerts row (handlePriceAlertsUpdate, price_alerts.go). wasActive
// must be priceAlertCondition(item) evaluated against the item's state
// *before* this request/scrape applied its changes — callers compute it
// early, the same way handleItemsUpdate/Patch already capture
// previousDone before applying a recurring item's completion logic.
//
// On a false→true transition it sets item.PriceAlertTriggered (a
// transient, response-only field — see models.Item.PriceAlertTriggered) so
// the calling handler's JSON response can show an immediate in-app toast,
// reports true, and fires a push notification to every user with access to
// the item's list who hasn't turned price alerts off
// (db.ListNotificationRecipientsFor with NotifyPriceTargets — the "Alertes
// de prix" master switch; the item's own target price is already the
// opt-in), excluding nobody — like a recurring
// due-date reminder, a price drop is not the result of any one person's
// action even when a manual price edit happens to be what triggered the
// check). Every other case (condition wasn't met, or was already true
// before this change) is a silent no-op — this must never fail or delay
// the request that triggered it, so delivery runs in its own detached
// goroutine on a bounded background context, mirroring notifyListChange.
func (app *Application) checkPriceDropAlert(item *models.Item, wasActive bool) bool {
	if wasActive || !priceAlertCondition(item) {
		return false
	}
	item.PriceAlertTriggered = true

	itemID, listID, title := item.ID, item.ListID, item.Title
	price, target := *item.Price, *item.TargetPrice

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pushSendTimeout)
		defer cancel()

		recipients, err := app.DB.ListNotificationRecipientsFor(ctx, listID, 0, db.NotifyPriceTargets)
		if err != nil {
			app.Logger.Error("listing notification recipients for price alert", "item_id", itemID, "list_id", listID, "error", err)
			return
		}
		if len(recipients) == 0 {
			return
		}

		payload := pushPayload{
			Title: "🔥 Bonne affaire !",
			Body:  fmt.Sprintf("« %s » est passé à %.2f € (sous votre seuil de %.2f €)", title, price, target),
			URL:   fmt.Sprintf("/?list=%d", listID),
		}
		app.sendToUsers(ctx, recipients, payload)
	}()
	return true
}
