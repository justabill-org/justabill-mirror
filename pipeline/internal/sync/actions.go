package sync

import (
	"net/url"
	"slices"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// BillActionRows maps a bill's Congress.gov actions to the rows ReplaceBillActions stores, in the
// order Congress.gov lists them (SortOrder 1, 2, …). An action's recorded vote is the last valid
// roll call in its recordedVotes (see [storedRecordedVote]).
func BillActionRows(actions []congress.Action) []repository.BillActionRow {
	rows := make([]repository.BillActionRow, 0, len(actions))
	for i, a := range actions {
		actionDate, _ := time.Parse(time.DateOnly, a.ActionDate)
		var sourceSystem *string
		if a.SourceSystem != nil && a.SourceSystem.Name != "" {
			sourceSystem = &a.SourceSystem.Name
		}
		rows = append(rows, repository.BillActionRow{
			ActionDate:   actionDate,
			ActionText:   a.Text,
			ActionType:   nilIfEmpty(a.Type),
			ActionCode:   nilIfEmpty(a.ActionCode),
			SourceSystem: sourceSystem,
			RecordedVote: storedRecordedVote(a.RecordedVotes),
			SortOrder:    i + 1,
		})
	}
	return rows
}

// storedRecordedVote picks the one roll call bill_actions.recorded_vote keeps for an action: the
// last valid entry in Congress.gov's list, nil when there is none. A vote is valid with a
// positive roll number, a chamber of "House" or "Senate", and an absolute http(s) URL, since the
// web links it as is. An action listing two roll calls therefore links the later-listed one.
func storedRecordedVote(votes []congress.RecordedVote) *model.RecordedVote {
	for _, v := range slices.Backward(votes) {
		if v.RollNumber <= 0 || (v.Chamber != chamberHouse && v.Chamber != chamberSenate) || !webURL(v.URL) {
			continue
		}
		return &model.RecordedVote{RollNumber: v.RollNumber, URL: v.URL, Chamber: v.Chamber}
	}
	return nil
}

// webURL reports whether raw is an absolute http or https URL with a host.
func webURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}
