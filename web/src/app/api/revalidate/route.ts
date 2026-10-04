// POST /api/revalidate: the pipeline marks the bills a sync changed stale.
// Design: docs/design/297-revalidate-after-sync.md. Helpers: lib/revalidate.ts.

import { revalidateTag } from "next/cache";
import {
  isAuthorized,
  MAX_BODY_BYTES,
  parseTags,
  profileFor,
  readBody,
  revalidateSecrets,
} from "@/lib/revalidate";

export async function POST(request: Request): Promise<Response> {
  const secrets = revalidateSecrets(process.env);
  if (secrets.length === 0) {
    // Not opted in (local dev, previews, the private phase): no endpoint.
    return Response.json({ error: "not found" }, { status: 404 });
  }
  if (!isAuthorized(request.headers.get("authorization"), secrets)) {
    return Response.json({ error: "unauthorized" }, { status: 401 });
  }

  const body = await readBody(request);
  if (body === null) {
    return Response.json({ error: `body over ${MAX_BODY_BYTES} bytes` }, { status: 400 });
  }
  const parsed = parseTags(body);
  if ("error" in parsed) {
    return Response.json({ error: parsed.error }, { status: 400 });
  }

  for (const tag of parsed.tags) {
    revalidateTag(tag, profileFor(tag));
  }
  console.info(`revalidate: ${parsed.tags.length} tags`);
  return Response.json({ revalidated: parsed.tags.length });
}
