package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"trakka/internal/db"
)

const (
	davUser = "alice"
	davPass = "app-password"
)

// fakeDAV is a small in-memory WebDAV server behind HTTP Basic auth, with
// a single /backups/ folder — the stand-in for a Nextcloud instance. It
// implements just what the client uses (PROPFIND depth 0/1, PUT, GET,
// DELETE) and answers PROPFIND in the same prefixed-"DAV:" multistatus
// shape Nextcloud does.
type fakeDAV struct {
	srv   *httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	down  atomic.Bool // when set, every request fails with 503
}

const davFolder = "/backups/"

func newFakeDAV(t *testing.T) *fakeDAV {
	t.Helper()
	f := &fakeDAV{files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDAV) serve(w http.ResponseWriter, r *http.Request) {
	if f.down.Load() {
		http.Error(w, "maintenance", http.StatusServiceUnavailable)
		return
	}
	if u, p, ok := r.BasicAuth(); !ok || u != davUser || p != davPass {
		w.Header().Set("WWW-Authenticate", `Basic realm="dav"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if r.URL.Path == davFolder || r.URL.Path == strings.TrimSuffix(davFolder, "/") {
		if r.Method != "PROPFIND" {
			http.Error(w, "not allowed", http.StatusMethodNotAllowed)
			return
		}
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
		b.WriteString(`<d:response><d:href>` + davFolder + `</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
		if r.Header.Get("Depth") == "1" {
			for name, data := range f.files {
				fmt.Fprintf(&b, `<d:response><d:href>%s%s</d:href><d:propstat><d:prop><d:resourcetype/><d:getcontentlength>%d</d:getcontentlength><d:getlastmodified>Mon, 28 Sep 2026 10:00:00 GMT</d:getlastmodified></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`,
					davFolder, url.PathEscape(name), len(data))
			}
		}
		b.WriteString(`</d:multistatus>`)
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, b.String())
		return
	}

	name, ok := strings.CutPrefix(r.URL.Path, davFolder)
	if !ok || name == "" || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.files[name] = data
		w.WriteHeader(http.StatusCreated)
	case http.MethodGet:
		data, ok := f.files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	case http.MethodDelete:
		if _, ok := f.files[name]; !ok {
			http.NotFound(w, r)
			return
		}
		delete(f.files, name)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "not allowed", http.StatusMethodNotAllowed)
	}
}

func (f *fakeDAV) url() string { return f.srv.URL + "/backups" }

func (f *fakeDAV) names(t *testing.T) []string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.files))
	for name := range f.files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (f *fakeDAV) file(name string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.files[name]
}

func (f *fakeDAV) put(name string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[name] = data
}

type testEnv struct {
	svc *Service
	db  *db.DB
	dav *fakeDAV
	now time.Time
}

func newTestEnv(t *testing.T, dav *fakeDAV) *testEnv {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dbPath := filepath.Join(t.TempDir(), "trakka.db")
	d, err := db.Open(dbPath, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	env := &testEnv{db: d, dav: dav, now: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)}
	env.svc = New(Options{DB: d, DBPath: dbPath, Logger: logger, Location: time.UTC, AllowPrivateNetworks: true})
	env.svc.now = func() time.Time { return env.now }
	return env
}

func (e *testEnv) configure(t *testing.T, auto bool, retention int) {
	t.Helper()
	err := e.svc.SaveConfig(context.Background(), ConfigUpdate{
		WebDAVURL: e.dav.url(), WebDAVUsername: davUser, WebDAVPassword: davPass,
		AutoEnabled: auto, Frequency: FrequencyDaily, Time: "03:00", Retention: retention,
	})
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
}

func (e *testEnv) mustCreateUser(t *testing.T, email string) {
	t.Helper()
	hash := "x"
	if _, err := e.db.CreateUser(context.Background(), email, &hash, nil, nil, email); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) backupNow(t *testing.T, source string) string {
	t.Helper()
	if err := e.svc.acquire("backup", source); err != nil {
		t.Fatal(err)
	}
	defer e.svc.release()
	run := e.svc.runBackup(context.Background(), source)
	if run.Status != "success" {
		t.Fatalf("backup failed: %s", run.Error)
	}
	return run.FileName
}

