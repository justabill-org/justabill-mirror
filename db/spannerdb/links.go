package spannerdb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/repository"
)

// Ontology link tables (docs/design/31-bill-ontology.md). Each Replace method
// deletes the bill's current links and writes the new ones in one commit, so a
// re-sync never leaves stale edges behind. Node rows (committees, subjects) are
// upserted in the same commit, so their enforced FKs always hold.

const (
	tableBillSponsorships = "bill_sponsorships"
	tableBillCommittees   = "bill_committees"
	tableBillSubjects     = "bill_subjects"
	tableBillRelations    = "bill_relations"
)

var errEmptyLinkID = errors.New("empty link id")

type sponsorshipMut struct {
	BillID        string           `spanner:"bill_id"`
	MemberID      string           `spanner:"member_id"`
	Role          string           `spanner:"role"`
	SponsoredDate spanner.NullDate `spanner:"sponsored_date"`
	IsOriginal    spanner.NullBool `spanner:"is_original"`
}

type committeeMut struct {
	CommitteeID   string             `spanner:"committee_id"`
	Name          string             `spanner:"name"`
	Chamber       spanner.NullString `spanner:"chamber"`
	CommitteeType spanner.NullString `spanner:"committee_type"`
}

type billCommitteeMut struct {
	BillID       string           `spanner:"bill_id"`
	CommitteeID  string           `spanner:"committee_id"`
	Activity     string           `spanner:"activity"`
	ActivityDate spanner.NullTime `spanner:"activity_date"`
}

type subjectMut struct {
	SubjectID string `spanner:"subject_id"`
	Name      string `spanner:"name"`
}

type billSubjectMut struct {
	BillID    string `spanner:"bill_id"`
	SubjectID string `spanner:"subject_id"`
}

type policyAreaMut struct {
	PolicyAreaID string `spanner:"policy_area_id"`
	Name         string `spanner:"name"`
}

type billRelationMut struct {
	BillID        string             `spanner:"bill_id"`
	RelatedBillID string             `spanner:"related_bill_id"`
	RelationType  string             `spanner:"relation_type"`
	IdentifiedBy  spanner.NullString `spanner:"identified_by"`
}

// slug turns a display name into a stable node key: lowercase ASCII letters and
// digits, with every other run of characters collapsed to one '-'. It must match
// the bills.policy_area_id generated column in db/schema.sql.
func slug(name string) string {
	var b strings.Builder
	sep := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if sep && b.Len() > 0 {
				b.WriteByte('-')
			}
			sep = false
			b.WriteRune(r)
			continue
		}
		sep = true
	}
	return b.String()
}

// committeeID normalises a Congress.gov committee systemCode (e.g. "HSWM00").
func committeeID(systemCode string) string {
	return strings.ToLower(strings.TrimSpace(systemCode))
}

// policyAreaMutations returns the policy_areas upsert for a bill's policy
// area, or nothing when the bill has none.
func policyAreaMutations(name *string) ([]*spanner.Mutation, error) {
	if name == nil || slug(*name) == "" {
		return nil, nil
	}
	m, err := spanner.InsertOrUpdateStruct("policy_areas", policyAreaMut{PolicyAreaID: slug(*name), Name: *name})
	if err != nil {
		return nil, fmt.Errorf("policy area mutation: %w", err)
	}
	return []*spanner.Mutation{m}, nil
}

// ReplaceBillSponsorships replaces a bill's sponsor or cosponsor links, whichever role says,
// in one transaction.
func (s *PipelineStoreImpl) ReplaceBillSponsorships(
	ctx context.Context, billID, role string, rows []repository.BillSponsorshipRow,
) error {
	if role != repository.SponsorRoleSponsor && role != repository.SponsorRoleCosponsor {
		return fmt.Errorf("invalid sponsorship role %q", role)
	}
	muts := make([]*spanner.Mutation, 0, len(rows))
	for _, r := range rows {
		if r.MemberID == "" {
			return fmt.Errorf("bill %s %s: %w", billID, role, errEmptyLinkID)
		}
		m, err := spanner.InsertOrUpdateStruct(tableBillSponsorships, sponsorshipMut{
			BillID:        billID,
			MemberID:      r.MemberID,
			Role:          role,
			SponsoredDate: ptrTimeToCivilDate(r.SponsoredDate),
			IsOriginal:    spanner.NullBool{Bool: r.IsOriginal, Valid: role == repository.SponsorRoleCosponsor},
		})
		if err != nil {
			return fmt.Errorf("sponsorship mutation: %w", err)
		}
		muts = append(muts, m)
	}
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		if _, delErr := txn.Update(ctx, spanner.Statement{
			SQL:    "DELETE FROM bill_sponsorships WHERE bill_id = @bill AND role = @role",
			Params: map[string]any{"bill": billID, "role": role},
		}); delErr != nil {
			return delErr
		}
		return txn.BufferWrite(muts)
	})
	if err != nil {
		return fmt.Errorf("replace %s sponsorships for %s: %w", role, billID, err)
	}
	return nil
}

