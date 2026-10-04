package spannerdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

const (
	colUserID            = "user_id"
	colBillID            = "bill_id"
	paramUserID          = "userID"
	colSignInProvider    = "sign_in_provider"
	colDistrictChangedAt = "district_changed_at"
	userColumns          = "user_id, auth_uid, state, district, created_at, sign_in_provider, district_changed_at"
	// voteCapWindow is the window model.VoteChecks.DailyCap counts votes in.
	voteCapWindow = 24 * time.Hour
	// voteRecordedAt is when the server stored a vote. Rows stored before
	// user_votes.recorded_at existed (migration 21) fall back to voted_at.
	voteRecordedAt = "COALESCE(recorded_at, voted_at)"
)

// userVoteColumns are the user_votes columns CastVote and ImportVotes write.
func userVoteColumns() []string {
	return []string{colUserID, colBillID, "vote", "voted_at", "app_check_ok", "recorded_at"}
}

// UserRepository implements repository.UserRepo with Spanner.
type UserRepository struct {
	client *spanner.Client
}

// CreateForAuthUID inserts a user with the given ID for authUID, or returns
// the existing user for authUID. Callers tell the cases apart by comparing
// the returned ID with id. provider (the firebase.sign_in_provider claim) is
// recorded on a new user, and on an existing one that has none yet, such as
// accounts created before it was recorded. The first provider stays.
func (r *UserRepository) CreateForAuthUID(ctx context.Context, id, authUID, provider string) (*model.User, error) {
	if authUID == "" {
		return nil, errors.New("create user: empty auth uid")
	}
	provider = clip(provider, model.MaxSignInProviderLen)
	var user *model.User
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		existing, err := readUser(ctx, txn, spanner.Statement{
			SQL:    "SELECT " + userColumns + " FROM users@{FORCE_INDEX=idx_users_auth_uid} WHERE auth_uid = @uid",
			Params: map[string]any{"uid": authUID},
		})
		if err != nil || existing != nil {
			user = existing
			if err != nil || existing.SignInProvider != nil || provider == "" {
				return err
			}
			user.SignInProvider = &provider
			return txn.BufferWrite([]*spanner.Mutation{
				spanner.Update("users", []string{colUserID, colSignInProvider}, []any{existing.ID, provider}),
			})
		}

		now := time.Now()
		user = &model.User{ID: id, AuthUID: authUID, CreatedAt: now, SignInProvider: nilIfEmpty(provider)}
		return txn.BufferWrite([]*spanner.Mutation{
			spanner.Insert("users", []string{colUserID, "auth_uid", "created_at", colSignInProvider},
				[]any{id, authUID, now, nilIfEmpty(provider)}),
		})
	})
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}

// Update patches the given fields on a user and returns the updated user, or
// nil if there's no such user. A change that sets the state or district is
// stamped in district_changed_at, and one within
// updates.DistrictChangeInterval of the last fails with a
// *repository.DistrictChangeError. Clearing the state is always allowed, and
// leaves the stamp alone so the next change still waits.
func (r *UserRepository) Update(
	ctx context.Context,
	id string,
	updates model.UserUpdate,
) (*model.User, error) {
	if updates.State == nil && updates.District == nil {
		return r.GetByID(ctx, id)
	}

	found := false
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		cur, err := readUser(ctx, txn, spanner.Statement{
			SQL:    "SELECT " + userColumns + " FROM users WHERE user_id = @id",
			Params: map[string]any{"id": id},
		})
		found = cur != nil
		if err != nil || cur == nil {
			return err
		}
		cols, vals, err := userUpdateColumns(cur, updates, time.Now())
		if err != nil {
			return err
		}
		return txn.BufferWrite([]*spanner.Mutation{spanner.Update("users", cols, vals)})
	})
	if err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	if !found {
		return nil, nil //nolint:nilnil // not found returns nil
	}
	return r.GetByID(ctx, id)
}

