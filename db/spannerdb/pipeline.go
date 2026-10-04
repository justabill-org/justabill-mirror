package spannerdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/repository"
)

// PipelineStoreImpl implements [repository.PipelineStore] with Spanner.
type PipelineStoreImpl struct {
	client *spanner.Client
}

// --- Members ---

// UpsertMember writes a member's name and photo URL, keyed by bioguide ID.
func (s *PipelineStoreImpl) UpsertMember(ctx context.Context, m repository.MemberRow) error {
	_, err := s.client.Apply(ctx, []*spanner.Mutation{
		spanner.InsertOrUpdate("members",
			[]string{"bioguide_id", "first_name", "last_name", "photo_url"},
			[]any{m.BioguideID, m.FirstName, m.LastName, ptrToNullString(m.PhotoURL)}),
	})
	return err
}

// UpsertMemberTerm writes start_date only when it's set, so a sync that doesn't know it (the
// current congress's member list) doesn't clear a date another source stored. It always writes
// end_date: NULL when EndDate is nil, because a term without an end is one the member still holds.
func (s *PipelineStoreImpl) UpsertMemberTerm(ctx context.Context, t repository.MemberTermRow) error {
	cols := []string{"member_id", colCongress, colChamber, "state", "district", "party", "end_date"}
	vals := []any{
		t.MemberID, int64(t.Congress), t.Chamber, t.State, ptrToNullInt64(t.District), t.Party,
		ptrTimeToCivilDate(t.EndDate),
	}
	if t.StartDate != nil {
		cols = append(cols, "start_date")
		vals = append(vals, timeToCivilDate(*t.StartDate))
	}
	_, err := s.client.Apply(ctx, []*spanner.Mutation{spanner.InsertOrUpdate("member_terms", cols, vals)})
	return err
}

// UpdateMemberLisID sets a senator's LIS ID, unless the member already has one.
func (s *PipelineStoreImpl) UpdateMemberLisID(ctx context.Context, bioguideID, lisID string) error {
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		_, updateErr := txn.Update(ctx, spanner.Statement{
			SQL:    "UPDATE members SET lis_id = @lisID WHERE bioguide_id = @id AND lis_id IS NULL",
			Params: map[string]any{"lisID": lisID, "id": bioguideID},
		})
		return updateErr
	})
	return err
}

// ListMemberIDs implements [repository.PipelineStore].
func (s *PipelineStoreImpl) ListMemberIDs(ctx context.Context) ([]string, error) {
	iter := s.client.Single().Read(ctx, "members", spanner.AllKeys(), []string{"bioguide_id"})
	defer iter.Stop()

	var ids []string
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return ids, nil
		}
		if err != nil {
			return nil, err
		}
		var id string
		if err = row.Columns(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
}

// --- Bills ---

// UpsertBill writes a bill's core columns and the policy area node it points at. It replaces
// laws too: a row with none clears them.
func (s *PipelineStoreImpl) UpsertBill(ctx context.Context, b repository.BillRow) error {
	// The bill's policy_area_id is generated from policy_area; upsert the node it points at.
	policyArea, err := policyAreaMutations(b.PolicyArea)
	if err != nil {
		return err
	}
	_, err = s.client.Apply(ctx, append(policyArea,
		spanner.InsertOrUpdate("bills",
			[]string{colBillID, colCongress, "bill_type", "number", "title",
				"introduced_date", "origin_chamber", "policy_area", "latest_action", "sponsors", "laws"},
			[]any{
				b.ID, int64(b.Congress), b.BillType, int64(b.Number), b.Title,
				ptrTimeToCivilDate(b.IntroducedDate), ptrToNullString(b.OriginChamber),
				ptrToNullString(b.PolicyArea), rawToNullJSON(b.LatestAction), rawToNullJSON(b.Sponsors),
				lawsToNullJSON(b.Laws),
			}),
	))
	return err
}

// billSyncedMut is the bills columns MarkBillSynced writes.
type billSyncedMut struct {
	BillID    string    `spanner:"bill_id"`
	UpdatedAt time.Time `spanner:"updated_at"`
}

// MarkBillSynced sets updated_at to now. It fails if the bill doesn't exist.
func (s *PipelineStoreImpl) MarkBillSynced(ctx context.Context, billID string) error {
	m, err := spanner.UpdateStruct("bills", billSyncedMut{BillID: billID, UpdatedAt: time.Now()})
	if err != nil {
		return err
	}
	_, err = s.client.Apply(ctx, []*spanner.Mutation{m})
	return err
}

