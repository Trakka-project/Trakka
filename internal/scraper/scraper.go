// Package scraper does a best-effort lookup of a listed price, a product
// image, and a product title on a user-supplied product page, for the
// automatic-price-fill feature: given an item's URL, FetchProductInfo
// downloads the page and looks for all three in OpenGraph/Schema.org
// metadata, JSON-LD, and classic microdata, with extra handling for
// storefronts (Amazon in particular) whose markup doesn't fit that generic
// shape cleanly. Every failure mode (network error, timeout, blocked host,
// no recognizable markup) is reported the same way — a non-nil error — and
// callers are expected to treat that as "nothing found" rather than a fatal
// condition; nothing here ever prevents an item from being created or
// updated.
package scraper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	fetchTimeout    = 8 * time.Second
	maxResponseSize = 2 << 20 // 2 MiB is generous for a page's <head> + markup
	maxRedirects    = 5

	// userAgent and the headers set alongside it in setBrowserHeaders make
	// this look like an ordinary desktop Chrome browser doing a real page
	// navigation rather than a bot or a library HTTP client. Several
	// storefronts this feature targets (Reichelt and Amazon among them)
	// block or serve a stripped no-price/no-image page — or, on Amazon, an
	// outright CAPTCHA/"Server Busy" challenge page — to a request that
	// looks automated: a generic/library User-Agent (Go's default, or the
	// previous "TrakkaBot/1.0" identifying string), a missing
	// Accept-Language, or the absence of the Client-Hints/Sec-Fetch-*
	// headers a real Chrome navigation always sends alongside its
	// User-Agent. This is purely to get the same page a real visitor's
	// browser would see for a URL the user themselves added to their own
	// list, not to defeat any access control — see the SSRF guard above for
	// the actual security boundary on what this package is allowed to
	// fetch. userAgent's Chrome version is kept in lockstep with secChUa's
	// ("124") since a mismatched pair is itself a signal some anti-bot
	// checks look for.
	userAgent       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	acceptLanguage  = "fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7"
	acceptHeader    = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8"
	deviceMemory    = "8"
	secChUa         = `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`
	secChUaMobile   = "?0"
	secChUaPlatform = `"Windows"`
	secFetchDest    = "document"
	secFetchMode    = "navigate"
	secFetchSite    = "none"
	secFetchUser    = "?1"
)

// httpClient is shared across calls: its Transport's DialContext is what
// enforces the SSRF guard below, and building that once (rather than per
// request) is the whole reason this is a package-level var.
var httpClient = &http.Client{
	Timeout: fetchTimeout,
	Transport: &http.Transport{
		DialContext: safeDialContext,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

// safeDialContext is a net.Dialer.DialContext replacement that resolves
// the target host itself and only ever connects to a public IP address —
// this is Trakka's SSRF defense for a feature that, by design, makes the
// server fetch an arbitrary URL a user typed in (an item's `url` field).
// Without this, that field would be a way to make the server issue
// requests to internal services, loopback, or a cloud metadata endpoint
// (e.g. 169.254.169.254). Resolving and validating here — then dialing the
// checked IP literal directly, rather than the hostname — also closes the
// DNS-rebinding TOCTOU gap a "resolve, check, then let the dialer
// re-resolve" approach would leave open. TLS (when the URL is https)
// still verifies the certificate against the original hostname, since
// net/http wraps whatever raw connection this returns using the request's
// original host for SNI/verification, not the dialed IP.
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("splitting address %q: %w", addr, err)
	}

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", host, err)
	}

	dialer := &net.Dialer{Timeout: fetchTimeout}
	var lastErr error
	for _, ip := range ips {
		if !isPublicIP(ip) {
			continue
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no public IP address for host %q", host)
	}
	return nil, lastErr
}

// blockedRanges are the address ranges Go's own net.IP predicates do not
// cover but that still must never be dialed on a user's behalf. Each is a
// range that either reaches the local host, reaches infrastructure a
// deployment does not consider "the internet", or exists to smuggle one of
// those two past a naive IPv6 check:
//
//	0.0.0.0/8           "this network" — on Linux, connecting to 0.x.y.z
//	                    reaches the local host
//	100.64.0.0/10       carrier-grade NAT / shared address space, routinely
//	                    used for internal networks in cloud and Kubernetes
//	                    deployments
//	192.0.0.0/24        IETF protocol assignments
//	192.0.2.0/24        TEST-NET-1
//	198.18.0.0/15       benchmarking range
//	198.51.100.0/24     TEST-NET-2
//	203.0.113.0/24      TEST-NET-3
//	240.0.0.0/4         reserved, includes the 255.255.255.255 broadcast
//	64:ff9b::/96        NAT64 — embeds an arbitrary IPv4 address, so without
//	                    this a NAT64-capable host could be used to reach
//	                    10.0.0.1 through an IPv6 literal
//	64:ff9b:1::/48      local-use NAT64
//	2002::/16           6to4 — embeds an IPv4 address the same way
//	100::/64            discard-only
//	2001:db8::/32       documentation
var blockedRanges = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "100::/64", "2001:db8::/32",
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("scraper: bad blocked CIDR " + cidr) // a constant above is malformed; a build-time bug
		}
		nets = append(nets, n)
	}
	return nets
}()

