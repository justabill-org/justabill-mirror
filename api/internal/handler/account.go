package handler

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/justabill-org/justabill/api/internal/auth"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

const (
	// recentSignIn is how recently the user must have signed in to delete
	// their account, so a stolen, refreshed session can't do it.
	recentSignIn = 5 * time.Minute
	// MaxImportBody caps a votes:import body; 1,000 votes fit well under it.
	// The router mounts the route with its own MaxBytes at this size, since
	// it's over the router-wide cap.
	MaxImportBody = 512 << 10
	// maxBillIDLen caps an imported bill ID (e.g. "119-hr-1234").
	maxBillIDLen = 64
)

// CreateMe creates the account for the signed-in user, or returns the
// existing one: 201 when created, 200 when it already existed. The web app
// calls it once after sign-in, so GET routes never write. It records the
// token's sign-in provider (email-link sign-in reports "password"). It asks
// Identity Platform whether the token was revoked first (401 invalid_token if
// so), so a deleted account's ID token, still valid for up to an hour, can't
// create a row no sign-in can reach or delete (#458).
func (h *Handler) CreateMe(w http.ResponseWriter, r *http.Request) {
	p, ok := h.verifyNotRevoked(w, r)
	if !ok {
		return
	}
	id := uuid.New().String()

	user, err := h.Users.CreateForAuthUID(r.Context(), id, p.UID, p.Provider)
	if err != nil {
		h.serverError(w, r, "failed to create account", err)
		return
	}
	status := http.StatusOK
	if user.ID == id {
		status = http.StatusCreated
		h.log.InfoContext(r.Context(), "account created", "user_id", id, "provider", p.Provider)
	}
	writeJSON(w, status, user)
}

// DeleteMe deletes the signed-in user's account: the users row (votes and
// favorites cascade) and then the Identity Platform user, whose refresh
// tokens are revoked. It needs a sign-in within the last five minutes, checked
// against Identity Platform. It answers 204, and repeating it after a partial
// failure is safe.
func (h *Handler) DeleteMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := h.verifyNotRevoked(w, r)
	if !ok {
		return
	}
	if !p.SignedInWithin(recentSignIn, time.Now()) {
		writeCodedError(w, http.StatusUnauthorized, "requires_recent_login",
			"sign in again to delete your account")
		return
	}

	if userID := mw.UserIDFromContext(ctx); userID != "" {
		if err := h.Users.Delete(ctx, userID); err != nil {
			h.serverError(w, r, "failed to delete account", err)
			return
		}
		h.log.InfoContext(ctx, "account deleted", "user_id", userID)
	}
	if h.forgetAuthUID != nil {
		h.forgetAuthUID(p.UID)
	}
	if err := h.Accounts.DeleteUser(ctx, p.UID); err != nil {
		h.log.ErrorContext(ctx, "delete identity platform user failed", "error", err)
		writeError(w, http.StatusBadGateway, "your data was deleted, but removing your sign-in failed; try again")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// verifyNotRevoked verifies the request's ID token again, asking Identity
// Platform whether it was revoked or its user deleted. On failure it writes
// 401 invalid_token, or 503 when accounts aren't set up or Identity Platform
// can't answer, and returns false.
func (h *Handler) verifyNotRevoked(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	ctx := r.Context()
	if h.Accounts == nil {
		writeError(w, http.StatusServiceUnavailable, "accounts are not configured")
		return auth.Principal{}, false
	}
	raw, _ := mw.BearerToken(r)
	p, err := h.Accounts.VerifyAndCheckRevoked(ctx, raw)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidToken) {
			writeCodedError(w, http.StatusUnauthorized, "invalid_token", "invalid, expired or revoked ID token")
			return auth.Principal{}, false
		}
		h.log.ErrorContext(ctx, "verify ID token for revocation failed", "error", err)
		writeCodedError(w, http.StatusServiceUnavailable, "auth_unavailable", "sign-in is temporarily unavailable")
		return auth.Principal{}, false
	}
	return p, true
}

