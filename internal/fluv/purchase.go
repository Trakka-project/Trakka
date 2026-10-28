package fluv

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"trakka/internal/models"
)

// Bounds of a purchase, Fluv's (its ledger package refuses anything
// larger).
const (
	maxTitleRunes = 100
	maxStoreRunes = 40
	maxQuantity   = 9999
	// maxAmountMinor keeps an absurd price (a typo of a dozen digits) from
	// being sent, and every amount exact as a float64.
	maxAmountMinor = 1 << 50
)

// purchaseBody is the JSON Fluv's POST …/purchases accepts — exactly these
// fields: it refuses unknown ones. Fluv takes the day of the purchase, not
// its time; the exact moment stays in fluv_purchases.purchased_at.
type purchaseBody struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Store       string `json:"store,omitempty"`
	Quantity    int    `json:"quantity"`
	AmountMinor int64  `json:"amount_minor"`
	Date        string `json:"date"`
}

// amountOf is what item cost, in cents: the price paid per unit (or, when
// none was entered, the estimated one — "cost what was estimated", see
// models.Item.ActualPrice) times the quantity. 0 when it has no price.
func amountOf(item *models.Item) int64 {
	unit := item.ActualPrice
	if unit == nil {
		unit = item.Price
	}
	if unit == nil || math.IsNaN(*unit) || math.IsInf(*unit, 0) || *unit <= 0 {
		return 0
	}
	cents := math.Round(*unit * float64(quantityOf(item)) * 100)
	if cents < 1 || cents > maxAmountMinor {
		return 0
	}
	return int64(cents)
}

// quantityOf is the item's quantity as Fluv counts it: at least 1. The
// amount uses the real quantity; Fluv's label only shows up to maxQuantity.
func quantityOf(item *models.Item) int {
	if item.Quantity < 1 {
		return 1
	}
	return item.Quantity
}

func labelQuantity(item *models.Item) int {
	return min(quantityOf(item), maxQuantity)
}

// cleanText makes s acceptable to Fluv, which refuses control characters,
// bidirectional overrides and the BOM in a label rather than cleaning
// them: they become spaces (dropped for the invisible ones), and the result
// is trimmed and cut to maxRunes.
func cleanText(s string, maxRunes int) string {
	s = strings.ToValidUTF8(s, " ")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 0x061C, r == 0x200E, r == 0x200F, r == 0xFEFF, r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
			continue
		case unicode.IsControl(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > maxRunes {
		out = strings.TrimSpace(string([]rune(out)[:maxRunes]))
	}
	return out
}

// titleOf is the item's title as Fluv labels it.
func titleOf(item *models.Item) string {
	if title := cleanText(item.Title, maxTitleRunes); title != "" {
		return title
	}
	return "Trakka"
}

// storeOf is what starts Fluv's label, "‹store› · ‹title›", so that a rule
// on it files the purchase: the name of the list the item was bought from.
func storeOf(list *models.List) string {
	return cleanText(list.Name, maxStoreRunes)
}

// newRef is the reference Fluv knows a purchase by: unique per time an item
// is bought, within Fluv's pattern ^[A-Za-z0-9._:-]{1,64}$.
func newRef(itemID int64) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generating purchase reference: %w", err)
	}
	return fmt.Sprintf("trakka-%d-%s", itemID, hex.EncodeToString(b[:])), nil
}
