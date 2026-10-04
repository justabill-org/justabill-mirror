package sync

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // version dates are converted to US Eastern; don't depend on the image's zoneinfo

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// Version metadata rules from docs/design/79-text-version-upsert.md.

const (
	codePublicLaw = "pl"
	codeEnrolled  = "enr"
	easternZone   = "America/New_York"
	hoursEST      = -5
)

// billsFile matches a GovInfo bill file name, BILLS-{congress}{type}{number}{code}.{ext}, and
// captures the version code.
var billsFile = regexp.MustCompile(`(?i)^BILLS-\d+[a-z]+\d+([a-z]{1,5})\.[a-z]+$`)

// versionMeta is one Congress.gov text version with its derived code and date. pos is its
// position in the API response, which lists versions newest first.
type versionMeta struct {
	tv   congress.TextVersion
	pos  int
	code string
	date *time.Time
}

// TextVersionRows turns a bill's text versions, as Congress.gov lists them (newest first), into
// rows for UpsertBillTextVersions. Each row gets its GovInfo version code, its US Eastern civil
// date and a chronological sort_order (1 is the oldest). When two versions share a code the
// first (newest) is kept and a warning is logged. Rows keep the API's order.
func TextVersionRows(
	ctx context.Context, logger *slog.Logger, billID string, versions []congress.TextVersion,
) []repository.TextVersionRow {
	eastern := easternLocation()
	metas := make([]versionMeta, 0, len(versions))
	seen := make(map[string]bool, len(versions))
	var duplicates []string
	for i, tv := range versions {
		code := versionCode(tv)
		if seen[code] {
			duplicates = append(duplicates, code)
			continue
		}
		seen[code] = true
		date, ok := easternDate(tv.Date, eastern)
		if !ok || date == nil {
			logger.DebugContext(ctx, "text version has no usable date",
				"bill_id", billID, "version_code", code, "date", tv.Date)
		}
		metas = append(metas, versionMeta{tv: tv, pos: i, code: code, date: date})
	}
	if len(duplicates) > 0 {
		logger.WarnContext(ctx, "dropped text versions with duplicate codes",
			"bill_id", billID, "version_codes", duplicates)
	}

	sortOrder := make(map[int]int, len(metas))
	for i, m := range chronological(metas) {
		sortOrder[m.pos] = i + 1
	}

	rows := make([]repository.TextVersionRow, 0, len(metas))
	for _, m := range metas {
		formatsJSON, _ := json.Marshal(m.tv.Formats)
		rows = append(rows, repository.TextVersionRow{
			BillID:      billID,
			VersionType: m.tv.Type,
			VersionCode: m.code,
			Date:        m.date,
			Formats:     formatsJSON,
			SortOrder:   sortOrder[m.pos],
		})
	}
	return rows
}

// versionCode returns the version's GovInfo code: the suffix of its first BILLS- file name,
// "pl" for a PLAW- file or a Public Law, then the type map, then a snake-case fallback.
func versionCode(tv congress.TextVersion) string {
	for _, f := range tv.Formats {
		name := f.URL
		if u, err := url.Parse(f.URL); err == nil {
			name = path.Base(u.Path)
		}
		if strings.HasPrefix(strings.ToUpper(name), "PLAW-") {
			return codePublicLaw
		}
		if m := billsFile.FindStringSubmatch(name); m != nil {
			return strings.ToLower(m[1])
		}
	}
	return versionTypeToCode(tv.Type)
}

// easternDate parses a Congress.gov version date (RFC 3339, midnight US Eastern) and returns its
// Eastern civil date as midnight UTC, which is how the store writes a DATE. A null date gives
// nil, true; an unparseable one gives nil, false.
func easternDate(raw string, eastern *time.Location) (*time.Time, bool) {
	if raw == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t, err = time.ParseInLocation(time.DateOnly, raw, eastern)
		if err != nil {
			return nil, false
		}
	}
	y, m, d := t.In(eastern).Date()
	return new(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)), true
}

// easternLocation returns US Eastern time. The zone database is embedded (time/tzdata), so the
// fixed EST fallback only guards against a broken build.
func easternLocation() *time.Location {
	loc, err := time.LoadLocation(easternZone)
	if err != nil {
		return time.FixedZone("EST", hoursEST*int(time.Hour/time.Second))
	}
	return loc
}

