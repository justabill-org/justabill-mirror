import { describe, expect, it } from "vitest";
import {
  absoluteShareUrl,
  aggregateShareText,
  billLabel,
  billLinkPath,
  billLinkText,
  billLinkUrl,
  billShareText,
  intentLinks,
  isAbortError,
  repShareText,
  shareFileName,
  shareSupport,
} from "../share-links";

describe("share text", () => {
  it("says the scorecard result with the same rounding as score()", () => {
    expect(repShareText("Hana Hill", { matching: 8, compared: 12 })).toBe(
      "I agree with Hana Hill on 8 of 12 bills (67%)."
    );
    expect(repShareText("Hana Hill", { matching: 1, compared: 8 })).toContain("(13%)");
  });

  it("says the sharer's vote on a bill", () => {
    expect(billShareText("hr-119-1", "yea")).toBe("I'd vote Yea on H.R. 1.");
    expect(billShareText("sjres-118-42", "nay")).toBe("I'd vote Nay on S.J.Res. 42.");
    expect(billLabel("not-a-bill")).toBe("not-a-bill");
  });

  it("says an aggregate cell's numbers with the not-a-poll label (#166)", () => {
    expect(aggregateShareText("hr-119-1", { scope_key: "CA-12", yea_pct: 62, nay_pct: 38 })).toBe(
      "Just a Bill users in CA-12: 62% Yea, 38% Nay on H.R. 1 (Just a Bill users, not a poll)."
    );
    expect(aggregateShareText("hr-119-1", { scope_key: "", yea_pct: 50, nay_pct: 50 })).toMatch(
      /^Just a Bill users nationwide:/
    );
  });
});

describe("absoluteShareUrl", () => {
  it("puts the path on the page's origin", () => {
    expect(absoluteShareUrl("/share/rep/X000001/8-of-12", "https://justabill.io")).toBe(
      "https://justabill.io/share/rep/X000001/8-of-12"
    );
  });
});

describe("intentLinks", () => {
  const url = "https://justabill.io/share/bill/hr-119-1/yea";
  const text = "I'd vote Yea on H.R. 1 & more?";
  const links = Object.fromEntries(intentLinks(url, text).map((l) => [l.channel, l.href]));

  it("offers X, Facebook, Bluesky, Threads, Reddit, WhatsApp, LinkedIn and email, in that order", () => {
    expect(intentLinks(url, text).map((l) => l.label)).toEqual([
      "X",
      "Facebook",
      "Bluesky",
      "Threads",
      "Reddit",
      "WhatsApp",
      "LinkedIn",
      "Email",
    ]);
  });

  it("encodes the text and the link", () => {
    const x = new URL(links.x);
    expect(x.origin + x.pathname).toBe("https://x.com/intent/post");
    expect(x.searchParams.get("text")).toBe(text);
    expect(x.searchParams.get("url")).toBe(url);
    expect(links.x).not.toContain("& more");

    expect(new URL(links.facebook).searchParams.get("u")).toBe(url);
    expect(new URL(links.bluesky).searchParams.get("text")).toBe(`${text} ${url}`);

    const mail = new URL(links.email);
    expect(mail.protocol).toBe("mailto:");
    expect(mail.searchParams.get("subject")).toBe("Just a Bill");
    expect(mail.searchParams.get("body")).toBe(`${text}\n\n${url}`);
  });

  it("sends the four newer sites to their own share pages (#810)", () => {
    const threads = new URL(links.threads);
    expect(threads.origin + threads.pathname).toBe("https://www.threads.com/intent/post");
    expect(threads.searchParams.get("text")).toBe(text);
    expect(threads.searchParams.get("url")).toBe(url);

    const reddit = new URL(links.reddit);
    expect(reddit.origin + reddit.pathname).toBe("https://www.reddit.com/submit");
    expect(reddit.searchParams.get("url")).toBe(url);
    expect(reddit.searchParams.get("title")).toBe(text);

    const whatsapp = new URL(links.whatsapp);
    expect(whatsapp.origin + whatsapp.pathname).toBe("https://wa.me/");
    expect(whatsapp.searchParams.get("text")).toBe(`${text} ${url}`);

    const linkedin = new URL(links.linkedin);
    expect(linkedin.origin + linkedin.pathname).toBe("https://www.linkedin.com/sharing/share-offsite/");
    expect(linkedin.searchParams.get("url")).toBe(url);
  });

  it("round-trips a title with &, #, %, +, quotes and accents through every link (#810)", () => {
    const tricky = `H.R. 7: "Señor's" Café & Roads #2 Act, 100% + more`;
    const link = "https://justabill.io/bills/hr-119-7";
    const sent = Object.fromEntries(intentLinks(link, tricky).map((l) => [l.channel, new URL(l.href)]));
    expect(sent.x.searchParams.get("text")).toBe(tricky);
    expect(sent.threads.searchParams.get("text")).toBe(tricky);
    expect(sent.reddit.searchParams.get("title")).toBe(tricky);
    expect(sent.bluesky.searchParams.get("text")).toBe(`${tricky} ${link}`);
    expect(sent.whatsapp.searchParams.get("text")).toBe(`${tricky} ${link}`);
    expect(sent.email.searchParams.get("body")).toBe(`${tricky}\n\n${link}`);
    for (const href of Object.values(sent)) {
      // Nothing outside the query: an unencoded # would start a fragment and drop the rest.
      expect(href.hash).toBe("");
    }
  });
});

