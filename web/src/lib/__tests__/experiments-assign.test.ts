import { NextRequest } from "next/server";
import { describe, expect, it } from "vitest";

import { assignVariant, cookieVariant, experimentResponse } from "../experiments/assign";
import type { Experiment } from "../experiments/registry";

const EXP: Experiment = {
  id: "vote-deck-layout",
  issue: 695,
  path: "/vote",
  hypothesis: "h",
  goal: "casts a first vote on /vote",
  baseline: 0.1,
  target: 0.12,
  samplePerArm: 3841,
  start: "2026-10-12",
  end: "2026-10-26",
};
const LIVE = new Date("2026-10-20T12:00:00Z");
const BROWSER = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15";
const PROD = { vercelEnv: "production" };

function req(url = "https://justabill.io/vote", headers: Record<string, string> = {}): NextRequest {
  return new NextRequest(url, { headers: { "user-agent": BROWSER, ...headers } });
}

describe("assignVariant", () => {
  it("draws control for a new visitor below one half, with a cookie to the experiment's end", () => {
    const a = assignVariant(req(), [EXP], LIVE, { ...PROD, random: () => 0.49 });
    expect(a.variant).toBe("control");
    expect(a.rewrite).toBeUndefined();
    expect(a.cookie).toEqual({
      name: "jab_exp",
      value: "vote-deck-layout.control",
      httpOnly: true,
      secure: true,
      sameSite: "lax",
      path: "/",
      maxAge: 5.5 * 24 * 60 * 60,
    });
  });

  it("draws treatment for a new visitor at one half or more, and rewrites to the sibling route", () => {
    const a = assignVariant(req(), [EXP], LIVE, { ...PROD, random: () => 0.5 });
    expect(a.variant).toBe("treatment");
    expect(a.rewrite).toBe("/vote/v/treatment");
    expect(a.cookie?.value).toBe("vote-deck-layout.treatment");
  });

  it("draws from crypto by default, about half each way", () => {
    const counts = { control: 0, treatment: 0 };
    for (let i = 0; i < 400; i++) counts[assignVariant(req(), [EXP], LIVE, PROD).variant]++;
    expect(counts.control).toBeGreaterThan(140);
    expect(counts.treatment).toBeGreaterThan(140);
  });

  it("keeps a returning visitor's arm without setting the cookie again", () => {
    const a = assignVariant(req(undefined, { cookie: "jab_exp=vote-deck-layout.treatment" }), [EXP], LIVE, {
      ...PROD,
      random: () => 0,
    });
    expect(a).toEqual({ experiment: EXP, variant: "treatment", rewrite: "/vote/v/treatment" });
  });

  it("draws again for a cookie naming another experiment or a bad arm", () => {
    for (const cookie of ["jab_exp=old-test.treatment", "jab_exp=vote-deck-layout.b", "jab_exp=garbage"]) {
      const a = assignVariant(req(undefined, { cookie }), [EXP], LIVE, { ...PROD, random: () => 0.9 });
      expect(a.cookie?.value).toBe("vote-deck-layout.treatment");
    }
  });

  it("gives crawlers control and no cookie", () => {
    const a = assignVariant(req(undefined, { "user-agent": "Googlebot/2.1 (+http://www.google.com/bot.html)" }), [EXP], LIVE, {
      ...PROD,
      random: () => 0.9,
    });
    expect(a).toEqual({ experiment: EXP, variant: "control" });
  });

  it("gives Global Privacy Control browsers control and no cookie, even with an old cookie", () => {
    const a = assignVariant(
      req(undefined, { "sec-gpc": "1", cookie: "jab_exp=vote-deck-layout.treatment" }),
      [EXP],
      LIVE,
      { ...PROD, random: () => 0.9 }
    );
    expect(a).toEqual({ experiment: EXP, variant: "control" });
  });

  it("serves control with no cookie before the start and after the end", () => {
    for (const now of [new Date("2026-10-11T23:00:00Z"), new Date("2026-10-26T00:00:00Z")]) {
      const a = assignVariant(req(undefined, { cookie: "jab_exp=vote-deck-layout.treatment" }), [EXP], now, {
        ...PROD,
        random: () => 0.9,
      });
      expect(a).toEqual({ experiment: EXP, variant: "control" });
    }
  });

  it("leaves pages without an experiment alone", () => {
    expect(assignVariant(req("https://justabill.io/bills"), [EXP], LIVE, PROD)).toEqual({ variant: "control" });
  });

  it("honors ?variant= outside production, even before the start, with no cookie", () => {
    const early = new Date("2026-10-01T00:00:00Z");
    const t = assignVariant(req("https://preview.vercel.app/vote?variant=treatment"), [EXP], early, {
      vercelEnv: "preview",
    });
    expect(t).toEqual({ experiment: EXP, variant: "treatment", rewrite: "/vote/v/treatment" });
    const c = assignVariant(req("https://localhost/vote?variant=control"), [EXP], LIVE, { vercelEnv: undefined });
    expect(c).toEqual({ experiment: EXP, variant: "control" });
  });

  it("ignores ?variant= in production", () => {
    const a = assignVariant(req("https://justabill.io/vote?variant=treatment"), [EXP], LIVE, {
      ...PROD,
      random: () => 0.1,
    });
    expect(a.variant).toBe("control");
    expect(a.cookie?.value).toBe("vote-deck-layout.control");
  });
});

describe("cookieVariant", () => {
  it("reads only this experiment's arm", () => {
    expect(cookieVariant("vote-deck-layout.control", EXP)).toBe("control");
    expect(cookieVariant("vote-deck-layout.treatment", EXP)).toBe("treatment");
    expect(cookieVariant("vote-deck.layout.treatment", EXP)).toBeUndefined();
    expect(cookieVariant("vote-deck-layout", EXP)).toBeUndefined();
    expect(cookieVariant(undefined, EXP)).toBeUndefined();
  });
});

describe("experimentResponse", () => {
  it("rewrites treatment to the sibling route, keeps the query and sets the cookie", () => {
    const res = experimentResponse(req("https://justabill.io/vote?congress=119"), [EXP], LIVE, {
      ...PROD,
      random: () => 0.9,
    });
    expect(res.headers.get("x-middleware-rewrite")).toBe("https://justabill.io/vote/v/treatment?congress=119");
    const cookie = res.headers.get("set-cookie") ?? "";
    expect(cookie).toContain("jab_exp=vote-deck-layout.treatment");
    expect(cookie).toMatch(/HttpOnly/i);
    expect(cookie).toMatch(/Secure/i);
    expect(cookie).toMatch(/SameSite=lax/i);
    expect(cookie).toContain("Path=/");
    expect(cookie).toContain(`Max-Age=${5.5 * 24 * 60 * 60}`);
  });

  it("passes control through untouched", () => {
    const res = experimentResponse(req(), [EXP], LIVE, { ...PROD, random: () => 0.1 });
    expect(res.headers.get("x-middleware-rewrite")).toBeNull();
    expect(res.headers.get("x-middleware-next")).toBe("1");
    expect(res.headers.get("set-cookie")).toContain("jab_exp=vote-deck-layout.control");
  });

  it("sets no cookie for a returning visitor or with no live experiment", () => {
    const back = experimentResponse(req(undefined, { cookie: "jab_exp=vote-deck-layout.control" }), [EXP], LIVE, PROD);
    expect(back.headers.get("set-cookie")).toBeNull();
    expect(experimentResponse(req()).headers.get("set-cookie")).toBeNull();
  });
});
