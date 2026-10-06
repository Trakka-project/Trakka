package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/validate"
)

// authorizeSpaceOwner writes a 404 (never a 403 — matches
// GetCustomCategoryForUser's own "don't let a lookup confirm another user's
// data exists" convention, shared with handleCustomCategoriesUpdate/Delete)
// and returns false unless the caller owns categoryID. Sharing a Space is
// the owning user's call alone, the same as renaming/deleting it.
func (app *Application) authorizeSpaceOwner(w http.ResponseWriter, r *http.Request, categoryID int64) bool {
	if _, err := app.DB.GetCustomCategoryForUser(r.Context(), categoryID, userFromContext(r).ID); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "custom category not found")
		return false
	} else if err != nil {
		app.serverError(w, r, err)
		return false
	}
	return true
}

// handleSpaceShareIndex lists everyone a Space has been shared with —
// the roster shown in the share modal.
func (app *Application) handleSpaceShareIndex(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !app.authorizeSpaceOwner(w, r, id) {
		return
	}

	shares, err := app.DB.ListSpaceShares(r.Context(), id)
	if err != nil {
		app.serverError(w, r, err)
		return
	}

	invitations, err := app.DB.ListPendingInvitations(r.Context(), db.InvitationKindSpace, id)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	for _, inv := range invitations {
		shares = append(shares, &models.SpaceShare{
			CustomCategoryID: id,
			Permission:       inv.Permission,
			CreatedAt:        inv.CreatedAt,
			Email:            inv.Email,
			Pending:          true,
		})
	}

	writeJSON(w, http.StatusOK, shares)
}