// ListBillIDsSyncedSince implements [repository.PipelineStore].
func (s *PipelineStoreImpl) ListBillIDsSyncedSince(
	ctx context.Context, congressNum int, since time.Time,
) ([]string, error) {
	iter := s.client.Single().Query(ctx, spanner.Statement{
		SQL:    `SELECT bill_id FROM bills WHERE congress = @congress AND updated_at >= @since`,
		Params: map[string]any{paramCongress: int64(congressNum), paramSince: since},
	})
	defer iter.Stop()

	var ids []string
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return ids, nil
		}
		if err != nil {
			return nil, err
		}
		var id string
		if err = row.Columns(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
}

// ListMissingVotedBillIDs uses idx_cv_congress; a congress has a few thousand roll calls.
func (s *PipelineStoreImpl) ListMissingVotedBillIDs(ctx context.Context, congressNum int) ([]string, error) {
	return readStrings(ctx, s.client.Single(), spanner.Statement{
		SQL: `SELECT DISTINCT cv.bill_id FROM congressional_votes cv
			LEFT JOIN bills b ON b.bill_id = cv.bill_id
			WHERE cv.congress = @congress AND cv.bill_id IS NOT NULL
				AND (b.bill_id IS NULL OR b.updated_at IS NULL)
			ORDER BY cv.bill_id`,
		Params: map[string]any{paramCongress: int64(congressNum)},
	})
}

// ReplaceBillActions replaces all of a bill's actions in one transaction.
func (s *PipelineStoreImpl) ReplaceBillActions(
	ctx context.Context,
	billID string,
	actions []repository.BillActionRow,
) error {
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		// Delete existing actions
		if _, delErr := txn.Update(ctx, spanner.Statement{
			SQL:    "DELETE FROM bill_actions WHERE bill_id = @billID",
			Params: map[string]any{paramBillID: billID},
		}); delErr != nil {
			return delErr
		}

		// Insert new actions (id auto-generated via GENERATE_UUID default)
		for _, a := range actions {
			if _, insErr := txn.Update(ctx, spanner.Statement{
				SQL: `INSERT INTO bill_actions
				 (bill_id, action_date, action_text, action_type, action_code, source_system, sort_order)
				 VALUES (@billID, @actionDate, @actionText, @actionType, @actionCode, @sourceSystem, @sortOrder)`,
				Params: map[string]any{
					paramBillID: billID, "actionDate": timeToCivilDate(a.ActionDate),
					"actionText": a.ActionText, "actionType": ptrToNullString(a.ActionType),
					"actionCode": ptrToNullString(a.ActionCode), "sourceSystem": ptrToNullString(a.SourceSystem),
					"sortOrder": int64(a.SortOrder),
				},
			}); insErr != nil {
				return insErr
			}
		}
		return nil
	})
	return err
}

// UpdateBillJSON sets one of a bill's relationship JSON columns: cosponsors, committees,
// subjects or related_bills. Any other column is an error.
func (s *PipelineStoreImpl) UpdateBillJSON(
	ctx context.Context, billID, column string, value json.RawMessage,
) error {
	valid := map[string]bool{
		"cosponsors": true, "committees": true, "subjects": true, "related_bills": true,
	}
	if !valid[column] {
		return fmt.Errorf("invalid bill JSON column: %s", column)
	}
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		_, updateErr := txn.Update(ctx, spanner.Statement{
			SQL:    fmt.Sprintf("UPDATE bills SET %s = @val WHERE bill_id = @billID", column),
			Params: map[string]any{"val": rawToNullJSON(value), paramBillID: billID},
		})
		return updateErr
	})
	return err
}

// UpdateBillStatus sets a bill's current status and its date.
func (s *PipelineStoreImpl) UpdateBillStatus(
	ctx context.Context, billID, status string, date *time.Time,
) error {
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		_, updateErr := txn.Update(ctx, billStatusUpdate(billID, status, date))
		return updateErr
	})
	return err
}

// billStatusUpdate sets a bill's current status and its date.
func billStatusUpdate(billID, status string, date *time.Time) spanner.Statement {
	return spanner.Statement{
		SQL:    "UPDATE bills SET current_status = @status, status_date = @date WHERE bill_id = @billID",
		Params: map[string]any{"status": status, "date": ptrTimeToCivilDate(date), paramBillID: billID},
	}
}

