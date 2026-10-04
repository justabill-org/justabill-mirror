import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { StatusPage } from "../layout/status-page";
import { ErrorPage } from "../layout/error-page";
import RootNotFound from "@/app/not-found";
import AppNotFound from "@/app/(app)/not-found";
import BillNotFound from "@/app/(app)/bills/[id]/not-found";
import RootError from "@/app/error";
import AppError from "@/app/(app)/error";
import GlobalError from "@/app/global-error";
import VoteLoading from "@/app/(app)/vote/loading";
import ScorecardLoading from "@/app/(app)/scorecard/loading";

describe("StatusPage", () => {
  it("links home and to the bills by default", () => {
    const html = renderToStaticMarkup(<StatusPage code="404" title="Gone" description="Nothing here." />);
    expect(html).toContain("<h1");
    expect(html).toContain("Gone");
    expect(html).toContain('href="/"');
    expect(html).toContain('href="/bills"');
  });

  it("can drop the links", () => {
    const html = renderToStaticMarkup(<StatusPage code="x" title="t" description="d" links={[]} />);
    expect(html).not.toContain("<nav");
  });
});

describe("not-found pages", () => {
  it("renders the root, group and bill 404s", () => {
    expect(renderToStaticMarkup(<RootNotFound />)).toContain("Page not found");
    expect(renderToStaticMarkup(<AppNotFound />)).toContain("Not found");
    const bill = renderToStaticMarkup(<BillNotFound />);
    expect(bill).toContain("Bill not found");
    expect(bill).toContain("/bills/hr-119-1");
  });
});

describe("error pages", () => {
  const error = Object.assign(new Error("An error occurred in the Server Components render."), {
    digest: "3141592653",
  });

  it("offers a retry and shows the digest, not the message", () => {
    for (const Page of [RootError, AppError]) {
      const html = renderToStaticMarkup(<Page error={error} retry={vi.fn()} />);
      expect(html).toContain("Something went wrong");
      expect(html).toContain("Try again");
      expect(html).toContain("3141592653");
      expect(html).not.toContain("Server Components render");
    }
  });

  it("omits the reference without a digest", () => {
    const html = renderToStaticMarkup(<ErrorPage error={new Error("boom")} retry={vi.fn()} />);
    expect(html).not.toContain("Reference");
  });

  it("renders its own document for root layout failures", () => {
    const html = renderToStaticMarkup(<GlobalError error={error} retry={vi.fn()} />);
    expect(html).toMatch(/^<html lang="en">/);
    expect(html).toContain("<body");
    expect(html).toContain("<title>Something went wrong | Just a Bill</title>");
  });
});

describe("loading pages", () => {
  it("announce that the page is loading", () => {
    expect(renderToStaticMarkup(<VoteLoading />)).toContain('role="status"');
    expect(renderToStaticMarkup(<VoteLoading />)).toContain("Loading bills to vote on");
    expect(renderToStaticMarkup(<ScorecardLoading />)).toContain("Loading your scorecard");
  });
});
