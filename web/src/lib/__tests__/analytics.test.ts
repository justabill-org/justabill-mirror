import { describe, it, expect, vi } from "vitest";
import { analyticsEnabled, redactAnalyticsEvent, redactSharePath, trackShare } from "../analytics";

describe("analyticsEnabled", () => {
  it("is on for Vercel production", () => {
    expect(analyticsEnabled({ NODE_ENV: "production", VERCEL: "1", VERCEL_ENV: "production" })).toBe(true);
  });

  it.each([
    ["a preview", { NODE_ENV: "production", VERCEL: "1", VERCEL_ENV: "preview" }],
    ["vercel dev", { NODE_ENV: "development", VERCEL: "1", VERCEL_ENV: "development" }],
    ["next start, compose or Playwright", { NODE_ENV: "production" }],
    ["next dev", { NODE_ENV: "development" }],
  ])("is off for %s", (_, env) => {
    expect(analyticsEnabled(env)).toBe(false);
  });
});

describe("redactAnalyticsEvent", () => {
  const view = (url: string) => ({ type: "pageview" as const, url });

  it("keeps a plain path", () => {
    expect(redactAnalyticsEvent(view("https://justabill.io/bills/119-hr-1"))).toEqual(
      view("https://justabill.io/bills/119-hr-1"),
    );
  });

  it("drops search text and filters", () => {
    expect(
      redactAnalyticsEvent(view("https://justabill.io/bills?q=123+Main+St&congress=119&page=2")),
    ).toEqual(view("https://justabill.io/bills"));
  });

  it("keeps campaign parameters and drops the rest", () => {
    expect(
      redactAnalyticsEvent(view("https://justabill.io/?utm_source=news&q=x&ref=hn&utm_campaign=launch")),
    ).toEqual(view("https://justabill.io/?utm_source=news&ref=hn&utm_campaign=launch"));
  });

  it("drops the fragment", () => {
    expect(redactAnalyticsEvent(view("https://justabill.io/scorecard#member-A000001"))).toEqual(
      view("https://justabill.io/scorecard"),
    );
  });

  it("keeps the event type", () => {
    expect(redactAnalyticsEvent({ type: "event", url: "https://justabill.io/vote?x=1" })).toEqual({
      type: "event",
      url: "https://justabill.io/vote",
    });
  });

  it("drops the whole query from a URL it can't parse", () => {
    expect(redactAnalyticsEvent(view("/bills?q=secret#top"))).toEqual(view("/bills"));
  });
});

describe("redactSharePath", () => {
  it.each([
    ["/share/rep/X000001/8-of-12", "/share/rep/[member]/[score]"],
    ["/share/rep/X000001/8-of-12/image.png", "/share/rep/[member]/[score]/image.png"],
    ["/share/bill/hr-119-1/yea", "/share/bill/[bill]/[vote]"],
    ["/share/bill/hr-119-1/nay/image.png", "/share/bill/[bill]/[vote]/image.png"],
    ["/share/bill/hr-119-1/yea/X000002", "/share/bill/[bill]/[vote]/[member]"],
    ["/share/bill/hr-119-1/yea/X000002/image.png", "/share/bill/[bill]/[vote]/[member]/image.png"],
    ["/share/rep/X000001/8-of-12/", "/share/rep/[member]/[score]"],
    ["/share/aggregate/hr-119-1/CA-12", "/share/aggregate/[bill]/[scope]"],
    ["/share/aggregate/hr-119-1/national/image.png", "/share/aggregate/[bill]/[scope]/image.png"],
    ["/share/something/else/entirely/and/more", "/share"],
  ])("rewrites %s", (path, route) => {
    expect(redactSharePath(path)).toBe(route);
  });

  it("leaves other paths alone", () => {
    expect(redactSharePath("/bills/hr-119-1")).toBe("/bills/hr-119-1");
    expect(redactSharePath("/shared")).toBe("/shared");
  });
});

describe("redactAnalyticsEvent on share pages", () => {
  it("never sends a share card's counts, vote or member", () => {
    const out = redactAnalyticsEvent({
      type: "pageview",
      url: "https://justabill.io/share/bill/hr-119-1/yea/X000002?utm_source=x&q=1",
    });
    expect(out.url).toBe("https://justabill.io/share/bill/[bill]/[vote]/[member]?utm_source=x");
    for (const secret of ["hr-119-1", "yea", "X000002"]) expect(out.url).not.toContain(secret);
  });

  it("rewrites a relative share URL too", () => {
    expect(redactAnalyticsEvent({ type: "pageview", url: "/share/rep/X000001/8-of-12#x" })).toEqual({
      type: "pageview",
      url: "/share/rep/[member]/[score]",
    });
  });
});

describe("trackShare", () => {
  it("sends only the kind and the channel", () => {
    const send = vi.fn();
    trackShare("rep", "copy", send);
    expect(send).toHaveBeenCalledWith("share", { kind: "rep", channel: "copy" });
  });
});
