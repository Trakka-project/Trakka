package backup

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"trakka/internal/validate"
)

// Error codes for WebDAV failures, returned to the admin console alongside
// the raw (English, technical) error text so it can show a translated,
// actionable explanation — "wrong password" and "folder doesn't exist" call
// for very different fixes, and a bare "HTTP 401" says neither.
const (
	CodeAuthFailed     = "auth_failed"
	CodeForbidden      = "forbidden"
	CodeNotFound       = "not_found"
	CodeNotFolder      = "not_a_folder"
	CodeNotWebDAV      = "not_webdav"
	CodeRedirect       = "redirect"
	CodeStorageFull    = "storage_full"
	CodeBlockedAddress = "blocked_address"
	CodeUnreachable    = "unreachable"
	CodeTLS            = "tls"
	CodeTimeout        = "timeout"
	CodeHTTPError      = "http_error"
	// CodePasswordUnreadable isn't a WebDAV failure as such: the stored
	// password couldn't be decrypted with the current key file (see
	// ErrSecretUnreadable), so no request was even attempted.
	CodePasswordUnreadable = "password_unreadable"
)

// webdavError is every failure the client below reports: either a
// transport-level error (DNS, connect, TLS, timeout) or an unexpected HTTP
// status, tagged with one of the codes above.
type webdavError struct {
	Op       string // HTTP method
	Target   string // path on the server, never including credentials
	Status   int    // 0 for a transport-level failure
	Location string // for a redirect
	Code     string
	err      error
}

func (e *webdavError) Error() string {
	switch {
	case e.Status != 0 && e.Location != "":
		return fmt.Sprintf("webdav %s %s: HTTP %d, redirected to %s — use that URL directly", e.Op, e.Target, e.Status, e.Location)
	case e.Status != 0:
		return fmt.Sprintf("webdav %s %s: HTTP %d %s", e.Op, e.Target, e.Status, http.StatusText(e.Status))
	case e.Code == CodeNotFolder:
		return fmt.Sprintf("webdav %s %s: the URL points to a file, not a folder", e.Op, e.Target)
	default:
		return fmt.Sprintf("webdav %s %s: %v", e.Op, e.Target, e.err)
	}
}

func (e *webdavError) Unwrap() error { return e.err }

// ErrorCode classifies an error returned by a WebDAV operation (or by
// anything wrapping one) into one of the Code* constants, or "" if it
// isn't a WebDAV failure at all.
func ErrorCode(err error) string {
	var we *webdavError
	if errors.As(err, &we) {
		return we.Code
	}
	if errors.Is(err, ErrSecretUnreadable) {
		return CodePasswordUnreadable
	}
	return ""
}

func codeForTransportError(err error) string {
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var netErr net.Error
	switch {
	case errors.Is(err, errBlockedAddress):
		return CodeBlockedAddress
	case errors.As(err, &certErr), errors.As(err, &unknownAuth), errors.As(err, &hostErr):
		return CodeTLS
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return CodeTimeout
	default:
		return CodeUnreachable
	}
}

func codeForStatus(status int) string {
	switch {
	case status >= 300 && status < 400:
		return CodeRedirect
	case status == http.StatusUnauthorized:
		return CodeAuthFailed
	case status == http.StatusForbidden:
		return CodeForbidden
	case status == http.StatusNotFound, status == http.StatusConflict:
		// 409 is what a WebDAV server answers a PUT into a parent
		// collection that doesn't exist (RFC 4918 §9.7.1).
		return CodeNotFound
	case status == http.StatusMethodNotAllowed, status == http.StatusNotImplemented:
		return CodeNotWebDAV
	case status == http.StatusInsufficientStorage, status == http.StatusRequestEntityTooLarge:
		return CodeStorageFull
	default:
		return CodeHTTPError
	}
}

// ErrInvalidWebDAVURL is returned by parseWebDAVURL; its message is shown
// to the admin as-is.
var ErrInvalidWebDAVURL = errors.New("the WebDAV URL must be an absolute http:// or https:// folder URL, without credentials, query string or fragment")

