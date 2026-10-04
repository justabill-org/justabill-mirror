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

// Current law and what bills change in it (docs/design/149-law-aware-assistant.md). The
// pipeline writes the US Code, each text version's references to it and the explanations;
// LawRepository reads them for the API.

const (
	tableUSCSections      = "usc_sections"
	tableUSCReleasePoints = "usc_release_points"
	tableBillLawRefs      = "bill_law_refs"
	tableBillLawChanges   = "bill_law_changes"

	// A commit holds at most 80,000 mutated cells and 100 MB. A section row has 10 cells, and a
	// few sections run to megabytes of text, so batches are bounded by rows and by bytes.
	uscBatchRows  = 500
	uscBatchBytes = 32 << 20
)

var (
	errInvalidLawRef = errors.New("invalid law reference")
	errEmptySection  = errors.New("empty section id")
)

type uscSectionMut struct {
	SectionID     string             `spanner:"section_id"`
	TitleNumber   int64              `spanner:"title_number"`
	SectionNumber string             `spanner:"section_number"`
	Heading       spanner.NullString `spanner:"heading"`
	Text          string             `spanner:"text"`
	Status        string             `spanner:"status"`
	PositiveLaw   bool               `spanner:"positive_law"`
	ReleasePoint  string             `spanner:"release_point"`
	ContentHash   string             `spanner:"content_hash"`
	UpdatedAt     time.Time          `spanner:"updated_at"`
}

type uscReleasePointMut struct {
	ReleasePoint  string            `spanner:"release_point"`
	PublishedDate spanner.NullDate  `spanner:"published_date"`
	SourceURL     string            `spanner:"source_url"`
	LoadedAt      spanner.NullTime  `spanner:"loaded_at"`
	SectionCount  spanner.NullInt64 `spanner:"section_count"`
}

type billLawRefMut struct {
	BillID         string             `spanner:"bill_id"`
	VersionID      string             `spanner:"version_id"`
	SectionID      string             `spanner:"section_id"`
	RefKind        string             `spanner:"ref_kind"`
	CiteText       spanner.NullString `spanner:"cite_text"`
	SubsectionPath spanner.NullString `spanner:"subsection_path"`
	Instruction    spanner.NullString `spanner:"instruction"`
	BillSectionRef spanner.NullString `spanner:"bill_section_ref"`
}

type billLawChangeMut struct {
	BillID            string    `spanner:"bill_id"`
	SectionID         string    `spanner:"section_id"`
	ChangeKind        string    `spanner:"change_kind"`
	Explanation       string    `spanner:"explanation"`
	SourceContentHash string    `spanner:"source_content_hash"`
	ReleasePoint      string    `spanner:"release_point"`
	ModelUsed         string    `spanner:"model_used"`
	PromptVersion     string    `spanner:"prompt_version"`
	GeneratedAt       time.Time `spanner:"generated_at"`
}

type uscSectionRow struct {
	SectionID     string             `spanner:"section_id"`
	TitleNumber   int64              `spanner:"title_number"`
	SectionNumber string             `spanner:"section_number"`
	Heading       spanner.NullString `spanner:"heading"`
	Text          string             `spanner:"text"`
	Status        string             `spanner:"status"`
	PositiveLaw   bool               `spanner:"positive_law"`
	ReleasePoint  string             `spanner:"release_point"`
	UpdatedAt     spanner.NullTime   `spanner:"updated_at"`
}

type uscReleasePointRow struct {
	ReleasePoint  string            `spanner:"release_point"`
	PublishedDate spanner.NullDate  `spanner:"published_date"`
	SourceURL     string            `spanner:"source_url"`
	LoadedAt      spanner.NullTime  `spanner:"loaded_at"`
	SectionCount  spanner.NullInt64 `spanner:"section_count"`
}

type billLawChangeRow struct {
	SectionID     string             `spanner:"section_id"`
	Heading       spanner.NullString `spanner:"heading"`
	ChangeKind    string             `spanner:"change_kind"`
	Explanation   string             `spanner:"explanation"`
	ReleasePoint  string             `spanner:"release_point"`
	ModelUsed     string             `spanner:"model_used"`
	PromptVersion string             `spanner:"prompt_version"`
	GeneratedAt   spanner.NullTime   `spanner:"generated_at"`
}

