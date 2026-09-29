package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"trakka/internal/auth"
	"trakka/internal/backup"
	"trakka/internal/db"
	"trakka/internal/settings"
)

// Every /api/v1/admin/backups/... endpoint below is gated by
// authorizeAdmin first, before it reads a request body or touches the
// backup service — a backup is a copy of every account's data, and a
// restore replaces it.

// Limits for the restore upload (multipart/form-data): the encrypted
// backup itself is streamed to disk, never buffered in memory, but still
// capped; the small text fields are read into memory with their own caps.
const (
	maxRestoreUploadBytes = 1 << 30 // 1 GiB
	maxRestoreFieldBytes  = 16 << 10
	// restoreDeadline replaces the server's 15s read/write timeouts for
	// the restore request alone: uploading a large backup over a slow
	// connection, or downloading it from the WebDAV server, can take far
	// longer than any ordinary API call.
	restoreDeadline = 15 * time.Minute
)

// writeCodedError is writeError plus a machine-readable "code" the admin
// console translates into an actionable message; "error" keeps the raw
// (English, technical) detail.
func writeCodedError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}

// backupsAvailable reports false (writing a 404) when the application was
// built without a backup service — only ever the case in handler tests
// that construct an Application by hand.
func (app *Application) backupsAvailable(w http.ResponseWriter) bool {
	if app.Backups == nil {
		writeError(w, http.StatusNotFound, "backups are not available on this server")
		return false
	}
	return true
}

func (app *Application) handleAdminBackupsStatus(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) || !app.backupsAvailable(w) {
		return
	}
	st, err := app.Backups.Status(r.Context())
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// adminBackupsConfigRequest is the PUT body: a full replacement of the
// backup configuration. webdav_password is write-only and optional — empty
// or omitted keeps the stored one (see adminSettingsUpdate's identical
// convention for the OIDC client secret).
type adminBackupsConfigRequest struct {
	WebDAVURL      string `json:"webdav_url"`
	WebDAVUsername string `json:"webdav_username"`
	WebDAVPassword string `json:"webdav_password"`
	AutoEnabled    bool   `json:"auto_enabled"`
	Frequency      string `json:"frequency"`
	Time           string `json:"time"`
	Weekday        int    `json:"weekday"`
	Retention      int    `json:"retention"`
}

func (app *Application) handleAdminBackupsConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) || !app.backupsAvailable(w) {
		return
	}
	var body adminBackupsConfigRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	err := app.Backups.SaveConfig(r.Context(), backup.ConfigUpdate{
		WebDAVURL:      strings.TrimSpace(body.WebDAVURL),
		WebDAVUsername: strings.TrimSpace(body.WebDAVUsername),
		WebDAVPassword: body.WebDAVPassword,
		AutoEnabled:    body.AutoEnabled,
		Frequency:      body.Frequency,
		Time:           body.Time,
		Weekday:        body.Weekday,
		Retention:      body.Retention,
	})
	var ve *backup.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, ve.Message)
		return
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.handleAdminBackupsStatus(w, r)
}

// adminBackupsTestRequest is an unsaved WebDAV configuration to try out;
// an empty webdav_password means "the stored one", so the admin can re-test
// a saved configuration without retyping it.
type adminBackupsTestRequest struct {
	WebDAVURL      string `json:"webdav_url"`
	WebDAVUsername string `json:"webdav_username"`
	WebDAVPassword string `json:"webdav_password"`
}

// handleAdminBackupsTest reports a failed connection as a normal 200 result
// ({"ok": false, "code": ..., "error": ...}) rather than an error status:
// the test itself ran fine, and the frontend treats 502/503/504 as "the
// Trakka server is unreachable", which is exactly the wrong message here.
func (app *Application) handleAdminBackupsTest(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) || !app.backupsAvailable(w) {
		return
	}
	var body adminBackupsTestRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	res, err := app.Backups.TestConnection(r.Context(), backup.ConnectionSettings{
		URL:      strings.TrimSpace(body.WebDAVURL),
		Username: strings.TrimSpace(body.WebDAVUsername),
		Password: body.WebDAVPassword,
	})
	var ve *backup.ValidationError
	if errors.As(err, &ve) {
		writeError(w, http.StatusBadRequest, ve.Message)
		return
	}
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleAdminBackupsRun starts a backup in the background and answers 202
// straight away; the admin console polls GET /api/v1/admin/backups until
// "running" clears.
func (app *Application) handleAdminBackupsRun(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) || !app.backupsAvailable(w) {
		return
	}
	switch err := app.Backups.StartManualBackup(r.Context()); {
	case errors.Is(err, backup.ErrNotConfigured):
		writeCodedError(w, http.StatusBadRequest, "not_configured", err.Error())
		return
	case errors.Is(err, backup.ErrBusy):
		writeCodedError(w, http.StatusConflict, "busy", err.Error())
		return
	case err != nil:
		app.serverError(w, r, err)
		return
	}
	st, err := app.Backups.Status(r.Context())
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

