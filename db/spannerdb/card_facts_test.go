package spannerdb_test

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// The /vote card facts of two real laws, as Congress.gov records them, and of bills built to hit
// each rule.
const (
	cardS4530    = "s-119-4530"
	cardHR187    = "hr-119-187"
	cardStatus   = "hr-119-900" // became_law in its status history, no law action
	cardCurrent  = "hr-119-901" // became_law as its current status only
	cardNothing  = "hr-119-902" // a procedural roll call, a non-passage action and a citation
	cardPrivate  = "hr-119-903" // a private law
	cardTwoVotes = "hr-119-904" // passed the House, then the Senate, then the House concurred
	cardLawsOnly = "hr-119-905" // a private law in bills.laws, became_law in its history, no law action
	cardLawsWin  = "hr-119-906" // bills.laws and a law action that disagree on the number
	cardLawsBad  = "hr-119-907" // bills.laws holds no law the card knows, so the action names it
	cardUnknown  = "hr-119-999" // no bills row

	// s4530CRS00 and s4530CRS49 are S. 4530's CRS summaries as introduced and as enacted.
	s4530CRS00 = "This bill authorizes the Capitol Police Board to waive the mandatory retirement age for " +
		"members of the Capitol Police up to age 62. (Under current law, a member of the Capitol Police is " +
		"generally subject to mandatory retirement at age 57 but may receive a waiver from the board " +
		"authorizing later retirement up to age 60.)"
	s4530CRS49Lead = "This act authorizes the Capitol Police Board to increase the mandatory retirement age " +
		"for a member of the Capitol Police to up to age 62 when the board determines such waiver is in the " +
		"public interest."
	s4530CRS49 = s4530CRS49Lead + "\n\nUnder current law, members of the Capitol Police are generally subject " +
		"to mandatory retirement at (1) age 57; or (2) upon completing 20 years of service if the member is " +
		"older than 57. Previously, a waiver from the board would authorize a member to continue working until " +
		"age 60.\n\nThe act authorizes the board to issue a waiver specifying a retirement age between 57 and 62."
	// hr187CRSLead follows H.R. 187's short title in its CRS summary.
	hr187CRSLead = "This act directs the Forest Service and the Department of the Interior to standardize and " +
		"publish data relating to the public's access to federal waterways for recreational use."
	hr187CRS = "Modernizing Access to our Public Waters Act or the MAPWaters Act of 2025\n\n" + hr187CRSLead +
		"\n\n(Sec. 3) The Forest Service and Interior must jointly develop and adopt interagency standards."
)

func cardDay(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestGetCardFacts(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	bills := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	seedCardFacts(t, store, client)

	t.Run("no ids", func(t *testing.T) {
		for _, ids := range [][]string{nil, {}} {
			got, err := bills.GetCardFacts(ctx, ids)
			if err != nil || got == nil || len(got) != 0 {
				t.Errorf("GetCardFacts(%v) = %v, %v; want an empty map", ids, got, err)
			}
		}
	})

	t.Run("a page mixing bills with and without each fact", func(t *testing.T) {
		got, err := bills.GetCardFacts(ctx, []string{
			cardS4530, cardHR187, cardStatus, cardCurrent, cardNothing, cardPrivate, cardTwoVotes, cardUnknown,
			cardHR187, cardLawsOnly, cardLawsWin, cardLawsBad,
		})
		if err != nil {
			t.Fatalf("GetCardFacts: %v", err)
		}
		checkCardFacts(t, got, wantCardFacts())
	})

	t.Run("a bill with no facts", func(t *testing.T) {
		got, err := bills.GetCardFacts(ctx, []string{cardNothing})
		if err != nil || len(got) != 0 {
			t.Errorf("GetCardFacts(%s) = %v, %v; want an empty map", cardNothing, got, err)
		}
	})
}

// checkCardFacts compares bill by bill and prints a mismatch as JSON, the shape the API serves.
func checkCardFacts(t *testing.T, got, want map[string]model.BillCardFacts) {
	t.Helper()
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("%s: got facts %s, want none", id, cardJSON(t, got[id]))
		}
	}
	for id, w := range want {
		g, ok := got[id]
		if !ok || !reflect.DeepEqual(g, w) {
			t.Errorf("%s:\n got %s\nwant %s", id, cardJSON(t, g), cardJSON(t, w))
		}
	}
}