const currentReleasePointSQL = `SELECT release_point, published_date, source_url, loaded_at, section_count
FROM usc_release_points
WHERE loaded_at IS NOT NULL
ORDER BY loaded_at DESC
LIMIT 1`

const sectionSQL = `SELECT section_id, title_number, section_number, heading, text, status, positive_law,
	release_point, updated_at
FROM usc_sections
WHERE section_id = @section`

const billLawChangesSQL = `SELECT c.section_id, s.heading, c.change_kind, c.explanation, c.release_point,
	c.model_used, c.prompt_version, c.generated_at
FROM bill_law_changes c
LEFT JOIN usc_sections s ON s.section_id = c.section_id
WHERE c.bill_id = @bill
ORDER BY c.section_id`

// LawRepository implements repository.LawRepo with Spanner.
type LawRepository struct {
	client *spanner.Client
}

// Section returns a US Code section's current text, or nil when it isn't loaded.
func (r *LawRepository) Section(ctx context.Context, sectionID string) (*model.USCSection, error) {
	stmt := spanner.Statement{SQL: sectionSQL, Params: map[string]any{"section": sectionID}}
	rows, err := queryGraph(ctx, r.client, stmt, "usc section", func(row uscSectionRow) model.USCSection {
		return model.USCSection{
			SectionID:     row.SectionID,
			TitleNumber:   int(row.TitleNumber),
			SectionNumber: row.SectionNumber,
			Heading:       nullStringPtr(row.Heading),
			Text:          row.Text,
			Status:        row.Status,
			PositiveLaw:   row.PositiveLaw,
			ReleasePoint:  row.ReleasePoint,
			UpdatedAt:     nullTimeValue(row.UpdatedAt),
		}
	})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// CurrentReleasePoint returns the most recently loaded release point, or nil.
func (r *LawRepository) CurrentReleasePoint(ctx context.Context) (*model.USCReleasePoint, error) {
	return currentReleasePoint(ctx, r.client)
}

// BillLawChanges returns the explanations of the bill's changes to law, by section ID.
func (r *LawRepository) BillLawChanges(ctx context.Context, billID string) ([]model.BillLawChange, error) {
	stmt := spanner.Statement{SQL: billLawChangesSQL, Params: map[string]any{paramBill: billID}}
	return queryGraph(ctx, r.client, stmt, "bill law changes", func(row billLawChangeRow) model.BillLawChange {
		return model.BillLawChange{
			SectionID:     row.SectionID,
			Heading:       nullStringPtr(row.Heading),
			ChangeKind:    row.ChangeKind,
			Explanation:   row.Explanation,
			ReleasePoint:  row.ReleasePoint,
			ModelUsed:     row.ModelUsed,
			PromptVersion: row.PromptVersion,
			GeneratedAt:   nullTimeValue(row.GeneratedAt),
		}
	})
}

func currentReleasePoint(ctx context.Context, client *spanner.Client) (*model.USCReleasePoint, error) {
	stmt := spanner.Statement{SQL: currentReleasePointSQL}
	rows, err := queryGraph(ctx, client, stmt, "usc release point", func(row uscReleasePointRow) model.USCReleasePoint {
		return model.USCReleasePoint{
			ReleasePoint:  row.ReleasePoint,
			PublishedDate: nullDatePtr(row.PublishedDate),
			SourceURL:     row.SourceURL,
			LoadedAt:      nullTimePtr(row.LoadedAt),
			SectionCount:  nullInt64Ptr(row.SectionCount),
		}
	})
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// UpsertUSCSections writes sections in batches bounded by row count and text size.
func (s *PipelineStoreImpl) UpsertUSCSections(ctx context.Context, rows []repository.USCSectionRow) error {
	var batch []*spanner.Mutation
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if _, err := s.client.Apply(ctx, batch); err != nil {
			return fmt.Errorf("upsert usc sections: %w", err)
		}
		batch, size = nil, 0
		return nil
	}
	for _, r := range rows {
		if r.SectionID == "" {
			return fmt.Errorf("usc title %d section %q: %w", r.TitleNumber, r.SectionNumber, errEmptySection)
		}
		m, err := spanner.InsertOrUpdateStruct(tableUSCSections, uscSectionMut{
			SectionID:     r.SectionID,
			TitleNumber:   int64(r.TitleNumber),
			SectionNumber: r.SectionNumber,
			Heading:       ptrToNullString(r.Heading),
			Text:          r.Text,
			Status:        r.Status,
			PositiveLaw:   r.PositiveLaw,
			ReleasePoint:  r.ReleasePoint,
			ContentHash:   r.ContentHash,
			UpdatedAt:     spanner.CommitTimestamp,
		})
		if err != nil {
			return fmt.Errorf("usc section mutation: %w", err)
		}
		rowSize := len(r.Text)
		if len(batch) > 0 && (len(batch) >= uscBatchRows || size+rowSize > uscBatchBytes) {
			if err = flush(); err != nil {
				return err
			}
		}
		batch = append(batch, m)
		size += rowSize
	}
	return flush()
}

// USCSectionHashes reads a title's sections by the key range of its ID prefix ("/us/usc/t42/").
func (s *PipelineStoreImpl) USCSectionHashes(ctx context.Context, title int) (map[string]string, error) {
	prefix := fmt.Sprintf("/us/usc/t%d/", title)
	// '0' follows '/', so the range holds exactly the IDs that start with prefix.
	end := prefix[:len(prefix)-1] + "0"
	iter := s.client.Single().Read(ctx, tableUSCSections,
		spanner.KeyRange{Start: spanner.Key{prefix}, End: spanner.Key{end}, Kind: spanner.ClosedOpen},
		[]string{"section_id", "content_hash"})
	defer iter.Stop()

	hashes := map[string]string{}
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return hashes, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read usc title %d hashes: %w", title, err)
		}
		var id, hash string
		if err = row.Columns(&id, &hash); err != nil {
			return nil, fmt.Errorf("read usc title %d hashes: %w", title, err)
		}
		hashes[id] = hash
	}
}

