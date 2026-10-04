// @vitest-environment jsdom
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { logs } from "@opentelemetry/api-logs";
import { trace, type TracerProvider } from "@opentelemetry/api";
import type { ReadableSpan, SpanProcessor } from "@opentelemetry/sdk-trace-web";

import { BrowserSpanProcessor, reduceUrl, RELAY_PATH, setupBrowserSdk, type BrowserSdk } from "../obs/browser-sdk";
import { sanitize } from "../obs/relay";

// Vitest resolves packages for Node even under jsdom, and the OTLP exporters' Node build posts with
// node:http. Use their browser builds, as Next.js bundles them, so the real fetch transport runs.
vi.mock("@opentelemetry/exporter-trace-otlp-http", async () =>
  import("../../../node_modules/@opentelemetry/exporter-trace-otlp-http/build/esm/platform/browser/index.js")
);
vi.mock("@opentelemetry/exporter-logs-otlp-http", async () =>
  import("../../../node_modules/@opentelemetry/exporter-logs-otlp-http/build/esm/platform/browser/index.js")
);

const API = "https://api.justabill.io";
const calls: { url: string; headers: Headers; body?: string }[] = [];
let sdk: BrowserSdk;

async function flush() {
  const provider = (trace.getTracerProvider() as unknown as { getDelegate(): TracerProvider }).getDelegate();
  await (provider as unknown as { forceFlush(): Promise<void> }).forceFlush();
  await (logs.getLoggerProvider() as unknown as { forceFlush(): Promise<void> }).forceFlush();
}

beforeAll(async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string | URL | Request, init: RequestInit = {}) => {
      // jsdom has its own Uint8Array, so instanceof would miss the exporter's body.
      const body = ArrayBuffer.isView(init.body) ? Buffer.from(init.body.buffer).toString("utf8") : undefined;
      calls.push({ url: String(input), headers: new Headers(init.headers), body });
      return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
    })
  );
  sdk = setupBrowserSdk({ origin: location.origin, apiOrigin: API, sampleRatio: 1 });
  // One traced action for both tests, so neither depends on the other running first.
  await sdk.withSpan("vote.cast", async () => {
    await fetch(`${API}/api/v1/bills/119-hr-1?congress=119`);
    await fetch("https://www.congress.gov/bill/119th-congress/house-bill/1");
  });
});

afterAll(() => {
  vi.unstubAllGlobals();
});

describe("setupBrowserSdk", () => {
  it("sends traceparent to the API origin only", () => {
    const api = calls.find((c) => c.url.startsWith(API));
    const other = calls.find((c) => c.url.includes("congress.gov"));
    expect(api?.headers.get("traceparent")).toMatch(/^00-[0-9a-f]{32}-[0-9a-f]{16}-01$/);
    expect(other?.headers.has("traceparent")).toBe(false);
  });

  it("exports spans and exception records to the relay in a shape it accepts", async () => {
    type Spans = { resourceSpans: { scopeSpans: { spans: { name: string; attributes: KV[] }[] }[] }[] };
    type KV = { key: string; value: unknown };
    const relayed = (signal: string) => calls.filter((c) => c.url === `${location.origin}${RELAY_PATH}/${signal}`);
    // Every export must get through the relay's checks; fetch spans end after their resource timing.
    const spans = () =>
      relayed("traces").flatMap((c) =>
        (sanitize("traces", c.body!, { NEXT_PUBLIC_API_URL: API }) as Spans).resourceSpans.flatMap((rs) => rs.scopeSpans.flatMap((ss) => ss.spans))
      );

    sdk.logException(new TypeError("boom"), "/bills/[id]");
    await vi.waitFor(
      async () => {
        await flush();
        expect(spans()).toHaveLength(3);
        expect(relayed("logs")).toHaveLength(1);
      },
      { timeout: 4000 }
    );

    for (const c of relayed("traces")) {
      expect(c.headers.get("content-type")).toBe("application/json");
      expect(c.headers.has("traceparent")).toBe(false);
      expect(c.body).not.toContain("congress=119");
    }
    expect(spans().map((s) => s.name).sort()).toEqual(["GET", "GET", "vote.cast"]);
    const urls = spans().flatMap((s) => s.attributes.filter((a) => a.key === "url.full").map((a) => a.value));
    expect(urls).toContainEqual({ stringValue: `${API}/api/v1/bills/{id}` });
    expect(urls).toContainEqual({ stringValue: "https://www.congress.gov/" });
    for (const c of relayed("traces")) expect(c.body).not.toContain("119-hr-1");

    const l = sanitize("logs", relayed("logs")[0].body!, {}) as {
      resourceLogs: { scopeLogs: { logRecords: { eventName?: string; attributes: KV[] }[] }[] }[];
    };
    const record = l.resourceLogs[0].scopeLogs[0].logRecords[0];
    expect(record.eventName).toBe("exception");
    expect(record.attributes.map((a) => a.key)).toEqual(
      expect.arrayContaining(["exception.type", "exception.message", "http.route"])
    );
  });
});

