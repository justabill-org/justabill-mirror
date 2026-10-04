import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { SponsorDisplay } from "../bill/sponsor-display";
import type { SponsorEntry } from "@/lib/sponsors";

const sponsor: SponsorEntry = {
  bioguideId: "A000001",
  name: "Ada Alvarez",
  party: "D",
  state: "CA",
  district: 12,
  sponsoredDate: "2025-01-03T00:00:00Z",
  isOriginal: false,
};
const cosponsors: SponsorEntry[] = [
  {
    bioguideId: "B000002",
    name: "Ben Brooks",
    party: "R",
    state: "TX",
    sponsoredDate: "2025-01-03",
    isOriginal: true,
  },
  { bioguideId: "C000003", name: "Cora Chen", isOriginal: false },
];

describe("SponsorDisplay", () => {
  it("links every sponsor to their member page", () => {
    const html = renderToStaticMarkup(<SponsorDisplay sponsor={sponsor} cosponsors={cosponsors} />);
    expect(html).toContain('href="/members/A000001"');
    expect(html).toContain('href="/members/B000002"');
    expect(html).toContain('href="/members/C000003"');
    expect(html).toContain("Cosponsors (2)");
    expect(html).toContain("Democrat - CA-12");
    expect(html).toContain("Republican - TX");
  });

  it("shows cosponsor dates as calendar dates, marking original cosponsors", () => {
    const html = renderToStaticMarkup(<SponsorDisplay sponsor={sponsor} cosponsors={cosponsors} />);
    expect(html).toContain("Added Jan 3, 2025 (Original)");
    // The sponsor's own date isn't shown as "Added".
    expect(html.match(/Added/g)).toHaveLength(1);
  });

  it("leaves out party and state when they are unknown", () => {
    const html = renderToStaticMarkup(<SponsorDisplay cosponsors={[cosponsors[1]]} />);
    expect(html).toContain("Cora Chen");
    expect(html).not.toContain(" - ");
    expect(html).not.toContain(">Sponsor<");
  });

  it("says when there is no sponsor information", () => {
    const html = renderToStaticMarkup(<SponsorDisplay cosponsors={[]} />);
    expect(html).toContain("No sponsor information available.");
  });
});
