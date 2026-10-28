package model_test

import (
	"testing"

	"github.com/justabill-org/justabill/db/model"
)

// roll is a passage roll call in chamber with the given yeas and nays.
func roll(chamber string, yeas, nays int) model.PassageEntry {
	return model.PassageEntry{Chamber: chamber, Method: model.PassageRoll, Yeas: &yeas, Nays: &nays}
}

func TestPassageRank(t *testing.T) {
	voice := model.PassageEntry{Chamber: "House", Method: model.PassageVoice}
	uc := model.PassageEntry{Chamber: "Senate", Method: model.PassageUC}
	noTally := model.PassageEntry{Chamber: "House", Method: model.PassageRoll}
	tests := []struct {
		name    string
		entries []model.PassageEntry
		want    int
		ok      bool
	}{
		{name: "no passage vote", entries: nil},
		{name: "empty passage", entries: []model.PassageEntry{}},
		{name: "voice vote", entries: []model.PassageEntry{voice}, want: model.PassageRankVoice, ok: true},
		{name: "unanimous consent", entries: []model.PassageEntry{uc}, want: model.PassageRankVoice, ok: true},
		{
			name:    "voice and unanimous consent",
			entries: []model.PassageEntry{voice, uc},
			want:    model.PassageRankVoice,
			ok:      true,
		},
		// 420–3: 3 nays of 423 is under 10%.
		{
			name:    "lopsided roll call",
			entries: []model.PassageEntry{roll("House", 420, 3)},
			want:    model.PassageRankRoll,
			ok:      true,
		},
		// 9 nays of 90 is exactly 10%.
		{
			name:    "exactly ten percent",
			entries: []model.PassageEntry{roll("Senate", 81, 9)},
			want:    model.PassageRankContested,
			ok:      true,
		},
		// 9 nays of 91 is just under 10%.
		{
			name:    "just under ten percent",
			entries: []model.PassageEntry{roll("Senate", 82, 9)},
			want:    model.PassageRankRoll,
			ok:      true,
		},
		{
			name:    "close vote",
			entries: []model.PassageEntry{roll("House", 216, 214)},
			want:    model.PassageRankContested,
			ok:      true,
		},
		{
			name:    "failed by nays",
			entries: []model.PassageEntry{roll("House", 10, 400)},
			want:    model.PassageRankContested,
			ok:      true,
		},
		{
			name:    "no yeas or nays",
			entries: []model.PassageEntry{roll("House", 0, 0)},
			want:    model.PassageRankRoll,
			ok:      true,
		},
		{
			name:    "roll call without totals",
			entries: []model.PassageEntry{noTally},
			want:    model.PassageRankRoll,
			ok:      true,
		},
		{
			name:    "contested in the other chamber",
			entries: []model.PassageEntry{roll("House", 420, 3), roll("Senate", 52, 48)},
			want:    model.PassageRankContested, ok: true,
		},
		{
			name:    "roll call beats voice vote",
			entries: []model.PassageEntry{voice, roll("Senate", 99, 0)},
			want:    model.PassageRankRoll, ok: true,
		},
		{
			name:    "contested beats voice vote",
			entries: []model.PassageEntry{roll("House", 300, 120), uc},
			want:    model.PassageRankContested, ok: true,
		},
		{name: "unknown method", entries: []model.PassageEntry{{Chamber: "House", Method: "other"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := model.PassageRank(tc.entries)
			if got != tc.want || ok != tc.ok {
				t.Errorf("PassageRank() = %d, %v; want %d, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
