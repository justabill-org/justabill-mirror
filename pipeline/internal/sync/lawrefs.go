package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

// Law references (docs/design/149-law-aware-assistant.md, item 3): each fetched text version's
// references to the US Code, parsed from its XML and supplemented by its GovInfo MODS, stored
// in bill_law_refs. sync-texts and sync-govinfo write them for every text they store
// (storeTextLawRefs); BackfillLawRefs writes them for the texts stored before.

const (
	stepLawRefs = "law_refs"

	// DefaultLawRefsPageSize is how many text versions BackfillLawRefs reads per page. The
	// sync_state checkpoint is written after every page. Omnibus texts run to megabytes, so
	// pages stay small.
	DefaultLawRefsPageSize = 25

	// lawRefsCursorSep joins the bill and version IDs of the backfill checkpoint.
	lawRefsCursorSep = " "
)

// lawRefCounts totals what storing one or more versions' law references did.
type lawRefCounts struct {
	versions   int
	refs       int
	fromMODS   int
	badXML     int
	modsFailed int
}

// storeLawRefs parses a stored text version's references to law and replaces the version's
// rows. An XML text that won't parse still gets its MODS citations. A failed MODS fetch is
// counted and logged, and the version keeps the references its XML gave. Only a failed write
// is returned. The MODS is that of the GovInfo package src.Formats names.
func (s *Service) storeLawRefs(ctx context.Context, src repository.LawRefSource, c *lawRefCounts) error {
	return s.storePackageLawRefs(ctx, src, billsPackageID(src.Formats), c)
}

// storePackageLawRefs is storeLawRefs with the MODS of GovInfo package packageID, or none when
// it's "".
func (s *Service) storePackageLawRefs(
	ctx context.Context, src repository.LawRefSource, packageID string, c *lawRefCounts,
) error {
	var refs []billtext.LawRef
	if strings.Contains(src.Format, "XML") {
		parsed, err := billtext.ParseLawRefs([]byte(src.Content))
		if err != nil {
			c.badXML++
			s.logger.WarnContext(ctx, "parse law references failed", "bill_id", src.BillID,
				"text_version_id", src.VersionID, "error", err)
		}
		refs = parsed
	}
	refs = s.supplementFromMODS(ctx, src, packageID, refs, c)
	rows := make([]repository.BillLawRefRow, 0, len(refs))
	for _, r := range refs {
		rows = append(rows, repository.BillLawRefRow{
			SectionID:      r.SectionID,
			RefKind:        r.Kind,
			CiteText:       optional(r.CiteText),
			SubsectionPath: optional(r.SubsectionPath),
			Instruction:    optional(r.Instruction),
			BillSectionRef: optional(r.BillSection),
		})
	}
	if err := s.store.ReplaceBillLawRefs(ctx, src.BillID, src.VersionID, rows); err != nil {
		return fmt.Errorf("store law references for %s version %s: %w", src.BillID, src.VersionID, err)
	}
	c.versions++
	c.refs += len(rows)
	return nil
}

// supplementFromMODS adds the US Code citations the version's GovInfo MODS (package packageID)
// lists and its XML didn't, as cites: one GovInfo request per version, when there's a GovInfo
// client and the version has a GovInfo package.
func (s *Service) supplementFromMODS(
	ctx context.Context, src repository.LawRefSource, packageID string, refs []billtext.LawRef, c *lawRefCounts,
) []billtext.LawRef {
	if s.govinfo == nil || packageID == "" {
		return refs
	}
	data, err := s.govinfo.FetchMODS(ctx, packageID)
	if err == nil {
		var mods []billtext.LawRef
		if mods, err = billtext.ParseMODSCitations(data); err == nil {
			merged := billtext.SupplementLawRefs(refs, mods)
			c.fromMODS += len(merged) - len(refs)
			return merged
		}
	}
	c.modsFailed++
	s.logger.WarnContext(ctx, "law references without MODS", "bill_id", src.BillID,
		"package_id", packageID, "error", err)
	return refs
}

// billsPackageID returns the GovInfo package of a text version, BILLS-119hr1ih, from the file
// name of its first BILLS- format, or "" when it has none (a public law, or no formats).
func billsPackageID(formatsJSON json.RawMessage) string {
	var formats []textFormat
	if err := json.Unmarshal(formatsJSON, &formats); err != nil {
		return ""
	}
	for _, f := range formats {
		name := f.URL
		if u, err := url.Parse(f.URL); err == nil {
			name = path.Base(u.Path)
		}
		if billsFile.MatchString(name) {
			return strings.TrimSuffix(name, path.Ext(name))
		}
	}
	return ""
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// BackfillLawRefs writes the law references of every fetched text version of a congress's
// bills, the texts sync-texts stored before it parsed them. It is idempotent: each version's
// rows are replaced. It pages by bill and version ID and checkpoints in sync_state (step
// "law_refs"), so an interrupted run resumes after the last finished page; a finished run
// records success, which clears the checkpoint, so the next run starts over. A failed write
// stops the run.
func (s *Service) BackfillLawRefs(ctx context.Context, congressNum, pageSize int) error {
	if pageSize <= 0 {
		pageSize = DefaultLawRefsPageSize
	}
	return s.runStep(ctx, stepLawRefs, congressNum, false, func(ctx context.Context) (int, error) {
		return s.backfillLawRefs(ctx, congressNum, pageSize)
	})
}

func (s *Service) backfillLawRefs(ctx context.Context, congressNum, pageSize int) (int, error) {
	var c lawRefCounts
	n, err := s.eachStoredText(ctx, stepLawRefs, congressNum, pageSize,
		func(ctx context.Context, src repository.LawRefSource) error {
			return s.storeLawRefs(ctx, src, &c)
		})
	if err != nil {
		return n, err
	}
	s.logger.InfoContext(ctx, "law reference backfill complete", "congress", congressNum,
		"versions", n, "refs", c.refs, "from_mods", c.fromMODS, "unparsed_xml", c.badXML,
		"mods_failed", c.modsFailed)
	return n, nil
}
