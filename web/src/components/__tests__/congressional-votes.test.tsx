import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { CongressionalVotes } from "../bill/congressional-votes";
import type { CongressionalVote } from "@/lib/types";

// Rows as the pipeline writes them for H.R. 1276 (119th): a House voice vote and a Senate
// unanimous-consent passage, neither with a roll number, session or counts.
const voice: CongressionalVote = {
  id: "house-119-voice-hr-119-1276-20251209",
  bill_id: "hr-119-1276",
  congress: 119,
  chamber: "House",
  vote_date: "2025-12-09T00:00:00Z",
  question:
    "Voice Vote: Passed/agreed to in House: On motion to suspend the rules and pass the bill, as amended " +
    "Agreed to by voice vote. (text: CR H5073)",
  result: "Passed",
};
const uc: CongressionalVote = {
  id: "senate-119-uc-hr-119-1276-20260807",
  bill_id: "hr-119-1276",
  congress: 119,
  chamber: "Senate",
  vote_date: "2026-08-07T00:00:00Z",
  question: "Unanimous Consent: Passed/agreed to in Senate: Passed Senate without amendment by Unanimous Consent.",
  result: "Passed",
};
const rollCall: CongressionalVote = {
  id: "house-119-1-roll019",
  bill_id: "hr-119-187",
  congress: 119,
  chamber: "House",
  session: 1,
  roll_number: 19,
  vote_date: "2025-01-21T00:00:00Z",
  question: "On Motion to Suspend the Rules and Pass, as Amended",
  result: "Passed",
  yeas: 413,
  nays: 0,
  present: 0,
  not_voting: 19,
};

const noRecord = "No individual votes were recorded, so this vote isn&#x27;t part of anyone&#x27;s scorecard.";

function render(votes: CongressionalVote[]): string {
  return renderToStaticMarkup(<CongressionalVotes votes={votes} />);
}

describe("CongressionalVotes", () => {
  it("labels a voice vote as unrecorded, with its floor action", () => {
    const html = render([voice]);
    expect(html).toContain("Passed by voice vote");
    expect(html).toContain("House Vote<span");
    expect(html).toContain(noRecord);
    expect(html).toContain("Floor action: Passed/agreed to in House: On motion to suspend the rules");
    expect(html).not.toContain("Floor action: Voice Vote:");
    expect(html).not.toContain("in favor");
    expect(html).not.toContain("Roll #");
    expect(html).not.toContain("Yeas");
  });

  it("labels a unanimous-consent passage as unrecorded", () => {
    const html = render([uc]);
    expect(html).toContain("Passed by unanimous consent");
    expect(html).toContain("Unanimous Consent</span>");
    expect(html).toContain("The Senate passed this measure by unanimous consent");
    expect(html).toContain(noRecord);
    expect(html).not.toContain("voice vote");
  });

  it("treats any vote without a roll number as unrecorded, including old per-day voice IDs", () => {
    const oldVoice = { ...voice, id: "house-119-voice-20251209", question: undefined };
    expect(render([oldVoice])).toContain("Passed by voice vote");

    const unknown = { ...voice, id: "senate-119-other-s-119-1-20260101", question: undefined };
    const html = render([unknown]);
    expect(html).toContain("Passed without a recorded vote");
    expect(html).toContain(noRecord);
    expect(html).not.toContain("Floor action");
  });

  it("renders a roll call with its number and counts", () => {
    const html = render([rollCall]);
    expect(html).toContain("Roll #19");
    expect(html).toContain("On Motion to Suspend the Rules and Pass, as Amended");
    expect(html).toContain("413");
    expect(html).toContain("Yeas");
    expect(html).toContain("Not Voting");
    expect(html).not.toContain(noRecord);
    expect(html).not.toContain("Passed by");
  });

  it("says so when there are no votes", () => {
    expect(render([])).toContain("No congressional votes recorded yet.");
  });
});
