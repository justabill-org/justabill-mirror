import { describe, it, expect } from "vitest";
import { billLabel, billMetadata, truncate, unavailableBillMetadata } from "../bill-metadata";
import { billLinkPath } from "../share-links";
import type { Bill, BillDetailResponse } from "../types";

function detail(bill: Partial<Bill>, shortSummary?: string): BillDetailResponse {
  return {
    bill: { id: "hr-119-1", congress: 119, bill_type: "hr", number: 1, title: "One Big Bill", ...bill },
    summary: shortSummary ? { bill_id: "hr-119-1", short_summary: shortSummary } : null,
    actions: [],
    text_versions: [],
    diffs: [],
    amendments: [],
    votes: [],
    status_history: [],
    gao_reports: [],
  };
}

describe("billLabel", () => {
  it("uses the display label, or the upper-cased type when unknown", () => {
    expect(billLabel("hjres", 12)).toBe("H.J.Res. 12");
    expect(billLabel("xyz", 3)).toBe("XYZ 3");
  });
});

describe("truncate", () => {
  it("leaves short text alone and collapses whitespace", () => {
    expect(truncate("  a   b\nc ", 10)).toBe("a b c");
  });

  it("cuts at a word boundary with an ellipsis", () => {
    const out = truncate("The quick brown fox jumps over the lazy dog", 20);
    expect(out).toBe("The quick brown fox…");
    expect(out.length).toBeLessThanOrEqual(20);
  });

  it("cuts mid-word when there's no late space", () => {
    expect(truncate("Supercalifragilistic", 10)).toBe("Supercali…");
  });
});

describe("billMetadata", () => {
  it("titles the page with the bill and describes it with the summary", () => {
    const md = billMetadata(detail({}, "Cuts taxes and funds roads."));
    expect(md.title).toBe("H.R. 1: One Big Bill | Just a Bill");
    expect(md.description).toBe("Cuts taxes and funds roads.");
    expect(md.alternates?.canonical).toBe("/bills/hr-119-1");
    expect(md.openGraph).toMatchObject({
      title: "H.R. 1: One Big Bill",
      description: "Cuts taxes and funds roads.",
      url: "/bills/hr-119-1",
      type: "article",
    });
  });

  it("falls back to the latest action, then a generic line", () => {
    const withAction = billMetadata(detail({ latest_action: { actionDate: "2026-01-01", text: "Referred." } }));
    expect(withAction.description).toBe("Latest action: Referred.");
    const bare = billMetadata(detail({ bill_type: "s", number: 5, id: "s-119-5" }));
    expect(bare.description).toBe("S. 5 in the 119th Congress. Read it in plain language and vote on it.");
  });

  it("keeps long titles and summaries to search-result lengths", () => {
    const md = billMetadata(detail({ title: "word ".repeat(60) }, "long ".repeat(100)));
    expect(String(md.title).length).toBeLessThanOrEqual(90 + " | Just a Bill".length);
    expect(String(md.description).length).toBeLessThanOrEqual(160);
  });

  // A shared bill link (#810) previews with these tags: og:url is the same page the share sheet links
  // to, and leaving openGraph.images and twitter.images unset lets the segment's opengraph-image.tsx
  // and twitter-image.tsx fill og:image and twitter:image (seo-routes.test.tsx renders those).
  it("previews a shared bill link with its own page and the segment's images", () => {
    const md = billMetadata(detail({ id: "sjres-118-42", bill_type: "sjres", congress: 118, number: 42 }, "Ends a rule."));
    expect(md.openGraph?.url).toBe("/bills/sjres-118-42");
    expect(md.openGraph?.url).toBe(billLinkPath("sjres-118-42"));
    expect(md.openGraph).toMatchObject({ title: "S.J.Res. 42: One Big Bill", description: "Ends a rule." });
    expect(md.openGraph).not.toHaveProperty("images");
    expect(md.twitter).not.toHaveProperty("images");
    expect(md.twitter).toMatchObject({ card: "summary_large_image" });
  });

  it("keeps an unreadable bill out of search results", () => {
    expect(unavailableBillMetadata.robots).toEqual({ index: false, follow: true });
  });
});