// TestBackupRestoreEndToEnd drives the whole feature against a real WebDAV
// server: configure, test the connection, back up, change data, restore
// from the remote copy, and confirm the change is rolled back.
func TestBackupRestoreEndToEnd(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, newFakeDAV(t))
	env.configure(t, false, 7)
	env.mustCreateUser(t, "before@example.com")

	res, err := env.svc.TestConnection(ctx, ConnectionSettings{URL: env.dav.url(), Username: davUser})
	if err != nil || !res.OK || !res.Writable {
		t.Fatalf("TestConnection with the stored password = %+v, %v", res, err)
	}

	name := env.backupNow(t, SourceManual)
	if got := env.dav.names(t); len(got) != 1 || got[0] != name {
		t.Fatalf("remote folder = %v, want just %s (and no leftover write-test file)", got, name)
	}

	// The uploaded file is ciphertext, not a readable SQLite database.
	if head := env.dav.file(name); len(head) < 16 || bytes.HasPrefix(head, []byte("SQLite format 3")) || string(head[:8]) != fileMagic {
		t.Fatalf("remote file %s is not an encrypted backup", name)
	}

	env.mustCreateUser(t, "after@example.com")

	listed, err := env.svc.ListRemote(ctx)
	if err != nil || len(listed) != 1 || listed[0].Name != name {
		t.Fatalf("ListRemote = %+v, %v", listed, err)
	}

	key, ok, err := env.svc.CurrentKey()
	if err != nil || !ok {
		t.Fatalf("CurrentKey: ok=%v err=%v", ok, err)
	}
	result, err := env.svc.RestoreFromRemote(ctx, name, key)
	if err != nil {
		t.Fatalf("RestoreFromRemote: %v", err)
	}
	if result.KeyAdopted {
		t.Fatal("restoring with the instance's own key must not report a key change")
	}
	if _, err := env.db.GetUserByEmail(ctx, "before@example.com"); err != nil {
		t.Fatalf("pre-backup user missing after restore: %v", err)
	}
	if _, err := env.db.GetUserByEmail(ctx, "after@example.com"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("post-backup user should be gone after restore, got %v", err)
	}

	// The restored database predates its own backup run; the history must
	// still show it, carried over from before the restore.
	runs, err := env.db.ListBackupRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].FileName != name || runs[0].Status != "success" {
		t.Fatalf("history after restore = %+v, want the restored backup's own run", runs)
	}
}