func cardJSON(t *testing.T, f model.BillCardFacts) string {
	t.Helper()
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantCardFacts() map[string]model.BillCardFacts {
	public, private := model.LawTypePublic, model.LawTypePrivate
	return map[string]model.BillCardFacts{
		cardS4530: {
			CRS: &model.CardCRS{
				VersionCode: "49", ActionDate: cardDay(2026, time.May, 29), ActionDesc: "Public Law",
				Lead: s4530CRS49Lead,
			},
			Passage: []model.PassageEntry{
				unrecorded("Senate", model.PassageUC, cardDay(2026, time.May, 14), "Unanimous Consent: passed"),
				unrecorded("House", model.PassageVoice, cardDay(2026, time.May, 19), "Voice Vote: agreed to"),
			},
			Enacted: &model.Enactment{
				Date:      cardDay(2026, time.May, 29),
				LawType:   &public,
				LawNumber: new("119-95"),
			},
			LawChangeCount: 2,
		},
		cardHR187: {
			CRS: &model.CardCRS{
				VersionCode: "49", ActionDate: cardDay(2025, time.December, 26), ActionDesc: "Public Law",
				Lead: hr187CRSLead,
			},
			Passage: []model.PassageEntry{
				rollCall(
					"House",
					19,
					cardDay(2025, time.January, 21),
					"On Motion to Suspend the Rules and Pass, as Amended",
					[4]int{413, 0, 0, 19},
				),
				unrecorded("Senate", model.PassageVoice, cardDay(2025, time.December, 16), "Voice Vote: passed"),
			},
			Enacted: &model.Enactment{
				Date: cardDay(2025, time.December, 26), LawType: &public, LawNumber: new("119-62"),
			},
		},
		cardStatus:  {Passage: []model.PassageEntry{}, Enacted: &model.Enactment{Date: cardDay(2026, time.March, 2)}},
		cardCurrent: {Passage: []model.PassageEntry{}, Enacted: &model.Enactment{Date: cardDay(2026, time.April, 6)}},
		cardPrivate: {
			Passage: []model.PassageEntry{},
			Enacted: &model.Enactment{Date: cardDay(2026, time.June, 1), LawType: &private, LawNumber: new("119-1")},
		},
		// bills.laws names the law; the date comes from the status history or the action.
		cardLawsOnly: {
			Passage: []model.PassageEntry{},
			Enacted: &model.Enactment{Date: cardDay(2026, time.July, 7), LawType: &private, LawNumber: new("119-3")},
		},
		cardLawsWin: {
			Passage: []model.PassageEntry{},
			Enacted: &model.Enactment{Date: cardDay(2026, time.July, 8), LawType: &public, LawNumber: new("119-120")},
		},
		cardLawsBad: {
			Passage: []model.PassageEntry{},
			Enacted: &model.Enactment{Date: cardDay(2026, time.July, 9), LawType: &public, LawNumber: new("119-7")},
		},
		cardTwoVotes: {
			Passage: []model.PassageEntry{
				rollCall("Senate", 50, cardDay(2025, time.April, 1), "On Passage of the Bill", [4]int{60, 40, 0, 0}),
				rollCall("House", 120, cardDay(2025, time.May, 1), "On Motion to Concur in the Senate Amendment",
					[4]int{300, 120, 1, 12}),
			},
		},
	}
}

func unrecorded(chamber, method string, date time.Time, question string) model.PassageEntry {
	return model.PassageEntry{Chamber: chamber, Method: method, Date: date, Question: &question, Result: new("Passed")}
}

// rollCall is a roll-call passage entry; tally is yeas, nays, present and not voting.
func rollCall(chamber string, roll int, date time.Time, question string, tally [4]int) model.PassageEntry {
	return model.PassageEntry{
		Chamber: chamber, Method: model.PassageRoll, Date: date, Question: &question, Result: new("Passed"),
		RollNumber: &roll, Yeas: &tally[0], Nays: &tally[1], Present: &tally[2], NotVoting: &tally[3],
	}
}

func seedCardFacts(t *testing.T, store *spannerdb.PipelineStoreImpl, client *spanner.Client) {
	t.Helper()
	seedCardS4530(t, store, client)
	seedCardHR187(t, store)

	for _, n := range []int{900, 901, 902, 903, 904} {
		cardBill(t, store, "hr", n)
	}
	cardStatusHistory(t, client, cardStatus, cardDay(2026, time.March, 2))
	if err := store.UpdateBillStatus(
		t.Context(),
		cardCurrent,
		"became_law",
		new(cardDay(2026, time.April, 6)),
	); err != nil {
		t.Fatalf("update status: %v", err)
	}
	cardActions(t, store, cardPrivate, "Became Private Law No: 119-1.", cardDay(2026, time.June, 1))
	seedCardLaws(t, store, client)

	// Nothing here is a fact: a procedural roll call, an unrecorded row that isn't a passage, an
	// action that names no law, a text that only cites a section, and a status short of law.
	cardVote(t, store, cardNothing, "house-119-roll401", "House", 401, cardDay(2025, time.February, 3),
		"On Motion to Recommit", [4]int{200, 210, 0, 22})
	testdb.SeedCongressionalVote(t.Context(), t, client, "house-119-roll-hr-119-902", new(cardNothing),
		testdb.FixtureCongress, "House", cardDay(2025, time.February, 4))
	cardActions(t, store, cardNothing, "Presented to President.", cardDay(2025, time.March, 1))
	ids := seedCardText(t, store, client, cardNothing)
	lawRefs(t, store, cardNothing, ids["enr"], ref(secFlag, model.LawRefCites))
	if err := store.UpdateBillStatus(
		t.Context(),
		cardNothing,
		"passed_house",
		new(cardDay(2025, time.March, 1)),
	); err != nil {
		t.Fatalf("update status: %v", err)
	}

	// The House's latest final vote replaces its first; the Senate's procedural vote is no passage.
	cardVote(t, store, cardTwoVotes, "house-119-roll100", "House", 100, cardDay(2025, time.March, 1),
		"On Passage", [4]int{250, 180, 0, 2})
	cardVote(t, store, cardTwoVotes, "senate-119-1-050", "Senate", 50, cardDay(2025, time.April, 1),
		"On Passage of the Bill", [4]int{60, 40, 0, 0})
	cardVote(t, store, cardTwoVotes, "senate-119-1-051", "Senate", 51, cardDay(2025, time.April, 2),
		"On the Motion to Table", [4]int{55, 45, 0, 0})
	cardVote(t, store, cardTwoVotes, "house-119-roll120", "House", 120, cardDay(2025, time.May, 1),
		"On Motion to Concur in the Senate Amendment", [4]int{300, 120, 1, 12})
}

// seedCardLaws stores bills whose laws column is set (#736): read on its own, over an action that
// names another number, and skipped when it names no public or private law.
func seedCardLaws(t *testing.T, store *spannerdb.PipelineStoreImpl, client *spanner.Client) {
	t.Helper()
	cardBill(t, store, "hr", 905, model.BillLaw{Type: model.BillLawTypePrivate, Number: "119-3"})
	cardStatusHistory(t, client, cardLawsOnly, cardDay(2026, time.July, 7))
	cardBill(t, store, "hr", 906, model.BillLaw{Type: model.BillLawTypePublic, Number: "119-120"})
	cardActions(t, store, cardLawsWin, "Became Public Law No: 119-12.", cardDay(2026, time.July, 8))
	cardBill(t, store, "hr", 907, model.BillLaw{Type: "Treaty", Number: "119-7"}, model.BillLaw{
		Type: model.BillLawTypePrivate,
	})
	cardActions(t, store, cardLawsBad, "Became Public Law No: 119-7.", cardDay(2026, time.July, 9))
}

// seedCardS4530 stores S. 4530: two CRS summaries, passage by unanimous consent and by voice
// vote, Public Law 119-95, and an enrolled text amending two sections (one of them twice) after
// an introduced text amending three.
func seedCardS4530(t *testing.T, store *spannerdb.PipelineStoreImpl, client *spanner.Client) {
	t.Helper()
	cardBill(t, store, "s", 4530, model.BillLaw{Type: model.BillLawTypePublic, Number: "119-95"})
	cardCRS(t, store, cardS4530, "00", "Introduced in Senate", cardDay(2026, time.May, 14), s4530CRS00)
	cardCRS(t, store, cardS4530, "49", "Public Law", cardDay(2026, time.May, 29), s4530CRS49)
	cardUnrecorded(t, store, cardS4530, "senate-119-uc-s-119-4530-20260514", "Senate",
		cardDay(2026, time.May, 14), "Unanimous Consent: passed")
	cardUnrecorded(t, store, cardS4530, "house-119-voice-s-119-4530-20260519", "House",
		cardDay(2026, time.May, 19), "Voice Vote: agreed to")
	cardActions(t, store, cardS4530, "Became Public Law No: 119-95.", cardDay(2026, time.May, 29))
	cardStatusHistory(t, client, cardS4530, cardDay(2026, time.May, 29))
	ids := seedCardText(t, store, client, cardS4530)
	lawRefs(t, store, cardS4530, ids["enr"],
		ref("/us/usc/t5/s8335", model.LawRefAmends), ref("/us/usc/t5/s8335", model.LawRefRepeals),
		ref("/us/usc/t5/s8425", model.LawRefAmends), ref(secFlag, model.LawRefCites))
	lawRefs(t, store, cardS4530, ids["is"],
		ref("/us/usc/t5/s8335", model.LawRefAmends), ref("/us/usc/t5/s8425", model.LawRefAmends),
		ref(secPhysicians, model.LawRefAmends))
}

// seedCardHR187 stores H.R. 187: a CRS summary opening with its short title, House roll call 19
// (413-0), a later procedural House vote, a Senate voice vote and Public Law 119-62.
func seedCardHR187(t *testing.T, store *spannerdb.PipelineStoreImpl) {
	t.Helper()
	cardBill(t, store, "hr", 187)
	cardCRS(t, store, cardHR187, "49", "Public Law", cardDay(2025, time.December, 26), hr187CRS)
	cardVote(t, store, cardHR187, "house-119-roll019", "House", 19, cardDay(2025, time.January, 21),
		"On Motion to Suspend the Rules and Pass, as Amended", [4]int{413, 0, 0, 19})
	cardVote(t, store, cardHR187, "house-119-roll030", "House", 30, cardDay(2025, time.January, 22),
		"On Motion to Recommit", [4]int{200, 213, 0, 19})
	cardUnrecorded(t, store, cardHR187, "senate-119-voice-hr-119-187-20251216", "Senate",
		cardDay(2025, time.December, 16), "Voice Vote: passed")
	cardActions(t, store, cardHR187, "Became Public Law No: 119-62.", cardDay(2025, time.December, 26))
}

// cardBill stores a 119th Congress bill and the laws it became (none writes a NULL laws column).
func cardBill(t *testing.T, store *spannerdb.PipelineStoreImpl, billType string, number int, laws ...model.BillLaw) {
	t.Helper()
	if err := store.UpsertBill(t.Context(), repository.BillRow{
		ID:       billType + "-119-" + strconv.Itoa(number),
		Congress: testdb.FixtureCongress,
		BillType: billType,
		Number:   number,
		Title:    "Card bill",
		Laws:     laws,
	}); err != nil {
		t.Fatalf("upsert bill: %v", err)
	}
}

func cardCRS(t *testing.T, store *spannerdb.PipelineStoreImpl, bill, code, desc string, date time.Time, text string) {
	t.Helper()
	if err := store.UpsertCRSSummaries(t.Context(), []repository.CRSSummaryRow{{
		BillID: bill, VersionCode: code, ActionDate: date, ActionDesc: desc, TextHTML: "<p>" + text + "</p>",
		Text: text, ContentHash: "hash-" + code, CRSUpdatedAt: date, SourceUpdatedAt: date,
	}}); err != nil {
		t.Fatalf("upsert crs summary: %v", err)
	}
}

func cardVote(
	t *testing.T, store *spannerdb.PipelineStoreImpl, bill, id, chamber string, roll int, date time.Time,
	question string, tally [4]int,
) {
	t.Helper()
	if err := store.UpsertCongressionalVote(t.Context(), repository.CongressionalVoteRow{
		ID: id, BillID: &bill, Congress: testdb.FixtureCongress, Chamber: chamber, Session: new(1), RollNumber: &roll,
		VoteDate: date, Question: &question, Result: new("Passed"),
		Yeas: &tally[0], Nays: &tally[1], Present: &tally[2], NotVoting: &tally[3],
	}); err != nil {
		t.Fatalf("upsert vote %s: %v", id, err)
	}
}

// cardUnrecorded stores a passage by voice vote or unanimous consent, as the pipeline's
// voicevotes.go does: no roll number and no tallies.
func cardUnrecorded(
	t *testing.T,
	store *spannerdb.PipelineStoreImpl,
	bill, id, chamber string,
	date time.Time,
	q string,
) {
	t.Helper()
	if err := store.UpsertCongressionalVote(t.Context(), repository.CongressionalVoteRow{
		ID: id, BillID: &bill, Congress: testdb.FixtureCongress, Chamber: chamber, VoteDate: date,
		Question: &q, Result: new("Passed"),
	}); err != nil {
		t.Fatalf("upsert vote %s: %v", id, err)
	}
}

// cardActions stores an introduction and the given action after it.
func cardActions(t *testing.T, store *spannerdb.PipelineStoreImpl, bill, text string, date time.Time) {
	t.Helper()
	if err := store.ReplaceBillActions(t.Context(), bill, []repository.BillActionRow{
		{ActionDate: cardDay(2025, time.January, 3), ActionText: "Introduced in House", SortOrder: 0},
		{ActionDate: date, ActionText: text, SortOrder: 1},
	}); err != nil {
		t.Fatalf("replace actions: %v", err)
	}
}

// cardStatusHistory writes a became_law status history row directly, so the test doesn't
// depend on how the pipeline replaces a bill's history.
func cardStatusHistory(t *testing.T, client *spanner.Client, bill string, date time.Time) {
	t.Helper()
	if _, err := client.Apply(t.Context(), []*spanner.Mutation{spanner.InsertOrUpdate("bill_status_history",
		[]string{"bill_id", "status", "status_date", "status_rank"},
		[]any{bill, "became_law", civil.DateOf(date), int64(10)})}); err != nil {
		t.Fatalf("status history: %v", err)
	}
}

// seedCardText stores an introduced and an enrolled text version, both with text; enrolled is
// the latest.
func seedCardText(
	t *testing.T,
	store *spannerdb.PipelineStoreImpl,
	client *spanner.Client,
	bill string,
) map[string]string {
	t.Helper()
	upsertVersions(
		t,
		store,
		bill,
		textVersion(bill, "Enrolled Bill", "enr", 2),
		textVersion(bill, "Introduced", "is", 1),
	)
	ids := versionIDs(t, client, bill)
	for _, code := range []string{"enr", "is"} {
		if err := store.InsertBillText(t.Context(), repository.BillTextRow{
			TextVersionID: ids[code], Format: "xml", Content: "<bill>" + code + "</bill>",
			ContentHash: "hash-" + code, FetchedAt: summaryNow(),
		}); err != nil {
			t.Fatalf("insert text: %v", err)
		}
	}
	return ids
}