// isPublicIP reports whether ip is safe for this server to connect to on a
// user's behalf: not loopback, not link-local (this also covers the
// 169.254.169.254 cloud metadata address), not a private/ULA range, not a
// multicast, unspecified, or otherwise non-globally-routable address.
//
// Go's own predicates (IsLoopback/IsPrivate/...) are the first half of this;
// they leave several ranges uncovered that are just as effective for reaching
// something the operator never meant to expose — see blockedRanges above for
// the list and why each one matters. An IPv4-mapped IPv6 address is unmapped
// before any of it, so ::ffff:127.0.0.1 is judged as 127.0.0.1 rather than as
// an unrecognized IPv6 address.
func isPublicIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, n := range blockedRanges {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// ProductInfo is what FetchProductInfo manages to extract from a product
// page — any field may be the zero value if that particular piece wasn't
// found, but at least one is always non-zero when err is nil.
type ProductInfo struct {
	// Price is a bare numeric amount (e.g. 15.9), never a formatted string
	// with a currency symbol — the app stores/persists a plain float and
	// formats it for display (with a currency symbol) entirely on the
	// frontend, so preserving that display formatting is a matter of
	// extracting the right number here, not of carrying a currency symbol
	// through this struct.
	Price *float64
	// ImageURL, when non-empty, is always an absolute http(s) URL — see
	// resolveImageURL — safe to persist and to render as an <img src>.
	ImageURL string
	// Title, when non-empty, is the product's name/title as the page
	// itself declares it (JSON-LD, OpenGraph, or <title>). Not currently
	// consumed by any caller — internal/handlers only fills in
	// price/image, never an item's name — but extracted here since it's
	// available from the same fetch at negligible extra cost, for a future
	// caller that wants it (e.g. suggesting a name when an item is created
	// from a bare URL).
	Title string
	// PriceSource names where Price was read from ("json-ld", "og:price",
	// "microdata", "amazon:primary", "dealabs:deal", ...) and PriceRaw the
	// text it was parsed from — diagnostics for the debug log, never
	// persisted.
	PriceSource string
	PriceRaw    string
	// DealExpired is set when rawURL is a Dealabs deal page whose deal has
	// expired (or was withdrawn): Price is then nil, since the price the page
	// still shows can no longer be had. See dealabsDealState.
	DealExpired bool
}

// setBrowserHeaders sets every header FetchProductInfo sends to make a
// request look like a real Chrome browser navigating to the page, rather
// than a bot or a bare library HTTP client — split out from FetchProductInfo
// so it can be unit-tested directly (against a plain *http.Request, no
// network) rather than only through a full fetch, which the SSRF guard
// above would refuse to run against a loopback test server anyway. See the
// const block above for why each of these matters.
func setBrowserHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("Accept-Language", acceptLanguage)
	req.Header.Set("Device-Memory", deviceMemory)
	req.Header.Set("Sec-Ch-Ua", secChUa)
	req.Header.Set("Sec-Ch-Ua-Mobile", secChUaMobile)
	req.Header.Set("Sec-Ch-Ua-Platform", secChUaPlatform)
	req.Header.Set("Sec-Fetch-Dest", secFetchDest)
	req.Header.Set("Sec-Fetch-Mode", secFetchMode)
	req.Header.Set("Sec-Fetch-Site", secFetchSite)
	req.Header.Set("Sec-Fetch-User", secFetchUser)
	// Ask every cache between this server and the site (a CDN, a proxy) for
	// a fresh copy rather than a stored one — what a browser's reload sends —
	// so a price check sees the price the site shows now. This http.Client
	// keeps no cache of its own.
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
}

// amazonBlockedMarkers are strings found on Amazon's own anti-bot challenge
// pages (a CAPTCHA interstitial, or a transient "Server Busy" page) —
// checked purely to log a clear, actionable signal when a fetch silently
// came back with a blocked page instead of the real product page, which
// otherwise looks identical to "nothing found" from a caller's point of
// view and would otherwise be very hard to tell apart from a genuine
// no-price listing.
var amazonBlockedMarkers = [...]string{
	"Server Busy",
	"Type the characters you see in this image",
}

// logIfAmazonBlocked logs a debug line when body looks like one of Amazon's
// own anti-bot challenge pages (see amazonBlockedMarkers) rather than a real
// product page. Deliberately scoped to isAmazon hosts only, so an unrelated
// site's page that happens to contain one of these phrases in ordinary
// content doesn't get logged as "blocked" — and a no-op if logger is nil,
// since not every caller necessarily has one to hand.
func logIfAmazonBlocked(logger *slog.Logger, rawURL string, isAmazon bool, body []byte) {
	if !isAmazon || logger == nil {
		return
	}
	for _, marker := range amazonBlockedMarkers {
		if bytes.Contains(body, []byte(marker)) {
			logger.Debug("amazon appears to have blocked this request (captcha or server-busy page)", "url", rawURL, "marker", marker)
			return
		}
	}
}