// UpsertAmendment writes an amendment to a bill.
func (s *PipelineStoreImpl) UpsertAmendment(ctx context.Context, a repository.AmendmentRow) error {
	_, err := s.client.Apply(ctx, []*spanner.Mutation{
		spanner.InsertOrUpdate("amendments",
			[]string{colBillID, "amendment_id", colCongress, "amendment_type", "amendment_number",
				"description", "purpose", "latest_action", colChamber},
			[]any{
				a.BillID, a.ID, int64(a.Congress), a.AmendmentType, int64(a.AmendmentNumber),
				ptrToNullString(a.Description), ptrToNullString(a.Purpose),
				rawToNullJSON(a.LatestAction), a.Chamber,
			}),
	})
	return err
}

// --- Votes ---

// UpsertCongressionalVote writes a roll call or voice vote's question, result and totals.
func (s *PipelineStoreImpl) UpsertCongressionalVote(ctx context.Context, v repository.CongressionalVoteRow) error {
	_, err := s.client.Apply(ctx, []*spanner.Mutation{congressionalVoteMutation(v)})
	return err
}

// StoreRollCall writes the vote row and every member_votes row in one Apply. A House roll call
// is about 440 rows of 3 columns, far below Spanner's 80,000 mutations per commit.
func (s *PipelineStoreImpl) StoreRollCall(
	ctx context.Context, v repository.CongressionalVoteRow, members []repository.MemberVoteRow,
) error {
	ms := make([]*spanner.Mutation, 0, len(members)+1)
	ms = append(ms, congressionalVoteMutation(v))
	for _, m := range members {
		ms = append(ms, spanner.InsertOrUpdate("member_votes",
			[]string{"vote_id", "member_id", "vote"},
			[]any{v.ID, m.MemberID, m.Vote}))
	}
	_, err := s.client.Apply(ctx, ms)
	return err
}

func congressionalVoteMutation(v repository.CongressionalVoteRow) *spanner.Mutation {
	return spanner.InsertOrUpdate("congressional_votes",
		[]string{"vote_id", colBillID, colCongress, colChamber, "session", "roll_number",
			"vote_date", "question", "result", "yeas", "nays", "present", "not_voting"},
		[]any{
			v.ID, ptrToNullString(v.BillID), int64(v.Congress), v.Chamber,
			ptrToNullInt64(v.Session), ptrToNullInt64(v.RollNumber), v.VoteDate,
			ptrToNullString(v.Question), ptrToNullString(v.Result),
			ptrToNullInt64(v.Yeas), ptrToNullInt64(v.Nays),
			ptrToNullInt64(v.Present), ptrToNullInt64(v.NotVoting),
		})
}

// ExistingVoteIDs reports which of voteIDs are already stored.
func (s *PipelineStoreImpl) ExistingVoteIDs(
	ctx context.Context, voteIDs []string,
) (map[string]bool, error) {
	stmt := spanner.Statement{
		SQL:    "SELECT vote_id FROM congressional_votes WHERE vote_id IN UNNEST(@ids)",
		Params: map[string]any{"ids": voteIDs},
	}
	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	existing := make(map[string]bool, len(voteIDs))
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return existing, err
		}
		var id string
		if err = row.Columns(&id); err != nil {
			return existing, err
		}
		existing[id] = true
	}
	return existing, nil
}

// --- GAO Reports ---

// UpsertGAOReport writes a GAO report.
func (s *PipelineStoreImpl) UpsertGAOReport(ctx context.Context, r repository.GAOReportRow) error {
	_, err := s.client.Apply(ctx, []*spanner.Mutation{
		spanner.InsertOrUpdate("gao_reports",
			[]string{"report_id", "title", "report_number", "report_type",
				"published_date", "summary", "pdf_url", "html_url"},
			[]any{
				r.ReportID, r.Title,
				ptrToNullString(r.ReportNumber), ptrToNullString(r.ReportType),
				ptrTimeToCivilDate(r.PublishedDate), ptrToNullString(r.Summary),
				ptrToNullString(r.PDFURL), ptrToNullString(r.HTMLURL),
			}),
	})
	return err
}

// LinkBillGAOReport links a bill to a GAO report.
func (s *PipelineStoreImpl) LinkBillGAOReport(ctx context.Context, billID, reportID string) error {
	_, err := s.client.Apply(ctx, []*spanner.Mutation{
		spanner.InsertOrUpdate("bill_gao_reports",
			[]string{colBillID, "report_id"},
			[]any{billID, reportID}),
	})
	return err
}

