import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  isAuthorized,
  isRevalidatableTag,
  MAX_BODY_BYTES,
  MAX_TAGS,
  parseTags,
  profileFor,
  readBody,
  revalidateSecrets,
} from "../revalidate";

vi.mock("next/cache", () => ({ revalidateTag: vi.fn() }));

const { revalidateTag } = await import("next/cache");
const { POST } = await import("@/app/api/revalidate/route");

const URL_ = "http://localhost:3000/api/revalidate";

function post(body: unknown, token?: string, headers: Record<string, string> = {}): Request {
  return new Request(URL_, {
    method: "POST",
    headers: { "content-type": "application/json", ...(token ? { authorization: `Bearer ${token}` } : {}), ...headers },
    body: typeof body === "string" ? body : JSON.stringify(body),
  });
}

function billTags(n: number): string[] {
  return Array.from({ length: n }, (_, i) => `bill:hr-119-${i + 1}`);
}

describe("isRevalidatableTag", () => {
  it.each(["bills", "bill:hr-119-1", "bill:s-119-5", "bill:hjres-118-12345", "bill:sconres-1-1"])(
    "allows %s",
    (tag) => expect(isRevalidatableTag(tag)).toBe(true)
  );

  it.each([
    "members",
    "member:X000001",
    "congresses",
    "bill:",
    "bill:HR-119-1",
    "bill:hr-119",
    "bill:hr-1190-1",
    "bill:hr-119-123456",
    "bill:hr-119-1 ",
    "bill:hr-119-1/extra",
    "bills ",
    "",
    42,
    null,
  ])("rejects %j", (tag) => expect(isRevalidatableTag(tag)).toBe(false));
});

describe("profileFor", () => {
  it("expires a bill's tag at once and serves the broad tag stale once", () => {
    expect(profileFor("bill:hr-119-1")).toEqual({ expire: 0 });
    expect(profileFor("bills")).toBe("max");
  });
});

describe("revalidateSecrets", () => {
  it("is empty when unset or blank", () => {
    expect(revalidateSecrets({})).toEqual([]);
    expect(revalidateSecrets({ REVALIDATE_SECRETS: "" })).toEqual([]);
    expect(revalidateSecrets({ REVALIDATE_SECRETS: " , ," })).toEqual([]);
  });

  it("splits and trims a rotation list", () => {
    expect(revalidateSecrets({ REVALIDATE_SECRETS: " old , new " })).toEqual(["old", "new"]);
  });
});

describe("isAuthorized", () => {
  it("accepts any configured secret as a bearer token", () => {
    expect(isAuthorized("Bearer old", ["old", "new"])).toBe(true);
    expect(isAuthorized("Bearer new", ["old", "new"])).toBe(true);
  });

  it.each([null, "", "Bearer ", "Bearer wrong", "bearer old", "Basic old", "old", "Bearer old2"])(
    "rejects %j",
    (header) => expect(isAuthorized(header, ["old"])).toBe(false)
  );
});

describe("parseTags", () => {
  it("returns the tags without duplicates", () => {
    expect(parseTags('{"tags":["bill:hr-119-1","bill:hr-119-1","bills"]}')).toEqual({
      tags: ["bill:hr-119-1", "bills"],
    });
  });

  it("accepts exactly the maximum", () => {
    expect(parseTags(JSON.stringify({ tags: billTags(MAX_TAGS) }))).toEqual({ tags: billTags(MAX_TAGS) });
  });

  it.each([
    ["not JSON", "{"],
    ["null", "null"],
    ["no tags", "{}"],
    ["tags not an array", '{"tags":"bills"}'],
    ["empty tags", '{"tags":[]}'],
    ["too many tags", JSON.stringify({ tags: billTags(MAX_TAGS + 1) })],
    ["an unknown tag", '{"tags":["bill:hr-119-1","members"]}'],
    ["a non-string tag", '{"tags":["bill:hr-119-1",7]}'],
  ])("rejects %s", (_, body) => expect(parseTags(body)).toHaveProperty("error"));
});

