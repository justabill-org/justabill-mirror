import { readFileSync } from "node:fs";
import path from "node:path";

import { afterEach, describe, it, expect, vi } from "vitest";

// The request's headers as Vercel hands them to a server render.
const request = vi.hoisted(() => ({ headers: new Headers() }));
vi.mock("next/headers", () => ({ headers: async () => request.headers }));

import { withServerKey as noServerKey, withVisitor as noVisitor } from "../no-server-key";
import { SERVER_KEY_HEADER, VISITOR_IP_HEADER, withServerKey, withVisitor } from "../server-key";

const key = "test-server-key-0123456789abcdefghijkl";

describe("withServerKey", () => {
  it("leaves the request alone when API_SERVER_KEY is unset or blank", () => {
    const init: RequestInit = { method: "POST", headers: { "Content-Type": "application/json" } };
    expect(withServerKey(init, {})).toBe(init);
    expect(withServerKey(init, { API_SERVER_KEY: "  " })).toBe(init);
    expect(withServerKey(undefined, {})).toEqual({});
  });

  it("adds X-Server-Key and keeps a record's header names as given", () => {
    const init: RequestInit = { method: "POST", headers: { "X-Dev-User-Id": "u1" }, body: "{}" };
    const got = withServerKey(init, { API_SERVER_KEY: ` ${key} ` });
    expect(got).toEqual({
      method: "POST",
      body: "{}",
      headers: { "X-Dev-User-Id": "u1", [SERVER_KEY_HEADER]: key },
    });
    expect(init.headers).toEqual({ "X-Dev-User-Id": "u1" });
  });

  it("converts a Headers object or a list of pairs instead of dropping them", () => {
    const env = { API_SERVER_KEY: key };
    const fromHeaders = withServerKey({ headers: new Headers({ Accept: "application/json" }) }, env);
    expect(fromHeaders.headers).toEqual({ accept: "application/json", [SERVER_KEY_HEADER]: key });
    const fromPairs = withServerKey({ headers: [["Accept", "text/plain"]] }, env);
    expect(fromPairs.headers).toEqual({ accept: "text/plain", [SERVER_KEY_HEADER]: key });
    expect(withServerKey({}, env).headers).toEqual({ [SERVER_KEY_HEADER]: key });
  });

  it("is server-only and reads a variable Next.js never inlines into the browser bundle", () => {
    const source = readFileSync(path.resolve(__dirname, "../server-key.ts"), "utf8");
    expect(source).toMatch(/^import "server-only";$/m);
    expect(source).toMatch(/env\.API_SERVER_KEY\b/);
    expect(source).not.toMatch(/env\.NEXT_PUBLIC_/);
  });
});

describe("withVisitor (#607)", () => {
  const vercel = { VERCEL: "1" };
  const cached: RequestInit = { next: { revalidate: 300, tags: ["bills"] } };

  afterEach(() => {
    request.headers = new Headers();
  });

  it("sends the visitor's x-real-ip on Vercel, uncached, and keeps the other headers", async () => {
    request.headers = new Headers({ "x-real-ip": "203.0.113.7", "x-forwarded-for": "198.51.100.1" });
    const init: RequestInit = { ...cached, headers: { Accept: "application/json" } };
    const got = await withVisitor(init, vercel);
    expect(got).toEqual({
      cache: "no-store",
      headers: { Accept: "application/json", [VISITOR_IP_HEADER]: "203.0.113.7" },
    });
    expect(got.next).toBeUndefined();
    expect(init).toEqual({ ...cached, headers: { Accept: "application/json" } });
  });

  it("passes the address as Vercel sent it, and leaves checking it to the API", async () => {
    request.headers = new Headers({ "x-real-ip": "2001:db8::1" });
    expect((await withVisitor({}, vercel)).headers).toEqual({ [VISITOR_IP_HEADER]: "2001:db8::1" });
  });

  it("sends nothing off Vercel, even when the request claims an address", async () => {
    request.headers = new Headers({ "x-real-ip": "203.0.113.7" });
    expect(await withVisitor(cached, {})).toBe(cached);
    expect(await withVisitor(cached, { VERCEL: "0" })).toBe(cached);
    expect(await withVisitor(undefined, {})).toEqual({});
  });

  it("leaves the call cached when Vercel sent no address", async () => {
    expect(await withVisitor(cached, vercel)).toBe(cached);
  });

  it("reads the address only from x-real-ip, and only under VERCEL=1", () => {
    const source = readFileSync(path.resolve(__dirname, "../server-key.ts"), "utf8");
    expect(source).toMatch(/env\.VERCEL !== "1"/);
    expect(source).toMatch(/\.get\("x-real-ip"\)/);
    expect(source).not.toMatch(/x-forwarded-for"\)/);
  });
});

describe("#server-key", () => {
  it("is the keyed module only under react-server, and the no-op everywhere else", () => {
    const pkg = JSON.parse(readFileSync(path.resolve(__dirname, "../../../package.json"), "utf8"));
    expect(pkg.imports["#server-key"]).toEqual({
      "react-server": "./src/lib/server-key.ts",
      default: "./src/lib/no-server-key.ts",
    });
  });

  it("sends no key from client code, even with API_SERVER_KEY set", () => {
    vi.stubEnv("API_SERVER_KEY", key);
    const init: RequestInit = { headers: { "Content-Type": "application/json" } };
    expect(noServerKey(init)).toBe(init);
    expect(noServerKey()).toEqual({});
    vi.unstubAllEnvs();
  });

  it("sends no visitor IP from client code, even on Vercel", async () => {
    vi.stubEnv("VERCEL", "1");
    request.headers = new Headers({ "x-real-ip": "203.0.113.7" });
    const init: RequestInit = { next: { revalidate: 300 } };
    expect(await noVisitor(init)).toBe(init);
    expect(await noVisitor()).toEqual({});
    vi.unstubAllEnvs();
  });
});
