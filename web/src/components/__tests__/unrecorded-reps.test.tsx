// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { cleanup, render, screen, within } from "@testing-library/react";
import { axeViolations } from "@/test/axe";
import type { LocalRep } from "@/lib/local/reps";
import type { BillSponsorship, CongressionalVote } from "@/lib/types";

// #1002: the bill page's voice-vote and unanimous-consent card lists what each of the visitor's
// saved representatives in the voting chamber demonstrably did about the bill: sponsored it,
// cosponsored it, or nothing on record. The reps come from this browser's store (jab.reps.v1).

const { localReps } = await import("@/lib/votes/hooks");
const { CongressionalVotes } = await import("../bill/congressional-votes");

const houseVoice: CongressionalVote = {
  id: "house-119-voice-hr-119-1276-20251209",
  bill_id: "hr-119-1276",
  congress: 119,
  chamber: "House",
  vote_date: "2025-12-09T00:00:00Z",
  question: "Voice Vote: Passed/agreed to in House: On motion to suspend the rules and pass the bill, as amended",
  result: "Passed",
};
const senateUC: CongressionalVote = {
  id: "senate-119-uc-hr-119-1276-20260807",
  bill_id: "hr-119-1276",
  congress: 119,
  chamber: "Senate",
  vote_date: "2026-08-07T00:00:00Z",
  question: "Unanimous Consent: Passed/agreed to in Senate: Passed Senate without amendment by Unanimous Consent.",
  result: "Passed",
};
const houseRoll: CongressionalVote = {
  id: "house-119-1-roll019",
  bill_id: "hr-119-1276",
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

const jane: LocalRep = { id: "D000001", name: "Jane Doe", party: "D", chamber: "House", state: "CO", district: 3 };
const john: LocalRep = { id: "R000001", name: "John Roe", party: "R", chamber: "Senate", state: "CO" };
const ann: LocalRep = { id: "P000001", name: "Ann Poe", party: "D", chamber: "Senate", state: "CO" };

function sponsorship(bioguideId: string, role: BillSponsorship["role"], date?: string): BillSponsorship {
  return {
    bioguide_id: bioguideId,
    first_name: "",
    last_name: "",
    role,
    sponsored_date: date,
    is_original: role === "cosponsor",
  };
}

function saveReps(members: LocalRep[]) {
  localReps().saveReps({ state: "CO", district: 3, looked_up_at: "2026-10-01T00:00:00Z", members });
}

/** The lines under "Your representatives", or null when the card has no such section. */
function repLines(): string[] | null {
  const heading = screen.queryByRole("heading", { name: "Your representatives" });
  if (!heading) return null;
  const list = heading.parentElement?.querySelector("ul");
  if (!list) throw new Error("no list under the heading");
  return within(list)
    .getAllByRole("listitem")
    .map((li) => li.textContent);
}

const NO_POSITIONS = "A voice vote or unanimous consent records no individual positions.";

beforeEach(() => {
  localReps().clearReps();
});

afterEach(() => {
  cleanup();
  localReps().clearReps();
});

describe("Your representatives on an unrecorded passage", () => {
  it("says a House rep cosponsored the bill, with the date, and leaves out the lead-in", () => {
    saveReps([jane, john]);
    render(<CongressionalVotes votes={[houseVoice]} sponsorships={[sponsorship("D000001", "cosponsor", "2025-03-18")]} />);
    expect(repLines()).toEqual(["Rep. Jane Doe cosponsored this bill (Mar 18, 2025)"]);
    expect(document.body.textContent).not.toContain(NO_POSITIONS);
  });

  it("leaves the date out of a cosponsorship that has none", () => {
    saveReps([jane]);
    render(<CongressionalVotes votes={[houseVoice]} sponsorships={[sponsorship("D000001", "cosponsor")]} />);
    expect(repLines()).toEqual(["Rep. Jane Doe cosponsored this bill"]);
  });

  it("says a rep sponsored the bill, without a date", () => {
    saveReps([jane]);
    render(<CongressionalVotes votes={[houseVoice]} sponsorships={[sponsorship("D000001", "sponsor", "2025-02-11")]} />);
    expect(repLines()).toEqual(["Rep. Jane Doe sponsored this bill"]);
    expect(document.body.textContent).not.toContain(NO_POSITIONS);
  });

  it("says no recorded position for a rep who didn't sponsor it, and adds the lead-in", () => {
    saveReps([jane]);
    render(<CongressionalVotes votes={[houseVoice]} sponsorships={[sponsorship("X000009", "sponsor")]} />);
    expect(repLines()).toEqual(["Rep. Jane Doe: no recorded position"]);
    expect(screen.getByText(NO_POSITIONS)).toBeTruthy();
  });

  it("lists only the senators on a Senate unanimous-consent passage", () => {
    saveReps([jane, john, ann]);
    render(
      <CongressionalVotes
        votes={[senateUC]}
        sponsorships={[sponsorship("D000001", "sponsor"), sponsorship("P000001", "cosponsor", "2025-05-06")]}
      />,
    );
    expect(repLines()).toEqual(["Sen. John Roe: no recorded position", "Sen. Ann Poe cosponsored this bill (May 6, 2025)"]);
    // One of them cosponsored it, so the lead-in isn't added.
    expect(document.body.textContent).not.toContain(NO_POSITIONS);
  });

  it("titles a delegate Del.", () => {
    saveReps([{ id: "N000147", name: "Eleanor Holmes Norton", party: "D", chamber: "House", state: "DC", district: 0 }]);
    render(<CongressionalVotes votes={[houseVoice]} sponsorships={[]} />);
    expect(repLines()).toEqual(["Del. Eleanor Holmes Norton: no recorded position"]);
  });

  it("adds nothing when every saved rep is in the other chamber", () => {
    saveReps([jane]);
    render(<CongressionalVotes votes={[senateUC]} sponsorships={[sponsorship("D000001", "sponsor")]} />);
    expect(repLines()).toBeNull();
    expect(document.body.textContent).not.toContain(NO_POSITIONS);
  });

  it("adds nothing when no reps are saved", () => {
    render(<CongressionalVotes votes={[houseVoice]} sponsorships={[sponsorship("D000001", "sponsor")]} />);
    expect(repLines()).toBeNull();
    expect(document.body.textContent).not.toContain(NO_POSITIONS);
  });

  it("adds nothing to a roll call", () => {
    saveReps([jane]);
    render(<CongressionalVotes votes={[houseRoll]} sponsorships={[sponsorship("D000001", "sponsor")]} />);
    expect(repLines()).toBeNull();
  });

  it("adds nothing to a passage whose method isn't a voice vote or unanimous consent", () => {
    saveReps([jane]);
    const unknown = { ...houseVoice, id: "house-119-other-hr-119-1276-20251209", question: undefined };
    render(<CongressionalVotes votes={[unknown]} sponsorships={[sponsorship("D000001", "sponsor")]} />);
    expect(screen.getByText("Passed without a recorded vote")).toBeTruthy();
    expect(repLines()).toBeNull();
  });

  it("says no recorded position for every rep when the sponsorships didn't load", () => {
    saveReps([john, ann]);
    render(<CongressionalVotes votes={[senateUC]} sponsorships={null} />);
    expect(repLines()).toEqual(["Sen. John Roe: no recorded position", "Sen. Ann Poe: no recorded position"]);
    expect(screen.getByText(NO_POSITIONS)).toBeTruthy();
  });

  it("renders the same card on the server whether or not reps are saved", () => {
    const sponsorships = [sponsorship("D000001", "cosponsor", "2025-03-18")];
    const without = renderToStaticMarkup(<CongressionalVotes votes={[houseVoice]} sponsorships={sponsorships} />);
    saveReps([jane]);
    const withReps = renderToStaticMarkup(<CongressionalVotes votes={[houseVoice]} sponsorships={sponsorships} />);
    expect(withReps).toBe(without);
    expect(withReps).not.toContain("Your representatives");
    expect(withReps).toContain("Passed by voice vote");
  });

  it("lists the reps under a heading with no axe violations", async () => {
    saveReps([john, ann]);
    const { container } = render(
      <CongressionalVotes votes={[senateUC]} sponsorships={[sponsorship("P000001", "sponsor")]} />,
    );
    const heading = screen.getByRole("heading", { name: "Your representatives" });
    expect(heading.tagName).toBe("H4");
    expect(heading.parentElement?.querySelector("ul")).not.toBeNull();
    expect(await axeViolations(container)).toEqual([]);
  });
});
