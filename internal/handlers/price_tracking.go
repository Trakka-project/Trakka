package handlers

import (
	"context"
	"fmt"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/scraper"
)

// dealSearchTimeout bounds one Dealabs search within a price check, on top
// of the item's own page fetch (priceCheckTimeout).
const dealSearchTimeout = 8 * time.Second

// Price notification inbox retention (db.PrunePriceNotifications): a read
// entry is kept a month, an unread one three.
const (
	priceNotificationReadRetentionDays = 30
	priceNotificationRetentionDays     = 90
)

// trackItemPrice is the one price check both background scans
// (RunPriceAlertScan, RunTargetPriceScan) and the on-demand
// POST /api/v1/items/{id}/price-check run for an item:
//
//  1. Fetch the item's own page. Whatever price it shows is recorded in the
//     item's price history (db.RecordPriceObservation). The page may be a
//     Dealabs deal the user accepted (see db.AcceptPriceAlert): once that
//     deal has expired its price is no longer followed, and the expiry is
//     announced once (announceDealExpired).
//  2. If the item's price follows its page — it has no price yet, the price
//     was found by the scraper (price_auto), or it has an active target
//     price — a different price is applied (db.UpdateItemPriceFromScan,
//     which also records the movement for the list's green/red indicator),
//     the move is announced (notifyPriceChange) and the target price
//     re-checked (checkPriceDropAlert). A price the user typed in is never
//     overwritten: a lower one on the page is proposed instead
//     (proposeBetterPrice), as before.
//  3. If deal search is on, look the item's title up on Dealabs and propose
//     the cheapest matching active deal below the current price.
//
// Returns the item as it stands after the check. Every failure to find
// anything (network error, blocked page, no price, no deal) is "nothing to
// do", never an error — only a genuine database error is returned.
func (app *Application) trackItemPrice(ctx context.Context, item *models.Item) (*models.Item, error) {
	if item.URL == nil || *item.URL == "" {
		return item, nil
	}
	current := *item

	fetchCtx, cancel := context.WithTimeout(ctx, priceCheckTimeout)
	info, err := scraper.FetchProductInfo(fetchCtx, *item.URL, app.Logger)
	cancel()
	if err != nil {
		app.Logger.Debug("price check found nothing", "item_id", item.ID, "url", *item.URL, "error", err)
	} else if info.DealExpired {
		if err := app.announceDealExpired(ctx, &current); err != nil {
			return item, err
		}
	} else if info.Price != nil {
		if err := app.applyObservedPrice(ctx, &current, *info.Price); err != nil {
			return item, err
		}
	}

	if app.Config.DealSearchEnabled && current.Price != nil {
		if err := app.searchBetterDeal(ctx, &current); err != nil {
			return &current, err
		}
	}
	return &current, nil
}

// applyObservedPrice is step 1–2 of trackItemPrice for a price observed on
// item's own page, updating item in place when the price is applied.
func (app *Application) applyObservedPrice(ctx context.Context, item *models.Item, observed float64) error {
	if _, err := app.DB.RecordPriceObservation(ctx, item.ID, observed); err != nil {
		app.Logger.Error("recording observed price", "item_id", item.ID, "error", err)
	}

	followsPage := item.Price == nil || item.PriceAuto || (item.AlertOnPriceDrop && item.TargetPrice != nil)
	if !followsPage {
		if observed < *item.Price {
			return app.proposeBetterPrice(ctx, item, observed, *item.URL)
		}
		return nil
	}
	if item.Price != nil && *item.Price == observed {
		return nil
	}

	// Captured before the new price lands — see checkPriceDropAlert.
	wasActive := priceAlertCondition(item)
	oldPrice := item.Price

	applied, err := app.DB.UpdateItemPriceFromScan(ctx, item.ID, *item.URL, oldPrice, observed)
	if err != nil || !applied {
		return err
	}

	item.Price = &observed
	item.PriceAuto = true
	targetReached := app.checkPriceDropAlert(item, wasActive)
	if oldPrice == nil {
		return nil
	}
	changedAt := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	item.PreviousPrice = oldPrice
	item.PriceChangedAt = &changedAt

	kind := db.PriceNotificationIncrease
	if observed < *oldPrice {
		kind = db.PriceNotificationDrop
	}
	// A target price reached just now already pushed "Bonne affaire" to
	// everyone; the drop still lands in the in-app inbox, without a second
	// push about the same change.
	app.notifyPriceChange(item, kind, *oldPrice, observed, nil, !targetReached)
	return nil
}

// announceDealExpired tells the item's list, once per deal url, that the
// Dealabs deal its url points to has expired — the price it shows can no
// longer be had, and the item needs a new link (or a new deal, which the
// deal search keeps looking for).
func (app *Application) announceDealExpired(ctx context.Context, item *models.Item) error {
	told, err := app.DB.HasPriceNotification(ctx, item.ID, db.PriceNotificationExpired, *item.URL)
	if err != nil || told {
		return err
	}
	price := 0.0
	if item.Price != nil {
		price = *item.Price
	}
	url := *item.URL
	app.notifyPriceChange(item, db.PriceNotificationExpired, price, price, &url, true)
	return nil
}