// FetchProductInfo downloads rawURL and attempts to extract a listed price,
// a product image, and a product title from its HTML, in a single fetch
// (all three come from the same page, so there is no reason to request it
// twice). rawURL is expected to have already passed internal/validate.URL;
// FetchProductInfo re-checks the scheme anyway as defense in depth against
// being called with something else by mistake. logger may be nil (only used
// for the best-effort "Amazon blocked us" debug line above); every other
// failure mode is reported uniformly as a non-nil error (network, timeout,
// blocked host, unrecognized markup, or the page simply carrying none of
// the three) — callers should treat it as "nothing found", not as an
// operation that needs to be retried or surfaced to the end user.
func FetchProductInfo(ctx context.Context, rawURL string, logger *slog.Logger) (*ProductInfo, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parsing url: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("unsupported url scheme %q", parsed.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	setBrowserHeaders(req)

	debug := func(msg string, args ...any) {
		if logger != nil {
			logger.Debug(msg, append([]any{"url", rawURL}, args...)...)
		}
	}

	debug("product page request")
	resp, err := httpClient.Do(req)
	if err != nil {
		debug("product page fetch failed", "error", err)
		return nil, fmt.Errorf("fetching %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	debug("product page response", "status", resp.StatusCode, "final_url", resp.Request.URL.String(),
		"content_type", ct, "age", resp.Header.Get("Age"), "cache_status", firstHeader(resp.Header, "X-Cache", "Cf-Cache-Status"))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: unexpected status %d", rawURL, resp.StatusCode)
	}
	if ct != "" && !strings.Contains(strings.ToLower(ct), "html") {
		return nil, fmt.Errorf("fetching %s: unexpected content-type %q", rawURL, ct)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", rawURL, err)
	}
	logIfAmazonBlocked(logger, rawURL, isAmazonHost(parsed.Hostname()), body)

	info, err := extractProductInfo(bytes.NewReader(body), parsed)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", rawURL, err)
	}
	if isDealabsDealURL(parsed) {
		// A deal page lists other deals' prices too: the deal's own price
		// (and whether it is still live) comes from its page state only,
		// never from the generic sources extractProductInfo reads.
		info.Price, info.PriceSource, info.PriceRaw = nil, "", ""
		if state, ok := dealabsDealState(body); ok {
			if state.live() {
				info.Price, info.PriceSource = state.Price, "dealabs:deal"
				info.PriceRaw = strconv.FormatFloat(*state.Price, 'f', -1, 64)
			} else {
				info.DealExpired = true
			}
		}
	}
	price := "none"
	if info.Price != nil {
		price = strconv.FormatFloat(*info.Price, 'f', 2, 64)
	}
	// page_title tells a real product page from a bot wall, a consent page or
	// a generic landing page that answered 200.
	debug("product page parsed", "bytes", len(body), "page_title", info.Title,
		"price_source", info.PriceSource, "price_raw", info.PriceRaw, "price", price, "deal_expired", info.DealExpired)
	if info.Price == nil && info.ImageURL == "" && info.Title == "" {
		return nil, fmt.Errorf("no price, image, or title found on %s", rawURL)
	}
	return info, nil
}

// firstHeader returns the first of keys present in h, for the debug log.
func firstHeader(h http.Header, keys ...string) string {
	for _, k := range keys {
		if v := h.Get(k); v != "" {
			return k + ": " + v
		}
	}
	return ""
}

