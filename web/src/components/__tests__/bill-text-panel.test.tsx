// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import type { BillAction, BillTextDiff, BillTextVersion, GraphRelatedBill } from "@/lib/types";
import { BillTextPanel } from "../bill/bill-text-panel";
import { axeViolations } from "@/test/axe";

// The reader fetches the text from the API; the panel only decides which version it opens.
vi.mock("@/components/bill/bill-text-reader", () => ({
  BillTextReader: ({ versionId }: { versionId: string }) => <p>Reading {versionId}</p>,
}));

afterEach(cleanup);

function version(code: string, sortOrder: number, versionType: string, date: string): BillTextVersion {
  return {
    id: `v-${code}`,
    bill_id: "s-119-4530",
    version_type: versionType,
    version_code: code,
    date,
    formats: [{ type: "PDF", url: `https://www.govinfo.gov/${code}.pdf` }],
    sort_order: sortOrder,
  };
}

const es = version("es", 1, "Engrossed in Senate", "2026-05-12T00:00:00Z");
const cps = version("cps", 2, "Considered and Passed Senate", "2026-06-02T00:00:00Z");
const enr = version("enr", 3, "Enrolled Bill", "2026-07-21T00:00:00Z");
const pl = version("pl", 4, "Public Law", "2026-08-20T00:00:00Z");

const esToCps: BillTextDiff = {
  id: "d1",
  bill_id: "s-119-4530",
  from_version_id: es.id,
  to_version_id: cps.id,
  diff_stats: { sections_added: 2, sections_removed: 0, sections_modified: 1, words_added: 140, words_removed: 3 },
};

const lawActions: BillAction[] = [
  { id: "a1", bill_id: "s-119-4530", action_date: "2026-08-04T00:00:00Z", action_text: "Became Public Law No: 119-95.", sort_order: 9 },
];

const houseCompanion: GraphRelatedBill = {
  bill_id: "hr-119-8364",
  congress: 119,
  bill_type: "hr",
  number: 8364,
  title: "Sample Companion Act",
  current_status: "reported",
  relation_types: ["Related bill"],
  shared_subjects: 3,
};

type Props = Parameters<typeof BillTextPanel>[0];

function renderPanel(overrides: Partial<Props> = {}) {
  return render(
    <BillTextPanel
      billId="s-119-4530"
      billType="s"
      currentStatus="became_law"
      versions={[pl, enr, cps, es]}
      diffs={[esToCps]}
      actions={lawActions}
      related={[houseCompanion]}
      {...overrides}
    />,
  );
}

function section(name: RegExp) {
  return screen.getByRole("button", { name });
}

