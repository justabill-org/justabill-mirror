package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/cra"
	"github.com/justabill-org/justabill/pipeline/internal/fedreg"
)

// The title fallback's acceptance rule (docs/design/590-cra-disapproved-rules.md, "Matching"):
// with several exact matches, the newest is taken only if it's within craNewestWithin of the
// resolution's introduction and the next newest is at least craNextOlderBy older.
const (
	craNewestWithin = 730 * 24 * time.Hour
	craNextOlderBy  = 365 * 24 * time.Hour
)

const (
	// frVolumeYearOffset turns a Federal Register volume into its year: volume 89 is 2024.
	frVolumeYearOffset = 1935
	// craSearchPerPage and craSearchPages bound a title search: the newest 300 documents
	// containing the phrase.
	craSearchPerPage = 100
	craSearchPages   = 3
	// craMaxConsecutiveErrors stops a run after this many lookups in a row failed: the Federal
	// Register is down, and the rest stay due for the next run.
	craMaxConsecutiveErrors = 5
)

// The hosts whose links are stored: the Federal Register's own pages and GovInfo's official PDF.
const (
	frHTMLHost = "www.federalregister.gov"
	frPDFHost  = "www.govinfo.gov"
)

// frDocumentNumberPattern is a Federal Register document number: "2024-29699", "E9-12345",
// "C2-2018-14378".
var frDocumentNumberPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,31}$`)

// SetFederalRegister sets the Federal Register client that SyncCRARules looks rules up with.
func (s *Service) SetFederalRegister(c *fedreg.Client) { s.fedreg = c }

// SyncCRARules matches up to limit (all when limit ≤ 0) of a congress's CRA resolutions that are
// due ([repository.PipelineStore.ListCRARuleChecks]) to the Federal Register documents they
// disapprove (docs/design/590-cra-disapproved-rules.md). It reads the resolutions' titles and
// texts from Spanner and makes no Congress.gov or GovInfo request. A bill whose lookup fails gets
// no row and stays due; the run fails only when every lookup failed. A changed row marks the bill,
// so its page is revalidated and the API's cached copy deleted.
func (s *Service) SyncCRARules(ctx context.Context, congressNum, limit int) error {
	if s.fedreg == nil {
		s.logger.InfoContext(ctx, "federal register client not configured, skipping CRA rule sync")
		return nil
	}
	return s.runStepOutcome(ctx, stepCRARules, congressNum, false, func(ctx context.Context) (stepOutcome, error) {
		return s.syncCRARules(ctx, congressNum, limit)
	})
}

func (s *Service) syncCRARules(ctx context.Context, congressNum, limit int) (stepOutcome, error) {
	checks, err := s.store.ListCRARuleChecks(ctx, congressNum, cra.MatcherVersion, limit)
	if err != nil {
		return stepOutcome{}, fmt.Errorf("list CRA rule checks: %w", err)
	}
	m := newCRAMatcher(s.fedreg, s.logger, time.Now())
	t := newCRATally()
	for _, c := range checks {
		if ctx.Err() != nil {
			t.log(ctx, s.logger, congressNum, len(checks))
			return stepOutcome{items: t.checked}, ctx.Err()
		}
		if t.consecutive >= craMaxConsecutiveErrors {
			t.stopped = true
			break
		}
		out, matchErr := m.match(ctx, c)
		if matchErr == nil {
			matchErr = s.storeCRARule(ctx, m, &out, t)
		}
		if matchErr != nil {
			s.logger.WarnContext(ctx, "CRA rule check failed", "bill_id", c.BillID, "error", matchErr)
		}
		t.record(out, matchErr)
	}
	t.log(ctx, s.logger, congressNum, len(checks))
	if t.errors > 0 && t.checked == t.unmatched[repository.CRAReasonUnparsed] {
		return stepOutcome{}, fmt.Errorf("every CRA rule lookup failed: %d of %d bills", t.errors, len(checks))
	}
	return stepOutcome{items: t.checked, warning: t.warning(len(checks))}, nil
}

// storeCRARule writes the outcome's documents, then its bill_cra_rules row, and marks the bill
// when a reader would see a change: a new or changed row, or a document written this run.
func (s *Service) storeCRARule(ctx context.Context, m *craMatcher, out *craOutcome, t *craTally) error {
	changed := false
	for _, d := range []*repository.FRDocumentRow{out.doc, out.withdrawn} {
		if d == nil {
			continue
		}
		wrote, err := m.upsertDocument(ctx, s.store, *d)
		if err != nil {
			return fmt.Errorf("upsert federal register document %s: %w", d.DocumentNumber, err)
		}
		changed = changed || wrote
	}
	out.row.ContextHash = craContextHash(*out)
	ruleChanged, err := s.store.UpsertCRARule(ctx, out.row)
	if err != nil {
		return fmt.Errorf("upsert CRA rule: %w", err)
	}
	if changed || ruleChanged {
		markBill(ctx, out.row.BillID)
		t.changed++
	}
	return nil
}

// craOutcome is one resolution's result: its row, and the documents to store with it.
type craOutcome struct {
	row       repository.CRARuleRow
	doc       *repository.FRDocumentRow
	withdrawn *repository.FRDocumentRow
	// citeMismatch is set when the resolution's citation was set aside: no document on the cited
	// page shares enough of its title.
	citeMismatch bool
}

func (o *craOutcome) unmatched(reason string) {
	o.row.Status = repository.CRAStatusUnmatched
	o.row.Reason = &reason
}

// craMatcher looks resolutions up in the Federal Register. It lives for one run: it caches each
// day's documents and remembers which documents the run wrote.
type craMatcher struct {
	fr      *fedreg.Client
	logger  *slog.Logger
	now     time.Time
	days    map[string][]fedreg.Document
	written map[string]bool
}

func newCRAMatcher(fr *fedreg.Client, logger *slog.Logger, now time.Time) *craMatcher {
	return &craMatcher{
		fr: fr, logger: logger, now: now,
		days: map[string][]fedreg.Document{}, written: map[string]bool{},
	}
}

// match parses a resolution and finds the document it disapproves: by its citation, guarded by
// the title, and else by exact title, agency and date. An error means a request failed, and
// nothing should be written for the bill.
func (m *craMatcher) match(ctx context.Context, c repository.CRARuleCheck) (craOutcome, error) {
	out := craOutcome{row: repository.CRARuleRow{
		BillID: c.BillID, SourceTextHash: c.TextHash, MatcherVersion: cra.MatcherVersion,
	}}
	res, err := cra.Parse(c.Title, c.Text)
	out.row.RuleTitle, out.row.RuleAgency, out.row.GAOOpinion = res.RuleTitle, res.Agency, res.GAOOpinion
	if err != nil {
		out.unmatched(repository.CRAReasonUnparsed)
		return out, nil //nolint:nilerr // an unparsed resolution is a result, recorded as such
	}
	var byCite *repository.FRDocumentRow
	if cit, ok := res.Disapproved(); ok {
		out.row.Cited = &cit.Text
		if byCite, err = m.byCitation(ctx, cit, res.RuleTitle, res.Withdrawal); err != nil {
			return out, err
		}
		if byCite != nil && byCite.DocType != fedreg.DocProposedRule {
			return m.matched(ctx, out, res, byCite, repository.CRAMethodCitation)
		}
		out.citeMismatch = byCite == nil
	}
	byTitle, reason, err := m.byTitle(ctx, res, m.introduced(c))
	switch {
	case err != nil:
		return out, err
	case byTitle != nil:
		return m.matched(ctx, out, res, byTitle, repository.CRAMethodTitle)
	case byCite != nil:
		// The text cites a proposed rule and no final rule has its title: the cited one it is.
		return m.matched(ctx, out, res, byCite, repository.CRAMethodCitation)
	case out.citeMismatch:
		reason = repository.CRAReasonCiteMismatch
	}
	out.unmatched(reason)
	return out, nil
}

// introduced is the date the title fallback searches up to: the resolution's introduction, or
// today when it has none.
func (m *craMatcher) introduced(c repository.CRARuleCheck) time.Time {
	if c.IntroducedDate != nil {
		return *c.IntroducedDate
	}
	return m.now
}

// matched completes a match: for a disapproved withdrawal, it also looks up the withdrawn
// document by its citation.
func (m *craMatcher) matched(
	ctx context.Context, out craOutcome, res cra.Resolution, doc *repository.FRDocumentRow, method string,
) (craOutcome, error) {
	out.row.Status = repository.CRAStatusMatched
	out.row.Method = &method
	out.row.DocumentNumber = &doc.DocumentNumber
	out.doc = doc
	if cit, ok := res.Withdrawn(); ok {
		w, err := m.byCitation(ctx, cit, res.WithdrawnTitle, false)
		if err != nil {
			return out, err
		}
		if w != nil {
			out.withdrawn = w
			out.row.WithdrawnDocumentNumber = &w.DocumentNumber
		}
	}
	return out, nil
}

// byCitation finds the cited document: the one of the cited volume starting on the cited page, or
// else covering it, that shares at least half the title's words (or, for a disapproved
// withdrawal, that is a withdrawal). With a date, it reads that day's documents; without one, it
// searches the citation's year for the title. Nil when no document passes.
func (m *craMatcher) byCitation(
	ctx context.Context, cit cra.Citation, title string, withdrawal bool,
) (*repository.FRDocumentRow, error) {
	var docs []fedreg.Document
	var err error
	if !cit.Date.IsZero() {
		docs, err = m.day(ctx, cit.Date)
	} else {
		docs, err = m.fr.Documents(ctx, fedreg.Query{
			Term: title, Year: cit.Volume + frVolumeYearOffset, PerPage: craSearchPerPage, MaxPages: craSearchPages,
			Types: []string{fedreg.TypeRule, fedreg.TypeNotice, fedreg.TypeProposedRule}, Truncate: true,
		})
		if errors.Is(err, fedreg.ErrPageCap) {
			err = nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("look up %d FR %d: %w", cit.Volume, cit.Page, err)
	}
	for _, d := range onCitedPage(docs, cit) {
		if !cra.TitleOverlap(title, d.Title) && (!withdrawal || !cra.IsWithdrawal(d.Title, d.Action)) {
			continue
		}
		if row, ok := m.documentRow(ctx, d); ok {
			return &row, nil
		}
	}
	return nil, nil //nolint:nilnil // no document on the cited page passes the guard
}

// day returns the rules, notices and proposed rules published on date, read once a run.
func (m *craMatcher) day(ctx context.Context, date time.Time) ([]fedreg.Document, error) {
	key := date.Format(time.DateOnly)
	if docs, ok := m.days[key]; ok {
		return docs, nil
	}
	docs, err := m.fr.Documents(ctx, fedreg.Query{
		PublishedOn: date, Types: []string{fedreg.TypeRule, fedreg.TypeNotice, fedreg.TypeProposedRule},
	})
	if err != nil {
		return nil, err
	}
	m.days[key] = docs
	return docs, nil
}

// onCitedPage returns the documents of the citation's volume that start on its page, then those
// whose page range covers it.
func onCitedPage(docs []fedreg.Document, cit cra.Citation) []fedreg.Document {
	var starts, covers []fedreg.Document
	for _, d := range docs {
		switch {
		case d.Volume != cit.Volume:
		case d.StartPage == cit.Page:
			starts = append(starts, d)
		case d.StartPage < cit.Page && cit.Page <= d.EndPage:
			covers = append(covers, d)
		}
	}
	return append(starts, covers...)
}

// byTitle is the fallback: rules and notices published by the introduction date whose title is
// the resolution's exactly, that aren't corrections, delays or extensions (or withdrawals, unless
// the resolution disapproves one), and whose agency matches. It returns the one accepted, or the
// reason none was: no_candidates or ambiguous.
func (m *craMatcher) byTitle(
	ctx context.Context, res cra.Resolution, introduced time.Time,
) (*repository.FRDocumentRow, string, error) {
	docs, err := m.fr.Documents(ctx, fedreg.Query{
		Term: res.RuleTitle, PublishedBy: introduced, Types: []string{fedreg.TypeRule, fedreg.TypeNotice},
		PerPage: craSearchPerPage, MaxPages: craSearchPages, Truncate: true,
	})
	truncated := errors.Is(err, fedreg.ErrPageCap)
	if err != nil && !truncated {
		return nil, "", fmt.Errorf("search the federal register by title: %w", err)
	}
	want := cra.NormalizeTitle(res.RuleTitle)
	var survivors []repository.FRDocumentRow
	for _, d := range docs {
		switch {
		case cra.NormalizeTitle(d.Title) != want,
			d.CorrectionOf != "" || cra.IsAmendment(d.Action),
			cra.IsWithdrawal(d.Title, d.Action) && !res.Withdrawal,
			!cra.AgencyMatches(res.Agency, agencyNames(d.Agencies)):
			continue
		}
		if row, ok := m.documentRow(ctx, d); ok && !row.PublicationDate.After(introduced) {
			survivors = append(survivors, row)
		}
	}
	return acceptByTitle(survivors, introduced, truncated)
}

// acceptByTitle applies the fallback's rule: a single survivor of any age; with several, the
// newest if it's within craNewestWithin of the introduction and the next newest is at least
// craNextOlderBy older. A truncated search may have missed older survivors, so a lone survivor
// isn't enough there.
func acceptByTitle(
	survivors []repository.FRDocumentRow, introduced time.Time, truncated bool,
) (*repository.FRDocumentRow, string, error) {
	slices.SortStableFunc(survivors, func(a, b repository.FRDocumentRow) int {
		return b.PublicationDate.Compare(a.PublicationDate)
	})
	switch {
	case len(survivors) == 0:
		return nil, repository.CRAReasonNoCandidates, nil
	case len(survivors) == 1 && !truncated:
		return &survivors[0], "", nil
	case len(survivors) > 1 && introduced.Sub(survivors[0].PublicationDate) <= craNewestWithin &&
		survivors[0].PublicationDate.Sub(survivors[1].PublicationDate) >= craNextOlderBy:
		return &survivors[0], "", nil
	default:
		return nil, repository.CRAReasonAmbiguous, nil
	}
}

func agencyNames(agencies []fedreg.Agency) []string {
	names := make([]string, 0, len(agencies))
	for _, a := range agencies {
		names = append(names, agencyName(a))
	}
	return names
}

// agencyName is the agency's listed name, or the name as the document gives it.
func agencyName(a fedreg.Agency) string {
	if a.Name != "" {
		return a.Name
	}
	return a.RawName
}

// upsertDocument writes a document once a run, and reports whether this run wrote it.
func (m *craMatcher) upsertDocument(
	ctx context.Context, store repository.PipelineStore, d repository.FRDocumentRow,
) (bool, error) {
	if wrote, ok := m.written[d.DocumentNumber]; ok {
		return wrote, nil
	}
	wrote, err := store.UpsertFRDocument(ctx, d)
	if err != nil {
		return false, err
	}
	m.written[d.DocumentNumber] = wrote
	return wrote, nil
}

// documentRow turns an API document into the stored row: text unescaped once and trimmed,
// dates parsed, links kept only on their expected hosts, and the content hash set. It reports
// false for a document missing what the table needs (a valid number, volume, page, title or
// publication date), which is then never a match.
func (m *craMatcher) documentRow(ctx context.Context, d fedreg.Document) (repository.FRDocumentRow, bool) {
	published, err := time.Parse(time.DateOnly, d.PublicationDate)
	title := plain(d.Title)
	if err != nil || !frDocumentNumberPattern.MatchString(d.DocumentNumber) || d.Volume <= 0 || d.StartPage <= 0 ||
		title == "" {
		m.logger.WarnContext(ctx, "federal register document skipped: incomplete", "document_number",
			d.DocumentNumber)
		return repository.FRDocumentRow{}, false
	}
	row := repository.FRDocumentRow{
		DocumentNumber: d.DocumentNumber, Citation: plain(d.Citation),
		Volume: d.Volume, StartPage: d.StartPage, EndPage: max(d.EndPage, d.StartPage),
		DocType: plain(d.Type), Action: nilIfEmpty(plain(d.Action)), Title: title,
		PublicationDate: published, Abstract: nilIfEmpty(plain(d.Abstract)),
		DocketID: nilIfEmpty(plain(d.DocketID())), RegulationIDNumbers: d.RegulationIDNumbers,
	}
	if row.Citation == "" {
		row.Citation = fmt.Sprintf("%d FR %d", d.Volume, d.StartPage)
	}
	if t, parseErr := time.Parse(time.DateOnly, d.EffectiveOn); parseErr == nil {
		row.EffectiveOn = &t
	}
	for _, a := range d.Agencies {
		if name := plain(agencyName(a)); name != "" {
			row.Agencies = append(row.Agencies, repository.FRAgency{Name: name, Slug: a.Slug})
		}
	}
	row.HTMLURL = d.HTMLURL
	if !onHost(d.HTMLURL, frHTMLHost) {
		m.dropURL(ctx, d.DocumentNumber, "html_url", d.HTMLURL)
		row.HTMLURL = "https://" + frHTMLHost + "/d/" + d.DocumentNumber
	}
	if onHost(d.PDFURL, frPDFHost) {
		row.PDFURL = &d.PDFURL
	} else if d.PDFURL != "" {
		m.dropURL(ctx, d.DocumentNumber, "pdf_url", d.PDFURL)
	}
	row.ContentHash = frContentHash(row)
	return row, true
}

// dropURL logs a link left out because it's off its expected host. Only the host is logged.
func (m *craMatcher) dropURL(ctx context.Context, documentNumber, field, raw string) {
	host := ""
	if u, err := url.Parse(raw); err == nil {
		host = u.Host
	}
	m.logger.WarnContext(ctx, "federal register link dropped: unexpected host", "document_number",
		documentNumber, "field", field, "host", host)
}

// onHost reports whether raw is an https URL on host.
func onHost(raw, host string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == host && u.User == nil
}

// plain unescapes a text field's entities once and collapses its whitespace.
func plain(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}

// frContentHash is the sha256 of a document row's fields, without the hash itself.
func frContentHash(r repository.FRDocumentRow) string {
	return hashJSON([]any{
		r.DocumentNumber, r.Citation, r.Volume, r.StartPage, r.EndPage, r.DocType, r.Action, r.Title,
		r.Agencies, r.PublicationDate.Format(time.DateOnly), r.EffectiveOn, r.Abstract, r.HTMLURL, r.PDFURL,
		r.DocketID, r.RegulationIDNumbers,
	})
}

// craContextHash is the sha256 of what the summary prompt's disapproved_rule block is built
// from: the rule as the resolution names it, the outcome, and the documents' content hashes.
func craContextHash(o craOutcome) string {
	var doc, withdrawn string
	if o.doc != nil {
		doc = o.doc.ContentHash
	}
	if o.withdrawn != nil {
		withdrawn = o.withdrawn.ContentHash
	}
	return hashJSON([]any{o.row.RuleTitle, o.row.RuleAgency, o.row.Status, o.row.Method, doc, withdrawn})
}

func hashJSON(v []any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Strings, numbers, times and tagged structs always marshal.
		panic(fmt.Sprintf("hash fields: %v", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// craTally counts a run's outcomes for its log line and warning.
type craTally struct {
	checked, matchedCitation, matchedTitle, citeMismatch, changed, errors int
	// unmatched counts the unmatched rows by reason.
	unmatched map[string]int
	// consecutive is the failures since the last success; stopped is set when they stopped the run.
	consecutive int
	stopped     bool
}

func newCRATally() *craTally { return &craTally{unmatched: map[string]int{}} }

func (t *craTally) record(out craOutcome, err error) {
	if err != nil {
		t.errors++
		t.consecutive++
		return
	}
	t.consecutive = 0
	t.checked++
	if out.citeMismatch {
		t.citeMismatch++
	}
	switch {
	case out.row.Status == repository.CRAStatusUnmatched && out.row.Reason != nil:
		t.unmatched[*out.row.Reason]++
	case out.row.Method != nil && *out.row.Method == repository.CRAMethodTitle:
		t.matchedTitle++
	default:
		t.matchedCitation++
	}
}

// warning is the run's sync_state warning when some lookups failed or the run stopped early.
func (t *craTally) warning(due int) string {
	switch {
	case t.stopped:
		return fmt.Sprintf("stopped after %d failed CRA rule lookups in a row; %d of %d due bills checked",
			craMaxConsecutiveErrors, t.checked, due)
	case t.errors > 0:
		return fmt.Sprintf("%d of %d CRA rule lookups failed", t.errors, due)
	default:
		return ""
	}
}

func (t *craTally) log(ctx context.Context, logger *slog.Logger, congressNum, due int) {
	logger.InfoContext(ctx, "cra rules checked", "congress", congressNum, "due", due, "checked", t.checked,
		"matched_citation", t.matchedCitation, "matched_title", t.matchedTitle,
		"unmatched_no_candidates", t.unmatched[repository.CRAReasonNoCandidates],
		"unmatched_ambiguous", t.unmatched[repository.CRAReasonAmbiguous],
		"unmatched_cite_mismatch", t.unmatched[repository.CRAReasonCiteMismatch],
		"unmatched_unparsed", t.unmatched[repository.CRAReasonUnparsed],
		"cite_mismatch", t.citeMismatch, "changed", t.changed, "errors", t.errors, "stopped", t.stopped)
}
