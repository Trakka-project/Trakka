// Package fluv sends what Trakka's users buy to Fluv, their personal finance
// app, which records each purchase as an expense (docs/FLUV.md).
//
// Each user connects their own Fluv in Paramètres → Intégrations: the
// purchase endpoint and the token Fluv made for its link with Trakka, plus
// the IP address(es) of Fluv the requests are locked to. Checking an item
// off a purchase list then queues the purchase in an outbox
// (fluv_purchases, migration 41) that a background sender delivers, with
// retries; un-checking it queues its cancellation. Nothing of this ever
// runs inside the request that checked the item off: Fluv being slow or
// down never slows Trakka down, and nothing is lost meanwhile.
//
// Security: the requests only ever go to the pinned addresses, never to
// what the URL's host name resolves to, and each address must pass the
// operator's Policy (pinnedDialer); the token is sealed with the instance
// key at rest (Sealer) and only ever sent in the Authorization header to
// those addresses, never logged. Import direction: internal/handlers →
// internal/fluv → internal/db, internal/models, internal/validate.
package fluv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"trakka/internal/db"
	"trakka/internal/models"
)

// sealPurpose names the Fluv token among the secrets sealed with the
// instance key (backup.Service.SealSecret).
const sealPurpose = "fluv-token"

const (
	// settleDelay is how long a new purchase waits before it is sent, so
	// that the price paid, typed right after checking the item off, or an
	// immediate un-check, corrects or drops it before Fluv ever sees it.
	settleDelay = 20 * time.Second
	// senderTick is how often the sender looks for due purchases when
	// nothing wakes it.
	senderTick = 15 * time.Second
	// batchSize bounds one pass of the sender.
	batchSize = 50
	// maxAttempts is how many times a purchase is tried before it waits
	// for the user's "Retry" — about two days with the backoff below.
	maxAttempts = 16
	maxBackoff  = 6 * time.Hour
	// retention is how long a settled purchase is kept, so that
	// un-checking or repricing its item can still reach it in Fluv.
	retention  = 180 * 24 * time.Hour
	pruneEvery = time.Hour
)

// Sealer seals the token at rest — backup.Service, whose instance key file
// lives outside the database.
type Sealer interface {
	SealSecret(purpose, plaintext string) (string, error)
	OpenSecret(purpose, sealed string) (string, error)
}

// Options configures New.
type Options struct {
	DB     *db.DB
	Sealer Sealer
	Logger *slog.Logger
	// Location is the zone the day of a purchase is taken in
	// (APP_TIMEZONE); nil means UTC.
	Location *time.Location
	Policy   Policy
	// Now is the clock; nil means time.Now. Tests move it forward rather
	// than wait for settleDelay or a backoff.
	Now func() time.Time
	// Resolver looks Fluv's host name up when a connection is saved or
	// tested (the automatic IP lock, Policy.lockTarget); nil means the
	// system resolver. Never used to send a purchase.
	Resolver Resolver
}

// Service owns the users' Fluv connections and the outbox sender. Safe for
// concurrent use.
type Service struct {
	db      *db.DB
	sealer  Sealer
	logger  *slog.Logger
	loc     *time.Location
	policy  Policy
	now     func() time.Time
	resolve Resolver
	wake    chan struct{}
}

