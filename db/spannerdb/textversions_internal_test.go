package spannerdb

import (
	"encoding/json"
	"reflect"
	"testing"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
)

func row(versionType, code string) repository.TextVersionRow {
	return repository.TextVersionRow{VersionType: versionType, VersionCode: code}
}

func TestPlanTextVersions(t *testing.T) {
	stored := []storedTextVersion{
		{ID: "id-ih", Code: "ih", Type: "Introduced in House"},
		{ID: "id-old", Code: "engrossed_amendment_senate", Type: "Engrossed Amendment Senate"},
		{ID: "id-rh", Code: "rh", Type: "Reported in House"},
	}
	tests := []struct {
		name      string
		versions  []repository.TextVersionRow
		updateIDs []string
		inserts   int
		pruneIDs  []string
		dupes     int
	}{
		{name: "empty input changes nothing"},
		{
			name: "code match, type fallback, insert and prune",
			versions: []repository.TextVersionRow{
				row("Engrossed Amendment Senate", "eas"),
				row("Enrolled Bill", "enr"),
				row("Introduced in House", "ih"),
			},
			updateIDs: []string{"id-old", "id-ih"},
			inserts:   1,
			pruneIDs:  []string{"id-rh"},
		},
		{
			name: "duplicate code keeps the first row",
			versions: []repository.TextVersionRow{
				row("Reported in House", "rh"),
				row("Something else", "rh"),
				row("Introduced in House", "ih"),
				row("Engrossed Amendment Senate", "eas"),
			},
			updateIDs: []string{"id-rh", "id-ih", "id-old"},
			dupes:     1,
		},
		{
			name: "code wins over type",
			versions: []repository.TextVersionRow{
				row("Reported in House", "ih"),
				row("Introduced in House", "rh"),
				row("Engrossed Amendment Senate", "engrossed_amendment_senate"),
			},
			updateIDs: []string{"id-ih", "id-rh", "id-old"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := planTextVersions(stored, tt.versions)
			var ids []string
			for _, u := range plan.updates {
				ids = append(ids, u.id)
			}
			if !reflect.DeepEqual(ids, tt.updateIDs) || len(plan.inserts) != tt.inserts ||
				!reflect.DeepEqual(plan.pruneIDs, tt.pruneIDs) || plan.duplicates != tt.dupes {
				t.Errorf("plan = updates %v, %d inserts, prune %v, %d dupes; want %v, %d, %v, %d",
					ids, len(plan.inserts), plan.pruneIDs, plan.duplicates,
					tt.updateIDs, tt.inserts, tt.pruneIDs, tt.dupes)
			}
			if got := len(plan.statements("b")); tt.versions == nil && got != 0 {
				t.Errorf("statements = %d, want 0", got)
			}
		})
	}
}

func TestFormatsChanged(t *testing.T) {
	const xml = `{"type":"Formatted XML","url":"https://x.test/ih.xml"}`
	const txt = `{"type":"Formatted Text","url":"https://x.test/ih.htm"}`
	stored := spanner.NullJSON{Valid: true, Value: []any{
		map[string]any{"type": "Formatted XML", "url": "https://x.test/ih.xml"},
		map[string]any{"type": "Formatted Text", "url": "https://x.test/ih.htm"},
	}}
	tests := []struct {
		name    string
		stored  spanner.NullJSON
		formats string
		want    bool
	}{
		{name: "same URLs in another order", stored: stored, formats: "[" + txt + "," + xml + "]"},
		{name: "same URLs, type renamed", stored: stored,
			formats: `[{"type":"XML","url":"https://x.test/ih.xml"},` + txt + `]`},
		{name: "a new URL", stored: stored,
			formats: `[{"type":"Formatted XML","url":"https://x.test/ih-corrected.xml"},` + txt + `]`, want: true},
		{name: "a URL dropped", stored: stored, formats: "[" + xml + "]", want: true},
		{name: "no URLs left", stored: stored, formats: `[]`},
		{name: "not a formats list", stored: stored, formats: `{"url":"https://x.test/a"}`},
		{name: "none stored", formats: "[" + xml + "]", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatsChanged(tt.stored, json.RawMessage(tt.formats)); got != tt.want {
				t.Errorf("formatsChanged = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPlanTextVersions_Refetch(t *testing.T) {
	oldURL := spanner.NullJSON{Valid: true, Value: []any{map[string]any{"url": "https://x.test/old.xml"}}}
	stored := []storedTextVersion{
		{ID: "id-ih", Code: "ih", Type: "Introduced in House", Formats: oldURL, Fetched: true},
		{ID: "id-rh", Code: "rh", Type: "Reported in House", Formats: oldURL},
		{ID: "id-eh", Code: "eh", Type: "Engrossed in House", Formats: oldURL, Fetched: true},
	}
	withURL := func(code, url string) repository.TextVersionRow {
		r := row("", code)
		r.Formats = json.RawMessage(`[{"type":"Formatted XML","url":"` + url + `"}]`)
		return r
	}
	plan := planTextVersions(stored, []repository.TextVersionRow{
		withURL("ih", "https://x.test/new.xml"), // fetched, new URL: refetch
		withURL("rh", "https://x.test/new.xml"), // never fetched: nothing to refetch
		withURL("eh", "https://x.test/old.xml"), // same URL
	})
	var refetch []string
	for _, u := range plan.updates {
		if u.refetch {
			refetch = append(refetch, u.id)
		}
	}
	if !reflect.DeepEqual(refetch, []string{"id-ih"}) {
		t.Errorf("refetch = %v, want [id-ih]", refetch)
	}
	if got := plan.result().RefetchCodes; !reflect.DeepEqual(got, []string{"ih"}) {
		t.Errorf("RefetchCodes = %v, want [ih]", got)
	}
	// 3 version updates and one fetched_at reset.
	if got := len(plan.statements("b")); got != 4 {
		t.Errorf("statements = %d, want 4", got)
	}
}
