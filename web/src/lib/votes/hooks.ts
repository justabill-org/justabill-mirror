"use client";

import { createContext, useContext, useSyncExternalStore } from "react";
import { createRepsStore, type LocalReps, type LocalRepsStore } from "@/lib/local/reps";
import type { LocalVote, LocalVotes } from "@/lib/local/votes";
import { localVoteBackend, localVotes, type VoteBackend, type VoteState, type VoteStorage } from "./backend";

/** Overrides the vote backend. Without a provider, votes are kept in this browser. */
export const VoteBackendContext = createContext<VoteBackend | null>(null);

let defaultBackend: VoteBackend | null = null;
let repsStore: LocalRepsStore | null = null;

export function useVoteBackend(): VoteBackend {
  const provided = useContext(VoteBackendContext);
  if (provided) return provided;
  defaultBackend ??= localVoteBackend();
  return defaultBackend;
}

/** Every vote, plus where they're kept. Empty during server render and hydration. */
export function useVotes(): VoteState {
  const backend = useVoteBackend();
  return useSyncExternalStore(backend.subscribe, backend.getSnapshot, backend.getServerSnapshot);
}

export function useVote(billId: string): { vote: LocalVote | undefined; storage: VoteStorage } {
  const { votes, storage } = useVotes();
  return { vote: votes[billId], storage };
}

/** The votes kept in this browser, whichever backend the page votes with. Empty on the server. */
export function useDeviceVotes(): LocalVotes {
  const store = localVotes();
  return useSyncExternalStore(store.subscribe, store.getSnapshot, store.getServerSnapshot).value;
}

export function localReps(): LocalRepsStore {
  repsStore ??= createRepsStore();
  return repsStore;
}

/** The visitor's saved members of Congress, or null. Null during server render. */
export function useLocalReps(): LocalReps | null {
  const store = localReps();
  return useSyncExternalStore(store.subscribe, store.getSnapshot, store.getServerSnapshot).value;
}
