// Package catalog holds the stdlib-only domain logic behind quick shopping
// lists (the "Courses"/groceries list type): how two product names are
// recognized as the same product (Key), the fixed set of store aisles items
// are grouped by (Aisles), a keyword-based first guess at an item's aisle
// (GuessAisle, AisleFromCategories), and how a product's purchase history
// turns into a "time to buy it again" suggestion (Assess, RankRepurchases).
//
// It knows nothing about the database or HTTP: internal/db stores what
// internal/handlers computes with it, the same split internal/recurrence
// already follows for recurring items.
//
// static/js/quick-shop.js carries a hand-kept port of Key (productKey) so
// the list view can recognize a duplicate before sending anything; a
// mismatch between the two is harmless — the server's merge in
// POST /api/v1/items is the authority — but keep them close.
package catalog

import (
	"strings"
	"unicode"
)

// foldRunes maps the accented and ligature letters French (and the other
// Latin-script languages a shopping list is likely to contain) uses to
// their unaccented ASCII spelling, so "Pâtes", "PATES" and "pates" share a
// key. Only lowercase forms: Key lowercases first.
var foldRunes = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'ā': "a",
	'ç': "c", 'ć': "c", 'č': "c",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ė': "e", 'ę': "e",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ī': "i",
	'ñ': "n", 'ń': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'ō': "o",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ū': "u",
	'ý': "y", 'ÿ': "y",
	'ś': "s", 'š': "s",
	'ź': "z", 'ż': "z", 'ž': "z",
	'œ': "oe", 'æ': "ae", 'ß': "ss",
}

// Key returns the comparison key of a product or item name: lowercased,
// accents and ligatures folded to ASCII, every run of non-letter/non-digit
// characters (spaces, hyphens, apostrophes, punctuation) collapsed into a
// single space, and trimmed. "Lait demi-écrémé", "lait  demi écrémé" and
// "LAIT DEMI-ECREME" all give "lait demi ecreme".
//
// There is deliberately no stemming: "pâtes" (pasta) and "pâté" differ only
// by a trailing "s" once folded, so "pomme" and "pommes" stay two products.
func Key(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	pendingSpace := false
	for _, r := range strings.ToLower(name) {
		folded, isFolded := foldRunes[r]
		if !isFolded && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			pendingSpace = b.Len() > 0
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		if isFolded {
			b.WriteString(folded)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Aisle keys, in the order a typical (French) supermarket walk passes them:
// fresh produce at the entrance, then bakery, the fresh counters, the dry
// aisles, drinks, non-food, and frozen food last so it stays cold. The
// frontend groups a list "Par rayon" in this order and translates each key
// (items.aisles.<key> in static/locales/*.json).
const (
	AisleProduce   = "produce"
	AisleBakery    = "bakery"
	AisleMeatFish  = "meat_fish"
	AisleDairy     = "dairy"
	AislePantry    = "pantry"
	AisleBreakfast = "breakfast"
	AisleDrinks    = "drinks"
	AisleBaby      = "baby"
	AislePets      = "pets"
	AisleHousehold = "household"
	AisleHygiene   = "hygiene"
	// The non-food aisles of a hypermarket or a DIY store, which the
	// built-in list templates (internal/shoptemplates: back to school,
	// travel, moving, home maintenance) file their items under.
	AisleStationery  = "stationery"
	AisleClothing    = "clothing"
	AisleElectronics = "electronics"
	AisleDIY         = "diy"
	AisleFrozen      = "frozen"
	AisleOther       = "other"
)

// Aisles lists every valid aisle key, in store order. AisleOther is a real,
// storable choice ("I put it under Other on purpose"), distinct from an
// item with no aisle at all, which the frontend shows in the same group
// but which a later guess or a learned aisle may still fill in.
var Aisles = []string{
	AisleProduce, AisleBakery, AisleMeatFish, AisleDairy, AislePantry,
	AisleBreakfast, AisleDrinks, AisleBaby, AislePets, AisleHousehold,
	AisleHygiene, AisleStationery, AisleClothing, AisleElectronics, AisleDIY,
	AisleFrozen, AisleOther,
}

// IsAisle reports whether key is one of Aisles.
func IsAisle(key string) bool {
	for _, a := range Aisles {
		if a == key {
			return true
		}
	}
	return false
}
