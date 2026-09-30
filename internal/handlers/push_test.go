package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"trakka/internal/config"
	"trakka/internal/models"
)

// testVAPIDConfig is a fixed, valid-shaped (but not cryptographically real)
// VAPID configuration for tests that only need app.Config.PushEnabled() to
// report true — none of these tests actually deliver a push (see
// TestSendToUsersNoSubscriptionsIsNoop's own comment for why an end-to-end
// delivery test isn't practical here), so the key values themselves are
// never used for real signing/encryption.
func testVAPIDConfig() config.Config {
	return config.Config{
		VAPIDPublicKey:  "test-public-key",
		VAPIDPrivateKey: "test-private-key",
		VAPIDSubject:    "mailto:ops@example.com",
	}
}

func TestHandlePushVAPIDPublicKeyDisabled(t *testing.T) {
	app := newTestApplication(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/push/vapid-public-key", nil)
	rec := httptest.NewRecorder()

	app.handlePushVAPIDPublicKey(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if enabled, _ := body["enabled"].(bool); enabled {
		t.Errorf("expected enabled:false when VAPID isn't configured, got %+v", body)
	}
	if _, hasKey := body["public_key"]; hasKey {
		t.Errorf("public_key must not be present when push is disabled, got %+v", body)
	}
}

func TestHandlePushVAPIDPublicKeyEnabled(t *testing.T) {
	app := newTestApplication(t)
	app.Config = testVAPIDConfig()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/push/vapid-public-key", nil)
	rec := httptest.NewRecorder()

	app.handlePushVAPIDPublicKey(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if enabled, _ := body["enabled"].(bool); !enabled {
		t.Errorf("expected enabled:true when VAPID is configured, got %+v", body)
	}
	if body["public_key"] != app.Config.VAPIDPublicKey {
		t.Errorf("public_key = %v, want %q", body["public_key"], app.Config.VAPIDPublicKey)
	}
}

func subscribeRequest(t *testing.T, user *models.User, jsonBody string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push/subscribe", strings.NewReader(jsonBody))
	return req.WithContext(context.WithValue(req.Context(), userContextKey, user))
}

func TestHandlePushSubscribeRequiresPushConfigured(t *testing.T) {
	app := newTestApplication(t)
	user := mustCreateTestUser(t, app, "user@example.com")

	rec := httptest.NewRecorder()
	app.handlePushSubscribe(rec, subscribeRequest(t, user, `{"endpoint":"https://push.example.com/ep","keys":{"p256dh":"p","auth":"a"}}`))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when push isn't configured, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePushSubscribeValidation(t *testing.T) {
	app := newTestApplication(t)
	app.Config = testVAPIDConfig()
	user := mustCreateTestUser(t, app, "user@example.com")

	cases := []struct {
		name string
		body string
	}{
		{"missing endpoint", `{"keys":{"p256dh":"p","auth":"a"}}`},
		{"empty endpoint", `{"endpoint":"","keys":{"p256dh":"p","auth":"a"}}`},
		{"non-https endpoint", `{"endpoint":"http://push.example.com/ep","keys":{"p256dh":"p","auth":"a"}}`},
		{"javascript scheme rejected by validate.URL", `{"endpoint":"javascript:alert(1)","keys":{"p256dh":"p","auth":"a"}}`},
		{"missing p256dh", `{"endpoint":"https://push.example.com/ep","keys":{"auth":"a"}}`},
		{"missing auth", `{"endpoint":"https://push.example.com/ep","keys":{"p256dh":"p"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			app.handlePushSubscribe(rec, subscribeRequest(t, user, tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandlePushSubscribeAndUnsubscribe(t *testing.T) {
	app := newTestApplication(t)
	app.Config = testVAPIDConfig()
	user := mustCreateTestUser(t, app, "user@example.com")

	rec := httptest.NewRecorder()
	app.handlePushSubscribe(rec, subscribeRequest(t, user, `{"endpoint":"https://push.example.com/ep","keys":{"p256dh":"p256","auth":"secret"}}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var sub models.PushSubscription
	if err := json.Unmarshal(rec.Body.Bytes(), &sub); err != nil {
		t.Fatalf("decoding subscribe response: %v", err)
	}
	if sub.Endpoint != "https://push.example.com/ep" {
		t.Errorf("Endpoint = %q, want the subscribed endpoint", sub.Endpoint)
	}

	subs, err := app.DB.ListPushSubscriptionsForUsers(context.Background(), []int64{user.ID})
	if err != nil {
		t.Fatalf("ListPushSubscriptionsForUsers: %v", err)
	}
	if len(subs) != 1 {
		t.Fatalf("expected exactly one stored subscription, got %d", len(subs))
	}

	unsubReq := httptest.NewRequest(http.MethodDelete, "/api/v1/push/subscribe", strings.NewReader(`{"endpoint":"https://push.example.com/ep"}`))
	unsubReq = unsubReq.WithContext(context.WithValue(unsubReq.Context(), userContextKey, user))
	rec = httptest.NewRecorder()
	app.handlePushUnsubscribe(rec, unsubReq)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Unsubscribing an endpoint that is no longer (or never was) subscribed
	// is idempotent — the desired end state already holds, so this must not
	// come back as a 404.
	unsubReq2 := httptest.NewRequest(http.MethodDelete, "/api/v1/push/subscribe", strings.NewReader(`{"endpoint":"https://push.example.com/ep"}`))
	unsubReq2 = unsubReq2.WithContext(context.WithValue(unsubReq2.Context(), userContextKey, user))
	rec = httptest.NewRecorder()
	app.handlePushUnsubscribe(rec, unsubReq2)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on a repeat unsubscribe, got %d: %s", rec.Code, rec.Body.String())
	}

	subsAfter, err := app.DB.ListPushSubscriptionsForUsers(context.Background(), []int64{user.ID})
	if err != nil {
		t.Fatalf("ListPushSubscriptionsForUsers: %v", err)
	}
	if len(subsAfter) != 0 {
		t.Fatalf("expected no subscriptions left, got %d", len(subsAfter))
	}
}

func testPushRequest(t *testing.T, user *models.User) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push/test", nil)
	return req.WithContext(context.WithValue(req.Context(), userContextKey, user))
}

func TestHandlePushTestRequiresPushConfigured(t *testing.T) {
	app := newTestApplication(t)
	user := mustCreateTestUser(t, app, "user@example.com")

	rec := httptest.NewRecorder()
	app.handlePushTest(rec, testPushRequest(t, user))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when push isn't configured, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePushTestRequiresExistingSubscription(t *testing.T) {
	app := newTestApplication(t)
	app.Config = testVAPIDConfig()
	user := mustCreateTestUser(t, app, "user@example.com")

	rec := httptest.NewRecorder()
	app.handlePushTest(rec, testPushRequest(t, user))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when the caller has no subscription, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandlePushTestReportsSubscriptionCount(t *testing.T) {
	app := newTestApplication(t)
	app.Config = testVAPIDConfig()
	user := mustCreateTestUser(t, app, "user@example.com")
	if _, err := app.DB.CreatePushSubscription(context.Background(), user.ID, "https://push.example.com/ep-1", "p", "a", ""); err != nil {
		t.Fatalf("CreatePushSubscription: %v", err)
	}
	if _, err := app.DB.CreatePushSubscription(context.Background(), user.ID, "https://push.example.com/ep-2", "p", "a", ""); err != nil {
		t.Fatalf("CreatePushSubscription: %v", err)
	}

	rec := httptest.NewRecorder()
	app.handlePushTest(rec, testPushRequest(t, user))

	// The test config's key material isn't cryptographically real and
	// push.example.com isn't a real push service, so delivery itself is
	// expected to fail (or, per the SSRF guard, never even dial out) — this
	// only asserts the handler's own contract: it found both subscriptions
	// and reported attempting delivery to them, exactly like
	// TestSendToUsersNoSubscriptionsIsNoop already covers for the
	// zero-subscriptions case.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if count, _ := body["sent_to_subscriptions"].(float64); count != 2 {
		t.Errorf("sent_to_subscriptions = %v, want 2", body["sent_to_subscriptions"])
	}
}

// TestSendToUsersNoSubscriptionsIsNoop confirms sendToUsers doesn't error or
// panic when there is simply nothing to deliver to. A genuine end-to-end
// delivery test (a fake push endpoint actually receiving an encrypted
// payload) isn't practical here for the same reason internal/scraper's own
// tests can't drive its SSRF-guarded httpClient against a local httptest
// server either: webpush.Send's SSRF guard (internal/webpush/ssrf.go)
// correctly refuses to dial a loopback address, which is exactly where
// httptest.NewServer listens — see internal/webpush's own RFC 8291 test
// vector coverage for how the encryption/signing halves of this pipeline
// are verified instead.
func TestSendToUsersNoSubscriptionsIsNoop(t *testing.T) {
	app := newTestApplication(t)
	app.Config = testVAPIDConfig()
	user := mustCreateTestUser(t, app, "user@example.com")

	// Must return promptly and without panicking even though user.ID has no
	// push_subscriptions row at all.
	app.sendToUsers(context.Background(), []int64{user.ID}, pushPayload{Title: "t", Body: "b", URL: "/"})
}

func TestSendToUsersDisabledIsNoop(t *testing.T) {
	app := newTestApplication(t)
	// app.Config is the zero value here — PushEnabled() is false.
	user := mustCreateTestUser(t, app, "user@example.com")
	if _, err := app.DB.CreatePushSubscription(context.Background(), user.ID, "https://push.example.com/ep", "p", "a", ""); err != nil {
		t.Fatalf("CreatePushSubscription: %v", err)
	}

	// Must not attempt any delivery (and therefore not hang or error) when
	// push isn't configured at all, even though a subscription exists.
	app.sendToUsers(context.Background(), []int64{user.ID}, pushPayload{Title: "t", Body: "b", URL: "/"})
}

// TestMarshalPushBodies checks the two payload variants sendToUsers picks
// between per subscription: the quiet one has no "vibrate" key at all
// (static/sw.js then shows the notification as silent), the vibrating one
// carries notificationVibratePattern, and both keep every other field.
func TestMarshalPushBodies(t *testing.T) {
	quiet, vibrating, err := marshalPushBodies(pushPayload{Title: "t", Body: "b", URL: "/", Tag: "x"})
	if err != nil {
		t.Fatalf("marshalPushBodies: %v", err)
	}

	var q map[string]any
	if err := json.Unmarshal(quiet, &q); err != nil {
		t.Fatalf("decoding quiet body: %v", err)
	}
	if _, ok := q["vibrate"]; ok {
		t.Fatalf("quiet body must not carry a vibrate key: %s", quiet)
	}

	var v pushPayload
	if err := json.Unmarshal(vibrating, &v); err != nil {
		t.Fatalf("decoding vibrating body: %v", err)
	}
	if len(v.Vibrate) != 3 || v.Vibrate[0] != 200 || v.Vibrate[1] != 100 || v.Vibrate[2] != 200 {
		t.Fatalf("unexpected vibrate pattern: %v", v.Vibrate)
	}
	if v.Title != "t" || v.Body != "b" || v.URL != "/" || v.Tag != "x" || q["tag"] != "x" {
		t.Fatalf("variants lost other fields: quiet=%s vibrating=%s", quiet, vibrating)
	}
}