describe("BrowserSpanProcessor", () => {
  function run(name: string, attributes: Record<string, unknown>) {
    const next = { onStart: vi.fn(), onEnd: vi.fn(), forceFlush: vi.fn(), shutdown: vi.fn() };
    const p = new BrowserSpanProcessor(next as unknown as SpanProcessor, "https://justabill.io", API);
    const span = { name, attributes } as unknown as ReadableSpan;
    p.onEnd(span);
    void p.forceFlush();
    void p.shutdown();
    expect(next.forceFlush).toHaveBeenCalled();
    expect(next.shutdown).toHaveBeenCalled();
    return { next, attributes };
  }

  it("reduces page URLs to route patterns and drops the user agent", () => {
    const { next, attributes } = run("documentLoad", {
      "url.full": "https://justabill.io/bills/119-hr-1?tab=text",
      "user_agent.original": "Mozilla/5.0",
    });
    expect(next.onEnd).toHaveBeenCalledOnce();
    expect(attributes).toEqual({ "url.full": "https://justabill.io/bills/[id]" });
  });

  it("leaves out resourceFetch spans", () => {
    expect(run("resourceFetch", { "url.full": "https://justabill.io/_next/x.js" }).next.onEnd).not.toHaveBeenCalled();
  });

  it("reduces API URLs to API route patterns", () => {
    const { attributes } = run("GET", { "url.full": `${API}/api/v1/members/A000360/alignment?congress=119` });
    expect(attributes).toEqual({ "url.full": `${API}/api/v1/members/{id}/alignment` });
  });

  it("drops a url.full that isn't an http(s) URL", () => {
    const { next, attributes } = run("GET", {
      "url.full": "blob:https://justabill.io/0a1b",
      "http.request.method": "GET",
    });
    expect(next.onEnd).toHaveBeenCalledOnce();
    expect(attributes).toEqual({ "http.request.method": "GET" });
  });
});

describe("reduceUrl", () => {
  // The regression test for #755: the API origin kept the whole path, bill ID included.
  it("never keeps a bill ID from another origin's path", () => {
    expect(reduceUrl("https://api.example/api/v1/bills/hr-119-1/vote", "https://web.example")).not.toContain(
      "hr-119-1"
    );
  });

  it.each([
    ["an API call", `${API}/api/v1/bills/hr-119-1/vote`, `${API}/api/v1/bills/{id}/vote`],
    ["an API call with a query", `${API}/api/v1/bills?q=tax&policy_area=Health`, `${API}/api/v1/bills`],
    ["a favorite", `${API}/api/v1/me/favorites/s-119-42`, `${API}/api/v1/me/favorites/{billID}`],
    ["a comparison", `${API}/api/v1/me/compare/A000360`, `${API}/api/v1/me/compare/{memberID}`],
    [
      "a district aggregate",
      `${API}/api/v1/bills/hr-119-1/aggregates/district-KS-3`,
      `${API}/api/v1/bills/{id}/aggregates/{scope_key}`,
    ],
    ["a law section", `${API}/api/v1/law/42/1983`, `${API}/api/v1/law/{title}/{section}`],
    ["the vote import", `${API}/api/v1/me/votes:import`, `${API}/api/v1/me/votes:import`],
    ["an unknown API path", `${API}/api/v1/bills/hr-119-1/secret/x`, `${API}/[unknown]`],
    ["a page", "https://justabill.io/members/A000360#votes", "https://justabill.io/members/[id]"],
    ["a relative page URL", "/bills/hr-119-1", "https://justabill.io/bills/[id]"],
    ["another origin", "https://www.congress.gov/bill/119th-congress/house-bill/1?s=1", "https://www.congress.gov/"],
  ])("reduces %s to its route pattern", (_what, url, want) => {
    expect(reduceUrl(url, "https://justabill.io", API)).toBe(want);
  });

  it("returns undefined for what isn't an http(s) URL", () => {
    expect(reduceUrl("http://[bad?x=1", "https://justabill.io", API)).toBeUndefined();
    expect(reduceUrl("data:text/plain,hr-119-1", "https://justabill.io", API)).toBeUndefined();
  });
});