// chronological returns the versions oldest first:
//   - dated versions by date, ties broken by reverse API position (the API is newest first);
//   - an undated version goes just after its next-older neighbor in the API list, or first;
//   - an undated enrolled bill goes after every dated bill-stage version;
//   - the public law goes last.
func chronological(metas []versionMeta) []versionMeta {
	var order, tail []versionMeta
	var enrolled *versionMeta
	for i := range metas {
		switch m := metas[i]; {
		case m.code == codePublicLaw:
			tail = append(tail, m)
		case m.date != nil:
			order = append(order, m)
		case m.code == codeEnrolled:
			enrolled = &metas[i]
		}
	}
	slices.SortStableFunc(order, func(a, b versionMeta) int {
		if c := a.date.Compare(*b.date); c != 0 {
			return c
		}
		return b.pos - a.pos
	})

	// Undated versions, oldest first, so an undated neighbor is already placed.
	for i, m := range slices.Backward(metas) {
		if m.date != nil || m.code == codePublicLaw || m.code == codeEnrolled {
			continue
		}
		order = slices.Insert(order, insertAfterOlder(order, metas[i+1:]), m)
	}

	if enrolled != nil {
		order = append(order, *enrolled)
	}
	slices.Reverse(tail) // several public laws can't share a code, but keep the rule total
	return append(order, tail...)
}

// insertAfterOlder returns the index just after the nearest version in older (the versions
// listed after it, nearest first) that is already in order, or 0 if there is none.
func insertAfterOlder(order, older []versionMeta) int {
	for _, o := range older {
		if at := slices.IndexFunc(order, func(p versionMeta) bool { return p.pos == o.pos }); at >= 0 {
			return at + 1
		}
	}
	return 0
}

// versionTypeToCode maps a Congress.gov version type to its GovInfo code (govinfo.gov/help/bills).
// Types are matched on their words without "in", "to", "by" or "the", so "Reported to Senate",
// "Reported in Senate" and GovInfo's "Reported in (Senate)" all give "rs". Unknown types fall back
// to snake case.
func versionTypeToCode(vt string) string {
	codes := map[string]string{
		"amendment senate":                        "as",
		"additional sponsors house":               "ash",
		"additional sponsors senate":              "sas",
		"agreed house":                            "ath",
		"agreed senate":                           "ats",
		"committee discharged house":              "cdh",
		"committee discharged senate":             "cds",
		"considered and passed house":             "cph",
		"considered and passed senate":            "cps",
		"engrossed amendment house":               "eah",
		"engrossed amendment senate":              "eas",
		"engrossed house":                         "eh",
		"engrossed senate":                        "es",
		"engrossed and deemed passed house":       "eph",
		"enrolled":                                codeEnrolled,
		"enrolled bill":                           codeEnrolled,
		"failed amendment house":                  "fah",
		"failed passage house":                    "fph",
		"failed passage senate":                   "fps",
		"held at desk house":                      "hdh",
		"held at desk senate":                     "hds",
		"introduced house":                        "ih",
		"introduced senate":                       "is",
		"indefinitely postponed house":            "iph",
		"indefinitely postponed senate":           "ips",
		"laid on table house":                     "lth",
		"laid on table senate":                    "lts",
		"ordered be printed house":                "oph",
		"ordered be printed senate":               "ops",
		"ordered be printed with house amendment": "pwah",
		"previous action vitiated":                "pav",
		"placed on calendar house":                "pch",
		"placed on calendar senate":               "pcs",
		"printed as passed":                       "pap",
		"public print":                            "pp",
		"public law":                              codePublicLaw,
		"referred with amendments house":          "rah",
		"referred with amendments senate":         "ras",
		"reference change house":                  "rch",
		"reference change senate":                 "rcs",
		"received house":                          "rdh",
		"received senate":                         "rds",
		"re engrossed amendment house":            "reah",
		"re engrossed amendment senate":           "res",
		"re enrolled bill":                        "renr",
		"referred house":                          "rfh",
		"referred senate":                         "rfs",
		"reported house":                          "rh",
		"reported senate":                         "rs",
		"returned house unanimous consent":        "rhuc",
		"referral instructions house":             "rih",
		"referral instructions senate":            "ris",
		"referred committee house":                "rth",
		"referred committee senate":               "rts",
		"sponsor change":                          "sc",
	}
	if c, ok := codes[typeKey(vt)]; ok {
		return c
	}
	return strings.ToLower(strings.ReplaceAll(vt, " ", "_"))
}

// typeKey lowercases a version type, splits it into words on anything but letters and drops
// "in", "to", "by" and "the".
func typeKey(vt string) string {
	words := strings.FieldsFunc(strings.ToLower(vt), func(r rune) bool { return r < 'a' || r > 'z' })
	words = slices.DeleteFunc(words, func(w string) bool {
		return w == "in" || w == "to" || w == "by" || w == "the"
	})
	return strings.Join(words, " ")
}