// handleSpaceShareCreate grants (or updates the permission of) a Space
// share, looked up by the recipient's email — mirrors
// handleHouseMembersInvite's "no email-sending infrastructure, so fail
// clearly rather than creating a ghost row" reasoning.
func (app *Application) handleSpaceShareCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !app.authorizeSpaceOwner(w, r, id) {
		return
	}

	var in struct {
		Email      string `json:"email"`
		Permission string `json:"permission"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Email = validate.Text(in.Email)
	if in.Email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	if !validate.MaxLen(in.Email, validate.MaxEmailLen) {
		writeError(w, http.StatusBadRequest, "email is too long")
		return
	}
	if !models.ValidSharePermissions[in.Permission] {
		writeError(w, http.StatusBadRequest, "permission must be \"read\" or \"write\"")
		return
	}

	// Recorded against the email, never a resolved user id, and granting
	// nothing until the recipient signs in — see handleHouseMembersInvite
	// and docs/AUDIT.md finding L-06 for why this endpoint must answer the same
	// whether or not the address has an account here. Sharing with your own
	// address is still rejected: that reveals only your own identity.
	if strings.EqualFold(in.Email, userFromContext(r).Email) {
		writeError(w, http.StatusBadRequest, "cannot share a space with yourself")
		return
	}

	invitation, err := app.DB.CreatePendingInvitation(
		r.Context(), db.InvitationKindSpace, id, in.Email, in.Permission, userFromContext(r).ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.notifyInvitation(db.InvitationKindSpace, id, invitation.Email, userFromContext(r))
	writeJSON(w, http.StatusCreated, invitation)
}

// handleSpaceShareInvitationRevoke withdraws an outstanding Space invitation
// that has not been materialized into a space_shares row yet.
func (app *Application) handleSpaceShareInvitationRevoke(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !app.authorizeSpaceOwner(w, r, id) {
		return
	}
	app.revokeInvitation(w, r, db.InvitationKindSpace, id)
}

// revokeInvitation is the shared tail of the two share-invitation revoke
// handlers (the caller does its own authorization first).
func (app *Application) revokeInvitation(w http.ResponseWriter, r *http.Request, kind string, targetID int64) {
	email := validate.Text(r.URL.Query().Get("email"))
	if email == "" {
		writeError(w, http.StatusBadRequest, "email query parameter is required")
		return
	}
	if err := app.DB.DeletePendingInvitation(r.Context(), kind, targetID, email); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (app *Application) handleSpaceShareRevoke(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if !app.authorizeSpaceOwner(w, r, id) {
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("userId"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	if err := app.DB.RevokeSpaceShare(r.Context(), id, userID); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "share not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSpaceSharePin lets any viewer who can see a Space without owning it
// pin or unpin the whole thing, so it shows up in their own "Espaces" tab
// and every list reachable through it shows up pinned on their own
// dashboard too — see db.ListSpacesVisibleToUser and the space_share/
// house_member branches of db.ListSharedListsForUser/
// db.ListPinnedHouseSpaceLists, which derive each such list's own pinned
// flag from this same row, so pinning a Space needs no per-list action.
// Mirrors handleListSharePin's authorization shape exactly: the caller here
// is the viewer, never the Space's owner, so this is deliberately NOT gated
// behind authorizeSpaceOwner. There are two ways a viewer can be entitled to
// pin, tried in order: an explicit space_shares grant first
// (SetSpaceSharePinned, unchanged from before this comment), and — only if
// that comes back ErrNotFound, meaning nobody explicitly shared anything
// with this caller — House-membership-based access instead
// (SetSpaceHousePinned, "does at least one of this Space's tagged lists
// belong to a House the caller is a member of"). Either path surfaces
// ErrNotFound as a 404 rather than a 403, matching this file's existing
// "don't distinguish nonexistent from unauthorized" convention; only when
// *neither* path recognizes the caller does this endpoint actually 404.
func (app *Application) handleSpaceSharePin(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var in struct {
		Pinned *bool `json:"pinned"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Pinned == nil {
		writeError(w, http.StatusBadRequest, "pinned is required")
		return
	}

	userID := userFromContext(r).ID

	share, err := app.DB.SetSpaceSharePinned(r.Context(), id, userID, *in.Pinned)
	if err == nil {
		writeJSON(w, http.StatusOK, share)
		return
	}
	if !errors.Is(err, db.ErrNotFound) {
		app.serverError(w, r, err)
		return
	}

	pinned, err := app.DB.SetSpaceHousePinned(r.Context(), id, userID, *in.Pinned)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "space not found or not accessible")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, models.SpacePinStatus{
		CustomCategoryID:    id,
		IsPinnedToDashboard: pinned,
		AccessSource:        "house_member",
	})
}