// userUpdateColumns returns the columns and values that apply u to cur,
// enforcing the district-change interval.
func userUpdateColumns(cur *model.User, u model.UserUpdate, now time.Time) ([]string, []any, error) {
	cols := []string{colUserID}
	vals := []any{cur.ID}
	state, district := cur.State, cur.District
	if u.State != nil {
		state = nilIfEmpty(*u.State)
		cols = append(cols, "state")
		vals = append(vals, state)
	}
	if u.District != nil {
		district = u.District
		cols = append(cols, "district")
		vals = append(vals, ptrToNullInt64(district))
	}

	if err := checkSeat(u, state, district); err != nil {
		return nil, nil, err
	}
	if samePtr(state, cur.State) && samePtr(district, cur.District) {
		return cols, vals, nil
	}
	// Without a state the user is in no state or district cell.
	if state == nil {
		return cols, vals, nil
	}
	if cur.DistrictChangedAt != nil && u.DistrictChangeInterval > 0 {
		next := cur.DistrictChangedAt.Add(u.DistrictChangeInterval)
		if now.Before(next) {
			return nil, nil, &repository.DistrictChangeError{NextAllowed: next}
		}
	}
	return append(cols, colDistrictChangedAt), append(vals, now), nil
}

// checkSeat checks the state and district an update leaves the user with: a state it sets must be
// a state, DC or territory, and a district it sets, or keeps through a new state, must be one of
// that state's House seats. Clearing the state alone leaves any stored district unchecked, since
// a user with no state is in no state or district cell.
func checkSeat(u model.UserUpdate, state *string, district *int) error {
	if u.State != nil && state != nil {
		if _, ok := model.HouseSeats(*state); !ok {
			return fmt.Errorf("%w: unknown state %q", repository.ErrInvalidSeat, *state)
		}
	}
	if district == nil || (u.District == nil && state == nil) {
		return nil
	}
	if state == nil {
		return fmt.Errorf("%w: district %d without a state", repository.ErrInvalidSeat, *district)
	}
	if !model.ValidDistrict(*state, *district) {
		return fmt.Errorf("%w: %s has no district %d", repository.ErrInvalidSeat, *state, *district)
	}
	return nil
}

// GetByID returns a single user by ID, or nil if not found.
func (r *UserRepository) GetByID(ctx context.Context, id string) (*model.User, error) {
	return readUser(ctx, r.client.Single(), spanner.Statement{
		SQL:    "SELECT " + userColumns + " FROM users WHERE user_id = @id",
		Params: map[string]any{"id": id},
	})
}

// GetByAuthUID returns the user for an identity provider subject, or nil if
// not found.
func (r *UserRepository) GetByAuthUID(ctx context.Context, authUID string) (*model.User, error) {
	return readUser(ctx, r.client.Single(), spanner.Statement{
		SQL:    "SELECT " + userColumns + " FROM users@{FORCE_INDEX=idx_users_auth_uid} WHERE auth_uid = @uid",
		Params: map[string]any{"uid": authUID},
	})
}

// Delete removes a user. Their votes and favorites are interleaved with
// ON DELETE CASCADE, so they go in the same commit. Deleting a missing user
// is not an error.
func (r *UserRepository) Delete(ctx context.Context, id string) error {
	_, err := r.client.Apply(ctx, []*spanner.Mutation{spanner.Delete("users", spanner.Key{id})})
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

// CastVote records or updates a user's vote on a bill, with the request's
// App Check result, and stamps voted_at and recorded_at with the server's
// time. It returns an error wrapping repository.ErrNotFound if the bill
// doesn't exist. With checks.DailyCap set, it counts the other bills whose
// votes were stored in the last 24 hours (imports included) in the same
// transaction, and fails with repository.ErrDailyVoteCap when that reaches
// the cap.
func (r *UserRepository) CastVote(
	ctx context.Context, userID, billID, vote string, checks model.VoteChecks,
) error {
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		if err := requireBill(ctx, txn, billID); err != nil {
			return err
		}
		now := time.Now()
		if checks.DailyCap > 0 {
			recent, err := countRecentVotes(ctx, txn, userID, billID, now)
			if err != nil {
				return err
			}
			if recent >= checks.DailyCap {
				return repository.ErrDailyVoteCap
			}
		}
		return txn.BufferWrite([]*spanner.Mutation{
			spanner.InsertOrUpdate("user_votes", userVoteColumns(),
				[]any{userID, billID, vote, now, ptrToNullBool(checks.AppCheckOK), now}),
		})
	})
	if err != nil {
		return fmt.Errorf("cast vote: %w", err)
	}
	return nil
}