// ListBillsForGAOCheck implements [repository.PipelineStore]. Bills whose status_date is NULL
// come last in each group. All of a congress's due bills when limit is zero or less.
func (s *PipelineStoreImpl) ListBillsForGAOCheck(
	ctx context.Context, congressNum, limit int,
) ([]string, error) {
	sql := `SELECT bill_id FROM bills
		WHERE congress = @congress AND (gao_checked_at IS NULL OR gao_checked_at < @recheck_before)
		ORDER BY gao_checked_at IS NULL DESC, status_date DESC, bill_id`
	p := map[string]any{
		paramCongress:    int64(congressNum),
		"recheck_before": time.Now().Add(-repository.GAORecheckAfter),
	}
	if limit > 0 {
		sql += " LIMIT @lim"
		p[paramLimit] = int64(limit)
	}
	iter := s.client.Single().Query(ctx, spanner.Statement{SQL: sql, Params: p})
	defer iter.Stop()

	var ids []string
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		var id string
		if err = row.Columns(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// gaoCheckedMut is the bills columns MarkGAOChecked writes.
type gaoCheckedMut struct {
	BillID       string    `spanner:"bill_id"`
	GAOCheckedAt time.Time `spanner:"gao_checked_at"`
}

// MarkGAOChecked implements [repository.PipelineStore]. It fails if the bill doesn't exist.
func (s *PipelineStoreImpl) MarkGAOChecked(ctx context.Context, billID string) error {
	m, err := spanner.UpdateStruct("bills", gaoCheckedMut{BillID: billID, GAOCheckedAt: time.Now()})
	if err != nil {
		return err
	}
	_, err = s.client.Apply(ctx, []*spanner.Mutation{m})
	return err
}

// --- Texts & Diffs ---

// QueryUnfetchedTextVersions implements [repository.PipelineStore]. A version is queued for a
// refetch when its text has no fetched_at (see UpsertBillTextVersions). It reads text_id and
// fetched_at from idx_bill_texts_version, which stores fetched_at (migration 31), so it never reads
// a text row, whose content runs to megabytes (docs/design/746-spanner-review.md).
func (s *PipelineStoreImpl) QueryUnfetchedTextVersions(
	ctx context.Context, limit int,
) ([]repository.TextVersionRef, error) {
	sql := `SELECT btv.version_id, btv.bill_id, btv.version_code, btv.formats, bt.text_id IS NOT NULL
		FROM bill_text_versions btv
		LEFT JOIN bill_texts@{FORCE_INDEX=idx_bill_texts_version} bt ON bt.version_id = btv.version_id
		WHERE bt.text_id IS NULL OR bt.fetched_at IS NULL
		ORDER BY btv.bill_id, btv.sort_order`
	p := map[string]any{}
	if limit > 0 {
		sql += " LIMIT @lim"
		p[paramLimit] = int64(limit)
	}

	iter := s.client.Single().Query(ctx, spanner.Statement{SQL: sql, Params: p})
	defer iter.Stop()

	var refs []repository.TextVersionRef
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}
		var (
			id, billID, versionCode string
			formats                 spanner.NullJSON
			refetch                 bool
		)
		if err = row.Columns(&id, &billID, &versionCode, &formats, &refetch); err != nil {
			return nil, err
		}
		refs = append(refs, repository.TextVersionRef{
			ID: id, BillID: billID, VersionCode: versionCode, Formats: nullJSONToRaw(formats), Refetch: refetch,
		})
	}
	return refs, nil
}

// InsertBillText stores a version's text, unless the version already has one.
func (s *PipelineStoreImpl) InsertBillText(ctx context.Context, t repository.BillTextRow) error {
	// Use DML to let GENERATE_UUID() default work; check existence first (DO NOTHING semantics).
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		// Check existence first (DO NOTHING semantics)
		checkIter := txn.Query(ctx, spanner.Statement{
			SQL:    "SELECT 1 FROM bill_texts WHERE version_id = @vid",
			Params: map[string]any{paramTextVersionID: t.TextVersionID},
		})
		_, checkErr := checkIter.Next()
		checkIter.Stop()
		if checkErr == nil {
			return nil // already exists
		}
		if !errors.Is(checkErr, iterator.Done) {
			return checkErr
		}

		_, insErr := txn.Update(ctx, spanner.Statement{
			SQL: `INSERT INTO bill_texts (version_id, format, content, content_gz, content_hash, sections, fetched_at)
			 VALUES (@vid, @format, @content, @contentGz, @hash, @sections, @fetchedAt)`,
			Params: map[string]any{
				paramTextVersionID: t.TextVersionID, "format": t.Format, "content": t.Content,
				paramContentGz: t.ContentGz, paramHash: t.ContentHash, paramSections: rawToNullJSON(t.Sections),
				"fetchedAt": t.FetchedAt,
			},
		})
		return insErr
	})
	return err
}