// extractProductInfo walks the parsed HTML tree once, looking for a price,
// an image, and a title together.
//
// Price is resolved in priority order: on an Amazon host, Amazon's own
// price-display markup is tried first (the amazon:* price candidates — a-offscreen
// text inside the current "buy box" price block, then any other
// a-price/a-offscreen pair, the older priceblock_* ids, the a-color-price
// class, and finally the buybox container's own text), since Amazon pages
// frequently carry no OpenGraph/JSON-LD price at all, or one that's stale
// relative to the actual displayed price; falling back, on any host
// (Amazon included), to Schema.org JSON-LD ("price"/"lowPrice"/"highPrice"
// nested in an Offer, the most structured and explicit source when
// present), an Amazon data-asin-price/data-price attribute, an Amazon
// inline JS state script (amazonScriptStatePrice — a priceAmount/
// displayPrice/buyingPrice/price key inside any <script> tag's raw text,
// for pages that render their price client-side from embedded JSON rather
// than static markup), OpenGraph/product: meta tags (og:price:amount /
// product:price:amount), a Twitter Card data/label pair whose label reads
// as "Price"/"Prix" (twitter:dataN, used by some storefronts — Reichelt
// among them — with no OpenGraph or JSON-LD price at all), then classic
// microdata (itemprop="price", read off either the element's own "content"
// attribute or its text) — and, only on an Amazon host and only if every
// source above found nothing at all, a last-resort direct regex over the
// raw, unparsed response body (amazonRawBodyPrice), for a page shape none
// of the structured sources above recognize.
//
// Image is resolved in priority order: on an Amazon host, Amazon's own
// product-image element (id="landingImage"/"imgBlkFront")'s
// data-old-hires/data-a-dynamic-image attributes first — Amazon's
// og:image is frequently the site's own logo or a navigation icon, not the
// product photo, so it's deliberately not trusted first there. Then,
// regardless of host: JSON-LD ("image"), og:image (skipped if it looks
// like Amazon chrome — logo/nav/favicon/tile — rather than a product
// photo), classic microdata (itemprop="image"), and finally the Twitter
// Card image (twitter:image) as a last resort.
//
// Title is resolved in priority order: JSON-LD (schema.org Product.name,
// preferring an object actually typed "Product" over an unrelated "name"
// elsewhere on the page), og:title, then the page's own <title>.
//
// It returns a zero-value field (never an error) for anything not found,
// since that's an expected outcome, not a failure; pageURL is used both to
// detect an Amazon host and to resolve a relative image URL to absolute.
func extractProductInfo(r io.Reader, pageURL *url.URL) (*ProductInfo, error) {
	// The full body is kept around (not just streamed into html.Parse)
	// because amazonRawBodyPrice needs the raw, unparsed text as its
	// absolute last resort — html.Parse itself only ever sees the copy
	// below, so a malformed-markup edge case that trips up the parser can't
	// affect this raw fallback either.
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading html: %w", err)
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parsing html: %w", err)
	}

	isAmazon := isAmazonHost(pageURL.Hostname())

	var ogPrice, ogImage, ogTitle, twitterImage, pageTitle string
	var microdataPrice, microdataImage, amazonImage string
	// Amazon-specific price sources, filled only when isAmazon — see
	// the price candidates below for how these five are prioritized against each
	// other, and the doc comment above for how the whole set fits into the
	// overall price-resolution order.
	var amazonPrimaryPrice, amazonAPriceText, amazonPriceblockText, amazonColorPriceText, amazonBuyboxText, amazonDataPriceText string
	// amazonScriptBlocks holds the raw text of every <script> tag on an
	// Amazon page (regardless of its type attribute, unlike ldjsonBlocks
	// below which only collects application/ld+json ones) — this is what
	// amazonScriptStatePrice scans for a price embedded in inline JS state
	// (e.g. a <script type="a-state" data-a-state='{"key":"..."}'> widget
	// payload) rather than static markup.
	var amazonScriptBlocks []string
	twitterLabels := map[string]string{}
	twitterData := map[string]string{}
	var ldjsonBlocks []string

	// inAmazonPrimaryPrice/inAmazonAPrice are threaded down through the
	// recursive walk (not package-level state) so a span.a-offscreen deep
	// inside, say, div#corePrice_desktop can tell it's inside that
	// container without the walk needing to track a full ancestor stack —
	// each recursive call just carries whether an ancestor (or the node
	// itself) already matched.
	var walk func(n *html.Node, inAmazonPrimaryPrice, inAmazonAPrice bool)
	walk = func(n *html.Node, inAmazonPrimaryPrice, inAmazonAPrice bool) {
		if n.Type == html.ElementNode {
			if isAmazon {
				if hasClass(n, "apexPriceToPay") || hasID(n, "corePrice_desktop") {
					inAmazonPrimaryPrice = true
				}
				if hasClass(n, "a-price") {
					inAmazonAPrice = true
				}
				if n.Data == "span" && hasClass(n, "a-offscreen") {
					if text := strings.TrimSpace(textContent(n)); text != "" {
						if inAmazonPrimaryPrice && amazonPrimaryPrice == "" {
							amazonPrimaryPrice = text
						} else if inAmazonAPrice && amazonAPriceText == "" {
							amazonAPriceText = text
						}
					}
				}
				if amazonPriceblockText == "" &&
					(hasID(n, "priceblock_ourprice") || hasID(n, "priceblock_dealprice") || hasID(n, "priceblock_saleprice")) {
					if text := strings.TrimSpace(textContent(n)); text != "" {
						amazonPriceblockText = text
					}
				}
				if amazonColorPriceText == "" && hasClass(n, "a-color-price") {
					if text := strings.TrimSpace(textContent(n)); text != "" {
						amazonColorPriceText = text
					}
				}
				if amazonBuyboxText == "" && hasID(n, "price_inside_buybox") {
					if text := strings.TrimSpace(textContent(n)); text != "" {
						amazonBuyboxText = text
					}
				}
				if amazonDataPriceText == "" {
					if v, ok := attrValue(n, "data-asin-price"); ok && strings.TrimSpace(v) != "" {
						amazonDataPriceText = v
					} else if v, ok := attrValue(n, "data-price"); ok && strings.TrimSpace(v) != "" {
						amazonDataPriceText = v
					}
				}
			}

			switch n.Data {
			case "meta":
				if name, content, ok := metaNameAndContent(n); ok {
					switch name {
					case "og:price:amount", "product:price:amount":
						if ogPrice == "" {
							ogPrice = content
						}
					case "og:image":
						if ogImage == "" {
							ogImage = content
						}
					case "og:title":
						if ogTitle == "" {
							ogTitle = content
						}
					case "twitter:image":
						if twitterImage == "" {
							twitterImage = content
						}
					default:
						if idx, isLabel := strings.CutPrefix(name, "twitter:label"); isLabel {
							if _, exists := twitterLabels[idx]; !exists {
								twitterLabels[idx] = content
							}
						} else if idx, isData := strings.CutPrefix(name, "twitter:data"); isData {
							if _, exists := twitterData[idx]; !exists {
								twitterData[idx] = content
							}
						}
					}
				}
			case "script":
				if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
					if isLDJSON(n) {
						ldjsonBlocks = append(ldjsonBlocks, n.FirstChild.Data)
					}
					if isAmazon {
						amazonScriptBlocks = append(amazonScriptBlocks, n.FirstChild.Data)
					}
				}
			case "title":
				if pageTitle == "" {
					if t := strings.TrimSpace(textContent(n)); t != "" {
						pageTitle = t
					}
				}
			}

			// itemprop microdata and Amazon's landing-image markup are
			// checked for every element, not just inside a specific
			// case above: a common microdata pattern is an itemprop on a
			// <meta>/<link> tag (already handled by the "meta" case for
			// og/twitter, but not for itemprop), and Amazon's
			// landingImage/imgBlkFront element is an ordinary <img>.
			if microdataPrice == "" && hasItemProp(n, "price") {
				microdataPrice = microdataValue(n)
			}
			if microdataImage == "" && hasItemProp(n, "image") {
				if v, ok := microdataImageValue(n); ok {
					microdataImage = v
				}
			}
			if isAmazon && amazonImage == "" {
				if v, ok := amazonLandingImage(n); ok {
					amazonImage = v
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inAmazonPrimaryPrice, inAmazonAPrice)
		}
	}
	walk(doc, false, false)

	info := &ProductInfo{}

	// Every price source, in priority order (see the doc comment above); the
	// first that parses wins. Each is named, and keeps the raw text it was
	// read from, for ProductInfo.PriceSource/PriceRaw — what a price check's
	// debug log shows, so a wrong price can be traced to the markup it came
	// from (a crossed-out price, a different offer, ...).
	type priceCandidate struct {
		source string
		read   func() (raw string, price float64, ok bool)
	}
	// text parses a source read as text; number takes a source that already
	// yields a number as is (re-parsing its formatted value could misread a
	// "1.299" as a thousands separator).
	text := func(v string) func() (string, float64, bool) {
		return func() (string, float64, bool) {
			p, ok := parsePriceString(v)
			return v, p, v != "" && ok
		}
	}
	number := func(get func() (float64, bool)) func() (string, float64, bool) {
		return func() (string, float64, bool) {
			p, ok := get()
			return strconv.FormatFloat(p, 'f', -1, 64), p, ok
		}
	}
	var candidates []priceCandidate
	if isAmazon {
		candidates = append(candidates,
			priceCandidate{"amazon:primary", text(amazonPrimaryPrice)},
			priceCandidate{"amazon:a-price", text(amazonAPriceText)},
			priceCandidate{"amazon:priceblock", text(amazonPriceblockText)},
			priceCandidate{"amazon:a-color-price", text(amazonColorPriceText)},
			priceCandidate{"amazon:buybox", text(amazonBuyboxText)})
	}
	candidates = append(candidates, priceCandidate{"json-ld", number(func() (float64, bool) { return firstJSONPrice(ldjsonBlocks) })})
	if isAmazon {
		candidates = append(candidates,
			priceCandidate{"amazon:data-price", text(amazonDataPriceText)},
			priceCandidate{"amazon:script-state", number(func() (float64, bool) { return amazonScriptStatePrice(amazonScriptBlocks) })})
	}
	twitterRaw, _ := resolveTwitterPrice(twitterLabels, twitterData)
	candidates = append(candidates,
		priceCandidate{"og:price", text(ogPrice)},
		priceCandidate{"twitter:data", text(twitterRaw)},
		priceCandidate{"microdata", text(microdataPrice)})
	if isAmazon {
		candidates = append(candidates, priceCandidate{"amazon:raw-body", number(func() (float64, bool) { return amazonRawBodyPrice(body) })})
	}
	for _, c := range candidates {
		if raw, p, ok := c.read(); ok {
			info.Price, info.PriceSource, info.PriceRaw = &p, c.source, raw
			break
		}
	}

	jsonldImage, _ := firstJSONImage(ldjsonBlocks)
	var imageCandidates []string
	if isAmazon && amazonImage != "" {
		imageCandidates = append(imageCandidates, amazonImage)
	}
	if jsonldImage != "" {
		imageCandidates = append(imageCandidates, jsonldImage)
	}
	if ogImage != "" && (!isAmazon || !isAmazonGenericImage(ogImage)) {
		imageCandidates = append(imageCandidates, ogImage)
	}
	if microdataImage != "" {
		imageCandidates = append(imageCandidates, microdataImage)
	}
	if twitterImage != "" {
		imageCandidates = append(imageCandidates, twitterImage)
	}
	for _, candidate := range imageCandidates {
		if resolved, ok := resolveImageURL(pageURL, candidate); ok {
			info.ImageURL = resolved
			break
		}
	}

	if title, ok := findJSONTitle(ldjsonBlocks); ok {
		info.Title = title
	} else if ogTitle != "" {
		info.Title = strings.TrimSpace(ogTitle)
	} else if pageTitle != "" {
		info.Title = pageTitle
	}

	return info, nil
}