// ExportMe returns everything stored about the signed-in user as a JSON
// download.
func (h *Handler) ExportMe(w http.ResponseWriter, r *http.Request) {
	userID := mw.UserIDFromContext(r.Context())

	export, err := h.Users.Export(r.Context(), userID)
	if err != nil {
		h.serverError(w, r, "failed to export account", err)
		return
	}
	if export == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="justabill-export.json"`)
	writeJSON(w, http.StatusOK, export)
}

// importVote is one vote in a votes:import request.
type importVote struct {
	BillID  string    `json:"bill_id"`
	Vote    string    `json:"vote"`
	VotedAt time.Time `json:"voted_at"`
}

// ImportMyVotes adds votes cast before signing in (the browser's local votes)
// to the signed-in user's account, with the request's App Check result.
// Votes already on the server win. Imported votes count toward the daily
// vote cap like cast ones: only as many as fit are stored, in request order,
// and the rest are listed in capped_bill_ids for the client to keep and send
// again later. It answers {"imported": n, "skipped": m, "capped": k,
// "capped_bill_ids": [...]}, where skipped counts the votes on bills already
// voted on or unknown.
func (h *Handler) ImportMyVotes(w http.ResponseWriter, r *http.Request) {
	userID := mw.UserIDFromContext(r.Context())

	var body struct {
		Votes []importVote `json:"votes"`
	}
	if !decodeJSON(w, http.MaxBytesReader(w, r.Body, MaxImportBody), &body) {
		return
	}
	if len(body.Votes) > model.MaxImportVotes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("at most %d votes per import", model.MaxImportVotes))
		return
	}
	votes, err := validImportVotes(body.Votes)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	checks := model.VoteChecks{AppCheckOK: mw.AppCheckFromContext(r.Context()), DailyCap: h.rules.DailyVoteCap}
	res, err := h.Users.ImportVotes(r.Context(), userID, votes, checks)
	if err != nil {
		h.serverError(w, r, "failed to import votes", err)
		return
	}
	capped := res.Capped
	if capped == nil {
		capped = []string{}
	} else {
		h.log.InfoContext(r.Context(), "daily vote cap reached on import", "user_id", userID)
	}
	writeJSON(w, http.StatusOK, importResponse{
		Imported: res.Imported, Skipped: len(votes) - res.Imported - len(capped),
		Capped: len(capped), CappedBillIDs: capped,
	})
}

// importResponse is the body of a votes:import answer.
type importResponse struct {
	Imported      int      `json:"imported"`
	Skipped       int      `json:"skipped"`
	Capped        int      `json:"capped"`
	CappedBillIDs []string `json:"capped_bill_ids"`
}

// validImportVotes checks each vote and normalizes its value.
func validImportVotes(in []importVote) ([]model.UserVote, error) {
	out := make([]model.UserVote, len(in))
	for i, v := range in {
		if v.BillID == "" || len(v.BillID) > maxBillIDLen {
			return nil, fmt.Errorf("votes[%d]: bill_id is required (at most %d characters)", i, maxBillIDLen)
		}
		vote, ok := normalizeVote(v.Vote)
		if !ok {
			return nil, fmt.Errorf("votes[%d]: %s", i, msgVoteValues)
		}
		out[i] = model.UserVote{BillID: v.BillID, Vote: vote, VotedAt: v.VotedAt}
	}
	return out, nil
}

// UpdateMe patches the current authenticated user's profile. A state that
// isn't a state, DC or territory, or a district that isn't one of its House
// seats, answers 400 invalid_seat and stores nothing. A change to the state or
// district within the district-change interval of the last one answers 429
// district_change_limited with Retry-After.
func (h *Handler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	userID := mw.UserIDFromContext(r.Context())

	var body struct {
		State    *string `json:"state"`
		District *int    `json:"district"`
	}
	if !decodeJSON(w, r.Body, &body) {
		return
	}

	user, err := h.Users.Update(r.Context(), userID, model.UserUpdate{
		State:                  body.State,
		District:               body.District,
		DistrictChangeInterval: h.rules.DistrictChangeInterval,
	})
	if errors.Is(err, repository.ErrInvalidSeat) {
		writeCodedError(w, http.StatusBadRequest, "invalid_seat",
			"state must be a US state, DC or territory postal code, and district one of its House seats "+
				"(0 where it has one seat)")
		return
	}
	if changeErr, limited := errors.AsType[*repository.DistrictChangeError](err); limited {
		writeDistrictChangeLimited(w, changeErr.NextAllowed, time.Now())
		return
	}
	if err != nil {
		h.serverError(w, r, "failed to update user", err)
		return
	}
	if user == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	writeJSON(w, http.StatusOK, user)
}

// writeDistrictChangeLimited answers a district change that came too soon.
func writeDistrictChangeLimited(w http.ResponseWriter, next, now time.Time) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(next.Sub(now).Seconds())))))
	after := next.UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusTooManyRequests, map[string]string{
		keyError:            "you changed your state or district recently; you can change it again after " + after,
		"code":              "district_change_limited",
		"next_change_after": after,
	})
}

// GetMe returns the current authenticated user's profile.
func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	userID := mw.UserIDFromContext(r.Context())

	user, err := h.Users.GetByID(r.Context(), userID)
	if err != nil {
		h.serverError(w, r, "failed to get user", err)
		return
	}
	if user == nil {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	writeJSON(w, http.StatusOK, user)
}
