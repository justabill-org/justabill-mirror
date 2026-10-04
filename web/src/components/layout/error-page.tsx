"use client";

import { useEffect } from "react";
import { Button } from "@/components/ui/button";
import { reportError } from "@/lib/obs/browser";
import { StatusPage } from "./status-page";

export interface ErrorPageProps {
  /** In production, a server error arrives with a generic message and a digest, not its details. */
  error: Error & { digest?: string };
  /** Re-fetches and re-renders the failed segment. */
  retry: () => void;
}

/** The body of every error.tsx (#73): says the page failed, offers a retry and a reference. */
export function ErrorPage({ error, retry }: ErrorPageProps) {
  useEffect(() => {
    reportError(error);
  }, [error]);

  return (
    <StatusPage
      code="Error"
      title="Something went wrong"
      description="We couldn't load this page. The problem is on our end, not yours. Please try again in a moment."
    >
      <Button className="mt-8" onClick={() => retry()}>
        Try again
      </Button>
      {error.digest && (
        <p className="mt-4 text-xs text-muted-foreground">
          Reference: <code className="font-mono">{error.digest}</code>
        </p>
      )}
    </StatusPage>
  );
}