// metaNameAndContent reads a <meta> tag's identifying attribute (either
// "name", as Twitter Card and generic meta tags use, or "property", as
// OpenGraph/Facebook tags use) together with its "content" value. Returns
// ok=false if either is missing, so callers never have to check for an
// empty name separately.
func metaNameAndContent(n *html.Node) (name, content string, ok bool) {
	for _, a := range n.Attr {
		switch a.Key {
		case "name", "property":
			if name == "" {
				name = a.Val
			}
		case "content":
			content = a.Val
		}
	}
	if name == "" || content == "" {
		return "", "", false
	}
	return name, content, true
}

func attrValue(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

// hasClass reports whether n's "class" attribute contains the exact class
// token wanted (class attributes are space-separated, e.g.
// class="a-price a-text-price apexPriceToPay").
func hasClass(n *html.Node, want string) bool {
	v, ok := attrValue(n, "class")
	if !ok {
		return false
	}
	for _, c := range strings.Fields(v) {
		if c == want {
			return true
		}
	}
	return false
}

// hasID reports whether n's "id" attribute exactly matches want.
func hasID(n *html.Node, want string) bool {
	v, ok := attrValue(n, "id")
	return ok && v == want
}

// amazonDomainRe matches any Amazon storefront host (amazon.com,
// amazon.fr, amazon.co.uk, smile.amazon.com, ...) without matching an
// unrelated domain that merely contains "amazon" (e.g. "notamazon.com").
var amazonDomainRe = regexp.MustCompile(`(?:^|\.)amazon\.[a-z][a-z.]*$`)

// isAmazonHost reports whether host is an Amazon storefront or one of its
// link shorteners (amzn.to, amzn.eu) — used to switch on Amazon's own
// product-image markup instead of trusting a generic og:image, which on
// Amazon is frequently the site's logo or a navigation icon rather than
// the product photo.
func isAmazonHost(host string) bool {
	host = strings.ToLower(host)
	if host == "amzn.to" || host == "amzn.eu" ||
		strings.HasSuffix(host, ".amzn.to") || strings.HasSuffix(host, ".amzn.eu") {
		return true
	}
	return amazonDomainRe.MatchString(host)
}

// amazonGenericImageTerms are substrings found in Amazon's own chrome
// images (logo, navigation sprites, favicons, tiled backgrounds) rather
// than an actual product photo — og:image on an Amazon page sometimes
// points at one of these instead of the product, so a URL containing any
// of them is never trusted as an og:image fallback there.
var amazonGenericImageTerms = [...]string{"amazon-logo", "nav2", "favicon", "tile"}

func isAmazonGenericImage(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	for _, term := range amazonGenericImageTerms {
		if strings.Contains(lower, term) {
			return true
		}
	}
	return false
}

// amazonScriptStatePriceRe matches a price embedded in an Amazon page's
// inline JS state rather than static markup — a <script type="a-state"
// data-a-state='{"key":"..."}'> widget payload, or a window.P.register(...)
// call — under any of the key names Amazon's own client-side rendering
// uses for it. Deliberately loose (no full JSON parse, since a script's
// body is a JS statement/expression, not necessarily valid standalone
// JSON): it just looks for one of these keys immediately followed by a
// number, optionally quoted (an unquoted "priceAmount":14.99, or a quoted
// "displayPrice":"14,99 €" whose leading digits/decimal-or-comma this still
// captures — trailing text like the currency symbol is simply left out of
// the match).
var amazonScriptStatePriceRe = regexp.MustCompile(`"(?:priceAmount|displayPrice|buyingPrice|price)"\s*:\s*"?([0-9]+(?:[.,][0-9]{1,2})?)`)

func amazonScriptStatePrice(blocks []string) (float64, bool) {
	for _, block := range blocks {
		if m := amazonScriptStatePriceRe.FindStringSubmatch(block); m != nil {
			if p, ok := parsePriceString(m[1]); ok {
				return p, true
			}
		}
	}
	return 0, false
}

// amazonRawBodyPriceRes are absolute-last-resort patterns tried directly
// against the raw, unparsed HTML response body — after Amazon's own DOM
// selectors, JSON-LD, a data-price attribute, and the inline-JS-state scan
// above have all come up empty, meaning Amazon served a page shape none of
// those structured sources recognize. A direct regex over raw markup has no
// notion of surrounding context (it could in principle match an unrelated
// number that happens to share a field name), which is exactly why this
// only ever runs once every more structured source has already had first
// refusal.
var amazonRawBodyPriceRes = []*regexp.Regexp{
	regexp.MustCompile(`"priceAmount"\s*:\s*([0-9]+\.?[0-9]*)`),
	regexp.MustCompile(`"price"\s*:\s*"([0-9]+[.,][0-9]{2})"`),
	regexp.MustCompile(`class="a-offscreen">([0-9]+[.,][0-9]{2})\s*€`),
}

func amazonRawBodyPrice(body []byte) (float64, bool) {
	for _, re := range amazonRawBodyPriceRes {
		if m := re.FindSubmatch(body); m != nil {
			if p, ok := parsePriceString(string(m[1])); ok {
				return p, true
			}
		}
	}
	return 0, false
}

// amazonLandingImage recognizes Amazon's main product-image element
// (id="landingImage" or id="imgBlkFront", depending on the page template)
// and reads the real, full-resolution product photo from it: the
// data-old-hires attribute when present (a direct URL), or otherwise the
// data-a-dynamic-image attribute (a JSON object mapping every available
// resolution of the same photo to its [width, height], from which the
// largest is picked). Both attributes hold the actual product photo,
// unlike the element's own src, which Amazon frequently loads with a small
// placeholder that's swapped in by client-side JS the static HTML never
// reflects.
func amazonLandingImage(n *html.Node) (string, bool) {
	id, _ := attrValue(n, "id")
	if id != "landingImage" && id != "imgBlkFront" {
		return "", false
	}
	if v, ok := attrValue(n, "data-old-hires"); ok && strings.TrimSpace(v) != "" {
		return v, true
	}
	if v, ok := attrValue(n, "data-a-dynamic-image"); ok && strings.TrimSpace(v) != "" {
		if best, ok := bestDynamicImage(v); ok {
			return best, true
		}
	}
	return "", false
}

// bestDynamicImage parses Amazon's data-a-dynamic-image attribute — a JSON
// object mapping each available image URL to its [width, height] in
// pixels — and returns the URL with the largest area, since Amazon lists
// several resolutions of the same product photo there and the largest is
// the real image (smaller ones are thumbnail-sized crops).
func bestDynamicImage(raw string) (string, bool) {
	var sizes map[string][]int
	if err := json.Unmarshal([]byte(raw), &sizes); err != nil {
		return "", false
	}
	var bestURL string
	bestArea := -1
	for u, dim := range sizes {
		area := 0
		if len(dim) >= 2 {
			area = dim[0] * dim[1]
		}
		if area > bestArea {
			bestArea = area
			bestURL = u
		}
	}
	return bestURL, bestURL != ""
}

// resolveImageURL turns a possibly-relative image URL (as found in HTML
// markup) into an absolute http(s) URL relative to pageURL, rejecting
// anything else (a "javascript:"/"data:" URI could otherwise end up here
// the same way it's blocked from item.url by internal/validate.URL).
func resolveImageURL(pageURL *url.URL, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	resolved := pageURL.ResolveReference(ref)
	scheme := strings.ToLower(resolved.Scheme)
	if scheme != "http" && scheme != "https" || resolved.Host == "" {
		return "", false
	}
	return resolved.String(), true
}

func isLDJSON(n *html.Node) bool {
	for _, a := range n.Attr {
		if a.Key == "type" && strings.EqualFold(a.Val, "application/ld+json") {
			return true
		}
	}
	return false
}

// hasItemProp reports whether n carries the Schema.org microdata attribute
// itemprop="want" (e.g. itemprop="price" or itemprop="image") — checked on
// every element node (see the walk function above), since this shows up
// both on ordinary elements (<span itemprop="price">) and on <meta>/<img>
// tags.
func hasItemProp(n *html.Node, want string) bool {
	for _, a := range n.Attr {
		if a.Key == "itemprop" && a.Val == want {
			return true
		}
	}
	return false
}

// microdataValue reads an itemprop element's value: its "content"
// attribute if present (the microdata convention for a value that differs
// from the element's visible text, e.g. <span itemprop="price"
// content="19.99">19,99 €</span>), otherwise its text content.
func microdataValue(n *html.Node) string {
	if v, ok := attrValue(n, "content"); ok {
		return v
	}
	return textContent(n)
}

// microdataImageValue reads an itemprop="image" element's actual image
// reference: a "content" attribute (the microdata-standard way to carry a
// value on a <meta>/<link> tag), an <img>'s own "src", or — as a last
// resort — its text content.
func microdataImageValue(n *html.Node) (string, bool) {
	for _, key := range [...]string{"content", "src"} {
		if v, ok := attrValue(n, key); ok && strings.TrimSpace(v) != "" {
			return v, true
		}
	}
	if t := strings.TrimSpace(textContent(n)); t != "" {
		return t, true
	}
	return "", false
}

func textContent(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

// priceLabelRe matches a Twitter Card twitter:labelN value that indicates
// its paired twitter:dataN is a price ("Price"/"Prix", case-insensitive) —
// the generic Twitter Card convention some storefronts (Reichelt among
// them) use to expose a price with no OpenGraph or JSON-LD price at all.
var priceLabelRe = regexp.MustCompile(`(?i)pr(ix|ice)`)

func resolveTwitterPrice(labels, data map[string]string) (string, bool) {
	for idx, label := range labels {
		if priceLabelRe.MatchString(label) {
			if v, ok := data[idx]; ok && v != "" {
				return v, true
			}
		}
	}
	return "", false
}

func firstJSONPrice(blocks []string) (float64, bool) {
	for _, block := range blocks {
		if p, ok := findJSONPrice(block); ok {
			return p, true
		}
	}
	return 0, false
}

// findJSONPrice parses a JSON-LD block (a single Schema.org object, or an
// array/@graph of them) and searches it for a "price" field, as found on a
// Product's nested Offer (https://schema.org/price), or — when a single
// exact price isn't given — "lowPrice"/"highPrice" from an AggregateOffer
// (https://schema.org/lowPrice, .../highPrice), which some Amazon listings
// use instead.
func findJSONPrice(raw string) (float64, bool) {
	var data any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return 0, false
	}
	return searchJSONPrice(data)
}

func searchJSONPrice(v any) (float64, bool) {
	switch val := v.(type) {
	case map[string]any:
		for _, key := range [...]string{"price", "priceAmount", "lowPrice", "highPrice"} {
			if raw, ok := val[key]; ok {
				if p, ok := coerceJSONPrice(raw); ok {
					return p, true
				}
			}
		}
		for _, child := range val {
			if p, ok := searchJSONPrice(child); ok {
				return p, true
			}
		}
	case []any:
		for _, child := range val {
			if p, ok := searchJSONPrice(child); ok {
				return p, true
			}
		}
	}
	return 0, false
}

func firstJSONImage(blocks []string) (string, bool) {
	for _, block := range blocks {
		if s, ok := findJSONImage(block); ok {
			return s, true
		}
	}
	return "", false
}

// findJSONImage parses a JSON-LD block and searches it for an "image"
// field, as found on a Product (https://schema.org/image). The value may
// be a plain string, an array of strings (the first is used), or a nested
// ImageObject with its own "url" field — searchJSONImage handles all three.
func findJSONImage(raw string) (string, bool) {
	var data any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return "", false
	}
	return searchJSONImage(data)
}