// countRecentVotes counts the user's votes stored in the voteCapWindow before
// now, leaving out the vote on exceptBill (empty to count them all).
func countRecentVotes(
	ctx context.Context, txn *spanner.ReadWriteTransaction, userID, exceptBill string, now time.Time,
) (int, error) {
	var recent int64
	err := eachRow(ctx, txn, spanner.Statement{
		SQL: `SELECT COUNT(*) FROM user_votes
		 WHERE user_id = @userID AND bill_id != @billID AND ` + voteRecordedAt + ` > @since`,
		Params: map[string]any{paramUserID: userID, paramBillID: exceptBill, paramSince: now.Add(-voteCapWindow)},
	}, func(row *spanner.Row) error { return row.Columns(&recent) })
	if err != nil {
		return 0, fmt.Errorf("count recent votes: %w", err)
	}
	return int(recent), nil
}

// DeleteVote removes a user's vote on a bill. Removing a vote that isn't
// there is not an error.
func (r *UserRepository) DeleteVote(ctx context.Context, userID, billID string) error {
	_, err := r.client.Apply(ctx, []*spanner.Mutation{
		spanner.Delete("user_votes", spanner.Key{userID, billID}),
	})
	if err != nil {
		return fmt.Errorf("delete vote: %w", err)
	}
	return nil
}

// writeForBill applies mut in a read-write transaction that first reads the
// bill, so favorites are never stored for a bill that doesn't exist.
func (r *UserRepository) writeForBill(ctx context.Context, billID string, mut *spanner.Mutation) error {
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		if err := requireBill(ctx, txn, billID); err != nil {
			return err
		}
		return txn.BufferWrite([]*spanner.Mutation{mut})
	})
	return err
}

// requireBill reads the bill in txn and returns an error wrapping
// repository.ErrNotFound if it doesn't exist. user_votes and user_favorites
// have no foreign key on bill_id.
func requireBill(ctx context.Context, txn *spanner.ReadWriteTransaction, billID string) error {
	if _, err := txn.ReadRow(ctx, "bills", spanner.Key{billID}, []string{colBillID}); err != nil {
		if errors.Is(err, spanner.ErrRowNotFound) {
			return fmt.Errorf("bill %q: %w", billID, repository.ErrNotFound)
		}
		return fmt.Errorf("read bill: %w", err)
	}
	return nil
}

// GetVotes returns a paginated list of a user's votes, newest first. bill_id breaks ties in
// voted_at (ImportVotes gives a whole batch one timestamp), so pages don't overlap. Each vote
// carries its bill's title, empty if the bills row is gone.
func (r *UserRepository) GetVotes(
	ctx context.Context,
	userID string,
	params model.ListParams,
) (*model.ListResult[model.UserVote], error) {
	total, err := countUserRows(ctx, r.client.Single(), "user_votes", userID)
	if err != nil {
		return nil, err
	}
	items, err := readUserVotes(ctx, r.client.Single(), spanner.Statement{
		SQL: `SELECT v.user_id, v.bill_id, v.vote, v.voted_at, COALESCE(b.title, '') AS bill_title
		 FROM user_votes v LEFT JOIN bills b ON b.bill_id = v.bill_id
		 WHERE v.user_id = @userID
		 ORDER BY v.voted_at DESC, v.bill_id
		 LIMIT @lim OFFSET @off`,
		Params: map[string]any{paramUserID: userID, paramLimit: int64(params.Limit), "off": int64(params.Offset)},
	})
	if err != nil {
		return nil, err
	}
	return &model.ListResult[model.UserVote]{
		Items: items, Total: total, Offset: params.Offset, Limit: params.Limit,
	}, nil
}