describe("readBody", () => {
  it("reads a body up to the limit", async () => {
    const body = "x".repeat(MAX_BODY_BYTES);
    expect(await readBody(post(body))).toBe(body);
  });

  it("stops at a body over the limit", async () => {
    expect(await readBody(post("x".repeat(MAX_BODY_BYTES + 1)))).toBeNull();
  });

  it("rejects a declared length over the limit without reading", async () => {
    const req = post("{}", undefined, { "content-length": String(MAX_BODY_BYTES + 1) });
    expect(await readBody(req)).toBeNull();
  });

  it("stops a streamed body over the limit without a declared length", async () => {
    const chunk = new TextEncoder().encode("x".repeat(MAX_BODY_BYTES / 2));
    const stream = () =>
      new ReadableStream<Uint8Array>({
        start(controller) {
          controller.enqueue(chunk);
          controller.enqueue(chunk);
          controller.enqueue(chunk);
          controller.close();
        },
      });
    const req = new Request(URL_, { method: "POST", body: stream(), duplex: "half" } as RequestInit);
    expect(await readBody(req)).toBeNull();
  });

  it("returns an empty string for no body", async () => {
    expect(await readBody(new Request(URL_, { method: "POST" }))).toBe("");
  });
});

describe("POST /api/revalidate", () => {
  beforeEach(() => {
    vi.mocked(revalidateTag).mockClear();
    vi.spyOn(console, "info").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.unstubAllEnvs();
    vi.restoreAllMocks();
  });

  it("doesn't exist without secrets", async () => {
    vi.stubEnv("REVALIDATE_SECRETS", "");
    const res = await POST(post({ tags: ["bills"] }, "anything"));
    expect(res.status).toBe(404);
    expect(revalidateTag).not.toHaveBeenCalled();
  });

  it.each([
    ["no token", undefined],
    ["a wrong token", "wrong"],
  ])("rejects %s with 401 and revalidates nothing", async (_, token) => {
    vi.stubEnv("REVALIDATE_SECRETS", "s3cret");
    const res = await POST(post({ tags: ["bill:hr-119-1"] }, token));
    expect(res.status).toBe(401);
    expect(revalidateTag).not.toHaveBeenCalled();
  });

  it("checks the secret before reading the body", async () => {
    vi.stubEnv("REVALIDATE_SECRETS", "s3cret");
    const res = await POST(post("{", "wrong"));
    expect(res.status).toBe(401);
  });

  it.each([
    ["bad JSON", "{"],
    ["an oversize body", "x".repeat(MAX_BODY_BYTES + 1)],
    ["too many tags", { tags: billTags(MAX_TAGS + 1) }],
    ["an unknown tag", { tags: ["bill:hr-119-1", "member:X000001"] }],
  ])("rejects %s with 400 and revalidates nothing", async (_, body) => {
    vi.stubEnv("REVALIDATE_SECRETS", "s3cret");
    const res = await POST(post(body, "s3cret"));
    expect(res.status).toBe(400);
    expect(await res.json()).toHaveProperty("error");
    expect(revalidateTag).not.toHaveBeenCalled();
  });

  it("expires each bill's tag at once", async () => {
    vi.stubEnv("REVALIDATE_SECRETS", "s3cret");
    const res = await POST(post({ tags: ["bill:hr-119-1", "bill:s-119-5"] }, "s3cret"));
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ revalidated: 2 });
    expect(revalidateTag).toHaveBeenCalledTimes(2);
    expect(revalidateTag).toHaveBeenCalledWith("bill:hr-119-1", { expire: 0 });
    expect(revalidateTag).toHaveBeenCalledWith("bill:s-119-5", { expire: 0 });
  });

  it("marks every bill page stale with the broad tag", async () => {
    vi.stubEnv("REVALIDATE_SECRETS", "s3cret");
    const res = await POST(post({ tags: ["bills"] }, "s3cret"));
    expect(await res.json()).toEqual({ revalidated: 1 });
    expect(revalidateTag).toHaveBeenCalledWith("bills", "max");
  });

  it("accepts either secret during a rotation", async () => {
    vi.stubEnv("REVALIDATE_SECRETS", "old,new");
    expect((await POST(post({ tags: ["bills"] }, "old"))).status).toBe(200);
    expect((await POST(post({ tags: ["bills"] }, "new"))).status).toBe(200);
  });

  it("logs the tag count, never the secret", async () => {
    vi.stubEnv("REVALIDATE_SECRETS", "s3cret");
    await POST(post({ tags: ["bill:hr-119-1"] }, "s3cret"));
    expect(console.info).toHaveBeenCalledWith("revalidate: 1 tags");
    expect(JSON.stringify(vi.mocked(console.info).mock.calls)).not.toContain("s3cret");
  });
});
