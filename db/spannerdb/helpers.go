package spannerdb

import (
	"encoding/json"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
)

// Column and query parameter names shared by more than one file.
const (
	colChamber  = "chamber"
	colCongress = "congress"

	paramChamber       = "chamber"
	paramContentGz     = "contentGz"
	paramNow           = "now"
	paramSince         = "since"
	paramSections      = "sections"
	paramState         = "state"
	paramTextVersionID = "vid"
)

func nullStringPtr(ns spanner.NullString) *string {
	if !ns.Valid {
		return nil
	}
	return &ns.StringVal
}

func ptrToNullString(s *string) spanner.NullString {
	if s == nil {
		return spanner.NullString{}
	}
	return spanner.NullString{StringVal: *s, Valid: true}
}

func nullInt64Ptr(ni spanner.NullInt64) *int {
	if !ni.Valid {
		return nil
	}
	v := int(ni.Int64)
	return &v
}

func ptrToNullInt64(i *int) spanner.NullInt64 {
	if i == nil {
		return spanner.NullInt64{}
	}
	return spanner.NullInt64{Int64: int64(*i), Valid: true}
}

func ptrToNullBool(b *bool) spanner.NullBool {
	if b == nil {
		return spanner.NullBool{}
	}
	return spanner.NullBool{Bool: *b, Valid: true}
}

func nullDatePtr(nd spanner.NullDate) *time.Time {
	if !nd.Valid {
		return nil
	}
	t := time.Date(nd.Date.Year, nd.Date.Month, nd.Date.Day, 0, 0, 0, 0, time.UTC)
	return &t
}

func nullTimePtr(nt spanner.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	return &nt.Time
}

// nullTimeValue returns nt's time, or the zero time when it is NULL.
func nullTimeValue(nt spanner.NullTime) time.Time {
	if !nt.Valid {
		return time.Time{}
	}
	return nt.Time
}

func timeToCivilDate(t time.Time) civil.Date {
	return civil.Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

func ptrTimeToCivilDate(t *time.Time) spanner.NullDate {
	if t == nil {
		return spanner.NullDate{}
	}
	return spanner.NullDate{Date: timeToCivilDate(*t), Valid: true}
}

func ptrTimeToNullTime(t *time.Time) spanner.NullTime {
	if t == nil {
		return spanner.NullTime{}
	}
	return spanner.NullTime{Time: *t, Valid: true}
}

func nullJSONToRaw(nj spanner.NullJSON) json.RawMessage {
	if !nj.Valid || nj.Value == nil {
		return nil
	}
	data, err := json.Marshal(nj.Value)
	if err != nil {
		return nil
	}
	return data
}

func rawToNullJSON(rm json.RawMessage) spanner.NullJSON {
	if rm == nil {
		return spanner.NullJSON{}
	}
	var v any
	if err := json.Unmarshal(rm, &v); err != nil {
		return spanner.NullJSON{}
	}
	return spanner.NullJSON{Value: v, Valid: true}
}

// lawsToNullJSON is the bills.laws value for laws: NULL when there are none.
func lawsToNullJSON(laws []model.BillLaw) spanner.NullJSON {
	if len(laws) == 0 {
		return spanner.NullJSON{}
	}
	return spanner.NullJSON{Value: laws, Valid: true}
}

// nullJSONToLaws reads bills.laws. NULL, or a value that isn't a list of laws, reads as none.
func nullJSONToLaws(nj spanner.NullJSON) []model.BillLaw {
	raw := nullJSONToRaw(nj)
	if raw == nil {
		return nil
	}
	var laws []model.BillLaw
	if err := json.Unmarshal(raw, &laws); err != nil || len(laws) == 0 {
		return nil
	}
	return laws
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