func searchJSONImage(v any) (string, bool) {
	switch val := v.(type) {
	case map[string]any:
		if raw, ok := val["image"]; ok {
			if s, ok := coerceJSONImage(raw); ok {
				return s, true
			}
		}
		for key, child := range val {
			if key == "image" {
				continue // already tried above; avoid matching an unrelated nested "image"
			}
			if s, ok := searchJSONImage(child); ok {
				return s, true
			}
		}
	case []any:
		for _, child := range val {
			if s, ok := searchJSONImage(child); ok {
				return s, true
			}
		}
	}
	return "", false
}

// coerceJSONImage normalizes the three shapes schema.org's "image"
// property may take: a plain URL string, an array (first entry wins), or
// an ImageObject ({"@type": "ImageObject", "url": "..."}).
func coerceJSONImage(v any) (string, bool) {
	switch val := v.(type) {
	case string:
		if val != "" {
			return val, true
		}
	case []any:
		for _, item := range val {
			if s, ok := coerceJSONImage(item); ok {
				return s, true
			}
		}
	case map[string]any:
		if u, ok := val["url"]; ok {
			if s, ok := u.(string); ok && s != "" {
				return s, true
			}
		}
	}
	return "", false
}

func coerceJSONPrice(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		if n >= 0 {
			return n, true
		}
	case string:
		return parsePriceString(n)
	}
	return 0, false
}

