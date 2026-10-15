package validate

import (
	"errors"
	"strings"

	"trakka/internal/catalog"
)

// ErrInvalidAisle is returned when a user-supplied aisle isn't one of
// catalog.Aisles.
var ErrInvalidAisle = errors.New("aisle must be one of: " + strings.Join(catalog.Aisles, ", "))

// Aisle trims and validates an item's or a catalog product's store aisle.
// An empty (or whitespace-only) input is valid and returns "", meaning "no
// aisle set"; anything else must be one of catalog.Aisles, as is (the keys
// are stable identifiers the frontend translates, never free text).
func Aisle(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if !catalog.IsAisle(trimmed) {
		return "", ErrInvalidAisle
	}
	return trimmed, nil
}

// ErrInvalidBarcode is returned when a barcode isn't 8 to 14 digits.
var ErrInvalidBarcode = errors.New("barcode must be 8 to 14 digits")

// Barcode validates a scanned or typed product barcode (EAN-8, UPC-A,
// EAN-13, GTIN-14) and returns it in the form the catalog stores: digits
// only, spaces and hyphens a person might type dropped, and a 12-digit
// UPC-A widened to its 13-digit EAN-13 spelling (a leading 0) so the same
// product matches whichever of the two a scanner reported. Only the shape
// is checked, not the check digit: UPC-E codes (8 digits) carry the check
// digit of their expanded UPC-A form, and both scanners already verify it.
// An empty input is valid and returns "".
func Barcode(raw string) (string, error) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-':
		default:
			return "", ErrInvalidBarcode
		}
	}
	code := b.String()
	if code == "" {
		if strings.TrimSpace(raw) == "" {
			return "", nil
		}
		return "", ErrInvalidBarcode
	}
	if len(code) < 8 || len(code) > 14 {
		return "", ErrInvalidBarcode
	}
	if len(code) == 12 {
		code = "0" + code
	}
	return code, nil
}