describe("a bill's own link (#810)", () => {
  it("is the bill's page, with nothing from the page it was shared from", () => {
    expect(billLinkPath("hr-119-1")).toBe("/bills/hr-119-1");
    expect(billLinkUrl("hr-119-1", "https://preview.example", undefined)).toBe("https://preview.example/bills/hr-119-1");
  });

  it("uses the site's canonical origin when it's set, as og:url does", () => {
    const site = new URL("https://justabill.io");
    expect(billLinkUrl("sjres-118-42", "https://justabill-git-x.vercel.app", site)).toBe(
      "https://justabill.io/bills/sjres-118-42"
    );
  });

  it("says the bill's number and title", () => {
    expect(billLinkText("hr-119-1", "One Big Bill")).toBe("H.R. 1: One Big Bill");
    expect(billLinkText("sjres-118-42", "")).toBe("S.J.Res. 42");
  });

  it("cuts a long title at a word boundary so text and link fit one post on X", () => {
    const title = "To amend title 42 of the United States Code ".repeat(14).trim();
    const out = billLinkText("hr-119-1", title);
    expect(out.length).toBeLessThanOrEqual(200);
    expect(out.startsWith("H.R. 1: To amend title 42")).toBe(true);
    expect(out.endsWith("…")).toBe(true);
    // The word before the ellipsis is whole.
    const lastWord = out.slice(0, -1).split(" ").pop();
    expect(["To", "amend", "title", "42", "of", "the", "United", "States", "Code"]).toContain(lastWord);
    // X counts any link as 23 characters, plus a space between text and link.
    expect(out.length + 1 + 23).toBeLessThanOrEqual(280);
  });
});

describe("shareSupport", () => {
  const file = new File(["png"], "card.png", { type: "image/png" });
  const share = async () => undefined;

  it("is none without navigator.share", () => {
    expect(shareSupport(undefined, file)).toBe("none");
    expect(shareSupport({}, file)).toBe("none");
  });

  it("shares the file when canShare allows it", () => {
    expect(shareSupport({ share, canShare: () => true }, file)).toBe("files");
  });

  it("falls back to the link", () => {
    expect(shareSupport({ share, canShare: () => false }, file)).toBe("url");
    expect(shareSupport({ share }, file)).toBe("url");
    expect(shareSupport({ share, canShare: () => true }, null)).toBe("url");
    expect(
      shareSupport(
        {
          share,
          canShare: () => {
            throw new TypeError("unsupported");
          },
        },
        file
      )
    ).toBe("url");
  });
});

describe("shareFileName", () => {
  it("names the file after the card", () => {
    expect(shareFileName("/share/rep/X000001/8-of-12")).toBe("just-a-bill-rep-X000001-8-of-12.png");
    expect(shareFileName("/share/bill/hr-119-1/yea/X000002")).toBe("just-a-bill-bill-hr-119-1-yea-X000002.png");
  });
});

describe("isAbortError", () => {
  it("is true only for a cancelled share", () => {
    expect(isAbortError(new DOMException("cancelled", "AbortError"))).toBe(true);
    expect(isAbortError(new DOMException("denied", "NotAllowedError"))).toBe(false);
    expect(isAbortError("AbortError")).toBe(false);
  });
});