// ImportVotes inserts votes (for example, ones cast before signing in) for
// bills the user hasn't voted on. A vote already stored for a bill wins, and
// within the batch the first vote for a bill wins. Votes for bills that don't
// exist are skipped. A missing or future voted_at becomes now, and
// recorded_at is always now. Every inserted vote gets checks.AppCheckOK, the
// import request's App Check result. With checks.DailyCap set, the votes
// stored in the last 24 hours plus the inserted ones stay within the cap: the
// first ones in request order are inserted and the rest are returned in
// Capped. The user must exist.
func (r *UserRepository) ImportVotes(
	ctx context.Context, userID string, votes []model.UserVote, checks model.VoteChecks,
) (model.ImportResult, error) {
	unique, err := uniqueVotes(votes)
	if err != nil {
		return model.ImportResult{}, fmt.Errorf("import votes: %w", err)
	}
	if len(unique) == 0 {
		return model.ImportResult{}, nil
	}
	billIDs := make([]string, len(unique))
	for i, v := range unique {
		billIDs[i] = v.BillID
	}

	var result model.ImportResult
	_, err = r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		// Skip bills the user already voted on and bills that don't exist.
		skip, readErr := readStrings(ctx, txn, spanner.Statement{
			SQL: `SELECT id FROM UNNEST(@billIDs) AS id
			 WHERE EXISTS (SELECT 1 FROM user_votes v WHERE v.user_id = @userID AND v.bill_id = id)
			    OR NOT EXISTS (SELECT 1 FROM bills b WHERE b.bill_id = id)`,
			Params: map[string]any{paramUserID: userID, paramBillIDs: billIDs},
		})
		if readErr != nil {
			return readErr
		}
		now := time.Now()
		fresh := withoutBills(unique, skip)
		room := len(fresh)
		if checks.DailyCap > 0 {
			recent, countErr := countRecentVotes(ctx, txn, userID, "", now)
			if countErr != nil {
				return countErr
			}
			room = min(room, max(0, checks.DailyCap-recent))
		}
		result = model.ImportResult{Imported: room}
		for _, v := range fresh[room:] {
			result.Capped = append(result.Capped, v.BillID)
		}
		if room == 0 {
			return nil
		}
		return txn.BufferWrite(importMutations(userID, fresh[:room], checks.AppCheckOK, now))
	})
	if err != nil {
		return model.ImportResult{}, fmt.Errorf("import votes: %w", err)
	}
	return result, nil
}

// uniqueVotes validates an import batch and keeps the first vote per bill.
func uniqueVotes(votes []model.UserVote) ([]model.UserVote, error) {
	if len(votes) > model.MaxImportVotes {
		return nil, fmt.Errorf("%d votes exceeds the limit of %d", len(votes), model.MaxImportVotes)
	}
	seen := make(map[string]bool, len(votes))
	unique := make([]model.UserVote, 0, len(votes))
	for _, v := range votes {
		if v.BillID == "" || v.Vote == "" {
			return nil, errors.New("bill_id and vote are required")
		}
		if !seen[v.BillID] {
			seen[v.BillID] = true
			unique = append(unique, v)
		}
	}
	return unique, nil
}

// withoutBills returns the votes whose bill isn't in skipIDs, in order.
func withoutBills(votes []model.UserVote, skipIDs []string) []model.UserVote {
	skip := make(map[string]bool, len(skipIDs))
	for _, id := range skipIDs {
		skip[id] = true
	}
	kept := make([]model.UserVote, 0, len(votes))
	for _, v := range votes {
		if !skip[v.BillID] {
			kept = append(kept, v)
		}
	}
	return kept
}