// handleAdminBackupsRemoteList lists the backup files on the WebDAV server.
// Like the connection test, a WebDAV-side failure is reported in the body
// ({"files": [], "code": ..., "error": ...}) with a 200, never as a
// 502-style status the frontend would mistake for Trakka itself being down.
func (app *Application) handleAdminBackupsRemoteList(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) || !app.backupsAvailable(w) {
		return
	}
	files, err := app.Backups.ListRemote(r.Context())
	switch {
	case errors.Is(err, backup.ErrNotConfigured):
		writeCodedError(w, http.StatusBadRequest, "not_configured", err.Error())
		return
	case err != nil && backup.ErrorCode(err) == "":
		app.serverError(w, r, err)
		return
	case err != nil:
		writeJSON(w, http.StatusOK, map[string]any{"files": []backup.RemoteBackup{}, "code": backup.ErrorCode(err), "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

// handleAdminBackupsKeyExport reveals the instance's backup encryption key
// for the admin to save off-server. A POST, not a GET, even though it
// returns data: it also records that this key has now been saved (clearing
// the "key not saved" warning), and this codebase's CSRF reasoning rests
// on no GET route ever changing state (see csrf.go).
func (app *Application) handleAdminBackupsKeyExport(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) || !app.backupsAvailable(w) {
		return
	}
	exported, err := app.Backups.ExportKey(r.Context())
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.Logger.Info("backup encryption key exported", "admin_user_id", userFromContext(r).ID, "fingerprint", exported.Fingerprint)
	writeJSON(w, http.StatusOK, exported)
}

// handleAdminBackupsRestore replaces the live database with a backup —
// either an uploaded .tkb file ("file") or one already in the WebDAV folder
// ("remote_name") — decrypted with the supplied key ("key" as text, or a
// "key_file" upload; omitted means the instance's current key). The body
// is multipart/form-data, read part by part in whatever order the fields
// arrive: the uploaded backup is streamed to a staging file on the data
// volume, never held in memory.
//
// On success every session in the restored database is whatever it was at
// backup time — typically the caller's own session no longer exists, and
// the admin console sends them back to the login page.
func (app *Application) handleAdminBackupsRestore(w http.ResponseWriter, r *http.Request) {
	if !app.authorizeAdmin(w, r) || !app.backupsAvailable(w) {
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(restoreDeadline))
	_ = rc.SetWriteDeadline(time.Now().Add(restoreDeadline))

	r.Body = http.MaxBytesReader(w, r.Body, maxRestoreUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart/form-data body")
		return
	}

	var keyText, remoteName string
	var staged *backup.StagedUpload
	// A closure, not `defer staged.Discard()`, which would bind the still-nil
	// value; a no-op once RestoreFromUpload has consumed the upload.
	defer func() { staged.Discard() }()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "reading upload: "+err.Error())
			return
		}
		switch part.FormName() {
		case "key", "key_file":
			value, err := readFormField(part)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if strings.TrimSpace(value) != "" {
				keyText = value
			}
		case "remote_name":
			if remoteName, err = readFormField(part); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			remoteName = strings.TrimSpace(remoteName)
		case "file":
			if staged != nil {
				writeError(w, http.StatusBadRequest, "only one backup file can be restored at a time")
				return
			}
			if staged, err = app.Backups.StageUpload(part); err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					writeError(w, http.StatusBadRequest, fmt.Sprintf("the backup file is larger than the %d MiB limit", maxRestoreUploadBytes>>20))
					return
				}
				app.serverError(w, r, err)
				return
			}
		default:
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unexpected form field %q", part.FormName()))
			return
		}
	}

	if (staged == nil) == (remoteName == "") {
		writeError(w, http.StatusBadRequest, "provide exactly one of an uploaded backup file or a remote backup name")
		return
	}

	var key backup.Key
	if keyText != "" {
		if key, err = backup.ParseKey(keyText); err != nil {
			writeCodedError(w, http.StatusBadRequest, "invalid_key", "the backup key is invalid — check it was copied completely and without typos")
			return
		}
	} else {
		current, ok, err := app.Backups.CurrentKey()
		if err != nil {
			app.serverError(w, r, err)
			return
		}
		if !ok {
			writeCodedError(w, http.StatusBadRequest, "key_required", backup.ErrNoKey.Error())
			return
		}
		key = current
	}

	// Detached from r.Context(): once the copy into the live database has
	// begun it must not be abandoned halfway because the browser tab was
	// closed; the deadline still bounds it.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), restoreDeadline)
	defer cancel()

	var result backup.RestoreResult
	if staged != nil {
		result, err = app.Backups.RestoreFromUpload(ctx, staged, key)
	} else {
		result, err = app.Backups.RestoreFromRemote(ctx, remoteName, key)
	}
	if err != nil {
		app.writeRestoreError(w, r, err)
		return
	}

	app.Logger.Warn("database restored from backup by admin",
		"admin_user_id", userFromContext(r).ID, "source_remote", remoteName, "from_schema_version", result.FromSchemaVersion,
		"safety_copy", result.SafetyCopy, "key_adopted", result.KeyAdopted)

	// The restored system_settings may configure OIDC differently from what
	// was in effect a moment ago; bring the live client in line with it.
	app.reloadOIDCFromSettings(ctx)

	writeJSON(w, http.StatusOK, result)
}

