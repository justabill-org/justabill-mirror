"use client";

import { useMemo } from "react";
import { castVote, deleteVote, getMyVotes } from "@/lib/api";
import { useUser } from "@/lib/auth/provider";
import type { AuthStatus } from "@/lib/auth/store";
import { createAccountVoteBackend, type AccountVotesApi } from "./account";
import { fixedVoteBackend, localVotes, type VoteBackend } from "./backend";
import { VoteBackendContext } from "./hooks";

const ACCOUNT_API: AccountVotesApi = { getMyVotes, castVote, deleteVote };

// Fixed backends for the states in between, shared so the context value stays stable.
const PENDING = fixedVoteBackend("server");

export interface AccountVotesProviderProps {
  children: React.ReactNode;
  /** For tests: builds the signed-in backend. */
  accountBackend?: (getIdToken: () => Promise<string>) => VoteBackend;
}

/**
 * Picks where votes go (#138): the account when signed in, this browser when signed out or when
 * accounts are off. While sign-in is still being worked out, nothing can be voted, so a vote never
 * lands on the device of someone who is actually signed in, or the other way around.
 */
export function AccountVotesProvider({ children, accountBackend }: AccountVotesProviderProps) {
  const { status, account, getIdToken, retry } = useUser();
  const accountId = status === "signed-in" ? account?.id : undefined;
  const value = useMemo(() => {
    if (accountId) {
      return accountBackend
        ? accountBackend(getIdToken)
        : createAccountVoteBackend({ getIdToken, api: ACCOUNT_API, titles: () => localVotes().getSnapshot().value });
    }
    return backendFor(status, retry);
    // A backend per account: a new sign-in loads that account's votes afresh.
  }, [accountId, status, getIdToken, retry, accountBackend]);
  return <VoteBackendContext.Provider value={value}>{children}</VoteBackendContext.Provider>;
}

/** The backend when no account is loaded: null means the browser's own store. */
function backendFor(status: AuthStatus, retry: () => void): VoteBackend | null {
  switch (status) {
    case "loading":
    case "signed-in":
      return PENDING;
    case "error":
      return fixedVoteBackend("unavailable", retry);
    default:
      return null;
  }
}
