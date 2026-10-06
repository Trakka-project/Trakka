package scraper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// This file is the deal-site half of price tracking: FetchProductInfo only
// ever reads the item's own page, while SearchDealabs looks the item's name
// up on Dealabs (www.dealabs.com, the French deal-sharing community) for an
// active deal at a lower price elsewhere.
//
// Dealabs is the one aggregator this can use. Idealo and LeDenicheur both
// answer an automated request with a bot wall (403), and LeDenicheur's
// robots.txt disallows /search outright; Google Shopping has no public
// search API. Dealabs' robots.txt allows /search?q= but disallows its
// filter parameters (hide_expired, sortBy, priceFrom, ...), so the search
// sends only q= and every filtering — expired deals, relevance, price
// sanity — happens here.

const (
	dealabsSearchURL = "https://www.dealabs.com/search"
	dealabsDealURL   = "https://www.dealabs.com/bons-plans/"

	// dealabsThreadComponent is the name of the Vue component Dealabs
	// server-renders once per search result, carrying the deal as JSON in
	// its data-vue3 attribute.
	dealabsThreadComponent = "ThreadMainListItemNormalizer"

	// maxDealQueryTokens caps how many words of an item's title are sent as
	// the query: a long, pasted marketing title would otherwise match no
	// deal at all, since every query word must appear in a deal's title.
	maxDealQueryTokens = 6

	// minDealPriceRatio is the lowest a deal's price may be relative to the
	// item's current price and still count as the same product. Below half
	// the price, a deal whose title happens to contain every query word is
	// far more likely an accessory ("Coussinets pour Sony WH-1000XM5") or a
	// different, smaller variant than a genuine bargain.
	minDealPriceRatio = 0.5
)

// Deal is one active offer found on a deal site.
type Deal struct {
	Title    string
	Price    float64
	URL      string
	Merchant string
}

// dealabsThread is the subset of a Dealabs search result's JSON this
// package reads.
type dealabsThread struct {
	ThreadID  string   `json:"threadId"`
	TitleSlug string   `json:"titleSlug"`
	Title     string   `json:"title"`
	Type      string   `json:"type"`
	Status    string   `json:"status"`
	IsExpired bool     `json:"isExpired"`
	IsLocal   bool     `json:"isLocal"`
	DeletedAt any      `json:"deletedAt"`
	Price     *float64 `json:"price"`
	Merchant  *struct {
		Name string `json:"merchantName"`
	} `json:"merchant"`
}

var (
	dealabsThreadIDPattern = regexp.MustCompile(`^[0-9]{1,12}$`)
	dealabsSlugPattern     = regexp.MustCompile(`^[a-z0-9-]{1,200}$`)
)

// SearchDealabs searches Dealabs for query (see DealSearchQuery for how an
// item title becomes one) and returns its active deals that carry a price.
// Like FetchProductInfo it goes through the SSRF-guarded httpClient, and
// every failure (network, blocked, unexpected markup) is a non-nil error
// callers treat as "nothing found".
func SearchDealabs(ctx context.Context, query string) ([]Deal, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty deal search query")
	}
	searchURL := dealabsSearchURL + "?" + url.Values{"q": {query}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building deal search request: %w", err)
	}
	setBrowserHeaders(req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searching dealabs: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("searching dealabs: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("reading dealabs search results: %w", err)
	}
	return parseDealabsDeals(bytes.NewReader(body))
}