// RecordUSCReleasePoint stores a loaded release point.
func (s *PipelineStoreImpl) RecordUSCReleasePoint(ctx context.Context, rp repository.USCReleasePointRow) error {
	count := rp.SectionCount
	m, err := spanner.InsertOrUpdateStruct(tableUSCReleasePoints, uscReleasePointMut{
		ReleasePoint:  rp.ReleasePoint,
		PublishedDate: ptrTimeToCivilDate(rp.PublishedDate),
		SourceURL:     rp.SourceURL,
		LoadedAt:      spanner.NullTime{Time: rp.LoadedAt, Valid: !rp.LoadedAt.IsZero()},
		SectionCount:  ptrToNullInt64(&count),
	})
	if err != nil {
		return fmt.Errorf("usc release point mutation: %w", err)
	}
	if _, err = s.client.Apply(ctx, []*spanner.Mutation{m}); err != nil {
		return fmt.Errorf("record usc release point %s: %w", rp.ReleasePoint, err)
	}
	return nil
}

// CurrentUSCReleasePoint returns the most recently loaded release point, or nil.
func (s *PipelineStoreImpl) CurrentUSCReleasePoint(ctx context.Context) (*model.USCReleasePoint, error) {
	return currentReleasePoint(ctx, s.client)
}

// ReplaceBillLawRefs deletes the text version's references and writes rows in one commit.
func (s *PipelineStoreImpl) ReplaceBillLawRefs(
	ctx context.Context, billID, versionID string, rows []repository.BillLawRefRow,
) error {
	if billID == "" || versionID == "" {
		return fmt.Errorf("law refs for bill %q version %q: %w", billID, versionID, errEmptyLinkID)
	}
	muts := []*spanner.Mutation{spanner.Delete(tableBillLawRefs, spanner.Key{billID, versionID}.AsPrefix())}
	for _, r := range rows {
		if err := checkLawRef(r.SectionID, r.RefKind, true); err != nil {
			return fmt.Errorf("bill %s version %s: %w", billID, versionID, err)
		}
		m, err := spanner.InsertOrUpdateStruct(tableBillLawRefs, billLawRefMut{
			BillID:         billID,
			VersionID:      versionID,
			SectionID:      r.SectionID,
			RefKind:        r.RefKind,
			CiteText:       ptrToNullString(r.CiteText),
			SubsectionPath: ptrToNullString(r.SubsectionPath),
			Instruction:    ptrToNullString(r.Instruction),
			BillSectionRef: ptrToNullString(r.BillSectionRef),
		})
		if err != nil {
			return fmt.Errorf("bill law ref mutation: %w", err)
		}
		muts = append(muts, m)
	}
	if _, err := s.client.Apply(ctx, muts); err != nil {
		return fmt.Errorf("replace law refs for %s version %s: %w", billID, versionID, err)
	}
	return nil
}

