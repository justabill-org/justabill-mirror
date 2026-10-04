// On-demand revalidation of cached bill pages, called by the pipeline after a
// sync (POST /api/revalidate). Design: docs/design/297-revalidate-after-sync.md.
//
// The endpoint exists only where REVALIDATE_SECRETS is set: a comma-separated
// list, so two secrets can be live during a rotation. The only thing a secret
// allows is marking the tags below stale.

import { createHash, timingSafeEqual } from "node:crypto";

type Env = Record<string, string | undefined>;

/** The most tags one call may carry; the pipeline sends `bills` above this. */
export const MAX_TAGS = 100;

/** The largest request body accepted, in bytes. */
export const MAX_BODY_BYTES = 16 * 1024;

/** The tag on every cached bill read and list (#74's CACHE_TAGS.bills). */
export const ALL_BILLS_TAG = "bills";

// bill:<id>, where the ID has #74's format: type-congress-number (hr-119-1).
const BILL_TAG = /^bill:[a-z]+-\d{1,3}-\d{1,5}$/;

/** A revalidateTag profile: expire now, or serve stale once and refresh. */
export type RevalidateProfile = "max" | { expire: number };

/** Whether the pipeline may revalidate this tag. New tags are added here. */
export function isRevalidatableTag(tag: unknown): tag is string {
  return typeof tag === "string" && (tag === ALL_BILLS_TAG || BILL_TAG.test(tag));
}

/**
 * The profile for a tag. A bill's tag expires at once, so the next request
 * renders fresh. The broad `bills` tag is only marked stale, so thousands of
 * pages don't all re-render in the foreground.
 */
export function profileFor(tag: string): RevalidateProfile {
  return tag === ALL_BILLS_TAG ? "max" : { expire: 0 };
}

/** The configured secrets; empty means the endpoint is off. */
export function revalidateSecrets(env: Env): string[] {
  return (env.REVALIDATE_SECRETS ?? "")
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

function digest(value: string): Buffer {
  return createHash("sha256").update(value).digest();
}

/**
 * Whether the Authorization header carries one of the secrets as a bearer
 * token. Both sides are hashed first, so the comparison is constant time and
 * doesn't leak the secret's length.
 */
export function isAuthorized(authorization: string | null, secrets: string[]): boolean {
  const match = /^Bearer (.+)$/.exec(authorization ?? "");
  if (!match) {
    return false;
  }
  const token = digest(match[1]);
  // Check every secret, so the time doesn't say which one matched.
  return secrets.reduce((ok, secret) => timingSafeEqual(token, digest(secret)) || ok, false);
}

/** A parsed request body: the tags to revalidate, or why it was rejected. */
export type ParsedTags = { tags: string[] } | { error: string };

/**
 * Reads the body, stopping once it's over MAX_BODY_BYTES, so an oversize
 * request is never buffered whole. Returns null when it's too large.
 */
export async function readBody(request: Request): Promise<string | null> {
  const declared = Number(request.headers.get("content-length") ?? 0);
  if (declared > MAX_BODY_BYTES) {
    return null;
  }
  if (!request.body) {
    return "";
  }
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) {
      break;
    }
    size += value.byteLength;
    if (size > MAX_BODY_BYTES) {
      await reader.cancel();
      return null;
    }
    chunks.push(value);
  }
  return Buffer.concat(chunks).toString("utf8");
}

/**
 * Parses `{"tags": string[]}`. Any unknown tag rejects the whole request, so
 * a bug in the caller is visible rather than half-applied.
 */
export function parseTags(body: string): ParsedTags {
  let parsed: unknown;
  try {
    parsed = JSON.parse(body);
  } catch {
    return { error: "body must be JSON" };
  }
  const tags = (parsed as { tags?: unknown } | null)?.tags;
  if (!Array.isArray(tags) || tags.length === 0) {
    return { error: 'body must be {"tags": [...]} with at least one tag' };
  }
  if (tags.length > MAX_TAGS) {
    return { error: `at most ${MAX_TAGS} tags per call` };
  }
  const unknown = tags.find((tag) => !isRevalidatableTag(tag));
  if (unknown !== undefined) {
    return { error: `tag not allowed: ${JSON.stringify(unknown).slice(0, 80)}` };
  }
  return { tags: [...new Set(tags as string[])] };
}