// importMutations inserts each vote, recorded at now.
func importMutations(userID string, votes []model.UserVote, appCheckOK *bool, now time.Time) []*spanner.Mutation {
	muts := make([]*spanner.Mutation, 0, len(votes))
	for _, v := range votes {
		votedAt := v.VotedAt
		if votedAt.IsZero() || votedAt.After(now) {
			votedAt = now
		}
		muts = append(muts, spanner.Insert("user_votes", userVoteColumns(),
			[]any{userID, v.BillID, v.Vote, votedAt, ptrToNullBool(appCheckOK), now}))
	}
	return muts
}

// AddFavorite adds a bill to a user's favorites. It returns an error wrapping
// repository.ErrNotFound if the bill doesn't exist.
func (r *UserRepository) AddFavorite(ctx context.Context, userID, billID string) error {
	return r.writeForBill(ctx, billID, spanner.InsertOrUpdate("user_favorites",
		[]string{colUserID, colBillID, "created_at"},
		[]any{userID, billID, time.Now()}))
}

// RemoveFavorite removes a bill from a user's favorites.
func (r *UserRepository) RemoveFavorite(ctx context.Context, userID, billID string) error {
	_, err := r.client.Apply(ctx, []*spanner.Mutation{
		spanner.Delete("user_favorites", spanner.Key{userID, billID}),
	})
	return err
}

// GetFavorites returns a paginated list of a user's favorited bills, newest first, with bill_id
// breaking ties so pages don't overlap.
func (r *UserRepository) GetFavorites(
	ctx context.Context,
	userID string,
	params model.ListParams,
) (*model.ListResult[model.UserFavorite], error) {
	total, err := countUserRows(ctx, r.client.Single(), "user_favorites", userID)
	if err != nil {
		return nil, err
	}
	items, err := readUserFavorites(ctx, r.client.Single(), spanner.Statement{
		SQL: `SELECT user_id, bill_id, created_at
		 FROM user_favorites WHERE user_id = @userID
		 ORDER BY created_at DESC, bill_id
		 LIMIT @lim OFFSET @off`,
		Params: map[string]any{paramUserID: userID, paramLimit: int64(params.Limit), "off": int64(params.Offset)},
	})
	if err != nil {
		return nil, err
	}
	return &model.ListResult[model.UserFavorite]{
		Items: items, Total: total, Offset: params.Offset, Limit: params.Limit,
	}, nil
}

// Export returns everything stored about a user from one consistent
// snapshot, or nil if the user doesn't exist.
func (r *UserRepository) Export(ctx context.Context, userID string) (*model.UserExport, error) {
	txn := r.client.ReadOnlyTransaction()
	defer txn.Close()

	var excludedAt spanner.NullTime
	user, err := readUser(ctx, txn, spanner.Statement{
		SQL:    "SELECT " + userColumns + ", agg_excluded_at FROM users WHERE user_id = @userID",
		Params: map[string]any{paramUserID: userID},
	}, &excludedAt)
	if err != nil || user == nil {
		return nil, err
	}
	params := map[string]any{paramUserID: userID}
	votes, err := readUserVotes(ctx, txn, spanner.Statement{
		SQL: `SELECT user_id, bill_id, vote, voted_at, app_check_ok, recorded_at FROM user_votes
		 WHERE user_id = @userID ORDER BY voted_at DESC, bill_id`,
		Params: params,
	})
	if err != nil {
		return nil, fmt.Errorf("export votes: %w", err)
	}
	favorites, err := readUserFavorites(ctx, txn, spanner.Statement{
		SQL: `SELECT user_id, bill_id, created_at FROM user_favorites
		 WHERE user_id = @userID ORDER BY created_at DESC, bill_id`,
		Params: params,
	})
	if err != nil {
		return nil, fmt.Errorf("export favorites: %w", err)
	}
	return &model.UserExport{
		User: *user, AuthUID: user.AuthUID, AggExcludedAt: nullTimePtr(excludedAt), Votes: votes, Favorites: favorites,
	}, nil
}

