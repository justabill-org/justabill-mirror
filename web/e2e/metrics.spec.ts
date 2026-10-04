import { expect, test } from "@playwright/test";
import { HOUSE_BILL } from "./helpers";

// The web server's http.server.request.duration (docs/design/376-web-request-metric.md), as the
// real OTLP exporter sends it from `next start` to the stub in e2e/stubs/otlp.mjs.

const OTLP_URL = `http://localhost:${process.env.E2E_OTLP_PORT || "18090"}`;

interface Point {
  service?: string;
  count: number;
  attributes: Record<string, string | number | boolean>;
}

async function serverPoints(): Promise<Point[]> {
  const res = await fetch(`${OTLP_URL}/points?metric=http.server.request.duration`);
  return (await res.json()) as Point[];
}

test("the web server records each request with its route pattern, never the path", async ({ page }) => {
  const missing = "hr-119-99999";
  const found = await page.goto(`/bills/${HOUSE_BILL.id}`);
  expect(found?.status()).toBe(200);
  const notFound = await page.goto(`/bills/${missing}?address=1+E2E+Way`);
  expect(notFound?.status()).toBe(404);

  const statuses = async () =>
    (await serverPoints())
      .filter((p) => p.service === "justabill-web" && p.attributes["http.route"] === "/bills/[id]")
      .map((p) => p.attributes["http.response.status_code"]);
  await expect.poll(statuses, { timeout: 15_000 }).toEqual(expect.arrayContaining([200, 404]));

  // The attributes are an allowlist: no point carries a bill ID, a raw path or a query.
  const values = JSON.stringify((await serverPoints()).map((p) => p.attributes));
  expect(values).not.toContain(HOUSE_BILL.id);
  expect(values).not.toContain(missing);
  expect(values).not.toContain("E2E");
});