// ReplaceBillCommittees replaces a bill's committee links and upserts the committees.
func (s *PipelineStoreImpl) ReplaceBillCommittees(
	ctx context.Context, billID string, rows []repository.BillCommitteeRow,
) error {
	muts := []*spanner.Mutation{spanner.Delete(tableBillCommittees, spanner.Key{billID}.AsPrefix())}
	// A committee appears once per activity; write its node once, taking each
	// field from the first row that has it.
	nodes := map[string]*committeeMut{}
	var order []string
	for _, r := range rows {
		id := committeeID(r.CommitteeID)
		if id == "" {
			return fmt.Errorf("bill %s committee %q: %w", billID, r.CommitteeName, errEmptyLinkID)
		}
		node, seen := nodes[id]
		if !seen {
			node = &committeeMut{CommitteeID: id}
			nodes[id] = node
			order = append(order, id)
		}
		mergeCommittee(node, r)
		link, err := spanner.InsertOrUpdateStruct(tableBillCommittees, billCommitteeMut{
			BillID:       billID,
			CommitteeID:  id,
			Activity:     r.Activity,
			ActivityDate: ptrTimeToNullTime(r.ActivityDate),
		})
		if err != nil {
			return fmt.Errorf("bill committee mutation: %w", err)
		}
		muts = append(muts, link)
	}
	for _, id := range order {
		m, err := spanner.InsertOrUpdateStruct("committees", *nodes[id])
		if err != nil {
			return fmt.Errorf("committee mutation: %w", err)
		}
		muts = append(muts, m)
	}
	if _, err := s.client.Apply(ctx, muts); err != nil {
		return fmt.Errorf("replace committees for %s: %w", billID, err)
	}
	return nil
}

func mergeCommittee(node *committeeMut, r repository.BillCommitteeRow) {
	if node.Name == "" {
		node.Name = r.CommitteeName
	}
	if !node.Chamber.Valid {
		node.Chamber = ptrToNullString(r.Chamber)
	}
	if !node.CommitteeType.Valid {
		node.CommitteeType = ptrToNullString(r.CommitteeType)
	}
}

// ReplaceBillSubjects replaces a bill's subject links and upserts the subjects.
func (s *PipelineStoreImpl) ReplaceBillSubjects(ctx context.Context, billID string, subjectNames []string) error {
	muts := []*spanner.Mutation{spanner.Delete(tableBillSubjects, spanner.Key{billID}.AsPrefix())}
	for _, name := range subjectNames {
		id := slug(name)
		if id == "" {
			continue
		}
		node, err := spanner.InsertOrUpdateStruct("subjects", subjectMut{SubjectID: id, Name: name})
		if err != nil {
			return fmt.Errorf("subject mutation: %w", err)
		}
		link, err := spanner.InsertOrUpdateStruct(tableBillSubjects, billSubjectMut{BillID: billID, SubjectID: id})
		if err != nil {
			return fmt.Errorf("bill subject mutation: %w", err)
		}
		muts = append(muts, node, link)
	}
	if _, err := s.client.Apply(ctx, muts); err != nil {
		return fmt.Errorf("replace subjects for %s: %w", billID, err)
	}
	return nil
}

// ReplaceBillRelations replaces a bill's links to related bills.
func (s *PipelineStoreImpl) ReplaceBillRelations(
	ctx context.Context, billID string, rows []repository.BillRelationRow,
) error {
	muts := []*spanner.Mutation{spanner.Delete(tableBillRelations, spanner.Key{billID}.AsPrefix())}
	for _, r := range rows {
		if r.RelatedBillID == "" {
			return fmt.Errorf("bill %s relation %q: %w", billID, r.RelationType, errEmptyLinkID)
		}
		m, err := spanner.InsertOrUpdateStruct(tableBillRelations, billRelationMut{
			BillID:        billID,
			RelatedBillID: r.RelatedBillID,
			RelationType:  r.RelationType,
			IdentifiedBy:  ptrToNullString(r.IdentifiedBy),
		})
		if err != nil {
			return fmt.Errorf("bill relation mutation: %w", err)
		}
		muts = append(muts, m)
	}
	if _, err := s.client.Apply(ctx, muts); err != nil {
		return fmt.Errorf("replace relations for %s: %w", billID, err)
	}
	return nil
}

type billLinkSourceRow struct {
	BillID         string           `spanner:"bill_id"`
	IntroducedDate spanner.NullDate `spanner:"introduced_date"`
	Sponsors       spanner.NullJSON `spanner:"sponsors"`
	Cosponsors     spanner.NullJSON `spanner:"cosponsors"`
	Committees     spanner.NullJSON `spanner:"committees"`
	Subjects       spanner.NullJSON `spanner:"subjects"`
	RelatedBills   spanner.NullJSON `spanner:"related_bills"`
}

// ListBillLinkSources pages through a congress's bills in bill_id order, starting after
// afterBillID ("" for the first page), with the JSON columns the link tables come from.
func (s *PipelineStoreImpl) ListBillLinkSources(
	ctx context.Context, congressNum int, afterBillID string, limit int,
) ([]repository.BillLinkSource, error) {
	iter := s.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT bill_id, introduced_date, sponsors, cosponsors, committees, subjects, related_bills
			FROM bills WHERE congress = @congress AND bill_id > @after
			ORDER BY bill_id LIMIT @lim`,
		Params: map[string]any{paramCongress: int64(congressNum), paramAfter: afterBillID, paramLimit: int64(limit)},
	})
	defer iter.Stop()

	var out []repository.BillLinkSource
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list bill link sources: %w", err)
		}
		var r billLinkSourceRow
		if err = row.ToStruct(&r); err != nil {
			return nil, fmt.Errorf("read bill link source: %w", err)
		}
		out = append(out, repository.BillLinkSource{
			BillID:         r.BillID,
			IntroducedDate: nullDatePtr(r.IntroducedDate),
			Sponsors:       nullJSONToRaw(r.Sponsors),
			Cosponsors:     nullJSONToRaw(r.Cosponsors),
			Committees:     nullJSONToRaw(r.Committees),
			Subjects:       nullJSONToRaw(r.Subjects),
			RelatedBills:   nullJSONToRaw(r.RelatedBills),
		})
	}
}
