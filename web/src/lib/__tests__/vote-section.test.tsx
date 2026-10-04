import { describe, expect, it, vi } from "vitest";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { billDetail } from "../examples";
import { createVoteStore } from "../local/votes";
import { localVoteBackend, type VoteBackend } from "../votes/backend";
import { VoteBackendContext } from "../votes/hooks";

vi.mock("next/headers", () => ({
  cookies: () => {
    throw new Error("the bill page must not read cookies");
  },
}));

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  getBill: vi.fn(async () => billDetail),
  getRelatedBills: vi.fn(async () => []),
}));

const { VoteSection } = await import("@/app/(app)/bills/[id]/vote-section");
const { default: BillDetailPage } = await import("@/app/(app)/bills/[id]/page");

// Server rendering uses getServerSnapshot, so these backends serve the client state there.
function rendered(backend: VoteBackend) {
  const asClient: VoteBackend = { ...backend, getServerSnapshot: backend.getSnapshot };
  return renderToStaticMarkup(
    createElement(
      VoteBackendContext.Provider,
      { value: asClient },
      createElement(VoteSection, { billId: "hr-119-1", billTitle: "Example Act" })
    )
  );
}

describe("VoteSection", () => {
  it("renders on the server with no vote and no storage note", () => {
    const html = renderToStaticMarkup(createElement(VoteSection, { billId: "hr-119-1", billTitle: "Example Act" }));
    expect(html).toContain("Your Vote");
    expect(html).not.toContain('aria-pressed="true"');
    expect(html).not.toContain("device");
    expect(html).not.toContain("won't be saved");
  });

  it("shows the saved vote and says it stays on this device", () => {
    const events = new EventTarget();
    const data = new Map<string, string>();
    const storage = {
      getItem: (k: string) => data.get(k) ?? null,
      setItem: (k: string, v: string) => void data.set(k, v),
      removeItem: (k: string) => void data.delete(k),
    };
    const store = createVoteStore({ env: () => ({ storage, events }), persist: () => undefined });
    store.setVote("hr-119-1", "nay", "Example Act");

    const html = rendered(localVoteBackend(store));
    expect(html).toMatch(/aria-pressed="true"[^>]*>Nay</);
    expect(html).toContain("Kept on this device only.");
  });

  it("warns when votes can only be kept in memory", () => {
    const store = createVoteStore({ env: () => ({ storage: null, events: null }), persist: () => undefined });
    const html = rendered(localVoteBackend(store));
    expect(html).toContain("Votes won&#x27;t be saved on this browser.");
    expect(html).not.toContain("Kept on this device");
  });
});

describe("bill page", () => {
  it("renders the vote card without reading the dev-user-id cookie", async () => {
    const page = await BillDetailPage({ params: Promise.resolve({ id: billDetail.bill.id }) });
    const html = renderToStaticMarkup(page);
    expect(html).toContain("Your Vote");
    expect(html).not.toContain("Sign in to cast your vote");
  });
});
