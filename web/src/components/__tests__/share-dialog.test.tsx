// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { track } from "@vercel/analytics";
import { BillShareButton } from "../share/share-button";
import { VoteSection } from "@/app/(app)/bills/[id]/vote-section";
import type { LocalReps } from "@/lib/local/reps";
import type { LocalEnv } from "@/lib/local/storage";
import { createVoteStore } from "@/lib/local/votes";
import { localVoteBackend } from "@/lib/votes/backend";
import { localReps, VoteBackendContext } from "@/lib/votes/hooks";
import { axeViolations } from "@/test/axe";

vi.mock("@vercel/analytics", () => ({ track: vi.fn() }));

type Req = { method: string; url: string; body: string };
let requests: Req[];
let imageStatus: number;
type ShareFn = (data: ShareData) => Promise<void>;
let shareSpy: ReturnType<typeof vi.fn<ShareFn>> | null;
let canShare: ((data: ShareData) => boolean) | undefined;
let clipboard: string[];

const PNG = new Uint8Array([0x89, 0x50, 0x4e, 0x47]);

// Positions in the 119th: S000001 voted Yea on hr-119-1, the others have no roll call on it.
function positionsFor(memberId: string, congress: string) {
  return {
    member_id: memberId,
    congress: Number(congress),
    rule: "final-passage-v1",
    positions:
      memberId === "S000001"
        ? [{ bill_id: "hr-119-1", vote: "Yea", vote_id: "v1", vote_date: "2025-01-01T00:00:00Z" }]
        : [{ bill_id: "hr-119-9", vote: "Nay", vote_id: "v9", vote_date: "2025-02-01T00:00:00Z" }],
  };
}

function setNavigator(name: string, value: unknown) {
  Object.defineProperty(navigator, name, { value, configurable: true, writable: true });
}

beforeEach(() => {
  requests = [];
  imageStatus = 200;
  clipboard = [];
  shareSpy = vi.fn<ShareFn>(async () => undefined);
  canShare = () => true;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string | URL, init?: RequestInit) => {
      const url = String(input);
      requests.push({ method: init?.method ?? "GET", url, body: typeof init?.body === "string" ? init.body : "" });
      if (url.endsWith("/image.png")) {
        return imageStatus === 200
          ? new Response(PNG, { headers: { "content-type": "image/png" } })
          : new Response("not found", { status: imageStatus });
      }
      const m = /\/members\/(\w+)\/positions\?congress=(\d+)/.exec(url);
      if (m) return Response.json(positionsFor(m[1], m[2]));
      return new Response("not found", { status: 404 });
    })
  );
  URL.createObjectURL = vi.fn(() => "blob:card");
  URL.revokeObjectURL = vi.fn();
  setNavigator("share", (data: ShareData) => shareSpy?.(data));
  setNavigator("canShare", (data: ShareData) => canShare?.(data) ?? false);
  setNavigator("clipboard", { writeText: async (text: string) => void clipboard.push(text) });
  vi.mocked(track).mockClear();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  for (const name of ["share", "canShare", "clipboard"]) {
    delete (navigator as unknown as Record<string, unknown>)[name];
  }
  localReps().clearReps();
});

const BILL_PATH = "/share/bill/hr-119-1/yea";
const BILL_URL = `${window.location.origin}${BILL_PATH}`;
const BILL_TEXT = "I'd vote Yea on H.R. 1.";
const BILL_FILE = "just-a-bill-bill-hr-119-1-yea.png";

// With no members saved in this browser, "Share my vote" opens straight on the vote-only card.
async function openBillDialog() {
  render(<BillShareButton billId="hr-119-1" vote="yea" />);
  expect(requests).toEqual([]);
  fireEvent.click(screen.getByRole("button", { name: "Share my vote" }));
  const dialog = await screen.findByRole("dialog");
  if (imageStatus === 200) await screen.findByAltText(BILL_TEXT);
  return dialog;
}

