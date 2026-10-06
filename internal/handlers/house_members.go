package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"trakka/internal/db"
	"trakka/internal/models"
	"trakka/internal/validate"
)

// handleHouseMembersIndex returns the house's roster: actual members first,
// then any outstanding invitations as entries with Pending set and UserID 0.
// Showing the pending half matters for more than tidiness — since an invite
// no longer takes effect until the invited person signs in (see
// handleHouseMembersInvite), a roster listing only real members would make a
// successful invitation look like it did nothing.
func (app *Application) handleHouseMembersIndex(w http.ResponseWriter, r *http.Request) {
	houseID, ok := pathID(w, r)
	if !ok {
		return
	}
	if !app.authorizeHouseAccess(w, r, houseID) {
		return
	}
	members, err := app.DB.ListHouseMembers(r.Context(), houseID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}

	invitations, err := app.DB.ListPendingInvitations(r.Context(), db.InvitationKindHouse, houseID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	for _, inv := range invitations {
		members = append(members, &models.HouseMember{
			HouseID:   houseID,
			Role:      "member",
			CreatedAt: inv.CreatedAt,
			Email:     inv.Email,
			Pending:   true,
		})
	}

	writeJSON(w, http.StatusOK, members)
}

func (app *Application) handleHouseMembersInvite(w http.ResponseWriter, r *http.Request) {
	houseID, ok := pathID(w, r)
	if !ok {
		return
	}
	if !app.authorizeHouseOwner(w, r, houseID) {
		return
	}

	var in struct {
		Email string `json:"email"`
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

	// An invitation is recorded against the email address, never against a
	// resolved user id, and it grants nothing until the invited person signs
	// in (db.MaterializePendingInvitations). That is what makes this endpoint
	// answer identically whether or not the address has an account here: the
	// previous version replied 404 "no account exists for this email yet",
	// which let any authenticated user probe the instance for the existence
	// of arbitrary email addresses, one request at a time (docs/AUDIT.md, L-06).
	//
	// The one case still answered differently is "already a member of THIS
	// house" — which discloses nothing, since the caller can simply read the
	// roster of their own house to learn the same thing.
	if already, err := app.houseMemberExistsForEmail(r.Context(), houseID, in.Email); err != nil {
		app.serverError(w, r, err)
		return
	} else if already {
		writeError(w, http.StatusConflict, "already a member")
		return
	}

	invitation, err := app.DB.CreatePendingInvitation(
		r.Context(), db.InvitationKindHouse, houseID, in.Email, "", userFromContext(r).ID)
	if err != nil {
		app.serverError(w, r, err)
		return
	}
	app.notifyInvitation(db.InvitationKindHouse, houseID, invitation.Email, userFromContext(r))
	writeJSON(w, http.StatusCreated, invitation)
}

// houseMemberExistsForEmail reports whether the address already belongs to a
// member of this house. Resolving the address here is safe even though the
// point of the change above is not to disclose whether it resolves at all:
// the answer only ever depends on the membership of a house the caller can
// already enumerate, so it tells them nothing they could not read directly.
func (app *Application) houseMemberExistsForEmail(ctx context.Context, houseID int64, email string) (bool, error) {
	user, err := app.DB.GetUserByEmail(ctx, email)
	if errors.Is(err, db.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return app.DB.UserCanAccessHouse(ctx, user.ID, houseID)
}

// handleHouseInvitationRevoke withdraws an outstanding invitation. Needed
// because an invitation now survives being sent to an address that has no
// account (a typo, most often), where previously it failed loudly and left
// nothing behind.
func (app *Application) handleHouseInvitationRevoke(w http.ResponseWriter, r *http.Request) {
	houseID, ok := pathID(w, r)
	if !ok {
		return
	}
	if !app.authorizeHouseOwner(w, r, houseID) {
		return
	}

	email := validate.Text(r.URL.Query().Get("email"))
	if email == "" {
		writeError(w, http.StatusBadRequest, "email query parameter is required")
		return
	}

	if err := app.DB.DeletePendingInvitation(r.Context(), db.InvitationKindHouse, houseID, email); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "invitation not found")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (app *Application) handleHouseMembersRemove(w http.ResponseWriter, r *http.Request) {
	houseID, ok := pathID(w, r)
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(r.PathValue("userId"), 10, 64)
	if err != nil || userID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	// A user may always remove themselves ("leave house") regardless of
	// role; removing anyone else requires being the owner.
	caller := userFromContext(r)
	if caller.ID != userID {
		if !app.authorizeHouseOwner(w, r, houseID) {
			return
		}
	} else if !app.authorizeHouseAccess(w, r, houseID) {
		return
	}

	if err := app.DB.RemoveHouseMember(r.Context(), houseID, userID); errors.Is(err, db.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not a member")
		return
	} else if err != nil {
		app.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
