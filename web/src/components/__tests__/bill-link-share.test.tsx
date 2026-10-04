// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { track } from "@vercel/analytics";
import { BillLinkShareButton } from "../share/bill-link-share";
import { axeViolations } from "@/test/axe";

vi.mock("@vercel/analytics", () => ({ track: vi.fn() }));

type ShareFn = (data: ShareData) => Promise<void>;
let clipboard: string[];

function setNavigator(name: string, value: unknown) {
  Object.defineProperty(navigator, name, { value, configurable: true, writable: true });
}

beforeEach(() => {
  clipboard = [];
  vi.stubEnv("NEXT_PUBLIC_SITE_URL", "");
  setNavigator("clipboard", { writeText: async (text: string) => void clipboard.push(text) });
  vi.mocked(track).mockClear();
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => {
      throw new Error("the bill's share sheet fetches nothing");
    })
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  for (const name of ["share", "canShare", "clipboard"]) {
    delete (navigator as unknown as Record<string, unknown>)[name];
  }
});

const TITLE = "One Big Bill";
const BILL_URL = `${window.location.origin}/bills/hr-119-1`;

function openSheet(title = TITLE) {
  render(<BillLinkShareButton billId="hr-119-1" title={title} />);
  const button = screen.getByRole("button", { name: "Share H.R. 1" });
  button.focus();
  fireEvent.click(button);
  return { button, dialog: screen.getByRole("dialog", { name: "Share this bill" }) };
}

describe("the bill's Share button", () => {
  it("opens a sheet with the bill, its link, Copy link and the eight sites", async () => {
    const { dialog } = openSheet();
    expect(dialog.textContent).toContain("H.R. 1: One Big Bill");
    expect(screen.getByLabelText("Link")).toHaveProperty("value", BILL_URL);
    expect(screen.getByRole("button", { name: "Copy link" })).toBeTruthy();
    const sites = within(screen.getByRole("list", { name: "Share on" })).getAllByRole("link");
    expect(sites.map((a) => a.textContent)).toEqual([
      "X",
      "Facebook",
      "Bluesky",
      "Threads",
      "Reddit",
      "WhatsApp",
      "LinkedIn",
      "Email",
    ]);
    // No card image to fetch or download.
    expect(screen.queryByRole("link", { name: "Download image" })).toBeNull();
    expect(fetch).not.toHaveBeenCalled();
    expect(await axeViolations(dialog)).toEqual([]);
  });

  it("opens each site's share page in a new tab with the bill's link and title, and counts it", () => {
    openSheet();
    const x = screen.getByRole("link", { name: "X" });
    expect(x.getAttribute("target")).toBe("_blank");
    expect(x.getAttribute("rel")).toBe("noopener noreferrer");
    const href = new URL(x.getAttribute("href")!);
    expect(href.searchParams.get("url")).toBe(BILL_URL);
    expect(href.searchParams.get("text")).toBe("H.R. 1: One Big Bill");
    fireEvent.click(screen.getByRole("link", { name: "Reddit" }));
    expect(track).toHaveBeenCalledWith("share", { kind: "link", channel: "reddit" });
  });

  it("shares the bill's own page from a filtered list or an experiment arm", () => {
    window.history.pushState({}, "", "/vote/v/treatment?status=passed_house&offset=20#card");
    try {
      openSheet();
      expect(screen.getByLabelText("Link")).toHaveProperty("value", BILL_URL);
    } finally {
      window.history.pushState({}, "", "/");
    }
  });

  it("uses the site's canonical origin when it's set", () => {
    vi.stubEnv("NEXT_PUBLIC_SITE_URL", "https://justabill.io");
    openSheet();
    expect(screen.getByLabelText("Link")).toHaveProperty("value", "https://justabill.io/bills/hr-119-1");
  });

  it("closes with Escape or the close button and returns focus to Share", () => {
    const { button } = openSheet();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(button);

    fireEvent.click(button);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(button);
  });

  it("cuts a very long title in the shared text", () => {
    const long = "To provide for the reconciliation of appropriations ".repeat(12).trim();
    openSheet(long);
    const text = new URL(screen.getByRole("link", { name: "X" }).getAttribute("href")!).searchParams.get("text")!;
    expect(text.length).toBeLessThanOrEqual(200);
    expect(text.endsWith("…")).toBe(true);
  });
});

describe("Copy link", () => {
  it("copies the bill's link and says so", async () => {
    openSheet();
    fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
    expect(await screen.findByText("Link copied.")).toBeTruthy();
    expect(clipboard).toEqual([BILL_URL]);
    expect(track).toHaveBeenCalledWith("share", { kind: "link", channel: "copy" });
  });

  it("says to copy the link by hand when the clipboard is denied", async () => {
    setNavigator("clipboard", {
      writeText: async () => {
        throw new DOMException("denied", "NotAllowedError");
      },
    });
    openSheet();
    fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
    expect(await screen.findByText("Couldn't copy the link. Select it above and copy it instead.")).toBeTruthy();
    expect(track).not.toHaveBeenCalled();
  });

  it("says the same in a browser with no clipboard at all", async () => {
    delete (navigator as unknown as Record<string, unknown>).clipboard;
    openSheet();
    fireEvent.click(screen.getByRole("button", { name: "Copy link" }));
    expect(await screen.findByText("Couldn't copy the link. Select it above and copy it instead.")).toBeTruthy();
  });
});

describe("Share…", () => {
  it("isn't shown without navigator.share", () => {
    openSheet();
    expect(screen.queryByRole("button", { name: "Share…" })).toBeNull();
  });

  it("shares the bill's link and title natively", async () => {
    const share = vi.fn<ShareFn>(async () => undefined);
    setNavigator("share", share);
    openSheet();
    fireEvent.click(screen.getByRole("button", { name: "Share…" }));
    expect(share).toHaveBeenCalledWith({ url: BILL_URL, text: "H.R. 1: One Big Bill" });
    await waitFor(() => expect(track).toHaveBeenCalledWith("share", { kind: "link", channel: "native" }));
  });

  it("says nothing when the visitor closes the phone's share sheet", async () => {
    const share = vi.fn<ShareFn>(async () => {
      throw new DOMException("cancelled", "AbortError");
    });
    setNavigator("share", share);
    openSheet();
    fireEvent.click(screen.getByRole("button", { name: "Share…" }));
    await waitFor(() => expect(share).toHaveBeenCalled());
    await Promise.resolve();
    expect(screen.getByRole("status").textContent).toBe("");
    expect(track).not.toHaveBeenCalled();
  });

  it("suggests copying the link when native sharing fails", async () => {
    setNavigator("share", async () => {
      throw new DOMException("denied", "NotAllowedError");
    });
    openSheet();
    fireEvent.click(screen.getByRole("button", { name: "Share…" }));
    expect(await screen.findByText("Sharing didn't work here. Copy the link instead.")).toBeTruthy();
  });
});