// findJSONTitle searches JSON-LD blocks for a product name — schema.org's
// Product.name (https://schema.org/name) — preferring an object whose own
// @type is "Product" over any other "name" the page's JSON-LD happens to
// carry. A page frequently emits more than one JSON-LD block (breadcrumbs,
// site/organization info, and the product itself), several of which have
// their own unrelated "name" (e.g. an Organization's), so the first pass
// only accepts a "name" from a Product; only if that fails everywhere does
// the second pass fall back to any "name" found anywhere.
func findJSONTitle(blocks []string) (string, bool) {
	var parsed []any
	for _, raw := range blocks {
		var data any
		if err := json.Unmarshal([]byte(raw), &data); err == nil {
			parsed = append(parsed, data)
		}
	}
	for _, data := range parsed {
		if name, ok := searchJSONProductName(data, true); ok {
			return name, true
		}
	}
	for _, data := range parsed {
		if name, ok := searchJSONProductName(data, false); ok {
			return name, true
		}
	}
	return "", false
}

func searchJSONProductName(v any, requireProductType bool) (string, bool) {
	switch val := v.(type) {
	case map[string]any:
		if !requireProductType || isProductType(val["@type"]) {
			if raw, ok := val["name"].(string); ok {
				if name := strings.TrimSpace(raw); name != "" {
					return name, true
				}
			}
		}
		for key, child := range val {
			if key == "name" {
				continue
			}
			if name, ok := searchJSONProductName(child, requireProductType); ok {
				return name, true
			}
		}
	case []any:
		for _, child := range val {
			if name, ok := searchJSONProductName(child, requireProductType); ok {
				return name, true
			}
		}
	}
	return "", false
}