describe("BillTextPanel", () => {
  it("leads a law with its final text: the Public Law print, its number and the date it became law", () => {
    renderPanel();
    const lead = section(/^Final text/);
    expect(lead.getAttribute("aria-expanded")).toBe("true");
    expect(lead.textContent).toContain("Public Law 119-95");
    expect(screen.getByText("Public Law 119-95 · became law August 4, 2026")).toBeTruthy();
    expect(screen.getByText("The text of the law as enacted.")).toBeTruthy();
  });

  it("shows the number from the bill's laws, not the action text, when it has them", () => {
    renderPanel({ laws: [{ type: "Public Law", number: "119-96" }] });
    expect(section(/^Final text/).textContent).toContain("Public Law 119-96");
    expect(screen.getByText("Public Law 119-96 · became law August 4, 2026")).toBeTruthy();
    expect(screen.queryByText(/119-95/)).toBeNull();
  });

  it("labels a private law as a Private Law", () => {
    renderPanel({
      laws: [{ type: "Private Law", number: "117-3" }],
      actions: [{ ...lawActions[0], action_text: "Became Private Law No: 117-3." }],
    });
    expect(section(/^Final text/).textContent).toContain("Private Law 117-3");
    expect(screen.getByText("Private Law 117-3 · became law August 4, 2026")).toBeTruthy();
    expect(screen.queryByText(/Public Law 117-3/)).toBeNull();
  });

  it("leads with the enrolled bill until the Public Law is printed", () => {
    renderPanel({ versions: [es, cps, enr], actions: [] });
    expect(section(/^Final text/).textContent).toContain("Enrolled Bill");
    expect(
      screen.getByText("The text both chambers passed in the same words and sent to the President."),
    ).toBeTruthy();
  });

  it("opens the final text in the reader", () => {
    renderPanel();
    fireEvent.click(screen.getByRole("button", { name: "Read the final text" }));
    const reader = screen.getByRole("dialog", { name: "Public Law" });
    expect(within(reader).getByText(`Reading ${pl.id}`)).toBeTruthy();
  });

  it("leads a bill that passed one chamber with its latest version and says it isn't final", () => {
    renderPanel({ versions: [es, cps], currentStatus: "passed_senate", actions: [] });
    const lead = section(/^Latest version/);
    expect(lead.textContent).toContain("Not final");
    expect(screen.queryByRole("button", { name: /^Final text/ })).toBeNull();
    expect(screen.getByText("June 2, 2026 · Status: Passed Senate")).toBeTruthy();
    expect(screen.getByText(/may still change/)).toBeTruthy();
  });

  it("lists the versions oldest first, by chamber and date, folded to one line until opened", () => {
    renderPanel();
    const toggle = section(/^Versions/);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.textContent).toContain("4");
    expect(toggle.textContent).toContain("Passed the Senate → Became law, Aug 20, 2026");

    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    const items = within(screen.getByRole("list", { name: "Versions" })).getAllByRole("listitem");
    expect(items.map((li) => li.querySelector("p.font-medium")?.textContent)).toEqual([
      "Passed the Senate",
      "Passed the Senate",
      "Passed by both chambers",
      "Became law",
    ]);
    expect(items[0].textContent).toContain("Senate");
    expect(items[0].textContent).toContain("Engrossed in Senate · May 12, 2026");
    expect(items[2].textContent).toContain("Both chambers");
    expect(within(items[3]).getByText("Final text")).toBeTruthy();
  });

  it("shows the changes between consecutive versions, and says when a pair has no comparison", () => {
    renderPanel();
    fireEvent.click(section(/^Versions/));
    const change = screen.getByRole("button", { name: /Changes: Engrossed in Senate → Considered and Passed Senate/ });
    expect(change.getAttribute("aria-expanded")).toBe("false");
    expect(change.textContent).toContain("+2 sections");
    // cps → enr and enr → pl have no stored diff.
    expect(screen.getAllByText("No comparison with the previous version yet.")).toHaveLength(2);
  });

  it("says the changes couldn't be loaded when the diffs failed, not that there are none", () => {
    renderPanel({ diffs: null });
    fireEvent.click(section(/^Versions/));
    expect(screen.getAllByText("The changes from the previous version couldn't be loaded right now.")).toHaveLength(3);
    expect(screen.queryByText("No comparison with the previous version yet.")).toBeNull();
  });

  it("shows the other chamber's companion bills with their status, linking to them", () => {
    renderPanel();
    const toggle = section(/^Companion bills/);
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.textContent).toContain("H.R. 8364");

    fireEvent.click(toggle);
    const link = screen.getByRole("link", { name: /House companion:\s*H\.R\. 8364/ });
    expect(link.getAttribute("href")).toBe("/bills/hr-119-8364");
    // The bill number is ink with tabular numerals, not amber monospace (#764).
    const number = within(link).getByText("H.R. 8364");
    expect(number.className.split(" ")).toEqual(expect.arrayContaining(["tabular-nums", "text-foreground"]));
    expect(number.className).not.toMatch(/font-mono|text-link/);
    expect(link.textContent).toContain("Related bill");
    expect(link.textContent).toContain("Status: Reported");
  });

  it("leaves out same-chamber relations and a lone version's empty Versions list", () => {
    renderPanel({
      versions: [es],
      related: [{ ...houseCompanion, bill_id: "s-119-12", bill_type: "s", number: 12 }],
    });
    expect(screen.queryByRole("button", { name: /^Versions/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^Companion bills/ })).toBeNull();
  });

  it("names versions Congress.gov left unnamed by their official name everywhere", () => {
    const unnamedEnr = { ...enr, version_type: "" };
    const unnamedPl = { ...pl, version_type: "" };
    const enrToPl: BillTextDiff = { ...esToCps, id: "d2", from_version_id: enr.id, to_version_id: pl.id };
    renderPanel({ versions: [es, cps, unnamedEnr, unnamedPl], diffs: [esToCps, enrToPl] });

    fireEvent.click(section(/^Versions/));
    expect(screen.getByRole("button", { name: /Changes: Enrolled Bill → Public Law/ })).toBeTruthy();
    expect(screen.getAllByRole("link", { name: "PDF: Public Law, PDF on GovInfo (opens in a new tab)" })).toHaveLength(2);

    fireEvent.click(screen.getByRole("button", { name: "Read the final text" }));
    expect(screen.getByRole("dialog", { name: "Public Law" })).toBeTruthy();
  });

  it("says when there's no text yet, and still shows the companions", () => {
    renderPanel({ versions: [] });
    expect(screen.getByText("No text versions available yet.")).toBeTruthy();
    expect(section(/^Companion bills/)).toBeTruthy();
  });

  it("has no axe violations, folded or with every section open", async () => {
    const { container } = renderPanel();
    expect(await axeViolations(container)).toEqual([]);

    fireEvent.click(section(/^Versions/));
    fireEvent.click(section(/^Companion bills/));
    expect(await axeViolations(container)).toEqual([]);
  });
});
