"use client";

import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { updateMe } from "@/lib/api";
import type { User } from "@/lib/types";

export interface CountInTotalsProps {
  user: User;
  /** A current ID token for PATCH /me (useUser().getIdToken). */
  getIdToken: () => Promise<string>;
  /** Called with the account the API answered with after a save. */
  onSaved: (user: User) => void;
  /** For tests. */
  api?: { updateMe: typeof updateMe };
}

const DEFAULT_API = { updateMe };

/**
 * "Count my votes in the published totals" (#1040, #982's plan decisions 7 and 8): switched off,
 * PATCH /me sets `agg_opted_out_at` and the aggregation job leaves the account's votes out of
 * every published total from its next run; switched on, they count again. The votes, the
 * scorecard and the district stay as they are either way.
 */
export function CountInTotals({ user, getIdToken, onSaved, api = DEFAULT_API }: CountInTotalsProps) {
  const [counted, setCounted] = useState(user.agg_opted_out_at == null);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState("");
  const [error, setError] = useState("");

  const handleChange = async (next: boolean) => {
    if (saving) return;
    setCounted(next);
    setSaving(true);
    setSaved("");
    setError("");
    try {
      const account = await api.updateMe(await getIdToken(), { count_in_totals: next });
      onSaved(account);
      setSaved(
        next
          ? "Your votes will count in the published totals from their next update."
          : "Your votes will be left out of the published totals from their next update."
      );
    } catch (err) {
      console.error("Failed to save count_in_totals:", err);
      setCounted(!next);
      setError("We couldn't save this setting. Please try again.");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">Published totals</CardTitle>
        <CardDescription>
          Just a Bill publishes rounded totals of how signed-in users voted on each bill, which never show who
          voted. Your votes and your scorecard stay the same either way.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-3">
        <label className="flex cursor-pointer items-start gap-3">
          <input
            type="checkbox"
            checked={counted}
            onChange={(e) => handleChange(e.target.checked)}
            aria-describedby="count-in-totals-note"
            className="mt-0.5 h-5 w-5 shrink-0 rounded border-border accent-primary"
          />
          <span className="text-sm font-medium text-foreground">Count my votes in the published totals</span>
        </label>
        <p id="count-in-totals-note" className="text-xs text-muted-foreground">
          A change applies from the next update of the totals, and you can change it back at any time.
        </p>
        {/* Always in the page, so a screen reader announces the message when it appears. */}
        <Alert variant="success">{saved}</Alert>
        <Alert variant="error">{error}</Alert>
      </CardContent>
    </Card>
  );
}