// TestRestoreOntoFreshInstanceAdoptsKey is the disaster-recovery path: a
// brand new instance (its own key, no config) restores an uploaded backup
// with the original instance's key, and ends up with the original's backup
// configuration — including a WebDAV password it can decrypt, because it
// adopted the original key.
func TestRestoreOntoFreshInstanceAdoptsKey(t *testing.T) {
	ctx := context.Background()
	dav := newFakeDAV(t)
	original := newTestEnv(t, dav)
	original.configure(t, true, 7)
	original.mustCreateUser(t, "owner@example.com")
	name := original.backupNow(t, SourceManual)
	exported, err := original.svc.ExportKey(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// The backup file as the admin would have downloaded it.
	remote := bytes.NewReader(dav.file(name))

	fresh := newTestEnv(t, newFakeDAV(t))
	if _, err := fresh.svc.ExportKey(ctx); err != nil { // the fresh instance has a key of its own
		t.Fatal(err)
	}
	key, err := ParseKey(exported.FileContent)
	if err != nil {
		t.Fatal(err)
	}

	staged, err := fresh.svc.StageUpload(remote)
	if err != nil {
		t.Fatal(err)
	}
	result, err := fresh.svc.RestoreFromUpload(ctx, staged, key)
	if err != nil {
		t.Fatalf("RestoreFromUpload: %v", err)
	}
	if !result.KeyAdopted {
		t.Fatal("expected the original key to be adopted")
	}
	if _, err := os.Stat(staged.path); !os.IsNotExist(err) {
		t.Fatal("staged upload was not cleaned up")
	}
	if _, err := fresh.db.GetUserByEmail(ctx, "owner@example.com"); err != nil {
		t.Fatalf("restored user missing: %v", err)
	}

	st, err := fresh.svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Config.WebDAVURL != dav.url()+"/" || !st.Config.AutoEnabled {
		t.Fatalf("restored config = %+v", st.Config)
	}
	if st.Key.Fingerprint != key.Fingerprint() || !st.Key.Saved {
		t.Fatalf("key info after restore = %+v", st.Key)
	}
	// The restored, sealed WebDAV password must open with the adopted key.
	if res, err := fresh.svc.TestConnection(ctx, ConnectionSettings{URL: st.Config.WebDAVURL, Username: davUser}); err != nil || !res.OK {
		t.Fatalf("connection with the restored password = %+v, %v", res, err)
	}
}

// TestRestoreRejectsWrongKeyAndGarbage makes sure a failed restore never
// touches the live database.
func TestRestoreRejectsWrongKeyAndGarbage(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, newFakeDAV(t))
	env.configure(t, false, 7)
	name := env.backupNow(t, SourceManual)
	env.mustCreateUser(t, "keep@example.com")

	other, _ := GenerateKey()
	var wrong *WrongKeyError
	if _, err := env.svc.RestoreFromRemote(ctx, name, other); !errors.As(err, &wrong) {
		t.Fatalf("wrong key: got %v", err)
	}
	staged, err := env.svc.StageUpload(strings.NewReader("not a backup"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.svc.RestoreFromUpload(ctx, staged, other); !errors.Is(err, ErrNotBackup) {
		t.Fatalf("garbage upload: got %v", err)
	}
	if _, err := env.svc.RestoreFromRemote(ctx, "../trakka.db", other); !errors.Is(err, ErrInvalidRemoteName) {
		t.Fatalf("path traversal name: got %v", err)
	}
	if _, err := env.db.GetUserByEmail(ctx, "keep@example.com"); err != nil {
		t.Fatalf("live data changed by a rejected restore: %v", err)
	}
}

// TestRetentionKeepsNewestAndIgnoresOtherFiles runs more backups than the
// retention allows and checks only the oldest backup files were deleted —
// an unrelated file in the same folder must survive.
func TestRetentionKeepsNewestAndIgnoresOtherFiles(t *testing.T) {
	env := newTestEnv(t, newFakeDAV(t))
	env.configure(t, false, 2)

	env.dav.put("notes.txt", []byte("keep me"))

	var made []string
	for i := 0; i < 4; i++ {
		env.now = env.now.Add(time.Hour)
		made = append(made, env.backupNow(t, SourceManual))
	}
	got := strings.Join(env.dav.names(t), ",")
	want := strings.Join([]string{"notes.txt", made[2], made[3]}, ",")
	if got != want {
		t.Fatalf("remote folder = %s, want %s", got, want)
	}
}

// TestConnectionFailures checks the error codes the admin console
// translates: bad credentials, a missing folder, and the SSRF guard
// refusing a loopback server when private networks aren't allowed.
func TestConnectionFailures(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, newFakeDAV(t))

	cases := []struct {
		name string
		conn ConnectionSettings
		code string
	}{
		{"bad password", ConnectionSettings{URL: env.dav.url(), Username: davUser, Password: "nope"}, CodeAuthFailed},
		{"missing folder", ConnectionSettings{URL: env.dav.srv.URL + "/nope/", Username: davUser, Password: davPass}, CodeNotFound},
	}
	for _, tc := range cases {
		res, err := env.svc.TestConnection(ctx, tc.conn)
		if err != nil || res.OK || res.Code != tc.code {
			t.Errorf("%s: got %+v, %v; want code %s", tc.name, res, err, tc.code)
		}
	}

	env.svc.allowPrivate = false
	res, err := env.svc.TestConnection(ctx, ConnectionSettings{URL: env.dav.url(), Username: davUser, Password: davPass})
	if err != nil || res.OK || res.Code != CodeBlockedAddress {
		t.Fatalf("loopback without BACKUP_WEBDAV_ALLOW_PRIVATE: got %+v, %v", res, err)
	}

	var ve *ValidationError
	for _, bad := range []string{"javascript:alert(1)", "ftp://host/x", "https://user:pw@host/dav/", "https://host/dav/?x=1"} {
		if _, err := env.svc.TestConnection(ctx, ConnectionSettings{URL: bad}); !errors.As(err, &ve) {
			t.Errorf("URL %q: got %v, want a ValidationError", bad, err)
		}
	}
}

// TestSchedulerAndAlerts walks the scheduler through a day: nothing runs
// for a slot that passed before automatic backups were switched on, the
// next slot runs once, a failure is retried after the delay (up to the
// attempt cap), and the alerts appear and clear as they should.
func TestSchedulerAndAlerts(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t, newFakeDAV(t))
	env.configure(t, true, 7) // armed at 2026-09-28 10:00 UTC, daily at 03:00

	runs := func() int {
		r, err := env.db.ListBackupRuns(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		return len(r)
	}
	alertCodes := func() string {
		st, err := env.svc.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var codes []string
		for _, a := range st.Alerts {
			codes = append(codes, a.Code)
		}
		return strings.Join(codes, ",")
	}

	if got := alertCodes(); got != AlertKeyNotSaved {
		t.Fatalf("fresh config alerts = %q, want just %s", got, AlertKeyNotSaved)
	}
	if _, err := env.svc.ExportKey(ctx); err != nil {
		t.Fatal(err)
	}
	if got := alertCodes(); got != "" {
		t.Fatalf("alerts after exporting the key = %q", got)
	}

	env.now = time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC) // today's 03:00 was before arming
	env.svc.RunScheduledIfDue(ctx)
	if runs() != 0 {
		t.Fatal("a slot from before the schedule was armed must not run")
	}

	env.now = time.Date(2026, 9, 29, 3, 1, 0, 0, time.UTC)
	env.svc.RunScheduledIfDue(ctx)
	env.now = env.now.Add(5 * time.Minute)
	env.svc.RunScheduledIfDue(ctx)
	if runs() != 1 {
		t.Fatalf("expected exactly one run for the 09-29 slot, got %d", runs())
	}

	// Next night the server is down.
	env.dav.down.Store(true)
	env.now = time.Date(2026, 9, 30, 3, 0, 30, 0, time.UTC)
	env.svc.RunScheduledIfDue(ctx)
	if got := alertCodes(); got != AlertLastBackupFailed {
		t.Fatalf("alerts after a failed scheduled run = %q", got)
	}
	env.now = env.now.Add(10 * time.Minute)
	env.svc.RunScheduledIfDue(ctx) // too soon to retry
	env.now = env.now.Add(30 * time.Minute)
	env.svc.RunScheduledIfDue(ctx) // retry #1
	env.now = env.now.Add(31 * time.Minute)
	env.svc.RunScheduledIfDue(ctx) // retry #2
	env.now = env.now.Add(31 * time.Minute)
	env.svc.RunScheduledIfDue(ctx) // capped
	if runs() != 4 {
		t.Fatalf("expected 1 success + 3 attempts, got %d runs", runs())
	}

	// Four days without a success: both alerts.
	env.now = time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	if got := alertCodes(); got != AlertLastBackupFailed+","+AlertNoRecentSuccess {
		t.Fatalf("alerts after days of failures = %q", got)
	}

	// The server comes back; the next slot succeeds and clears everything.
	env.dav.down.Store(false)
	env.now = time.Date(2026, 10, 3, 3, 0, 5, 0, time.UTC)
	env.svc.RunScheduledIfDue(ctx)
	if got := alertCodes(); got != "" {
		t.Fatalf("alerts after a successful run = %q", got)
	}
}