// FindPreviousVersion returns the bill's latest fetched version before versionID, with its
// sections, or nil when there is none.
func (s *PipelineStoreImpl) FindPreviousVersion(
	ctx context.Context, billID, versionID string,
) (*repository.PreviousVersionInfo, error) {
	stmt := spanner.Statement{
		SQL: `SELECT btv.version_id, bt.sections
		 FROM bill_text_versions btv
		 JOIN bill_texts bt ON bt.version_id = btv.version_id
		 WHERE btv.bill_id = @billID AND btv.sort_order < (
		   SELECT sort_order FROM bill_text_versions WHERE bill_id = @billID AND version_id = @versionID
		 )
		 ORDER BY btv.sort_order DESC
		 LIMIT 1`,
		Params: map[string]any{paramBillID: billID, "versionID": versionID},
	}

	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, nil //nolint:nilnil // no previous version
	}
	if err != nil {
		return nil, err
	}

	var (
		prevID   string
		sections spanner.NullJSON
	)
	if err = row.Columns(&prevID, &sections); err != nil {
		return nil, err
	}
	return &repository.PreviousVersionInfo{
		VersionID: prevID, Sections: nullJSONToRaw(sections),
	}, nil
}

// LoadSections returns the parsed sections stored with a version's text.
func (s *PipelineStoreImpl) LoadSections(ctx context.Context, versionID string) (json.RawMessage, error) {
	stmt := spanner.Statement{
		SQL:    "SELECT sections FROM bill_texts WHERE version_id = @vid",
		Params: map[string]any{paramTextVersionID: versionID},
	}
	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if err != nil {
		return nil, err
	}
	var sections spanner.NullJSON
	if err = row.Columns(&sections); err != nil {
		return nil, err
	}
	return nullJSONToRaw(sections), nil
}

// errNoBillText is returned when a version has no stored text to update.
var errNoBillText = errors.New("no stored bill text")

// UpdateBillTextSections implements [repository.PipelineStore].
func (s *PipelineStoreImpl) UpdateBillTextSections(
	ctx context.Context, versionID string, sections json.RawMessage,
) error {
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		n, updErr := txn.Update(ctx, spanner.Statement{
			SQL:    "UPDATE bill_texts SET sections = @sections WHERE version_id = @vid",
			Params: map[string]any{paramTextVersionID: versionID, paramSections: rawToNullJSON(sections)},
		})
		if updErr != nil {
			return updErr
		}
		if n == 0 {
			return fmt.Errorf("version %s: %w", versionID, errNoBillText)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("update bill text sections: %w", err)
	}
	return nil
}

// InsertBillTextDiff stores the diff between two versions, unless that pair already has one. An
// empty diff (d.IsEmpty) is stored too, so the pair isn't diffed again, and every reader skips it.
func (s *PipelineStoreImpl) InsertBillTextDiff(ctx context.Context, d repository.BillTextDiffRow) error {
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		// Check existence (DO NOTHING semantics)
		checkIter := txn.Query(ctx, spanner.Statement{
			SQL:    "SELECT 1 FROM bill_text_diffs WHERE from_version_id = @fromVID AND to_version_id = @toVID",
			Params: map[string]any{"fromVID": d.FromVersionID, "toVID": d.ToVersionID},
		})
		_, checkErr := checkIter.Next()
		checkIter.Stop()
		if checkErr == nil {
			return nil // already exists
		}
		if !errors.Is(checkErr, iterator.Done) {
			return checkErr
		}

		_, insErr := txn.Update(ctx, spanner.Statement{
			SQL: `INSERT INTO bill_text_diffs
			 (bill_id, from_version_id, to_version_id, diff_stats, diff_content, generated_at, is_empty)
			 VALUES (@billID, @fromVID, @toVID, @stats, @content, @generatedAt, @isEmpty)`,
			Params: map[string]any{
				paramBillID: d.BillID, "fromVID": d.FromVersionID, "toVID": d.ToVersionID,
				"stats": rawToNullJSON(d.DiffStats), "content": rawToNullJSON(d.DiffContent),
				"generatedAt": d.GeneratedAt, "isEmpty": d.IsEmpty,
			},
		})
		return insErr
	})
	return err
}

// --- GovInfo ---