describe("the share dialog", () => {
  it("prefetches the card image when the dialog opens and shows the exact link", async () => {
    const dialog = await openBillDialog();
    expect(requests.map((r) => r.url)).toEqual([`${BILL_PATH}/image.png`]);
    expect(screen.getByLabelText("Link")).toHaveProperty("value", BILL_URL);
    expect(dialog.textContent).toContain("It doesn't include your other votes or your address.");
    const download = screen.getByRole("link", { name: "Download image" });
    expect(download.getAttribute("href")).toBe("blob:card");
    expect(download.getAttribute("download")).toBe(BILL_FILE);
    expect(await axeViolations(dialog)).toEqual([]);
  });

  it("links to each site's share page in a new tab", async () => {
    await openBillDialog();
    const x = screen.getByRole("link", { name: "X" });
    expect(x.getAttribute("target")).toBe("_blank");
    expect(x.getAttribute("rel")).toBe("noopener noreferrer");
    expect(new URL(x.getAttribute("href")!).searchParams.get("url")).toBe(BILL_URL);
    fireEvent.click(screen.getByRole("link", { name: "Bluesky" }));
    expect(track).toHaveBeenCalledWith("share", { kind: "bill", channel: "bluesky" });
  });
});

describe("Share…", () => {
  it("shares the image file with the link when the browser can", async () => {
    await openBillDialog();
    fireEvent.click(screen.getByRole("button", { name: "Share…" }));
    expect(shareSpy).toHaveBeenCalledTimes(1);
    const data = shareSpy!.mock.calls[0][0] as ShareData;
    expect(data.url).toBe(BILL_URL);
    expect(data.text).toBe(BILL_TEXT);
    expect(data.files?.map((f) => [f.name, f.type])).toEqual([[BILL_FILE, "image/png"]]);
    await waitFor(() => expect(track).toHaveBeenCalledWith("share", { kind: "bill", channel: "native" }));
  });

  it("shares only the link when files can't be shared", async () => {
    canShare = () => false;
    await openBillDialog();
    fireEvent.click(screen.getByRole("button", { name: "Share…" }));
    expect(shareSpy).toHaveBeenCalledWith({ url: BILL_URL, text: BILL_TEXT });
  });

  it("isn't offered when the browser has no share sheet", async () => {
    delete (navigator as unknown as Record<string, unknown>).share;
    await openBillDialog();
    expect(screen.queryByRole("button", { name: "Share…" })).toBeNull();
    expect(screen.getByRole("button", { name: "Copy link" })).toBeTruthy();
  });

  it("says nothing when the visitor closes the share sheet", async () => {
    shareSpy = vi.fn<ShareFn>(async () => {
      throw new DOMException("cancelled", "AbortError");
    });
    await openBillDialog();
    fireEvent.click(screen.getByRole("button", { name: "Share…" }));
    await waitFor(() => expect(shareSpy).toHaveBeenCalled());
    await Promise.resolve();
    expect(screen.getByRole("status", { name: "" }).textContent).toBe("");
    expect(track).not.toHaveBeenCalled();
  });

  it("offers the link instead when sharing fails", async () => {
    shareSpy = vi.fn<ShareFn>(async () => {
      throw new DOMException("denied", "NotAllowedError");
    });
    await openBillDialog();
    fireEvent.click(screen.getByRole("button", { name: "Share…" }));
    expect(await screen.findByText("Sharing didn't work here. Copy the link instead.")).toBeTruthy();
  });
});