// ListLawRefSources returns the next page of the congress's fetched bill texts, with their
// version's formats, after (afterBillID, afterVersionID) in bill and version ID order.
func (s *PipelineStoreImpl) ListLawRefSources(
	ctx context.Context, congressNum int, afterBillID, afterVersionID string, limit int,
) ([]repository.LawRefSource, error) {
	iter := s.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT v.bill_id, v.version_id, v.version_code, v.formats, t.format, t.content,
				t.content_hash, t.content_gz
			FROM bills b
			JOIN bill_text_versions v ON v.bill_id = b.bill_id
			JOIN bill_texts t ON t.version_id = v.version_id
			WHERE b.congress = @congress
			  AND (v.bill_id > @after_bill OR (v.bill_id = @after_bill AND v.version_id > @after_version))
			ORDER BY v.bill_id, v.version_id
			LIMIT @lim`,
		Params: map[string]any{
			paramCongress: int64(congressNum), "after_bill": afterBillID, "after_version": afterVersionID,
			paramLimit: int64(limit),
		},
	})
	defer iter.Stop()

	var out []repository.LawRefSource
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list law ref sources: %w", err)
		}
		var (
			src     repository.LawRefSource
			formats spanner.NullJSON
			hash    string
			gz      []byte
		)
		if err = row.Columns(&src.BillID, &src.VersionID, &src.VersionCode, &formats, &src.Format,
			&src.Content, &hash, &gz); err != nil {
			return nil, fmt.Errorf("read law ref source: %w", err)
		}
		if src.Content, err = storedText(src.Content, hash, gz); err != nil {
			return nil, fmt.Errorf("law ref source %s: %w", src.VersionID, err)
		}
		src.Formats = nullJSONToRaw(formats)
		out = append(out, src)
	}
}

// ReplaceBillLawChanges deletes the bill's explanations and writes rows in one commit.
func (s *PipelineStoreImpl) ReplaceBillLawChanges(
	ctx context.Context, billID string, rows []repository.BillLawChangeRow,
) error {
	muts, err := lawChangeMutations(billID, rows)
	if err != nil {
		return err
	}
	if _, err = s.client.Apply(ctx, muts); err != nil {
		return fmt.Errorf("replace law changes for %s: %w", billID, err)
	}
	return nil
}

// lawChangeMutations deletes the bill's explanations and writes rows, applied in order.
func lawChangeMutations(billID string, rows []repository.BillLawChangeRow) ([]*spanner.Mutation, error) {
	if billID == "" {
		return nil, fmt.Errorf("law changes: %w", errEmptyLinkID)
	}
	muts := []*spanner.Mutation{spanner.Delete(tableBillLawChanges, spanner.Key{billID}.AsPrefix())}
	for _, r := range rows {
		if err := checkLawRef(r.SectionID, r.ChangeKind, false); err != nil {
			return nil, fmt.Errorf("bill %s: %w", billID, err)
		}
		m, err := spanner.InsertOrUpdateStruct(tableBillLawChanges, billLawChangeMut{
			BillID:            billID,
			SectionID:         r.SectionID,
			ChangeKind:        r.ChangeKind,
			Explanation:       r.Explanation,
			SourceContentHash: r.SourceContentHash,
			ReleasePoint:      r.ReleasePoint,
			ModelUsed:         r.ModelUsed,
			PromptVersion:     r.PromptVersion,
			GeneratedAt:       spanner.CommitTimestamp,
		})
		if err != nil {
			return nil, fmt.Errorf("bill law change mutation: %w", err)
		}
		muts = append(muts, m)
	}
	return muts, nil
}

// checkLawRef rejects an empty section ID and an unknown kind. Only references may cite; a
// change is always an amendment, a repeal or an addition.
func checkLawRef(sectionID, kind string, allowCites bool) error {
	if sectionID == "" {
		return errEmptySection
	}
	switch kind {
	case model.LawRefAmends, model.LawRefRepeals, model.LawRefAdds:
		return nil
	case model.LawRefCites:
		if allowCites {
			return nil
		}
	}
	return fmt.Errorf("section %s kind %q: %w", sectionID, kind, errInvalidLawRef)
}
