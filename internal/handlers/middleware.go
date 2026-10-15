package handlers

import (
	"log/slog"
	"net/http"
	"time"
)

// SecurityHeaders sets standard hardening headers on every response.
//
// script-src is 'self' and nothing else: the Tailwind Play CDN runtime that
// used to be loaded from https://cdn.tailwindcss.com is now vendored at
// static/js/tailwind.js and served from this origin, so no third party can
// execute code inside Trakka's own origin any more (see docs/AUDIT.md, BP-01 —
// the CDN's URL is unversioned, so Subresource Integrity could not pin it).
// style-src still needs 'unsafe-inline' because that runtime generates its
// utility CSS into a <style> tag at run time rather than serving a
// stylesheet; eliminating it requires precompiling Tailwind with the CLI,
// which would introduce the build step this project deliberately does not
// have.
// img-src's "http: https:" is the other deliberate widening: an item's
// product thumbnail (internal/scraper's og:image/JSON-LD/twitter:image
// lookup, static/js/list_view.js's buildItemThumbnail) can point at any
// storefront's CDN, the same way an item's own url already links out
// anywhere — there is no fixed allowlist of image hosts to name here, so
// img-src has to trust the scheme rather than a host list. The actual
// scraped URL is still constrained the same way item.url is: scraper.
// resolveImageURL only ever accepts an absolute http(s) URL (see
// internal/scraper/scraper.go), so this can never be used to load a
// "javascript:"/"data:" URI dressed up as an image source.
//
// hsts asks for Strict-Transport-Security to be asserted. It is wired from
// SESSION_COOKIE_SECURE (see Routes) rather than from whether this specific
// request arrived over TLS, because in the deployment this app is designed
// for the TLS terminates at a reverse proxy and the request reaching Go is
// plain HTTP — so r.TLS is nil even on a properly HTTPS-only instance.
// Asserting HSTS unconditionally would instead pin a developer's browser to
// https for a localhost instance that only ever serves http.
func SecurityHeaders(hsts bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: http: https:; "+
				"connect-src 'self'; "+
				"worker-src 'self'; "+
				"manifest-src 'self'; "+
				"object-src 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'; "+
				"frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		// Cross-origin isolation headers: nothing in this app is meant to be
		// embedded in, or to share a browsing-context group with, another
		// site. COOP severs the window.opener relationship a malicious opener
		// would otherwise keep; CORP stops another origin from loading
		// Trakka's own responses as a subresource.
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		// Trakka uses none of these device/browser capabilities but the
		// camera; denying them outright means a compromised third-party script
		// (the Tailwind Play CDN is the one third-party script this CSP
		// trusts) cannot reach for them either. camera=(self) is for the
		// barcode scanner of "Courses" lists (static/js/scanner.js): this
		// origin only, never a frame, and the browser still asks the user
		// before any page script gets a stream.
		h.Set("Permissions-Policy",
			"accelerometer=(), autoplay=(), camera=(self), display-capture=(), "+
				"encrypted-media=(), fullscreen=(self), geolocation=(), gyroscope=(), "+
				"magnetometer=(), microphone=(), midi=(), payment=(), usb=(), xr-spatial-tracking=()")
		if hsts {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// Logging records method, path, status, and duration for every request.
func Logging(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// Recover is a last-resort safety net against an unexpected panic (e.g. a
// bug surfacing deep in a dependency). It is not a substitute for the
// explicit error handling used throughout the handlers and db packages —
// every predictable failure is already handled with a returned error
// before it would ever reach here.
func Recover(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic recovered", "path", r.URL.Path, "recovered", rec)
				writeError(w, http.StatusInternalServerError, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(status int) {
	sw.status = status
	sw.ResponseWriter.WriteHeader(status)
}
