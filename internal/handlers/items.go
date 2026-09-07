package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/validate"
)

// maxItemQuantity bounds an item's quantity. Nothing enforced an upper
// limit before, so a request could store a quantity of 2^31 and have the
// frontend's price x quantity line totals overflow into meaningless
// figures; a shopping list never legitimately needs more than this.
const maxItemQuantity = 100000

func (app *Application) handleItemsIndex(w http.ResponseWriter, r *http.Request) {
	listIDStr := r.URL.Query().Get("list_id")
	if listIDStr == "" {
		writeError(w, http.StatusBadRequest, "list_id query parameter is required")
		return
	}
	listID, err := strconv.ParseInt(listIDStr, 10, 64)
	if err != nil || listID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid list_id")
		return
	}

	list, err := app.DB.GetList(r.Context(), listID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "list_id does not reference an existing list")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeListAccess(w, r, list, false) {
		return
	}

	items, err := app.DB.ListItemsByList(r.Context(), listID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (app *Application) handleItemsCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ListID                int64    `json:"list_id"`
		Title                 string   `json:"title"`
		URL                   string   `json:"url"`
		Quantity              int      `json:"quantity"`
		Price                 *float64 `json:"price"`
		Position              int      `json:"position"`
		TargetMonth           string   `json:"target_month"`
		DueDate               string   `json:"due_date"`
		RecurrenceRule        string   `json:"recurrence_rule"`
		RecurrenceEndDate     string   `json:"recurrence_end_date"`
		IsUrgent              bool     `json:"is_urgent"`
		RecurrenceLeadMinutes *int     `json:"recurrence_lead_minutes"`
		TargetPrice           *float64 `json:"target_price"`
		AlertOnPriceDrop      bool     `json:"alert_on_price_drop"`
		Labels                []string `json:"labels"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	in.Title = validate.Text(in.Title)
	if in.ListID <= 0 {
		writeError(w, http.StatusBadRequest, "list_id is required")
		return
	}
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
	cleanURL, err := validate.URL(in.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Price != nil && *in.Price < 0 {
		writeError(w, http.StatusBadRequest, "price cannot be negative")
		return
	}
	if in.TargetPrice != nil && *in.TargetPrice < 0 {
		writeError(w, http.StatusBadRequest, "target_price cannot be negative")
		return
	}
	cleanMonth, err := validate.Month(in.TargetMonth)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cleanDueDate, err := validate.Date(in.DueDate)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cleanRecurrenceRule, err := validate.Recurrence(in.RecurrenceRule)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cleanRecurrenceEndDate, err := validate.Date(in.RecurrenceEndDate)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.RecurrenceLeadMinutes != nil && *in.RecurrenceLeadMinutes < 0 {
		writeError(w, http.StatusBadRequest, "recurrence_lead_minutes cannot be negative")
		return
	}
	cleanLabels, err := validate.Labels(in.Labels)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	list, err := app.DB.GetList(r.Context(), in.ListID)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "list_id does not reference an existing list")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeListAccess(w, r, list, true) {
		return
	}

	item, err := app.DB.CreateItem(r.Context(), in.ListID, in.Title, nullableString(cleanURL), in.Quantity, in.Price, false, in.Position,
		nullableString(cleanMonth), nullableString(cleanDueDate), nullableString(cleanRecurrenceRule), nullableString(cleanRecurrenceEndDate), in.IsUrgent, in.RecurrenceLeadMinutes,
		in.TargetPrice, in.AlertOnPriceDrop)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	// SetItemLabels is a separate call (see its own doc comment) rather than
	// another CreateItem parameter — only worth the extra write when there's
	// actually something to store, since a fresh item already defaults to an
	// empty label set.
	if len(cleanLabels) > 0 {
		item, err = app.DB.SetItemLabels(r.Context(), item.ID, cleanLabels)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
	}
	// A brand new item has no "before" state to compare against, so it can
	// only ever transition from inactive to active — see
	// checkPriceDropAlert's wasActive contract. Checked against the
	// manually-supplied price only, before scrapeProductInfo runs: if the
	// scraper ends up filling in the price instead (item.Price was still
	// nil here), that event is checked separately below, since it's
	// otherwise reported by scrapeProductInfo's own background goroutine
	// (see scrape.go) and re-checking it here would double-fire the push.
	app.checkPriceDropAlert(item, false)
	hadNoPriceBeforeScrape := item.Price == nil
	item.PriceStatus = app.scrapeProductInfo(item, "")
	if hadNoPriceBeforeScrape && item.Price != nil && priceAlertCondition(item) {
		// The scraper found and persisted this price within the request's
		// bounded wait — its own goroutine already fired the push
		// notification (see scrapeProductInfo), this just carries the
		// toast signal through to this specific response.
		item.PriceAlertTriggered = true
	}
	app.notifyListChange(list, userFromContext(r), item.Title, false)
	writeJSON(w, http.StatusCreated, item)
}

func (app *Application) handleItemsShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	item, err := app.DB.GetItem(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeItemAccess(w, r, item.ListID, false) {
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (app *Application) handleItemsUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	existing, err := app.DB.GetItem(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeItemAccess(w, r, existing.ListID, true) {
		return
	}
	previousURL := stringValue(existing.URL)
	// See checkPriceDropAlert's wasActive contract: this is the item's
	// target-price condition before any of this request's changes apply,
	// captured up front alongside previousURL for the same reason.
	wasPriceAlertActive := priceAlertCondition(existing)

	var in struct {
		Title                 string   `json:"title"`
		URL                   string   `json:"url"`
		Quantity              int      `json:"quantity"`
		Price                 *float64 `json:"price"`
		Done                  bool     `json:"done"`
		Position              int      `json:"position"`
		TargetMonth           string   `json:"target_month"`
		DueDate               string   `json:"due_date"`
		RecurrenceRule        string   `json:"recurrence_rule"`
		RecurrenceEndDate     string   `json:"recurrence_end_date"`
		IsUrgent              bool     `json:"is_urgent"`
		RecurrenceLeadMinutes *int     `json:"recurrence_lead_minutes"`
		TargetPrice           *float64 `json:"target_price"`
		AlertOnPriceDrop      bool     `json:"alert_on_price_drop"`
		Labels                []string `json:"labels"`
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
	cleanURL, err := validate.URL(in.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Price != nil && *in.Price < 0 {
		writeError(w, http.StatusBadRequest, "price cannot be negative")
		return
	}
	if in.TargetPrice != nil && *in.TargetPrice < 0 {
		writeError(w, http.StatusBadRequest, "target_price cannot be negative")
		return
	}
	cleanMonth, err := validate.Month(in.TargetMonth)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cleanDueDate, err := validate.Date(in.DueDate)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cleanRecurrenceRule, err := validate.Recurrence(in.RecurrenceRule)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cleanRecurrenceEndDate, err := validate.Date(in.RecurrenceEndDate)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.RecurrenceLeadMinutes != nil && *in.RecurrenceLeadMinutes < 0 {
		writeError(w, http.StatusBadRequest, "recurrence_lead_minutes cannot be negative")
		return
	}
	cleanLabels, err := validate.Labels(in.Labels)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// A scraped image is tied to the url it was found on: if the url just
	// changed to something new, the existing image no longer describes it
	// and must not carry over, exactly like price_auto resetting to false
	// on any full update.
	imageURL := existing.ImageURL
	if cleanURL != previousURL {
		imageURL = nil
	}

	// A recurring item being checked off (false → true) doesn't stay done —
	// see applyRecurrenceCompletion — so the done/due_date actually
	// persisted below may differ from what was requested. This is computed
	// against a scratch models.Item rather than existing/in directly so the
	// same helper can be shared with handleItemsPatch.
	advanced := &models.Item{
		Done:              in.Done,
		DueDate:           nullableString(cleanDueDate),
		RecurrenceRule:    nullableString(cleanRecurrenceRule),
		RecurrenceEndDate: nullableString(cleanRecurrenceEndDate),
	}
	// Captured before applyRecurrenceCompletion runs: for a recurring item,
	// that call flips advanced.Done back to false the moment it detects this
	// same false→true transition, so checking advanced.Done afterward could
	// no longer tell a genuine check-off apart from an item that was never
	// touched — see notifyListChange below, which needs to know a check-off
	// happened at all, regardless of whether the item then immediately
	// un-checked itself for its next occurrence.
	justCompleted := !existing.Done && in.Done
	applyRecurrenceCompletion(advanced, existing.Done)

	item, err := app.DB.UpdateItem(r.Context(), id, in.Title, nullableString(cleanURL), in.Quantity, in.Price, false, imageURL, advanced.Done, in.Position,
		nullableString(cleanMonth), advanced.DueDate, advanced.RecurrenceRule, advanced.RecurrenceEndDate, in.IsUrgent, in.RecurrenceLeadMinutes,
		in.TargetPrice, in.AlertOnPriceDrop)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	// PUT is a full replace: an omitted labels field resets it to empty,
	// the same convention type/custom_category_id already follow here — see
	// SetItemLabels' own doc comment for why this is a separate call rather
	// than another UpdateItem parameter.
	item, err = app.DB.SetItemLabels(r.Context(), item.ID, cleanLabels)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	// Checked against the manually-supplied price only, before
	// scrapeProductInfo runs — see the identical reasoning in
	// handleItemsCreate for why a scraper-filled price is checked
	// separately below instead, to avoid double-firing the push.
	app.checkPriceDropAlert(item, wasPriceAlertActive)
	hadNoPriceBeforeScrape := item.Price == nil
	item.PriceStatus = app.scrapeProductInfo(item, previousURL)
	if hadNoPriceBeforeScrape && item.Price != nil && priceAlertCondition(item) {
		item.PriceAlertTriggered = true
	}
	// Only a genuine check-off notifies (see notifyListChange's own doc
	// comment for why this is scoped to "add or check off" and not every
	// field edit) — an ordinary PUT that never touched Done at all must not
	// fire a "checked an item" push.
	if justCompleted {
		if list, listErr := app.DB.GetList(r.Context(), item.ListID); listErr == nil {
			app.notifyListChange(list, userFromContext(r), item.Title, true)
		}
	}
	writeJSON(w, http.StatusOK, item)
}

// handleItemsPatch applies a partial update (e.g. just toggling "done"),
// the common case when checking an item off a list.
func (app *Application) handleItemsPatch(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var in struct {
		Title                 *string         `json:"title"`
		URL                   *string         `json:"url"`
		Quantity              *int            `json:"quantity"`
		Price                 json.RawMessage `json:"price"`
		Done                  *bool           `json:"done"`
		Position              *int            `json:"position"`
		TargetMonth           *string         `json:"target_month"`
		DueDate               *string         `json:"due_date"`
		RecurrenceRule        *string         `json:"recurrence_rule"`
		RecurrenceEndDate     *string         `json:"recurrence_end_date"`
		IsUrgent              *bool           `json:"is_urgent"`
		RecurrenceLeadMinutes json.RawMessage `json:"recurrence_lead_minutes"`
		TargetPrice           json.RawMessage `json:"target_price"`
		AlertOnPriceDrop      *bool           `json:"alert_on_price_drop"`
		Labels                *[]string       `json:"labels"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}

	item, err := app.DB.GetItem(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeItemAccess(w, r, item.ListID, true) {
		return
	}
	previousURL := stringValue(item.URL)
	previousDone := item.Done
	// See checkPriceDropAlert's wasActive contract: captured before any of
	// this request's mutations are applied below, the same reasoning as
	// previousURL/previousDone.
	wasPriceAlertActive := priceAlertCondition(item)

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
	if in.URL != nil {
		cleanURL, err := validate.URL(*in.URL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		newURL := nullableString(cleanURL)
		// See handleItemsUpdate: a scraped image is tied to the url it was
		// found on, so changing url invalidates it.
		if stringValue(newURL) != previousURL {
			item.ImageURL = nil
		}
		item.URL = newURL
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
	// in.Price is nil when "price" was absent from the request body (leave
	// item.Price/PriceAuto untouched); present-but-"null" clears it;
	// present with a number sets it. A plain *float64 can't tell "absent"
	// apart from "explicit null" since both decode to a nil pointer, hence
	// RawMessage. Either way a price supplied in the request is a manual
	// value, so PriceAuto always resets to false here — the only path that
	// ever sets it true is the scraper's own UpdateItemPriceIfMissing.
	if in.Price != nil {
		if string(in.Price) == "null" {
			item.Price = nil
			item.PriceAuto = false
		} else {
			var price float64
			if err := json.Unmarshal(in.Price, &price); err != nil {
				writeError(w, http.StatusBadRequest, "price must be a number")
				return
			}
			if price < 0 {
				writeError(w, http.StatusBadRequest, "price cannot be negative")
				return
			}
			item.Price = &price
			item.PriceAuto = false
		}
	}
	if in.Done != nil {
		item.Done = *in.Done
	}
	if in.Position != nil {
		item.Position = *in.Position
	}
	// A nil in.TargetMonth means the field was absent from the request
	// (leave item.TargetMonth untouched, e.g. a plain "done" toggle);
	// present-but-empty clears it back to unscheduled, mirroring how URL is
	// handled above.
	if in.TargetMonth != nil {
		cleanMonth, err := validate.Month(*in.TargetMonth)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		item.TargetMonth = nullableString(cleanMonth)
	}
	if in.DueDate != nil {
		cleanDueDate, err := validate.Date(*in.DueDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		item.DueDate = nullableString(cleanDueDate)
	}
	if in.RecurrenceRule != nil {
		cleanRecurrenceRule, err := validate.Recurrence(*in.RecurrenceRule)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		item.RecurrenceRule = nullableString(cleanRecurrenceRule)
		if item.RecurrenceRule == nil {
			// Turning recurrence off entirely: due_date/recurrence_end_date
			// only mean anything while the item is recurring.
			item.DueDate = nil
			item.RecurrenceEndDate = nil
		}
	}
	if in.RecurrenceEndDate != nil {
		cleanRecurrenceEndDate, err := validate.Date(*in.RecurrenceEndDate)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		item.RecurrenceEndDate = nullableString(cleanRecurrenceEndDate)
	}
	if in.IsUrgent != nil {
		item.IsUrgent = *in.IsUrgent
	}
	// Same absent/null/number three-way as Price above: absent leaves
	// item.TargetPrice untouched, "null" clears the threshold, a number
	// sets it.
	if in.TargetPrice != nil {
		if string(in.TargetPrice) == "null" {
			item.TargetPrice = nil
		} else {
			var targetPrice float64
			if err := json.Unmarshal(in.TargetPrice, &targetPrice); err != nil {
				writeError(w, http.StatusBadRequest, "target_price must be a number")
				return
			}
			if targetPrice < 0 {
				writeError(w, http.StatusBadRequest, "target_price cannot be negative")
				return
			}
			item.TargetPrice = &targetPrice
		}
	}
	if in.AlertOnPriceDrop != nil {
		item.AlertOnPriceDrop = *in.AlertOnPriceDrop
	}
	// A nil in.Labels means the field was absent (leave item.Labels
	// untouched, e.g. a plain "done" toggle); a present value (including an
	// explicit empty array) replaces the label set entirely — there's no
	// meaningful difference between "clear the labels" and "absent" worth a
	// three-way RawMessage distinction the way Price/TargetPrice need one.
	var cleanLabels []string
	if in.Labels != nil {
		var err error
		cleanLabels, err = validate.Labels(*in.Labels)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		item.Labels = cleanLabels
	}
	// Same absent/null/number three-way as Price above: absent leaves
	// item.RecurrenceLeadMinutes untouched, "null" clears the per-item
	// override back to "use the instance default", a number sets it.
	if in.RecurrenceLeadMinutes != nil {
		if string(in.RecurrenceLeadMinutes) == "null" {
			item.RecurrenceLeadMinutes = nil
		} else {
			var minutes int
			if err := json.Unmarshal(in.RecurrenceLeadMinutes, &minutes); err != nil {
				writeError(w, http.StatusBadRequest, "recurrence_lead_minutes must be a number")
				return
			}
			if minutes < 0 {
				writeError(w, http.StatusBadRequest, "recurrence_lead_minutes cannot be negative")
				return
			}
			item.RecurrenceLeadMinutes = &minutes
		}
	}

	// Captured before applyRecurrenceCompletion runs — see the identical
	// comment in handleItemsUpdate for why this can't be read off
	// item.Done after that call for a recurring item.
	justCompleted := !previousDone && item.Done

	// See applyRecurrenceCompletion: a recurring item being checked off
	// (false → true) here advances due_date and flips Done back to false
	// instead of actually persisting as done.
	applyRecurrenceCompletion(item, previousDone)

	updated, err := app.DB.UpdateItem(r.Context(), id, item.Title, item.URL, item.Quantity, item.Price, item.PriceAuto, item.ImageURL, item.Done, item.Position,
		item.TargetMonth, item.DueDate, item.RecurrenceRule, item.RecurrenceEndDate, item.IsUrgent, item.RecurrenceLeadMinutes,
		item.TargetPrice, item.AlertOnPriceDrop)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	// PATCH's "absent = untouched" convention: only write labels when the
	// field was actually present in the request (see SetItemLabels' own doc
	// comment for why this is a separate call).
	if in.Labels != nil {
		updated, err = app.DB.SetItemLabels(r.Context(), updated.ID, cleanLabels)
		if err != nil {
			app.serverError(w, r, err)
			return
		}
	}
	// Checked against the manually-supplied price only, before
	// scrapeProductInfo runs — see the identical reasoning in
	// handleItemsCreate for why a scraper-filled price is checked
	// separately below instead, to avoid double-firing the push.
	app.checkPriceDropAlert(updated, wasPriceAlertActive)
	hadNoPriceBeforeScrape := updated.Price == nil
	updated.PriceStatus = app.scrapeProductInfo(updated, previousURL)
	if hadNoPriceBeforeScrape && updated.Price != nil && priceAlertCondition(updated) {
		updated.PriceAlertTriggered = true
	}
	if justCompleted {
		if list, listErr := app.DB.GetList(r.Context(), updated.ListID); listErr == nil {
			app.notifyListChange(list, userFromContext(r), updated.Title, true)
		}
	}
	writeJSON(w, http.StatusOK, updated)
}

func (app *Application) handleItemsDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	existing, err := app.DB.GetItem(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeItemAccess(w, r, existing.ListID, true) {
		return
	}

	if err := app.DB.DeleteItem(r.Context(), id); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "item not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