// New builds a Service. Run starts its sender.
func New(opts Options) *Service {
	loc := opts.Location
	if loc == nil {
		loc = time.UTC
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	resolve := opts.Resolver
	if resolve == nil {
		resolve = defaultResolver
	}
	return &Service{
		db:      opts.DB,
		sealer:  opts.Sealer,
		logger:  opts.Logger,
		loc:     loc,
		policy:  opts.Policy,
		now:     now,
		resolve: resolve,
		wake:    make(chan struct{}, 1),
	}
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// Settings is a user's connection as Paramètres shows it. The token is
// write-only: only whether one is stored.
type Settings struct {
	Configured  bool   `json:"configured"`
	Enabled     bool   `json:"enabled"`
	EndpointURL string `json:"endpoint_url,omitempty"`
	// PinnedIPs are the addresses the connection is locked to, resolved
	// from the endpoint's host when it was saved or last tested: read-only.
	PinnedIPs []string `json:"pinned_ips"`
	TokenSet  bool     `json:"token_set"`
	// OperatorAllowlist says FLUV_INTEGRATION_ALLOWED_IPS is set: a locked
	// address must be in it, rather than public.
	OperatorAllowlist bool    `json:"operator_allowlist"`
	LastSuccessAt     *string `json:"last_success_at,omitempty"`
	LastErrorCode     string  `json:"last_error_code,omitempty"`
	LastErrorAt       *string `json:"last_error_at,omitempty"`
	// Pending counts the purchases (and cancellations) still to send,
	// Failed those waiting for the user.
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
}

// Update is what PUT /api/v1/integrations/fluv saves. There is no address
// to enter: Save resolves and locks it (Policy.lockTarget).
type Update struct {
	EndpointURL string `json:"endpoint_url"`
	// Token replaces the stored token; empty keeps it.
	Token string `json:"token"`
	// Enabled pauses the connection when false; absent means true.
	Enabled *bool `json:"enabled"`
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Get returns userID's connection; Configured is false when there is none.
func (s *Service) Get(ctx context.Context, userID int64) (Settings, error) {
	out := Settings{PinnedIPs: []string{}, OperatorAllowlist: len(s.policy.Allowed) > 0}
	integ, err := s.db.GetFluvIntegration(ctx, userID)
	if errors.Is(err, db.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return Settings{}, err
	}
	pins, _ := splitPins(integ.PinnedIPs)
	pending, failed, err := s.db.FluvQueueStats(ctx, userID)
	if err != nil {
		return Settings{}, err
	}
	out.Configured, out.Enabled, out.EndpointURL, out.TokenSet = true, integ.Enabled, integ.EndpointURL, integ.TokenSealed != ""
	if pins != nil {
		out.PinnedIPs = pins
	}
	out.LastSuccessAt = optionalString(integ.LastSuccessAt)
	out.LastErrorCode = integ.LastErrorCode
	out.LastErrorAt = optionalString(integ.LastErrorAt)
	out.Pending, out.Failed = pending, failed
	return out, nil
}

// Save validates and stores userID's connection, returning it as Get
// would: the endpoint's host is resolved now and the connection locked to
// the addresses it resolves to (Policy.lockTarget). Refusals are
// *ValidationError. Saving also re-queues the purchases that failed: most
// failures are fixed by new settings.
func (s *Service) Save(ctx context.Context, userID int64, u Update) (Settings, error) {
	t, err := s.policy.lockTarget(ctx, s.resolve, u.EndpointURL)
	if err != nil {
		return Settings{}, err
	}
	var sealed string
	if u.Token != "" {
		if !validToken(u.Token) {
			return Settings{}, errInvalidToken
		}
		if sealed, err = s.sealer.SealSecret(sealPurpose, u.Token); err != nil {
			return Settings{}, err
		}
	} else {
		existing, err := s.db.GetFluvIntegration(ctx, userID)
		if errors.Is(err, db.ErrNotFound) {
			return Settings{}, invalid("token", "required", "paste the token Fluv showed when you linked Trakka")
		}
		if err != nil {
			return Settings{}, err
		}
		sealed = existing.TokenSealed
	}
	enabled := u.Enabled == nil || *u.Enabled
	if err := s.db.SaveFluvIntegration(ctx, userID, t.endpoint.String(), joinPins(t.pins), sealed, enabled); err != nil {
		return Settings{}, err
	}
	if n, err := s.db.RetryFailedFluvPurchases(ctx, userID); err != nil {
		return Settings{}, err
	} else if n > 0 {
		s.Wake()
	}
	return s.Get(ctx, userID)
}

// Delete removes userID's connection and the purchases not sent yet
// (db.DeleteFluvIntegration). Returns db.ErrNotFound if there was none.
func (s *Service) Delete(ctx context.Context, userID int64) error {
	return s.db.DeleteFluvIntegration(ctx, userID)
}

// Retry re-queues userID's failed purchases and returns how many.
func (s *Service) Retry(ctx context.Context, userID int64) (int64, error) {
	n, err := s.db.RetryFailedFluvPurchases(ctx, userID)
	if err == nil && n > 0 {
		s.Wake()
	}
	return n, err
}

// TestResult is the outcome of Test.
type TestResult struct {
	OK     bool   `json:"ok"`
	Status int    `json:"status,omitempty"`
	Code   string `json:"code,omitempty"`
	// PinnedIPs are the addresses the connection is locked to after the
	// test; Relocked says the test changed them.
	PinnedIPs []string `json:"pinned_ips,omitempty"`
	Relocked  bool     `json:"relocked,omitempty"`
}

// Test checks userID's saved connection end to end, with a request that
// changes nothing: taking back a purchase that doesn't exist. Fluv answers
// it 404 only once the address and the token are accepted (403 for an
// address it doesn't allow, 401 for a wrong token). The endpoint's host is
// resolved again first, and the test runs against what it resolves to now;
// the lock moves to those addresses only if Fluv accepts the test there —
// with https, only a server holding a valid certificate for the host name
// can. Returns db.ErrNotFound if there is no connection.
func (s *Service) Test(ctx context.Context, userID int64) (TestResult, error) {
	integ, err := s.db.GetFluvIntegration(ctx, userID)
	if err != nil {
		return TestResult{}, err
	}
	stored, token, code := s.prepare(integ)
	if code == "token_unreadable" {
		_ = s.db.RecordFluvExchange(ctx, userID, code)
		return TestResult{Code: code}, nil
	}
	t, err := s.policy.lockTarget(ctx, s.resolve, integ.EndpointURL)
	var verr *ValidationError
	if errors.As(err, &verr) {
		_ = s.db.RecordFluvExchange(ctx, userID, verr.Code)
		s.logger.Warn("Fluv connection test: endpoint not lockable", "user_id", userID, "code", verr.Code)
		return TestResult{Code: verr.Code}, nil
	}
	if err != nil {
		return TestResult{}, err
	}
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return TestResult{}, err
	}
	resp, err := do(ctx, newClient(t, s.policy), http.MethodDelete,
		t.endpoint.String()+"/trakka-test-"+hex.EncodeToString(b[:]), token, nil)
	result := TestResult{}
	switch {
	case err != nil:
		result.Code = networkErrorCode(err)
	case resp.status == http.StatusNotFound || resp.status/100 == 2:
		result.OK, result.Status = true, resp.status
	default:
		result.Status, result.Code = resp.status, statusCode(resp)
	}
	if result.OK {
		newPins := joinPins(t.pins)
		if stored == nil || newPins != joinPins(stored.pins) {
			if err := s.db.SetFluvPinnedIPs(ctx, userID, newPins); err != nil {
				return TestResult{}, err
			}
			result.Relocked = true
			s.logger.Info("Fluv connection re-locked to new addresses", "user_id", userID, "pinned_ips", newPins)
		}
		result.PinnedIPs, _ = splitPins(newPins)
		_ = s.db.RecordFluvExchange(ctx, userID, "")
	} else {
		_ = s.db.RecordFluvExchange(ctx, userID, result.Code)
		s.logger.Warn("Fluv connection test failed", "user_id", userID, "status", result.Status, "code", result.Code)
	}
	return result, nil
}

// prepare turns a stored connection back into a target and its token, or
// returns the error code that prevents using it.
func (s *Service) prepare(integ *db.FluvIntegration) (*target, string, string) {
	pins, err := splitPins(integ.PinnedIPs)
	if err != nil {
		return nil, "", "blocked_address"
	}
	t, err := s.policy.validateTarget(integ.EndpointURL, pins)
	if err != nil {
		return nil, "", "blocked_address"
	}
	token, err := s.sealer.OpenSecret(sealPurpose, integ.TokenSealed)
	if err != nil || !validToken(token) {
		return nil, "", "token_unreadable"
	}
	return t, token, ""
}

// statusCode is the error code of an answer that isn't a success: Fluv's
// problem code, else one made of the status.
func statusCode(resp response) string {
	if resp.code != "" {
		return resp.code
	}
	switch {
	case resp.status >= 300 && resp.status < 400:
		return "redirect_refused"
	case resp.status == http.StatusUnauthorized:
		return "unauthenticated"
	case resp.status == http.StatusForbidden:
		return "address_not_allowed"
	case resp.status == http.StatusNotFound:
		return "not_found"
	case resp.status == http.StatusTooManyRequests:
		return "rate_limited"
	case resp.status >= 500:
		return "server_error"
	default:
		return "rejected"
	}
}

// ---------------------------------------------------------------------------
// Items checked off and un-checked
// ---------------------------------------------------------------------------

// ItemChanged queues what Fluv must learn from an item going from before to
// after on list, changed by actorID: a purchase when it was checked off, a
// cancellation when it was un-checked, a correction when what it cost
// changed while bought. Only purchase lists count, and only items with a
// price. A purchase goes to the Fluv of the person who checked the item
// off; un-checking or correcting it acts on that purchase, whoever does
// it, since Fluv should mirror whether the item is bought. Errors are
// logged, never returned: the item change itself has already succeeded.
func (s *Service) ItemChanged(ctx context.Context, actorID int64, list *models.List, before, after *models.Item) {
	if s == nil || list == nil || before == nil || after == nil || !models.IsPurchaseListType(list.Type) {
		return
	}
	var err error
	switch {
	case !before.Done && after.Done:
		err = s.record(ctx, actorID, list, after, s.now())
	case before.Done && !after.Done:
		err = s.cancelItem(ctx, after.ID)
	case before.Done && after.Done:
		err = s.correct(ctx, actorID, list, before, after)
	}
	if err != nil {
		s.logger.Error("queueing purchase for Fluv", "item_id", after.ID, "error", err)
	}
}

// record queues item, bought at at, for userID's Fluv — if they connected
// one and it is enabled.
func (s *Service) record(ctx context.Context, userID int64, list *models.List, item *models.Item, at time.Time) error {
	amount := amountOf(item)
	if amount == 0 {
		return nil
	}
	integ, err := s.db.GetFluvIntegration(ctx, userID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !integ.Enabled {
		return nil
	}
	return s.insert(ctx, userID, list, item, amount, at)
}

func (s *Service) insert(ctx context.Context, userID int64, list *models.List, item *models.Item, amount int64, at time.Time) error {
	ref, err := newRef(item.ID)
	if err != nil {
		return err
	}
	itemID := item.ID
	return s.db.InsertFluvPurchase(ctx, db.FluvPurchase{
		UserID: userID, ItemID: &itemID, Ref: ref,
		Title: titleOf(item), Store: storeOf(list), Quantity: labelQuantity(item), AmountMinor: amount,
		PurchasedAt: at.UTC().Format(time.RFC3339), PurchasedOn: at.In(s.loc).Format(time.DateOnly),
	}, s.now().Add(settleDelay))
}

// cancelItem queues the cancellation of item itemID's current purchase.
func (s *Service) cancelItem(ctx context.Context, itemID int64) error {
	active, err := s.db.ActiveFluvPurchaseForItem(ctx, itemID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.db.CancelFluvPurchase(ctx, active.ID); err != nil {
		return err
	}
	s.Wake()
	return nil
}

// correct follows a bought item whose cost or title changed: the purchase
// is corrected in place while it was never sent, else cancelled and sent
// again under a new reference (Fluv ignores a reference it already has),
// keeping its date. A bought item that only now gets a price — checked off
// first, the price paid typed after — is recorded then.
func (s *Service) correct(ctx context.Context, actorID int64, list *models.List, before, after *models.Item) error {
	amount := amountOf(after)
	active, err := s.db.ActiveFluvPurchaseForItem(ctx, after.ID)
	if errors.Is(err, db.ErrNotFound) {
		if amountOf(before) == 0 && amount > 0 {
			return s.record(ctx, actorID, list, after, s.now())
		}
		return nil
	}
	if err != nil {
		return err
	}
	title, quantity := titleOf(after), labelQuantity(after)
	if active.AmountMinor == amount && active.Title == title && active.Quantity == quantity {
		return nil
	}
	if amount == 0 {
		return s.cancelItem(ctx, after.ID)
	}
	ok, err := s.db.RepriceUnsentFluvPurchase(ctx, active.ID, title, quantity, amount)
	if err != nil || ok {
		return err
	}
	if err := s.db.CancelFluvPurchase(ctx, active.ID); err != nil {
		return err
	}
	at, err := time.Parse(time.RFC3339, active.PurchasedAt)
	if err != nil {
		at = s.now()
	}
	if err := s.insert(ctx, active.UserID, list, after, amount, at); err != nil {
		return err
	}
	s.Wake()
	return nil
}

// ---------------------------------------------------------------------------
// Sender
// ---------------------------------------------------------------------------

// Wake makes the sender look for due purchases now rather than at its next
// tick.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run is the sender: it delivers due purchases every senderTick (or when
// woken) and prunes old settled ones hourly, until ctx is canceled. One
// purchase at a time, so a user's purchases reach Fluv in order.
func (s *Service) Run(ctx context.Context) {
	if err := s.db.ReleaseFluvPurchases(ctx); err != nil {
		s.logger.Error("releasing in-flight Fluv purchases", "error", err)
	}
	ticker := time.NewTicker(senderTick)
	defer ticker.Stop()
	lastPrune := time.Time{}
	for {
		s.SendDue(ctx)
		if now := s.now(); now.Sub(lastPrune) >= pruneEvery {
			lastPrune = now
			if n, err := s.db.PruneFluvPurchases(ctx, now.Add(-retention)); err != nil {
				s.logger.Error("pruning Fluv purchases", "error", err)
			} else if n > 0 {
				s.logger.Debug("pruned settled Fluv purchases", "deleted", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// SendDue delivers the purchases due now, at most batchSize of them.
func (s *Service) SendDue(ctx context.Context) {
	for range batchSize {
		if ctx.Err() != nil {
			return
		}
		p, err := s.db.ClaimNextFluvPurchase(ctx, s.now())
		if errors.Is(err, db.ErrNotFound) {
			return
		}
		if err != nil {
			s.logger.Error("claiming Fluv purchase", "error", err)
			return
		}
		s.deliver(ctx, p)
	}
}

// deliver makes one attempt at p and records its outcome.
func (s *Service) deliver(ctx context.Context, p *db.FluvPurchase) {
	attempt := db.FluvAttempt{Acted: p.Desired, Synced: p.Synced, Attempts: p.Attempts + 1}
	finish := func() {
		// Recorded even when ctx was canceled mid-attempt (shutdown), so
		// the row isn't left in flight.
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := s.db.FinishFluvAttempt(finishCtx, p.ID, attempt); err != nil {
			s.logger.Error("recording Fluv attempt", "ref", p.Ref, "error", err)
		}
		_ = s.db.RecordFluvExchange(finishCtx, p.UserID, attempt.ErrorCode)
	}

	integ, err := s.db.GetFluvIntegration(ctx, p.UserID)
	if errors.Is(err, db.ErrNotFound) {
		return // removed meanwhile, along with this row
	}
	if err != nil {
		s.retryLater(&attempt, "internal_error")
		finish()
		return
	}
	t, token, code := s.prepare(integ)
	if code != "" {
		attempt.Failed, attempt.ErrorCode = true, code
		s.logger.Warn("Fluv purchase not sent", "user_id", p.UserID, "ref", p.Ref, "code", code)
		finish()
		return
	}

	client := newClient(t, s.policy)
	var resp response
	if p.Desired == db.FluvDesiredRecord {
		resp, err = do(ctx, client, http.MethodPost, t.endpoint.String(), token, purchaseBody{
			ID: p.Ref, Title: p.Title, Store: p.Store, Quantity: p.Quantity, AmountMinor: p.AmountMinor, Date: p.PurchasedOn,
		})
	} else {
		resp, err = do(ctx, client, http.MethodDelete, t.endpoint.String()+"/"+url.PathEscape(p.Ref), token, nil)
	}
	attempt.Status = resp.status

	switch {
	case err != nil:
		s.retryLater(&attempt, networkErrorCode(err))
	case p.Desired == db.FluvDesiredRecord && (resp.status == http.StatusOK || resp.status == http.StatusCreated):
		attempt.Synced = db.FluvSyncedRecorded
	case p.Desired == db.FluvDesiredCancel && (resp.status/100 == 2 || resp.status == http.StatusNotFound):
		// 404: Fluv has no such purchase — never recorded, or deleted there.
		attempt.Synced = db.FluvSyncedCancelled
	case resp.status == http.StatusRequestTimeout || resp.status == http.StatusTooEarly ||
		resp.status == http.StatusTooManyRequests || resp.status >= 500:
		s.retryLater(&attempt, statusCode(resp))
	default:
		// 401 (token revoked), 403 (Fluv doesn't allow this server's
		// address), 404 (no such endpoint), 422 (refused purchase), a
		// redirect...: retrying changes nothing until the user acts.
		attempt.Failed, attempt.ErrorCode = true, statusCode(resp)
	}

	if attempt.ErrorCode == "" {
		s.logger.Debug("Fluv purchase delivered", "user_id", p.UserID, "ref", p.Ref, "action", p.Desired, "status", resp.status)
	} else {
		s.logger.Warn("Fluv purchase not delivered", "user_id", p.UserID, "ref", p.Ref, "action", p.Desired,
			"status", resp.status, "code", attempt.ErrorCode, "attempt", attempt.Attempts, "gave_up", attempt.Failed)
	}
	finish()
}

// retryLater schedules the next attempt with an exponential backoff, or
// flags the purchase failed after maxAttempts.
func (s *Service) retryLater(a *db.FluvAttempt, code string) {
	a.ErrorCode = code
	if a.Attempts >= maxAttempts {
		a.Failed = true
		return
	}
	a.NextAttempt = s.now().Add(backoff(a.Attempts))
}

// backoff is the wait after the nth failed attempt: 1, 2, 4… minutes, at
// most maxBackoff.
func backoff(n int) time.Duration {
	d := time.Minute
	for i := 1; i < n && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}