// handleListShareIndex lists everyone a List has been shared with. Scoped
// to actual House membership (not authorizeListAccess) — see
// handleListShareCreate for why: only a list's real House can manage who
// else it's shared with, so access granted through a share can never be
// used to extend further access.
func (app *Application) handleListShareIndex(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	list, err := app.DB.GetList(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeHouseAccess(w, r, list.HouseID) {
		return
	}

	shares, err := app.DB.ListListShares(r.Context(), id)
	if err != nil {
		app.serverError(w, r, err)
		return
	}

	invitations, err := app.DB.ListPendingInvitations(r.Context(), db.InvitationKindList, id)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	for _, inv := range invitations {
		shares = append(shares, &models.ListShare{
			ListID:     id,
			Permission: inv.Permission,
			CreatedAt:  inv.CreatedAt,
			Email:      inv.Email,
			Pending:    true,
		})
	}

	writeJSON(w, http.StatusOK, shares)
}

// handleListShareCreate grants (or updates the permission of) a List
// share, looked up by the recipient's email. Deliberately requires actual
// House membership of the list (authorizeHouseAccess), not merely write
// access to it (authorizeListAccess) — a list has no single "owner" the way
// a Space does, so letting anyone with write-via-share also grant further
// shares would let access cascade uncontrolled; only the list's real House
// members, who fundamentally own it, can extend it to someone new.
func (app *Application) handleListShareCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	list, err := app.DB.GetList(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeHouseAccess(w, r, list.HouseID) {
		return
	}

	var in struct {
		Email      string `json:"email"`
		Permission string `json:"permission"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Email = validate.Text(in.Email)
	if in.Email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	if !validate.MaxLen(in.Email, validate.MaxEmailLen) {
		writeError(w, http.StatusBadRequest, "email is too long")
		return
	}
	if !models.ValidSharePermissions[in.Permission] {
		writeError(w, http.StatusBadRequest, "permission must be \"read\" or \"write\"")
		return
	}

	// Same reasoning as handleSpaceShareCreate above (docs/AUDIT.md, L-06): the
	// invitation is keyed by email and grants nothing until the recipient
	// signs in, so the reply cannot be used to test whether an address is
	// registered here. The "already a member of this list's house" rejection
	// is kept because it only reflects the membership of a house the caller
	// belongs to and can enumerate anyway.
	if strings.EqualFold(in.Email, userFromContext(r).Email) {
		writeError(w, http.StatusBadRequest, "cannot share a list with yourself")
		return
	}
	if already, err := app.houseMemberExistsForEmail(r.Context(), list.HouseID, in.Email); err != nil {
		app.serverError(w, r, err)
		return
	} else if already {
		writeError(w, http.StatusBadRequest, "this person is already a member of the list's house")
		return
	}

	invitation, err := app.DB.CreatePendingInvitation(
		r.Context(), db.InvitationKindList, id, in.Email, in.Permission, userFromContext(r).ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.notifyInvitation(db.InvitationKindList, id, invitation.Email, userFromContext(r))
	writeJSON(w, http.StatusCreated, invitation)
}

// handleListShareInvitationRevoke withdraws an outstanding List invitation
// that has not been materialized into a list_shares row yet.
func (app *Application) handleListShareInvitationRevoke(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	list, err := app.DB.GetList(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeHouseAccess(w, r, list.HouseID) {
		return
	}
	app.revokeInvitation(w, r, db.InvitationKindList, id)
}

// handleListSharePin lets a share *recipient* pin or unpin a list directly
// shared with them, so it shows up alongside their own House's lists on the
// dashboard instead of only in the "Partagé avec moi" tab. Unlike every
// other handler in this file, the caller here is the recipient, not
// whoever manages the list's House — authorization is simply "does the
// caller hold a list_shares row for this list" (enforced by
// SetListSharePinned's own WHERE clause, surfaced as ErrNotFound rather
// than a 403 to match this file's existing "don't distinguish nonexistent
// from unauthorized" convention), not authorizeHouseAccess/
// authorizeListAccess. This is deliberate, not an oversight: a share
// recipient is, in the ordinary case, NOT a member of the list's House at
// all — that's exactly why the list had to be shared with them directly in
// the first place — so gating this endpoint behind House membership would
// make it unusable for the exact audience it exists for. See
// TestHandleListSharePinDoesNotRequireHouseMembership for a regression
// test covering precisely this. Not scoped to a direct List share only any
// more: a list reached solely via a shared Space can also be pinned this
// way — db.SetListSharePinned auto-creates the list_shares row that carries
// the flag in that case (see its own comment) — so the "not a member of
// this house" bug report this test guards against can no longer resurface
// via that path either, and a Space can additionally be pinned as a whole
// via handleSpaceSharePin below, which covers every list reachable through
// it in one action instead.
func (app *Application) handleListSharePin(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	var in struct {
		Pinned *bool `json:"pinned"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Pinned == nil {
		writeError(w, http.StatusBadRequest, "pinned is required")
		return
	}

	share, err := app.DB.SetListSharePinned(r.Context(), id, userFromContext(r).ID, *in.Pinned)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list share not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, share)
}

func (app *Application) handleListShareRevoke(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	list, err := app.DB.GetList(r.Context(), id)
	if errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "list not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	if !app.authorizeHouseAccess(w, r, list.HouseID) {
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("userId"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	if err := app.DB.RevokeListShare(r.Context(), id, userID); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "share not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
