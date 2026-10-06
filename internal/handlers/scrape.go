package handlers

import (
	"context"
	"time"

	"trakka/internal/models"
	"trakka/internal/scraper"
)

// scrapeTimeout bounds the total background product lookup kicked off when
// an item's url is set or changes — the network fetch plus the follow-up
// database write(s).
//
// syncScrapeTimeout is the shorter window handleItemsCreate/Update/Patch
// actually wait on before responding: long enough to catch the common case
// (most sites resolve in well under a second) so the API response can
// include the freshly-detected price/image directly and the client never
// has to reload or poll to see it, short enough that item creation still
// feels instant against a slow or unresponsive site. If the lookup is still
// running when syncScrapeTimeout expires, it keeps running in the
// background against the longer scrapeTimeout exactly as before, and the
// response reports price_status "pending" instead of the price itself —
// this is what keeps the feature from ever blocking or failing item
// creation/update outright, it just no longer *always* defers to the
// background path the way it unconditionally used to.
const (
	scrapeTimeout     = 12 * time.Second
	syncScrapeTimeout = 2500 * time.Millisecond

	// maxConcurrentScrapes bounds how many product-page fetches this process
	// has in flight at once. Every item create/update carrying a new url
	// starts one, so without a ceiling an authenticated user could script a
	// burst of item creations and turn the server into an outbound request
	// amplifier — a load generator pointed at a third-party site from
	// Trakka's address, and a pile of concurrent sockets and 2 MiB read
	// buffers on the server itself. Requests over the limit wait for a slot
	// rather than being dropped, and give up with the rest of the lookup when
	// scrapeTimeout expires; the user-visible effect of saturation is simply
	// a price_status of "pending", which the frontend already handles.
	maxConcurrentScrapes = 8
)

// scrapeSem is the semaphore enforcing maxConcurrentScrapes, shared by every
// request goroutine in the process.
var scrapeSem = make(chan struct{}, maxConcurrentScrapes)

// scrapeProductInfo is the single entry point handleItemsCreate/Update/Patch
// call after persisting an item, for an item whose url was just set to
// something new — a brand new item created with a url, or an existing item
// whose url was just changed. previousURL is the item's url before this
// request's changes were applied ("" if it had none); comparing against it
// is what keeps an unrelated edit (retitling, toggling done, resaving the
// same url) from re-triggering a fetch of a page that was already scraped
// (successfully or not) for this exact url.
//
// It looks up whichever of price/image the item is still missing (an item
// can already have a manual price but no image, or vice versa) in a single
// fetch — see scraper.FetchProductInfo — since both come from the same
// page. The lookup always runs in a detached goroutine on its own context
// (never r.Context(), which is canceled the instant the HTTP response is
// written), and only ever writes back via db.UpdateItemPriceIfMissing /
// db.UpdateItemImageIfMissing, which silently do nothing if the item was
// deleted, given a manual value meanwhile, or pointed at a different url
// while the fetch was in flight.
//
// The caller waits up to syncScrapeTimeout for that goroutine to finish
// before responding: on success within the window it mutates
// item.Price/item.PriceAuto/item.ImageURL in place so the caller can return
// them straight in the JSON body; on timeout the goroutine is left running
// exactly as the old fully-async design always did. The returned string is
// price_status exactly as before ("found"/"pending"/"none") — it never
// reports on the image, which is treated as a best-effort enhancement: if
// only the image is still outstanding when syncScrapeTimeout fires (the
// item already had a price), this returns "found" rather than "pending",
// since price detection genuinely isn't pending — the image simply arrives
// silently in the background and shows up next time the item is fetched.
func (app *Application) scrapeProductInfo(item *models.Item, previousURL string) string {
	needsPrice := item.Price == nil
	needsImage := item.ImageURL == nil
	if !needsPrice && !needsImage {
		return "found"
	}
	if item.URL == nil || *item.URL == "" || *item.URL == previousURL {
		if needsPrice {
			return "none"
		}
		return "found"
	}
	itemID, url := item.ID, *item.URL
	// Captured here rather than read from item inside the goroutine below,
	// since item itself may be concurrently mutated by the caller once this
	// function returns (the syncScrapeTimeout race) — these three don't
	// change as a result of scraping, so a snapshot taken now is accurate
	// for the notification check either way.
	listID, title, targetPrice, alertOnPriceDrop := item.ListID, item.Title, item.TargetPrice, item.AlertOnPriceDrop

	done := make(chan *scraper.ProductInfo, 1)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), scrapeTimeout)
		defer cancel()

		// Wait for a slot, or give up if the whole lookup times out first.
		select {
		case scrapeSem <- struct{}{}:
			defer func() { <-scrapeSem }()
		case <-ctx.Done():
			app.Logger.Debug("product lookup skipped: scrape concurrency limit saturated", "item_id", itemID, "url", url)
			done <- nil
			return
		}

		info, err := scraper.FetchProductInfo(ctx, url, app.Logger)
		if err != nil {
			app.Logger.Debug("automatic product lookup found nothing", "item_id", itemID, "url", url, "error", err)
			done <- nil
			return
		}
		if info.Price != nil {
			// The first price seen on the page starts the item's price
			// history (see trackItemPrice), whether or not it fills in the
			// item's price below.
			if _, err := app.DB.RecordPriceObservation(ctx, itemID, *info.Price); err != nil {
				app.Logger.Error("recording scraped price", "item_id", itemID, "error", err)
			}
			if err := app.DB.UpdateItemPriceIfMissing(ctx, itemID, url, *info.Price); err != nil {
				app.Logger.Error("saving automatically scraped price", "item_id", itemID, "error", err)
			} else {
				// needsPrice (checked before this goroutine was even
				// started) means item.Price was nil at the time, so
				// priceAlertCondition was necessarily false beforehand —
				// wasActive is always false here, barring the same
				// vanishingly rare concurrent-write race
				// UpdateItemPriceIfMissing's own doc comment already
				// treats as an acceptable silent no-op.
				app.checkPriceDropAlert(&models.Item{
					ID: itemID, ListID: listID, Title: title,
					Price: info.Price, TargetPrice: targetPrice, AlertOnPriceDrop: alertOnPriceDrop,
				}, false)
			}
		}
		if info.ImageURL != "" {
			if err := app.DB.UpdateItemImageIfMissing(ctx, itemID, url, info.ImageURL); err != nil {
				app.Logger.Error("saving automatically scraped image", "item_id", itemID, "error", err)
			}
		}
		done <- info
	}()

	select {
	case info := <-done:
		if info != nil {
			if needsPrice && info.Price != nil {
				item.Price = info.Price
				item.PriceAuto = true
			}
			if needsImage && info.ImageURL != "" {
				item.ImageURL = &info.ImageURL
			}
		}
		if item.Price != nil {
			return "found"
		}
		return "none"
	case <-time.After(syncScrapeTimeout):
		if needsPrice {
			return "pending"
		}
		return "found"
	}
}