func readFormField(part io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(part, maxRestoreFieldBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading form field: %w", err)
	}
	if len(data) > maxRestoreFieldBytes {
		return "", errors.New("form field too large")
	}
	return string(data), nil
}

// writeRestoreError maps a restore failure to a 4xx with a code the admin
// console translates, or a 500 for anything unexpected.
func (app *Application) writeRestoreError(w http.ResponseWriter, r *http.Request, err error) {
	var wrongKey *backup.WrongKeyError
	switch {
	case errors.Is(err, backup.ErrBusy):
		writeCodedError(w, http.StatusConflict, "busy", err.Error())
	case errors.As(err, &wrongKey):
		writeCodedError(w, http.StatusBadRequest, "wrong_key", err.Error())
	case errors.Is(err, backup.ErrNotBackup):
		writeCodedError(w, http.StatusBadRequest, "not_a_backup", err.Error())
	case errors.Is(err, backup.ErrUnsupportedVersion):
		writeCodedError(w, http.StatusBadRequest, "unsupported_version", err.Error())
	case errors.Is(err, backup.ErrCorrupt), errors.Is(err, db.ErrRestoreCorrupt):
		writeCodedError(w, http.StatusBadRequest, "corrupt", err.Error())
	case errors.Is(err, db.ErrRestoreNotTrakka):
		writeCodedError(w, http.StatusBadRequest, "not_trakka", err.Error())
	case errors.Is(err, db.ErrRestoreTooNew):
		writeCodedError(w, http.StatusBadRequest, "too_new", err.Error())
	case errors.Is(err, backup.ErrNotConfigured):
		writeCodedError(w, http.StatusBadRequest, "not_configured", err.Error())
	case errors.Is(err, backup.ErrInvalidRemoteName):
		writeCodedError(w, http.StatusBadRequest, "invalid_name", err.Error())
	case backup.ErrorCode(err) != "":
		writeCodedError(w, http.StatusBadRequest, backup.ErrorCode(err), err.Error())
	default:
		app.serverError(w, r, err)
	}
}

// reloadOIDCFromSettings rebuilds the live OIDC client from whatever
// system_settings now says — after a restore, which can bring back a
// different SSO configuration than the one in effect when the process
// started. Mirrors cmd/server's startup logic, including its
// degrade-rather-than-fail posture: an unreachable IdP leaves OIDC
// disabled (logged) instead of failing the already-completed restore.
func (app *Application) reloadOIDCFromSettings(ctx context.Context) {
	if app.Auth == nil {
		return
	}
	current, err := settings.Resolve(ctx, app.DB, app.Config)
	if err != nil {
		app.Logger.Error("reloading settings after restore", "error", err)
		return
	}
	if !current.OIDCEnabled || current.OIDCIssuer == "" || current.OIDCClientID == "" ||
		current.OIDCClientSecret == "" || app.Config.BaseURL == "" {
		app.Auth.SetOIDC(nil)
		return
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
	defer cancel()
	client, err := auth.NewOIDCClient(discoveryCtx, current.OIDCIssuer, current.OIDCClientID, current.OIDCClientSecret, app.Config.BaseURL+"/auth/oidc/callback")
	if err != nil {
		app.Logger.Error("oidc discovery failed after restore; OIDC disabled until fixed", "error", err)
		app.Auth.SetOIDC(nil)
		return
	}
	app.Auth.SetOIDC(client)
}
