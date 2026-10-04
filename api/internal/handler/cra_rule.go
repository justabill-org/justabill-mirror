package handler

import (
	"context"
	"net/url"

	"github.com/justabill-org/justabill/db/model"
)

// Hosts of the Federal Register links GET /bills/{id} serves for a CRA resolution
// (docs/design/590-cra-disapproved-rules.md, "Security and privacy").
const (
	federalRegisterHost = "www.federalregister.gov"
	govInfoHost         = "www.govinfo.gov"
)

// federalRegisterSearch is the Federal Register's document search page.
const federalRegisterSearch = "https://" + federalRegisterHost + "/documents/search"

// disapprovedRule reads the rule bill id disapproves, if it is a checked CRA resolution, and
// makes it safe to serve: each document's links are kept only on their own host, and SearchURL
// is a Federal Register search for the rule.
func (h *Handler) disapprovedRule(ctx context.Context, id string) (*model.CRARule, error) {
	rule, err := h.Bills.GetCRARule(ctx, id)
	if err != nil || rule == nil {
		return nil, err
	}
	for _, doc := range []*model.FRDocument{rule.Document, rule.WithdrawnDocument} {
		if doc == nil {
			continue
		}
		doc.HTMLURL = h.servedURL(ctx, id, "html_url", doc.HTMLURL, federalRegisterHost)
		doc.PDFURL = h.servedURL(ctx, id, "pdf_url", doc.PDFURL, govInfoHost)
	}
	rule.SearchURL = ruleSearchURL(rule)
	return rule, nil
}

// servedURL returns raw when it is an https URL on host, and otherwise nil, logging the drop
// without the URL. The pipeline drops such URLs at ingest; this keeps one that got past it, or
// was stored before it, off the page.
func (h *Handler) servedURL(ctx context.Context, billID, field string, raw *string, host string) *string {
	if raw == nil {
		return nil
	}
	u, err := url.Parse(*raw)
	if err == nil && u.Scheme == "https" && u.Host == host && u.User == nil {
		return raw
	}
	h.log.WarnContext(ctx, "dropped a federal register link off its host", "bill_id", billID, "field", field)
	return nil
}

// ruleSearchURL is a Federal Register search for the rule as the resolution names it: its title
// as a quoted phrase, then its agency's words.
func ruleSearchURL(rule *model.CRARule) string {
	term := `"` + rule.RuleTitle + `"`
	if rule.RuleAgency != "" {
		term += " " + rule.RuleAgency
	}
	return federalRegisterSearch + "?" + url.Values{"conditions[term]": {term}}.Encode()
}
