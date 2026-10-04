// @vitest-environment jsdom
import { isValidElement, type ReactElement } from "react";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { billList } from "../examples";
import { createVoteStore } from "../local/votes";
import type { LocalEnv } from "../local/storage";
import type { DeckCard } from "../vote-deck";
import { localVoteBackend, type VoteBackend } from "../votes/backend";
import { VoteBackendContext } from "../votes/hooks";
import { deckOf } from "@/test/deck";

// The A/A run on /vote (#695, docs/design/580-ab-experiments.md): the treatment route is /vote
// itself, both arms count an exposure, and a first vote (never a skip) counts the conversion.
// The run's end PR deletes this file with the route, the registry entry and src/proxy.ts.

vi.mock("../api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api")>()),
  listBillsWithCards: vi.fn(() => Promise.reject(new Error("no API in the build"))),
  listCongresses: vi.fn(() => Promise.reject(new Error("no API in the build"))),
}));

const trackConversion = vi.fn();
vi.mock("../experiments/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../experiments/client")>()),
  trackConversion: (id: string) => trackConversion(id),
}));

const { ExperimentExposure } = await import("../experiments/client");
const { findExperiment, VOTE_AA } = await import("../experiments/registry");
const votePage = await import("@/app/(app)/vote/page");
const treatment = await import("@/app/(app)/vote/v/[variant]/page");
const { VotingSession } = await import("@/app/(app)/vote/voting-session");

afterEach(() => {
  cleanup();
  trackConversion.mockClear();
  vi.useRealTimers();
});

/** The first element of the given type in a server component's returned tree. */
function findElement<P>(node: unknown, type: (props: P) => unknown): ReactElement<P> | undefined {
  if (Array.isArray(node)) {
    for (const child of node) {
      const found = findElement(child, type);
      if (found) return found;
    }
    return undefined;
  }
  if (!isValidElement<{ children?: unknown }>(node)) return undefined;
  if (node.type === type) return node as unknown as ReactElement<P>;
  return findElement(node.props.children, type);
}

describe("the A/A run's arms", () => {
  it("is registered for /vote", () => {
    expect(findExperiment(VOTE_AA)?.path).toBe("/vote");
  });

  it("serves /vote itself as the treatment, prerendered, with /vote as its canonical URL", () => {
    expect(treatment.generateStaticParams()).toEqual([{ variant: "treatment" }]);
    expect(treatment.dynamicParams).toBe(false);
    expect(treatment.revalidate).toBe(votePage.revalidate);
    expect(treatment.metadata).toEqual({ ...votePage.metadata, alternates: { canonical: "/vote" } });
    expect(treatment.default().type).toBe(votePage.default);
  });

  it("counts an exposure on /vote, which both arms render", async () => {
    vi.stubEnv("NEXT_PHASE", "phase-production-build");
    vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const exposure = findElement(await votePage.default(), ExperimentExposure);
    expect(exposure?.props.id).toBe(VOTE_AA);
  });
});

describe("the A/A run's conversion", () => {
  const cards: DeckCard[] = billList.items.slice(0, 3).map((bill) => ({ bill, summary: null }));

  function session() {
    const data = new Map<string, string>();
    const env = (): LocalEnv => ({
      storage: {
        getItem: (k) => data.get(k) ?? null,
        setItem: (k, v) => void data.set(k, v),
        removeItem: (k) => void data.delete(k),
      },
      events: new EventTarget(),
    });
    const local = localVoteBackend(createVoteStore({ env, persist: () => undefined }));
    const backend: VoteBackend = { ...local, getServerSnapshot: local.getSnapshot };
    render(
      <VoteBackendContext.Provider value={backend}>
        <VotingSession {...deckOf(cards)} />
      </VoteBackendContext.Provider>
    );
  }

  it("is a vote cast, not a skip", async () => {
    vi.useFakeTimers();
    session();

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Skip" })));
    await act(async () => vi.advanceTimersByTime(1000));
    expect(trackConversion).not.toHaveBeenCalled();

    await act(async () => fireEvent.click(screen.getByRole("button", { name: "Nay" })));
    await act(async () => vi.advanceTimersByTime(1000));
    expect(trackConversion).toHaveBeenCalledWith(VOTE_AA);
  });
});