func isProductType(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.EqualFold(t, "Product")
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok && strings.EqualFold(s, "Product") {
				return true
			}
		}
	}
	return false
}

// priceCleanRe strips everything but digits, dots and commas (currency
// symbols, spaces, non-breaking spaces, "€"/"$"/"EUR", etc.) before
// attempting to parse a price out of free-form text content.
var priceCleanRe = regexp.MustCompile(`[^\d.,]`)

// parsePriceString extracts a non-negative decimal amount from a
// price-shaped string, tolerating both "1234.56"/"1,234.56" (US-style)
// and "1234,56"/"1.234,56" (European-style) formatting.
func parsePriceString(raw string) (float64, bool) {
	s := priceCleanRe.ReplaceAllString(strings.TrimSpace(raw), "")
	if s == "" {
		return 0, false
	}

	switch {
	case strings.Contains(s, ",") && strings.Contains(s, "."):
		if strings.LastIndex(s, ",") > strings.LastIndex(s, ".") {
			// e.g. "1.234,56": '.' is a thousands separator, ',' is decimal.
			s = strings.ReplaceAll(s, ".", "")
			s = strings.ReplaceAll(s, ",", ".")
		} else {
			// e.g. "1,234.56": ',' is a thousands separator.
			s = strings.ReplaceAll(s, ",", "")
		}
	case strings.Contains(s, ","):
		// Only a comma: treat it as a decimal separator if it looks like one
		// (a single comma, at most two digits after it), else as a
		// thousands separator to strip.
		parts := strings.Split(s, ",")
		if len(parts) == 2 && len(parts[1]) <= 2 {
			s = strings.ReplaceAll(s, ",", ".")
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	}

	val, err := strconv.ParseFloat(s, 64)
	if err != nil || val < 0 {
		return 0, false
	}
	return val, true
}