// FindUnfetchedVersion returns the ID of the bill's version with versionCode when it has no
// stored text yet, and an error otherwise.
func (s *PipelineStoreImpl) FindUnfetchedVersion(ctx context.Context, billID, versionCode string) (string, error) {
	stmt := spanner.Statement{
		SQL: `SELECT btv.version_id FROM bill_text_versions btv
		 LEFT JOIN bill_texts bt ON bt.version_id = btv.version_id
		 WHERE btv.bill_id = @billID AND btv.version_code = @versionCode AND bt.text_id IS NULL`,
		Params: map[string]any{paramBillID: billID, "versionCode": versionCode},
	}

	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return "", errors.New("no unfetched version found")
	}
	if err != nil {
		return "", err
	}

	var id string
	if err = row.Columns(&id); err != nil {
		return "", err
	}
	return id, nil
}

// --- Senators ---

// LISLookup maps every stored LIS ID to its member's bioguide ID.
func (s *PipelineStoreImpl) LISLookup(ctx context.Context) (map[string]string, error) {
	stmt := spanner.NewStatement("SELECT bioguide_id, lis_id FROM members WHERE lis_id IS NOT NULL")
	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	m := make(map[string]string)
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return m, err
		}
		var bioguideID, lisID string
		if err = row.Columns(&bioguideID, &lisID); err != nil {
			continue
		}
		m[lisID] = bioguideID
	}
	return m, nil
}

// SetMemberLisID reads the member and the LIS ID's current holder, clears the holder's
// lis_id if it's someone else, then sets the member's, all in one transaction.
func (s *PipelineStoreImpl) SetMemberLisID(
	ctx context.Context, bioguideID, lisID string,
) (repository.LisIDChange, error) {
	var change repository.LisIDChange
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		change = repository.LisIDChange{}
		row, readErr := txn.ReadRow(ctx, "members", spanner.Key{bioguideID}, []string{"lis_id"})
		if errors.Is(readErr, spanner.ErrRowNotFound) {
			return fmt.Errorf("%w: %s", repository.ErrMemberNotFound, bioguideID)
		}
		if readErr != nil {
			return readErr
		}
		var previous spanner.NullString
		if readErr = row.Columns(&previous); readErr != nil {
			return readErr
		}
		change.Previous = previous.StringVal

		holders, readErr := lisIDHolders(ctx, txn, lisID)
		if readErr != nil {
			return readErr
		}
		for _, holder := range holders {
			if holder == bioguideID {
				continue
			}
			change.TakenFrom = holder
			if _, updateErr := txn.Update(ctx, spanner.Statement{
				SQL:    "UPDATE members SET lis_id = NULL WHERE bioguide_id = @id",
				Params: map[string]any{"id": holder},
			}); updateErr != nil {
				return updateErr
			}
		}
		if previous.StringVal == lisID {
			return nil
		}
		_, updateErr := txn.Update(ctx, spanner.Statement{
			SQL:    "UPDATE members SET lis_id = @lis WHERE bioguide_id = @id",
			Params: map[string]any{"lis": lisID, "id": bioguideID},
		})
		return updateErr
	})
	return change, err
}

// lisIDHolders returns the members whose lis_id is lisID: at most one, since the column is
// unique.
func lisIDHolders(ctx context.Context, txn *spanner.ReadWriteTransaction, lisID string) ([]string, error) {
	iter := txn.Query(ctx, spanner.Statement{
		SQL:    "SELECT bioguide_id FROM members WHERE lis_id = @lis",
		Params: map[string]any{"lis": lisID},
	})
	defer iter.Stop()

	var ids []string
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return ids, nil
		}
		if err != nil {
			return nil, err
		}
		var id string
		if err = row.Columns(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
}

// senatorNameRecord is a ListSenators row.
type senatorNameRecord struct {
	BioguideID string `spanner:"bioguide_id"`
	FirstName  string `spanner:"first_name"`
	LastName   string `spanner:"last_name"`
	State      string `spanner:"state"`
}

