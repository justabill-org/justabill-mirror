import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { PreviewBanner } from "../layout/preview-banner";

const TEXT = "Preview: example data. The API opens to previews at go-public.";

describe("PreviewBanner (#678)", () => {
  it("says the page shows example data on a Vercel preview", () => {
    const html = renderToStaticMarkup(<PreviewBanner env={{ NODE_ENV: "production", VERCEL_ENV: "preview" }} />);
    expect(html).toContain(TEXT);
    expect(html).toContain('aria-label="Preview notice"');
  });

  it("renders nothing in production, under next dev or once turned off", () => {
    for (const env of [
      { NODE_ENV: "production", VERCEL_ENV: "production" },
      { NODE_ENV: "development" },
      { NODE_ENV: "production", VERCEL_ENV: "preview", PREVIEW_EXAMPLE_DATA: "off" },
    ]) {
      expect(renderToStaticMarkup(<PreviewBanner env={env} />)).toBe("");
    }
  });

  it("reads process.env by default", () => {
    vi.stubEnv("VERCEL_ENV", "preview");
    expect(renderToStaticMarkup(<PreviewBanner />)).toContain(TEXT);
    vi.stubEnv("VERCEL_ENV", "production");
    expect(renderToStaticMarkup(<PreviewBanner />)).toBe("");
  });
});