// searchBetterDeal is step 3 of trackItemPrice: a Dealabs search for item's
// title, proposing the best matching deal (scraper.BestDeal) cheaper than
// item's current price.
func (app *Application) searchBetterDeal(ctx context.Context, item *models.Item) error {
	query, ok := scraper.DealSearchQuery(item.Title)
	if !ok {
		return nil
	}
	searchCtx, cancel := context.WithTimeout(ctx, dealSearchTimeout)
	deals, err := scraper.SearchDealabs(searchCtx, query)
	cancel()
	if err != nil {
		app.Logger.Debug("deal search found nothing", "item_id", item.ID, "error", err)
		return nil
	}
	deal, ok := scraper.BestDeal(deals, query, *item.Price)
	if !ok {
		return nil
	}
	return app.proposeBetterPrice(ctx, item, deal.Price, deal.URL)
}

// proposeBetterPrice records found (at sourceURL) as a pending price alert
// for item, to accept or reject from the 🔔 drawer, and announces it — once:
// nothing happens if the item already has a pending alert, or ever had one
// for the same price at the same place (so a rejected deal is not proposed
// again on every scan that still sees it).
func (app *Application) proposeBetterPrice(ctx context.Context, item *models.Item, found float64, sourceURL string) error {
	seen, err := app.DB.HasPriceAlertForSource(ctx, item.ID, sourceURL, found)
	if err != nil || seen {
		return err
	}
	created, err := app.DB.CreatePriceAlertIfNonePending(ctx, item.ID, *item.Price, found, sourceURL)
	if err != nil || !created {
		return err
	}
	app.notifyPriceChange(item, db.PriceNotificationDeal, *item.Price, found, &sourceURL, true)
	return nil
}

// notifyPriceChange tells every user with access to item's list who wants
// this kind of price alert (Paramètres → "Alertes de prix": a drop or a
// better deal under price_drop_alerts_enabled, an increase under
// price_increase_alerts_enabled, all of them — and an expired deal — under
// the price_alerts_enabled master switch) about it: always as an entry in their in-app inbox
// (price_notifications, shown in the 🔔 drawer), and as a push notification
// too when push is true — sendToUsers then reaches whichever of them have a
// subscription. The inbox entry is what makes the alert independent of
// push: with push turned off, never set up, or not configured on this
// instance at all, it is still waiting the next time Trakka is opened.
// Runs detached on its own bounded context, like checkPriceDropAlert.
func (app *Application) notifyPriceChange(item *models.Item, kind string, oldPrice, newPrice float64, sourceURL *string, push bool) {
	itemID, listID, title := item.ID, item.ListID, item.Title
	pref := db.NotifyPriceDrops
	switch kind {
	case db.PriceNotificationIncrease:
		pref = db.NotifyPriceIncreases
	case db.PriceNotificationExpired:
		pref = db.NotifyPriceDealGone
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pushSendTimeout)
		defer cancel()

		recipients, err := app.DB.ListNotificationRecipientsFor(ctx, listID, 0, pref)
		if err != nil {
			app.Logger.Error("listing price alert recipients", "item_id", itemID, "list_id", listID, "error", err)
			return
		}
		if len(recipients) == 0 {
			return
		}
		if err := app.DB.CreatePriceNotifications(ctx, recipients, itemID, kind, oldPrice, newPrice, sourceURL); err != nil {
			app.Logger.Error("queuing in-app price notifications", "item_id", itemID, "error", err)
		}
		if push {
			app.sendToUsers(ctx, recipients, priceChangePayload(kind, title, oldPrice, newPrice, listID))
		}
	}()
}

// priceChangePayload is the push notification for notifyPriceChange.
// French only, like every other push (see sendToUsers).
func priceChangePayload(kind, title string, oldPrice, newPrice float64, listID int64) pushPayload {
	payload := pushPayload{URL: fmt.Sprintf("/?list=%d", listID)}
	switch kind {
	case db.PriceNotificationDrop:
		payload.Title = "📉 Baisse de prix"
		payload.Body = fmt.Sprintf("« %s » : %.2f € → %.2f €", title, oldPrice, newPrice)
	case db.PriceNotificationIncrease:
		payload.Title = "📈 Hausse de prix"
		payload.Body = fmt.Sprintf("« %s » : %.2f € → %.2f €", title, oldPrice, newPrice)
	case db.PriceNotificationExpired:
		payload.Title = "⌛ Bon plan expiré"
		payload.Body = fmt.Sprintf("« %s » : le bon plan à %.2f € n'est plus disponible", title, newPrice)
	default:
		payload.Title = "💡 Meilleur prix trouvé"
		payload.Body = fmt.Sprintf("« %s » à %.2f € au lieu de %.2f €", title, newPrice, oldPrice)
	}
	return payload
}

// runPriceTracking runs trackItemPrice over items, pausing
// targetPriceScanDelay between two of them (politeness towards the
// merchants' sites and Dealabs, see its doc comment) and stopping promptly
// once ctx is canceled. One item's failure is logged and never stops the
// rest.
func (app *Application) runPriceTracking(ctx context.Context, scan string, items []*models.Item) {
	app.Logger.Info("running "+scan, "item_count", len(items))
	for i, item := range items {
		if ctx.Err() != nil {
			return
		}
		if _, err := app.trackItemPrice(ctx, item); err != nil {
			app.Logger.Error(scan+" check failed", "item_id", item.ID, "error", err)
		}
		if i < len(items)-1 && sleepOrCanceled(ctx, targetPriceScanDelay) {
			return
		}
	}
}
