package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/scoring"
)

// JSON keys shared by the vote and favorite responses.
const (
	keyBillID = "bill_id"
	keyStatus = "status"
	statusOK  = "ok"
)

const (
	msgVoteValues   = "vote must be one of: yea, nay, skip"
	msgBillNotFound = "bill not found"
)

// normalizeVote lower-cases a user vote and reports whether it's valid.
func normalizeVote(v string) (string, bool) {
	vote := strings.ToLower(v)
	return vote, vote == "yea" || vote == "nay" || vote == "skip"
}

// CastVote records a user's vote on a bill, with the request's App Check
// result. Past the daily vote cap it answers 429 daily_vote_cap.
func (h *Handler) CastVote(w http.ResponseWriter, r *http.Request) {
	userID := mw.UserIDFromContext(r.Context())
	billID := chi.URLParam(r, "id")

	var body struct {
		Vote string `json:"vote"`
	}
	if !decodeJSON(w, r.Body, &body) {
		return
	}
	if body.Vote == "" {
		writeError(w, http.StatusBadRequest, "vote is required")
		return
	}

	vote, ok := normalizeVote(body.Vote)
	if !ok {
		writeError(w, http.StatusBadRequest, msgVoteValues)
		return
	}

	checks := model.VoteChecks{AppCheckOK: mw.AppCheckFromContext(r.Context()), DailyCap: h.rules.DailyVoteCap}
	if err := h.Users.CastVote(r.Context(), userID, billID, vote, checks); err != nil {
		if errors.Is(err, repository.ErrDailyVoteCap) {
			h.log.InfoContext(r.Context(), "daily vote cap reached", "user_id", userID)
			writeCodedError(w, http.StatusTooManyRequests, "daily_vote_cap",
				fmt.Sprintf("you can vote on at most %d bills a day; try again later", h.rules.DailyVoteCap))
			return
		}
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, msgBillNotFound)
			return
		}
		h.serverError(w, r, "failed to cast vote", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		keyBillID: billID,
		"vote":    vote,
		keyStatus: statusOK,
	})
}

// DeleteVote removes the user's vote on a bill (the privacy policy's "remove
// a vote"). It answers 204, also when there was no vote to remove, so
// repeating it is safe.
func (h *Handler) DeleteVote(w http.ResponseWriter, r *http.Request) {
	userID := mw.UserIDFromContext(r.Context())
	if err := h.Users.DeleteVote(r.Context(), userID, chi.URLParam(r, "id")); err != nil {
		h.serverError(w, r, "failed to delete vote", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetMyVotes returns the authenticated user's vote history.
func (h *Handler) GetMyVotes(w http.ResponseWriter, r *http.Request) {
	userID := mw.UserIDFromContext(r.Context())
	params, err := parseListParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := h.Users.GetVotes(r.Context(), userID, params)
	if err != nil {
		h.serverError(w, r, "failed to get votes", err)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// GetMyFavorites returns the authenticated user's favorited bills.
func (h *Handler) GetMyFavorites(w http.ResponseWriter, r *http.Request) {
	userID := mw.UserIDFromContext(r.Context())
	params, err := parseListParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := h.Users.GetFavorites(r.Context(), userID, params)
	if err != nil {
		h.serverError(w, r, "failed to get favorites", err)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// AddFavorite adds a bill to the user's favorites.
func (h *Handler) AddFavorite(w http.ResponseWriter, r *http.Request) {
	userID := mw.UserIDFromContext(r.Context())
	billID := chi.URLParam(r, "billID")

	if err := h.Users.AddFavorite(r.Context(), userID, billID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, msgBillNotFound)
			return
		}
		h.serverError(w, r, "failed to add favorite", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		keyBillID: billID,
		keyStatus: statusOK,
	})
}

// RemoveFavorite removes a bill from the user's favorites.
func (h *Handler) RemoveFavorite(w http.ResponseWriter, r *http.Request) {
	userID := mw.UserIDFromContext(r.Context())
	billID := chi.URLParam(r, "billID")
	if !validBillID(billID) {
		writeError(w, http.StatusBadRequest, msgInvalidBillID)
		return
	}
	if !h.billExists(w, r, billID) {
		return
	}

	if err := h.Users.RemoveFavorite(r.Context(), userID, billID); err != nil {
		h.serverError(w, r, "failed to remove favorite", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		keyBillID: billID,
		keyStatus: statusOK,
	})
}

// GetScorecard returns the user's alignment scores with the members holding their seats in the
// current congress, on those members' votes in the ?congress= filter's congresses (repeatable;
// every loaded congress when omitted).
func (h *Handler) GetScorecard(w http.ResponseWriter, r *http.Request) {
	userID := mw.UserIDFromContext(r.Context())
	congresses, ok := h.congressFilter(w, r)
	if !ok {
		return
	}

	scores, err := h.Scorecard.GetScorecard(r.Context(), userID, congresses)
	if err != nil {
		h.serverError(w, r, "failed to get scorecard", err)
		return
	}
	// Counts and the rule only: never the user's votes.
	h.log.DebugContext(r.Context(), "scorecard", "rule", scoring.RuleName, "members", len(scores))

	writeJSON(w, http.StatusOK, map[string]any{
		"scores": scores,
	})
}

// CompareMember returns a detailed vote comparison with a specific member, in the ?congress=
// filter's congresses (repeatable; every loaded congress when omitted).
func (h *Handler) CompareMember(w http.ResponseWriter, r *http.Request) {
	userID := mw.UserIDFromContext(r.Context())
	memberID := chi.URLParam(r, "memberID")
	congresses, ok := h.congressFilter(w, r)
	if !ok {
		return
	}

	comparisons, err := h.Scorecard.CompareWithMember(r.Context(), userID, memberID, congresses)
	if err != nil {
		h.serverError(w, r, "failed to compare with member", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"member_id":   memberID,
		"comparisons": comparisons,
	})
}
