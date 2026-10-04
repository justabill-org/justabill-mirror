import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import path from "node:path";
import {
  abstractPreview,
  CRA_PROMPT_RULE,
  docketUrl,
  documentKind,
  showsDisapprovedRule,
} from "../disapproved-rule";
import type { DisapprovedRule } from "../types";

// #643: the helpers behind the bill page's card for the rule a CRA resolution disapproves.

const rule: DisapprovedRule = {
  status: "unmatched",
  reason: "no_candidates",
  rule_title: "Some Rule",
  rule_agency: "Some Agency",
  cited: null,
  gao_opinion: false,
  document: null,
  withdrawn_document: null,
  search_url: "https://www.federalregister.gov/documents/search?conditions%5Bterm%5D=%22Some+Rule%22",
  checked_at: "2026-10-04T06:00:00Z",
};

describe("showsDisapprovedRule", () => {
  it("shows the card on a joint resolution the API sent a rule for", () => {
    expect(showsDisapprovedRule("sjres", rule)).toBe(true);
    expect(showsDisapprovedRule("hjres", rule)).toBe(true);
  });

  it("never shows it on another kind of bill, or without a rule", () => {
    expect(showsDisapprovedRule("hr", rule)).toBe(false);
    expect(showsDisapprovedRule("sconres", rule)).toBe(false);
    expect(showsDisapprovedRule("sjres", null)).toBe(false);
    expect(showsDisapprovedRule("sjres", undefined)).toBe(false);
  });
});

describe("documentKind", () => {
  it.each([
    ["Final rule; official interpretation.", "Rule", "Final rule"],
    ["Interim final rule; request for comments.", "Rule", "Interim final rule"],
    ["Notice of guidance.", "Notice", "Notice of guidance"],
    ["withdrawal of guidance", "Notice", "Withdrawal of guidance"],
    [null, "Rule", "Rule"],
    ["  ; ", "Notice", "Notice"],
  ])("reads %j (type %s) as %s", (action, type, want) => {
    expect(documentKind({ action, type })).toBe(want);
  });
});

describe("docketUrl", () => {
  it("links a Regulations.gov docket", () => {
    expect(docketUrl("CFPB-2024-0002")).toBe("https://www.regulations.gov/docket/CFPB-2024-0002");
    expect(docketUrl("ED-2024-OPE-0069")).toBe("https://www.regulations.gov/docket/ED-2024-OPE-0069");
  });

  it("links nothing for no docket or an ID that isn't shaped like one", () => {
    for (const id of [null, "", "Docket No. CFPB-2024-0002", "../../evil", "CFPB 2024 0002", "CFPB"]) {
      expect(docketUrl(id)).toBeUndefined();
    }
  });
});

describe("abstractPreview", () => {
  it("shows a short abstract whole", () => {
    expect(abstractPreview("  The agency amends a rule.  ")).toEqual({ preview: "The agency amends a rule.", truncated: false });
  });

  it("cuts a long one at the last space before the limit, with an ellipsis", () => {
    expect(abstractPreview("one two, three four", 12)).toEqual({ preview: "one two…", truncated: true });
    expect(abstractPreview("one two three", 13)).toEqual({ preview: "one two three", truncated: false });
  });

  it("cuts a long word with no space at the limit", () => {
    expect(abstractPreview("abcdefghij", 4)).toEqual({ preview: "abcd…", truncated: true });
  });
});

/** systemInstructionBill as the model gets it: prompt.go's raw-string pieces joined. */
function billInstruction(): string {
  const root = path.resolve(__dirname, "../../../..");
  const src = readFileSync(path.join(root, "pipeline/internal/ai/prompt.go"), "utf8");
  const start = src.indexOf("const systemInstructionBill = `");
  if (start < 0) throw new Error("systemInstructionBill not found in prompt.go");
  const body = src.slice(start + "const systemInstructionBill = `".length);
  // Pieces are joined as `...` +\n\t`...`; the constant ends at the first backtick not followed by " +".
  return body.replace(/`\s*\+\s*`/g, "").split("`")[0];
}

describe("CRA_PROMPT_RULE", () => {
  it("quotes the summary prompt's rule for CRA resolutions word for word", () => {
    const rules = billInstruction().split("\n- ");
    const cra = rules.find((r) => r.startsWith("The bill may be a Congressional Review Act"));
    expect(cra?.split("\n\n")[0]).toBe(CRA_PROMPT_RULE);
  });
});
