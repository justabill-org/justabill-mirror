import { NextRequest } from "next/server";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { MAX_EVENT_BYTES, parseEventBody, recordExperimentEvent } from "../experiments/endpoint";
import type { Experiment } from "../experiments/registry";
import {
  ATTR_EXPERIMENT_ID,
  ATTR_EXPERIMENT_VARIANT,
  METRIC_EXPERIMENT_CONVERSIONS,
  METRIC_EXPERIMENT_EXPOSURES,
} from "../obs/names";
import { LoadShedder } from "../obs/relay";
import { setupTestTelemetry, type TestTelemetry } from "../obs/testing";

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
const OPTS = { experiments: [EXP], now: new Date("2026-10-20T12:00:00Z") };
const BROWSER = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36";

function post(body: string, headers: Record<string, string> = {}): NextRequest {
  return new NextRequest("https://justabill.io/api/experiments", {
    method: "POST",
    headers: { "content-type": "text/plain;charset=UTF-8", "user-agent": BROWSER, ...headers },
    body,
  });
}

const event = (e: string, experiment = EXP.id) => JSON.stringify({ experiment, event: e });
const ARM = { cookie: "jab_exp=vote-deck-layout.treatment" };

let tel: TestTelemetry;

beforeEach(() => {
  tel = setupTestTelemetry();
});

afterEach(async () => {
  await tel.shutdown();
});

async function sum(metric: string): Promise<{ total: number; attributes: unknown[] }> {
  const points = await tel.points(metric);
  return { total: points.reduce((n, p) => n + (p.value as number), 0), attributes: points.map((p) => p.attributes) };
}

describe("recordExperimentEvent", () => {
  it("counts an exposure with the cookie's arm", async () => {
    const res = await recordExperimentEvent(post(event("exposure"), ARM), new LoadShedder(), OPTS);
    expect(res.status).toBe(204);
    expect(res.headers.get("cache-control")).toBe("no-store");
    expect(await sum(METRIC_EXPERIMENT_EXPOSURES)).toEqual({
      total: 1,
      attributes: [{ [ATTR_EXPERIMENT_ID]: "vote-deck-layout", [ATTR_EXPERIMENT_VARIANT]: "treatment" }],
    });
    expect((await sum(METRIC_EXPERIMENT_CONVERSIONS)).total).toBe(0);
  });

  it("counts a conversion on its own counter", async () => {
    const control = { cookie: "jab_exp=vote-deck-layout.control" };
    await recordExperimentEvent(post(event("conversion"), control), new LoadShedder(), OPTS);
    expect(await sum(METRIC_EXPERIMENT_CONVERSIONS)).toEqual({
      total: 1,
      attributes: [{ [ATTR_EXPERIMENT_ID]: "vote-deck-layout", [ATTR_EXPERIMENT_VARIANT]: "control" }],
    });
  });

  it("never takes the arm from the body", async () => {
    const body = JSON.stringify({ experiment: EXP.id, event: "exposure", variant: "control" });
    const res = await recordExperimentEvent(post(body, ARM), new LoadShedder(), OPTS);
    expect(res.status).toBe(400);
    expect((await sum(METRIC_EXPERIMENT_EXPOSURES)).total).toBe(0);
  });

  it.each([
    ["no cookie", event("exposure"), {}],
    ["a cookie for another experiment", event("exposure"), { cookie: "jab_exp=old-test.treatment" }],
    ["an experiment not in the registry", event("exposure", "old-test"), { cookie: "jab_exp=old-test.treatment" }],
    ["a Global Privacy Control browser", event("exposure"), { ...ARM, "sec-gpc": "1" }],
    ["a crawler", event("exposure"), { ...ARM, "user-agent": "Googlebot/2.1 (+http://www.google.com/bot.html)" }],
  ])("drops an event with %s and still answers 204", async (_, body, headers) => {
    const res = await recordExperimentEvent(post(body, headers), new LoadShedder(), OPTS);
    expect(res.status).toBe(204);
    expect((await sum(METRIC_EXPERIMENT_EXPOSURES)).total).toBe(0);
  });

  it("drops events outside the experiment's window", async () => {
    const after = { ...OPTS, now: new Date("2026-10-26T00:00:01Z") };
    expect((await recordExperimentEvent(post(event("exposure"), ARM), new LoadShedder(), after)).status).toBe(204);
    expect((await sum(METRIC_EXPERIMENT_EXPOSURES)).total).toBe(0);
  });

  it.each([
    ["an unknown event", event("click")],
    ["not JSON", "exposure"],
    ["an array", "[1,2]"],
    ["a missing field", JSON.stringify({ experiment: EXP.id })],
    ["a number for the experiment", JSON.stringify({ experiment: 1, event: "exposure" })],
  ])("refuses %s with 400", async (_, body) => {
    const res = await recordExperimentEvent(post(body, ARM), new LoadShedder(), OPTS);
    expect(res.status).toBe(400);
    expect((await sum(METRIC_EXPERIMENT_EXPOSURES)).total).toBe(0);
  });

  it("refuses a body over 256 bytes with 413, declared or not", async () => {
    const big = JSON.stringify({ experiment: "x".repeat(MAX_EVENT_BYTES), event: "exposure" });
    expect((await recordExperimentEvent(post(big, ARM), new LoadShedder(), OPTS)).status).toBe(413);
    const declared = post(event("exposure"), { ...ARM, "content-length": "1000" });
    expect((await recordExperimentEvent(declared, new LoadShedder(), OPTS)).status).toBe(413);
    expect((await sum(METRIC_EXPERIMENT_EXPOSURES)).total).toBe(0);
  });

  it("answers 429 once the instance's minute is full", async () => {
    const shedder = new LoadShedder(1, () => 0);
    expect((await recordExperimentEvent(post(event("exposure"), ARM), shedder, OPTS)).status).toBe(204);
    const res = await recordExperimentEvent(post(event("exposure"), ARM), shedder, OPTS);
    expect(res.status).toBe(429);
    expect(res.headers.get("retry-after")).toBe("60");
    expect((await sum(METRIC_EXPERIMENT_EXPOSURES)).total).toBe(1);
  });

  it("uses the live registry and the clock by default", async () => {
    const res = await recordExperimentEvent(post(event("exposure"), ARM), new LoadShedder());
    expect(res.status).toBe(204);
    expect((await sum(METRIC_EXPERIMENT_EXPOSURES)).total).toBe(0);
  });
});

describe("parseEventBody", () => {
  it("takes exactly the two known fields", () => {
    expect(parseEventBody(event("exposure"))).toEqual({ experiment: EXP.id, event: "exposure" });
    expect(parseEventBody("null")).toBeUndefined();
    expect(parseEventBody(JSON.stringify({ experiment: EXP.id, event: "exposure", vote: "yea" }))).toBeUndefined();
  });
});

describe("POST /api/experiments", () => {
  it("answers through the route handler", async () => {
    const { POST } = await import("../../app/api/experiments/route");
    const res = await POST(post(event("exposure"), ARM));
    expect(res.status).toBe(204);
  });
});