// TestSaveConfigValidation covers the rejected inputs.
func TestSaveConfigValidation(t *testing.T) {
	env := newTestEnv(t, newFakeDAV(t))
	base := ConfigUpdate{WebDAVURL: env.dav.url(), Frequency: FrequencyDaily, Time: "03:00", Retention: 7}

	bad := map[string]func(u *ConfigUpdate){
		"bad url":       func(u *ConfigUpdate) { u.WebDAVURL = "javascript:alert(1)" },
		"bad frequency": func(u *ConfigUpdate) { u.Frequency = "hourly" },
		"bad time":      func(u *ConfigUpdate) { u.Time = "25:00" },
		"bad weekday":   func(u *ConfigUpdate) { u.Weekday = 7 },
		"zero retain":   func(u *ConfigUpdate) { u.Retention = 0 },
		"auto, no url":  func(u *ConfigUpdate) { u.WebDAVURL = ""; u.AutoEnabled = true },
	}
	for name, mutate := range bad {
		u := base
		mutate(&u)
		var ve *ValidationError
		if err := env.svc.SaveConfig(context.Background(), u); !errors.As(err, &ve) {
			t.Errorf("%s: got %v, want a ValidationError", name, err)
		}
	}

	// A valid save stores the URL normalized with a trailing slash and the
	// password sealed, never in plaintext.
	base.WebDAVPassword = davPass
	if err := env.svc.SaveConfig(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	stored, err := env.db.GetAllSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored[settingWebDAVURL] != env.dav.url()+"/" {
		t.Fatalf("stored URL %q", stored[settingWebDAVURL])
	}
	if stored[settingWebDAVPassword] == "" || strings.Contains(stored[settingWebDAVPassword], davPass) {
		t.Fatalf("stored password %q is not sealed", stored[settingWebDAVPassword])
	}

	// Clearing the URL switches backups off and drops the credentials.
	cleared := base
	cleared.WebDAVURL, cleared.WebDAVPassword = "", ""
	if err := env.svc.SaveConfig(context.Background(), cleared); err != nil {
		t.Fatal(err)
	}
	st, err := env.svc.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Config.WebDAVURL != "" || st.Config.WebDAVPasswordSet {
		t.Fatalf("config after clearing = %+v", st.Config)
	}
}