// ListSenators returns every senator with a term in congress, ordered by bioguide ID.
func (s *PipelineStoreImpl) ListSenators(ctx context.Context, congress int) ([]repository.SenatorName, error) {
	iter := s.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT m.bioguide_id, m.first_name, m.last_name, mt.state
		 FROM member_terms mt JOIN members m ON m.bioguide_id = mt.member_id
		 WHERE mt.congress = @congress AND mt.chamber = 'Senate'
		 ORDER BY m.bioguide_id`,
		Params: map[string]any{paramCongress: int64(congress)},
	})
	defer iter.Stop()

	var senators []repository.SenatorName
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return senators, nil
		}
		if err != nil {
			return nil, err
		}
		var r senatorNameRecord
		if err = row.ToStruct(&r); err != nil {
			return nil, err
		}
		senators = append(senators, repository.SenatorName(r))
	}
}

// IncompleteVoteIDs counts each roll call's member_votes rows, which are interleaved in
// congressional_votes, so the join reads each vote's rows next to it.
func (s *PipelineStoreImpl) IncompleteVoteIDs(ctx context.Context, congress int, chamber string) ([]string, error) {
	iter := s.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT cv.vote_id
		 FROM congressional_votes cv
		 LEFT JOIN (SELECT vote_id, COUNT(*) AS n FROM member_votes GROUP BY vote_id) mv
		   ON mv.vote_id = cv.vote_id
		 WHERE cv.congress = @congress AND cv.chamber = @voteChamber AND cv.roll_number IS NOT NULL
		   AND COALESCE(mv.n, 0) < cv.yeas + cv.nays + cv.present + cv.not_voting
		 ORDER BY cv.vote_id`,
		Params: map[string]any{paramCongress: int64(congress), "voteChamber": chamber},
	})
	defer iter.Stop()

	var ids []string
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return ids, nil
		}
		if err != nil {
			return nil, err
		}
		var id string
		if err = row.Columns(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
}

// --- Sync State ---

// GetSyncState returns a step's sync state for a congress, or nil when the step has never run.
func (s *PipelineStoreImpl) GetSyncState(
	ctx context.Context, step string, congress int,
) (*repository.SyncStateRow, error) {
	var r syncStateRecord
	found, err := readSyncState(s.client.Single().Read(
		ctx, "sync_state", spanner.Key{step, int64(congress)}, syncStateColumns()), &r)
	if err != nil || !found {
		return nil, err
	}
	return &repository.SyncStateRow{
		Step:                r.Step,
		Congress:            int(r.Congress),
		LastSyncedAt:        nullTimeValue(r.LastSyncedAt),
		LastOffset:          nullStringPtr(r.LastOffset),
		ItemsSynced:         int(r.ItemsSynced),
		ErrorCount:          int(r.ErrorCount),
		LastError:           nullStringPtr(r.LastError),
		LastErrorAt:         nullTimeValue(r.LastErrorAt),
		LastAttemptAt:       nullTimeValue(r.LastAttemptAt),
		ConsecutiveFailures: int(r.ConsecutiveFailures),
	}, nil
}

// syncStateRecord is a sync_state row as Spanner stores it.
type syncStateRecord struct {
	Step                string             `spanner:"step"`
	Congress            int64              `spanner:"congress"`
	LastSyncedAt        spanner.NullTime   `spanner:"last_synced_at"`
	LastOffset          spanner.NullString `spanner:"last_offset"`
	ItemsSynced         int64              `spanner:"items_synced"`
	ErrorCount          int64              `spanner:"error_count"`
	LastError           spanner.NullString `spanner:"last_error"`
	LastErrorAt         spanner.NullTime   `spanner:"last_error_at"`
	LastAttemptAt       spanner.NullTime   `spanner:"last_attempt_at"`
	ConsecutiveFailures int64              `spanner:"consecutive_failures"`
}

func syncStateColumns() []string {
	return []string{
		"step", colCongress, "last_synced_at", "last_offset", "items_synced",
		"error_count", "last_error", "last_error_at", "last_attempt_at", "consecutive_failures",
	}
}

// readSyncState decodes the one row iter returns into dst. It reports false, with no error,
// when there is no row.
func readSyncState(iter *spanner.RowIterator, dst any) (bool, error) {
	defer iter.Stop()
	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, row.ToStruct(dst)
}

// syncSuccessWrite, syncFailureWrite and syncCheckpointWrite are the sync_state columns each
// writer owns. InsertOrUpdate leaves every other column as it was.
type syncSuccessWrite struct {
	Step                string             `spanner:"step"`
	Congress            int64              `spanner:"congress"`
	LastSyncedAt        time.Time          `spanner:"last_synced_at"`
	LastAttemptAt       time.Time          `spanner:"last_attempt_at"`
	ItemsSynced         int64              `spanner:"items_synced"`
	LastOffset          spanner.NullString `spanner:"last_offset"`
	ConsecutiveFailures int64              `spanner:"consecutive_failures"`
}