// parseDealabsDeals extracts every active, priced deal from a Dealabs
// search results page. A page with no results returns an empty slice.
func parseDealabsDeals(r io.Reader) ([]Deal, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("parsing dealabs search results: %w", err)
	}

	deals := []Deal{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if raw, ok := attrValue(n, "data-vue3"); ok {
				if deal, ok := dealFromVueProps(raw); ok {
					deals = append(deals, deal)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return deals, nil
}

// live reports whether a deal is still one the user can get: an active,
// non-expired, non-deleted deal with a price.
func (t *dealabsThread) live() bool {
	return t.Type == "Deal" && t.Status == "Activated" && !t.IsExpired && t.DeletedAt == nil &&
		t.Price != nil && *t.Price > 0
}

// isDealabsDealURL reports whether u is a Dealabs deal page
// (https://www.dealabs.com/bons-plans/<slug>-<id>) — the URL an item gets
// when the user accepts a Dealabs deal (see SearchDealabs). Dealabs never
// exposes the merchant's own product link to a crawler (only a /visit/
// redirect, which its robots.txt disallows), so the deal page is what a
// later price check follows.
func isDealabsDealURL(u *url.URL) bool {
	return strings.EqualFold(u.Hostname(), "www.dealabs.com") && strings.HasPrefix(u.Path, "/bons-plans/")
}

// dealabsStateMarker introduces the JSON state Dealabs server-renders into
// every deal page; its threadDetail key is the deal itself.
const dealabsStateMarker = "window.__INITIAL_STATE__ = "

// dealabsDealState extracts a deal page's own deal (threadDetail) from its
// window.__INITIAL_STATE__ script, reporting false when the page has none.
func dealabsDealState(body []byte) (*dealabsThread, bool) {
	i := bytes.Index(body, []byte(dealabsStateMarker))
	if i < 0 {
		return nil, false
	}
	var state struct {
		ThreadDetail *dealabsThread `json:"threadDetail"`
	}
	// Decode reads exactly one JSON value and ignores the rest of the
	// script after it.
	if err := json.NewDecoder(bytes.NewReader(body[i+len(dealabsStateMarker):])).Decode(&state); err != nil || state.ThreadDetail == nil {
		return nil, false
	}
	return state.ThreadDetail, true
}

// dealFromVueProps decodes one data-vue3 attribute, reporting false unless
// it is a search result for a deal that is still live, has a price, and has
// an id/slug that build a well-formed deal URL. Local deals (one physical
// store's in-person offer) are left out: they are not a price the user can
// generally get.
func dealFromVueProps(raw string) (Deal, bool) {
	var component struct {
		Name  string `json:"name"`
		Props struct {
			Thread *dealabsThread `json:"thread"`
		} `json:"props"`
	}
	if err := json.Unmarshal([]byte(raw), &component); err != nil || component.Name != dealabsThreadComponent {
		return Deal{}, false
	}
	t := component.Props.Thread
	if t == nil || !t.live() || t.IsLocal {
		return Deal{}, false
	}
	if !dealabsThreadIDPattern.MatchString(t.ThreadID) || !dealabsSlugPattern.MatchString(t.TitleSlug) {
		return Deal{}, false
	}
	deal := Deal{
		Title: strings.TrimSpace(t.Title),
		Price: *t.Price,
		URL:   dealabsDealURL + t.TitleSlug + "-" + t.ThreadID,
	}
	if t.Merchant != nil {
		deal.Merchant = strings.TrimSpace(t.Merchant.Name)
	}
	return deal, true
}

// dealStopWords are words left out of a query and ignored when matching:
// they say nothing about which product is meant.
var dealStopWords = map[string]bool{
	"le": true, "la": true, "les": true, "un": true, "une": true, "des": true, "de": true, "du": true,
	"et": true, "ou": true, "pour": true, "avec": true, "sans": true, "en": true, "au": true, "aux": true,
	"sur": true, "par": true, "the": true, "an": true, "of": true, "for": true, "with": true, "and": true,
}

// accentFold maps the accented letters of French product names to their
// plain form, so "réduction" and "reduction" are the same word.
var accentFold = strings.NewReplacer(
	"à", "a", "â", "a", "ä", "a", "á", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"î", "i", "ï", "i", "í", "i", "ô", "o", "ö", "o", "ó", "o", "ù", "u", "û", "u", "ü", "u", "ú", "u",
	"ç", "c", "ñ", "n", "œ", "oe", "æ", "ae", "ÿ", "y",
)

// dealTokens splits s into lowercase, accent-folded words, splitting on
// anything that is not a letter or a digit ("WH-1000XM5" is "wh",
// "1000xm5"). Stop words and single letters are dropped; a lone digit is
// kept ("iPhone 15 Pro 2" keeps "2").
func dealTokens(s string) []string {
	fields := strings.FieldsFunc(accentFold.Replace(strings.ToLower(s)), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	tokens := fields[:0]
	for _, f := range fields {
		if dealStopWords[f] || (len(f) < 2 && !unicode.IsDigit(rune(f[0]))) {
			continue
		}
		tokens = append(tokens, f)
	}
	return tokens
}

func hasDigit(s string) bool {
	return strings.IndexFunc(s, unicode.IsDigit) >= 0
}

// DealSearchQuery turns an item title into a deal-site query, reporting
// false for a title too vague to identify one product — searching for
// "Casque" or "Lait" would only propose unrelated deals. A title is
// specific enough with at least two words one of which carries a digit (a
// model number: "Dyson V15", "Sony WH-1000XM5"), or at least three words
// ("Nintendo Switch OLED").
func DealSearchQuery(title string) (string, bool) {
	tokens := dealTokens(title)
	if len(tokens) > maxDealQueryTokens {
		tokens = tokens[:maxDealQueryTokens]
	}
	withDigit := false
	for _, tok := range tokens {
		if hasDigit(tok) {
			withDigit = true
			break
		}
	}
	if len(tokens) < 2 || (!withDigit && len(tokens) < 3) {
		return "", false
	}
	return strings.Join(tokens, " "), true
}

// dealMatches reports whether a deal titled dealTitle is about the product
// query describes: every query word must appear in it, either as a word of
// its own or, for a word of four characters or more, inside its title with
// the separators removed ("wh1000xm5" matches "WH-1000XM5").
func dealMatches(query, dealTitle string) bool {
	titleTokens := dealTokens(dealTitle)
	words := make(map[string]bool, len(titleTokens))
	for _, tok := range titleTokens {
		words[tok] = true
	}
	compact := strings.Join(titleTokens, "")
	for _, q := range strings.Fields(query) {
		if words[q] || (len(q) >= 4 && strings.Contains(compact, q)) {
			continue
		}
		return false
	}
	return true
}

// BestDeal picks the cheapest of deals that matches query (dealMatches) and
// is cheaper than currentPrice, but not suspiciously so (minDealPriceRatio).
// Reports false when none qualifies.
func BestDeal(deals []Deal, query string, currentPrice float64) (Deal, bool) {
	var best Deal
	found := false
	for _, d := range deals {
		if d.Price >= currentPrice || d.Price < currentPrice*minDealPriceRatio || !dealMatches(query, d.Title) {
			continue
		}
		if !found || d.Price < best.Price {
			best, found = d, true
		}
	}
	return best, found
}