// parseWebDAVURL validates an admin-entered WebDAV folder URL — the same
// http(s)-only rule internal/validate.URL applies to every user-supplied
// URL, plus: no credentials embedded in it (they belong in the dedicated
// username/password fields, where they are encrypted at rest and never
// echoed back or logged), no query string or fragment (meaningless for a
// WebDAV collection). The returned URL's path always ends in "/", so that
// backup file names resolve inside the folder rather than replacing its
// last segment.
func parseWebDAVURL(raw string) (*url.URL, error) {
	trimmed, err := validate.URL(raw)
	if err != nil || trimmed == "" {
		return nil, ErrInvalidWebDAVURL
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrInvalidWebDAVURL
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
		if u.RawPath != "" {
			u.RawPath += "/"
		}
	}
	return u, nil
}

// safeFileName restricts which names this client will ever build a URL
// for: backup files are named by this package (see backupFileName), and a
// name coming back from a PROPFIND listing or from the restore form must
// never be able to walk out of the configured folder.
var safeFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// webdavClient is a deliberately minimal WebDAV client — PROPFIND to list
// and probe, PUT/GET/DELETE for single files — over net/http alone, rather
// than a third-party WebDAV library, per the codebase's standard-library-
// first convention. Only HTTP Basic authentication is supported, which is
// what Nextcloud/ownCloud app passwords, Synology, Apache mod_dav, rclone
// serve, etc. all accept.
type webdavClient struct {
	base     *url.URL
	username string
	password string
	http     *http.Client
}

const userAgent = "Trakka-Backup/1"

func newWebDAVClient(rawURL, username, password string, allowPrivate bool) (*webdavClient, error) {
	base, err := parseWebDAVURL(rawURL)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		// Never an environment-configured proxy: the dial guard below must
		// see the real destination, not a proxy's address.
		Proxy:               nil,
		DialContext:         dialGuard{allowPrivate: allowPrivate, timeout: 15 * time.Second}.DialContext,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 15 * time.Second,
		// Generous: a server may only answer a large PUT once it has
		// finished writing the whole file on its side.
		ResponseHeaderTimeout: 2 * time.Minute,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConns:          2,
	}
	return &webdavClient{
		base:     base,
		username: username,
		password: password,
		http: &http.Client{
			Transport: transport,
			// Redirects are reported, never followed: following one would
			// resend the Basic credentials to wherever it points (possibly
			// another host), and a WebDAV folder URL that redirects is
			// almost always a mistyped one (missing trailing path, http vs
			// https) the admin should fix at the source.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			// No overall Timeout: an upload's duration scales with the
			// database size. Every call is bounded by its context instead.
		},
	}, nil
}

func (c *webdavClient) fileURL(name string) (string, error) {
	if !safeFileName.MatchString(name) {
		return "", fmt.Errorf("refusing unsafe remote file name %q", name)
	}
	return c.base.JoinPath(name).String(), nil
}

func (c *webdavClient) do(ctx context.Context, method, target string, body io.Reader, size int64, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.ContentLength = size
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", userAgent)
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &webdavError{Op: method, Target: req.URL.Path, Code: codeForTransportError(err), err: err}
	}
	return resp, nil
}