type syncFailureWrite struct {
	Step                string    `spanner:"step"`
	Congress            int64     `spanner:"congress"`
	ErrorCount          int64     `spanner:"error_count"`
	ConsecutiveFailures int64     `spanner:"consecutive_failures"`
	LastError           string    `spanner:"last_error"`
	LastErrorAt         time.Time `spanner:"last_error_at"`
	LastAttemptAt       time.Time `spanner:"last_attempt_at"`
}

// syncWarningWrite is a success with a warning: the success columns plus the message and its
// time, without the failure counts. It's one mutation because a commit shouldn't write the same
// row twice.
type syncWarningWrite struct {
	Step                string             `spanner:"step"`
	Congress            int64              `spanner:"congress"`
	LastSyncedAt        time.Time          `spanner:"last_synced_at"`
	LastAttemptAt       time.Time          `spanner:"last_attempt_at"`
	ItemsSynced         int64              `spanner:"items_synced"`
	LastOffset          spanner.NullString `spanner:"last_offset"`
	ConsecutiveFailures int64              `spanner:"consecutive_failures"`
	LastError           string             `spanner:"last_error"`
	LastErrorAt         time.Time          `spanner:"last_error_at"`
}

type syncCheckpointWrite struct {
	Step        string             `spanner:"step"`
	Congress    int64              `spanner:"congress"`
	LastOffset  spanner.NullString `spanner:"last_offset"`
	ItemsSynced int64              `spanner:"items_synced"`
}

// RecordSyncSuccess writes only the watermark and progress columns, so the error history
// (error_count, last_error, last_error_at) survives. The watermark is run.Watermark when that
// is earlier than run.StartedAt. A warning replaces last_error and
// last_error_at but not the counts.
func (s *PipelineStoreImpl) RecordSyncSuccess(ctx context.Context, run repository.SyncRun) error {
	watermark := run.StartedAt
	if !run.Watermark.IsZero() && run.Watermark.Before(watermark) {
		watermark = run.Watermark
	}
	var row any = syncSuccessWrite{
		Step:          run.Step,
		Congress:      int64(run.Congress),
		LastSyncedAt:  watermark,
		LastAttemptAt: run.StartedAt,
		ItemsSynced:   int64(run.ItemsSynced),
	}
	if run.Warning != "" {
		row = syncWarningWrite{
			Step:          run.Step,
			Congress:      int64(run.Congress),
			LastSyncedAt:  watermark,
			LastAttemptAt: run.StartedAt,
			ItemsSynced:   int64(run.ItemsSynced),
			LastError:     run.Warning,
			LastErrorAt:   time.Now(),
		}
	}
	m, err := spanner.InsertOrUpdateStruct("sync_state", row)
	if err != nil {
		return err
	}
	_, err = s.client.Apply(ctx, []*spanner.Mutation{m})
	return err
}

// RecordSyncFailure reads the counters and writes them back incremented in one read-write
// transaction. A missing row counts from zero and gets a NULL watermark.
func (s *PipelineStoreImpl) RecordSyncFailure(ctx context.Context, run repository.SyncRun) error {
	key := spanner.Key{run.Step, int64(run.Congress)}
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		var prev syncFailureWrite
		// A missing row leaves the counts at zero.
		if _, readErr := readSyncState(txn.Read(
			ctx, "sync_state", key, []string{"error_count", "consecutive_failures"}), &prev); readErr != nil {
			return readErr
		}
		m, mErr := spanner.InsertOrUpdateStruct("sync_state", syncFailureWrite{
			Step:                run.Step,
			Congress:            int64(run.Congress),
			ErrorCount:          prev.ErrorCount + 1,
			ConsecutiveFailures: prev.ConsecutiveFailures + 1,
			LastError:           run.Error,
			LastErrorAt:         time.Now(),
			LastAttemptAt:       run.StartedAt,
		})
		if mErr != nil {
			return mErr
		}
		return txn.BufferWrite([]*spanner.Mutation{m})
	})
	return err
}

// SaveSyncCheckpoint stores a resumable position and a progress count without touching the
// watermark or the error history.
func (s *PipelineStoreImpl) SaveSyncCheckpoint(
	ctx context.Context, step string, congress int, offset *string, items int,
) error {
	m, err := spanner.InsertOrUpdateStruct("sync_state", syncCheckpointWrite{
		Step:        step,
		Congress:    int64(congress),
		LastOffset:  ptrToNullString(offset),
		ItemsSynced: int64(items),
	})
	if err != nil {
		return err
	}
	_, err = s.client.Apply(ctx, []*spanner.Mutation{m})
	return err
}

// Compile-time interface check.
var _ repository.PipelineStore = (*PipelineStoreImpl)(nil)