describe("Copy link and a missing card", () => {
  it("copies the link", async () => {
    await openBillDialog();
    fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
    expect(await screen.findByText("Link copied.")).toBeTruthy();
    expect(clipboard).toEqual([BILL_URL]);
    expect(track).toHaveBeenCalledWith("share", { kind: "bill", channel: "copy" });
  });

  it("offers nothing to share when the card would 404", async () => {
    imageStatus = 404;
    await openBillDialog();
    expect(await screen.findByText(/This card isn't available/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy link" })).toHaveProperty("disabled", true);
    expect(screen.queryByRole("link", { name: "X" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Download image" })).toBeNull();
  });

  it("still shares the link when the image fails to load", async () => {
    imageStatus = 500;
    await openBillDialog();
    expect(await screen.findByText(/didn't load/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy link" })).toHaveProperty("disabled", false);
  });
});

// Design 88, "Testing", item 2: with 50 votes in the store, only the shared bill and vote (and
// the picked member) leave the browser, and nothing leaves before the Share click.
describe("the bill page's share button", () => {
  const reps: LocalReps = {
    state: "WY",
    district: 0,
    looked_up_at: "2026-10-04T00:00:00Z",
    members: [
      { id: "H000001", name: "Hana Hill", party: "", chamber: "House", state: "WY", district: 0 },
      { id: "S000001", name: "Sam Stone", party: "", chamber: "Senate", state: "WY" },
      { id: "S000002", name: "Sue Sand", party: "", chamber: "Senate", state: "WY" },
    ],
  };
  const others = Array.from({ length: 49 }, (_, i) => `s-119-${i + 2}`);

  function renderBillPage() {
    const data = new Map<string, string>();
    const env: LocalEnv = {
      storage: {
        getItem: (k) => data.get(k) ?? null,
        setItem: (k, v) => void data.set(k, v),
        removeItem: (k) => void data.delete(k),
      },
      events: new EventTarget(),
    };
    const store = createVoteStore({ env: () => env, persist: () => undefined });
    store.setVote("hr-119-1", "yea", "Act One");
    others.forEach((id, i) => store.setVote(id, i % 2 === 0 ? "nay" : "skip", `Other Act ${i}`));
    expect(Object.keys(store.getSnapshot().value)).toHaveLength(50);
    localReps().saveReps(reps);
    render(
      <VoteBackendContext.Provider value={localVoteBackend(store)}>
        <VoteSection billId="hr-119-1" billTitle="Act One" />
      </VoteBackendContext.Provider>
    );
  }

  it("isn't shown for a skip", () => {
    const store = createVoteStore({ env: () => null, persist: () => undefined });
    store.setVote("hr-119-1", "skip");
    render(
      <VoteBackendContext.Provider value={localVoteBackend(store)}>
        <VoteSection billId="hr-119-1" billTitle="Act One" />
      </VoteBackendContext.Provider>
    );
    expect(screen.queryByRole("button", { name: "Share my vote" })).toBeNull();
  });

  it("sends only the printed values, and nothing before the click", async () => {
    renderBillPage();
    expect(requests).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Share my vote" }));
    // The picker starts on the member with a recorded vote.
    const sam = await screen.findByRole("radio", { name: /Sam Stone/ });
    await waitFor(() => expect(sam).toHaveProperty("checked", true));
    expect(screen.getByRole("radio", { name: /Sam Stone/ }).parentElement?.textContent).toContain("voted Yea");
    await screen.findByAltText("I'd vote Yea on H.R. 1.");

    // Share with the file, copy the link, then switch to "just my vote".
    fireEvent.click(screen.getByRole("button", { name: "Share…" }));
    fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
    await screen.findByText("Link copied.");
    const intents = screen.getAllByRole("link").map((a) => a.getAttribute("href") ?? "");
    fireEvent.click(screen.getByRole("radio", { name: "No one, just my vote" }));
    await waitFor(() => expect(requests.at(-1)?.url).toBe("/share/bill/hr-119-1/yea/image.png"));

    const origin = window.location.origin;
    expect(requests.map((r) => `${r.method} ${r.url.replace(/^.*\/api\/v1/, "")}`).sort()).toEqual([
      "GET /members/H000001/positions?congress=119",
      "GET /members/S000001/positions?congress=119",
      "GET /members/S000002/positions?congress=119",
      "GET /share/bill/hr-119-1/yea/S000001/image.png",
      "GET /share/bill/hr-119-1/yea/image.png",
    ]);
    const shared = shareSpy!.mock.calls[0][0] as ShareData;
    expect(shared.url).toBe(`${origin}/share/bill/hr-119-1/yea/S000001`);
    expect(clipboard).toEqual([`${origin}/share/bill/hr-119-1/yea/S000001`]);

    const outgoing = [
      ...requests.flatMap((r) => [r.url, r.body]),
      shared.url ?? "",
      shared.text ?? "",
      ...(shared.files ?? []).map((f) => f.name),
      ...clipboard,
      ...intents.map((href) => decodeURIComponent(href)),
    ];
    for (const value of outgoing) {
      for (const id of others) expect(value).not.toContain(id);
      expect(value.toLowerCase()).not.toMatch(/\bnay\b|\bskip\b|act one|other act/);
    }
  });
});