// expectStatus turns any response whose status isn't in ok into a
// webdavError, closing its body.
func expectStatus(method string, resp *http.Response, ok ...int) error {
	for _, s := range ok {
		if resp.StatusCode == s {
			return nil
		}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	e := &webdavError{Op: method, Target: resp.Request.URL.Path, Status: resp.StatusCode, Code: codeForStatus(resp.StatusCode)}
	if e.Code == CodeRedirect {
		e.Location = resp.Header.Get("Location")
	}
	return e
}

// multistatus is the subset of a PROPFIND 207 response this client reads.
// Tags are matched by local name only (no namespace), which accepts the
// "DAV:" elements whatever prefix a given server happens to bind them to.
type multistatus struct {
	Responses []struct {
		Href      string `xml:"href"`
		Propstats []struct {
			Status string `xml:"status"`
			Prop   struct {
				ResourceType struct {
					Collection *struct{} `xml:"collection"`
				} `xml:"resourcetype"`
				ContentLength string `xml:"getcontentlength"`
				LastModified  string `xml:"getlastmodified"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

const propfindBody = `<?xml version="1.0" encoding="utf-8"?>` +
	`<d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:getcontentlength/><d:getlastmodified/></d:prop></d:propfind>`

// maxPropfindResponse bounds how much of a listing is read — far more than
// a folder of backups needs, small enough that a hostile or misconfigured
// server can't balloon this process's memory.
const maxPropfindResponse = 8 << 20

func (c *webdavClient) propfind(ctx context.Context, depth string) (*multistatus, error) {
	header := http.Header{"Depth": {depth}, "Content-Type": {"application/xml; charset=utf-8"}}
	resp, err := c.do(ctx, "PROPFIND", c.base.String(), strings.NewReader(propfindBody), int64(len(propfindBody)), header)
	if err != nil {
		return nil, err
	}
	if err := expectStatus("PROPFIND", resp, http.StatusMultiStatus); err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var ms multistatus
	if err := xml.NewDecoder(io.LimitReader(resp.Body, maxPropfindResponse)).Decode(&ms); err != nil {
		return nil, &webdavError{Op: "PROPFIND", Target: c.base.Path, Code: CodeNotWebDAV, err: fmt.Errorf("unreadable PROPFIND response: %w", err)}
	}
	return &ms, nil
}

// remoteFile is one file found in the backup folder.
type remoteFile struct {
	Name     string
	Size     int64
	Modified time.Time
}

// probe checks the folder exists, the credentials are accepted, and the URL
// really is a WebDAV collection.
func (c *webdavClient) probe(ctx context.Context) error {
	ms, err := c.propfind(ctx, "0")
	if err != nil {
		return err
	}
	for _, r := range ms.Responses {
		for _, ps := range r.Propstats {
			if strings.Contains(ps.Status, " 200") && ps.Prop.ResourceType.Collection != nil {
				return nil
			}
		}
	}
	return &webdavError{Op: "PROPFIND", Target: c.base.Path, Code: CodeNotFolder}
}

// list returns the plain files (not sub-folders) directly inside the folder.
func (c *webdavClient) list(ctx context.Context) ([]remoteFile, error) {
	ms, err := c.propfind(ctx, "1")
	if err != nil {
		return nil, err
	}
	self := strings.TrimSuffix(c.base.Path, "/")
	var files []remoteFile
	for _, r := range ms.Responses {
		href, err := url.Parse(strings.TrimSpace(r.Href))
		if err != nil {
			continue
		}
		p := strings.TrimSuffix(href.Path, "/")
		if p == self || p == "" {
			continue
		}
		for _, ps := range r.Propstats {
			if !strings.Contains(ps.Status, " 200") || ps.Prop.ResourceType.Collection != nil {
				continue
			}
			f := remoteFile{Name: path.Base(p)}
			f.Size, _ = strconv.ParseInt(strings.TrimSpace(ps.Prop.ContentLength), 10, 64)
			f.Modified, _ = http.ParseTime(strings.TrimSpace(ps.Prop.LastModified))
			files = append(files, f)
			break
		}
	}
	return files, nil
}

// put uploads body (exactly size bytes) as name, replacing any existing file.
func (c *webdavClient) put(ctx context.Context, name string, body io.Reader, size int64) error {
	target, err := c.fileURL(name)
	if err != nil {
		return err
	}
	header := http.Header{"Content-Type": {"application/octet-stream"}}
	resp, err := c.do(ctx, http.MethodPut, target, body, size, header)
	if err != nil {
		return err
	}
	if err := expectStatus(http.MethodPut, resp, http.StatusOK, http.StatusCreated, http.StatusNoContent); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.Body.Close()
}

// get opens name for reading; the caller must close the returned body.
func (c *webdavClient) get(ctx context.Context, name string) (io.ReadCloser, error) {
	target, err := c.fileURL(name)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodGet, target, nil, 0, nil)
	if err != nil {
		return nil, err
	}
	if err := expectStatus(http.MethodGet, resp, http.StatusOK); err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// delete removes name; a file that is already gone counts as success.
func (c *webdavClient) delete(ctx context.Context, name string) error {
	target, err := c.fileURL(name)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodDelete, target, nil, 0, nil)
	if err != nil {
		return err
	}
	if err := expectStatus(http.MethodDelete, resp, http.StatusOK, http.StatusNoContent, http.StatusNotFound); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return resp.Body.Close()
}