// querier is satisfied by read-only and read-write transactions.
type querier interface {
	Query(ctx context.Context, stmt spanner.Statement) *spanner.RowIterator
}

// eachRow calls fn for every row stmt returns.
func eachRow(ctx context.Context, q querier, stmt spanner.Statement, fn func(*spanner.Row) error) error {
	iter := q.Query(ctx, stmt)
	defer iter.Stop()
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = fn(row); err != nil {
			return err
		}
	}
}

// countUserRows counts a user's rows in table, which must be a constant.
func countUserRows(ctx context.Context, q querier, table, userID string) (int, error) {
	var total int64
	stmt := spanner.Statement{
		SQL:    "SELECT COUNT(*) FROM " + table + " WHERE user_id = @userID",
		Params: map[string]any{paramUserID: userID},
	}
	err := eachRow(ctx, q, stmt,
		func(row *spanner.Row) error { return row.Columns(&total) })
	return int(total), err
}

// readUser returns the first user stmt selects (userColumns), or nil. Any
// columns stmt selects after userColumns are read into extra.
func readUser(ctx context.Context, q querier, stmt spanner.Statement, extra ...any) (*model.User, error) {
	var user *model.User
	err := eachRow(ctx, q, stmt, func(row *spanner.Row) error {
		u, err := scanUser(row, extra...)
		user = &u
		return err
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// readUserVotes reads user_id, bill_id, vote and voted_at, then any of
// app_check_ok, recorded_at and bill_title that stmt selects after them, by
// name.
func readUserVotes(ctx context.Context, q querier, stmt spanner.Statement) ([]model.UserVote, error) {
	items := []model.UserVote{}
	err := eachRow(ctx, q, stmt, func(row *spanner.Row) error {
		var v model.UserVote
		cols := []any{&v.UserID, &v.BillID, &v.Vote, &v.VotedAt}
		names := row.ColumnNames()
		for _, name := range names[min(len(cols), len(names)):] {
			switch name {
			case "app_check_ok":
				cols = append(cols, &v.AppCheckOK)
			case "recorded_at":
				cols = append(cols, &v.RecordedAt)
			case "bill_title":
				cols = append(cols, &v.Title)
			}
		}
		if err := row.Columns(cols...); err != nil {
			return err
		}
		items = append(items, v)
		return nil
	})
	return items, err
}

func readUserFavorites(ctx context.Context, q querier, stmt spanner.Statement) ([]model.UserFavorite, error) {
	items := []model.UserFavorite{}
	err := eachRow(ctx, q, stmt, func(row *spanner.Row) error {
		var f model.UserFavorite
		if err := row.Columns(&f.UserID, &f.BillID, &f.CreatedAt); err != nil {
			return err
		}
		items = append(items, f)
		return nil
	})
	return items, err
}

func readStrings(ctx context.Context, q querier, stmt spanner.Statement) ([]string, error) {
	var out []string
	err := eachRow(ctx, q, stmt, func(row *spanner.Row) error {
		var s string
		if err := row.Columns(&s); err != nil {
			return err
		}
		out = append(out, s)
		return nil
	})
	return out, err
}

func scanUser(row *spanner.Row, extra ...any) (model.User, error) {
	var (
		u         model.User
		state     spanner.NullString
		district  spanner.NullInt64
		provider  spanner.NullString
		changedAt spanner.NullTime
	)
	cols := append([]any{&u.ID, &u.AuthUID, &state, &district, &u.CreatedAt, &provider, &changedAt}, extra...)
	if err := row.Columns(cols...); err != nil {
		return model.User{}, err
	}
	u.State = nullStringPtr(state)
	u.District = nullInt64Ptr(district)
	u.SignInProvider = nullStringPtr(provider)
	u.DistrictChangedAt = nullTimePtr(changedAt)
	return u, nil
}

// samePtr reports whether a and b are both nil or point to equal values.
func samePtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// clip cuts s to at most n characters.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
