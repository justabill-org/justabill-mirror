"use client";

import { useState } from "react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ApiError, deleteMe, exportMe } from "@/lib/api";
import { deleteAccount } from "@/lib/auth/delete-account";
import { errorCode } from "@/lib/auth/store";
import { signInErrorMessage } from "@/lib/auth/providers";
import type { UseUser } from "@/lib/auth/provider";
import { downloadJson } from "@/lib/download";
import type { AccountExport } from "@/lib/types";

export interface YourDataProps {
  user: Pick<UseUser, "getIdToken" | "reauthenticate" | "signOut">;
  /** For tests. */
  api?: { exportMe: (token: string) => Promise<AccountExport>; deleteMe: (token: string) => Promise<void> };
  /** For tests; defaults to a file download. */
  save?: (text: string, filename: string) => void;
}

const DEFAULT_API = { exportMe, deleteMe };

/** What a failed deletion says. A cancelled sign-in says nothing: the account is just kept. */
function deleteErrorMessage(err: unknown): string | null {
  if (err instanceof ApiError) {
    if (err.status === 502) return "Your data was deleted, but removing your sign-in failed. Please try again.";
    return "We couldn't delete your account. Please try again.";
  }
  const code = errorCode(err);
  return code ? signInErrorMessage(code) : "We couldn't delete your account. Please try again.";
}

/**
 * "Download my data" and "Delete my account" (#138, design #55): the export is exactly what the
 * API keeps (GET /me/export), and deletion removes the account, its votes and followed bills, and
 * the sign-in, right away.
 */
export function YourData({ user, api = DEFAULT_API, save = downloadJson }: YourDataProps) {
  const [downloading, setDownloading] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [message, setMessage] = useState<string | null>(null);

  const handleDownload = async () => {
    setDownloading(true);
    setMessage(null);
    try {
      const data = await api.exportMe(await user.getIdToken());
      save(JSON.stringify(data, null, 2), "just-a-bill-account.json");
    } catch (err) {
      console.error("Failed to export the account:", err);
      setMessage("We couldn't download your data. Please try again.");
    } finally {
      setDownloading(false);
    }
  };

  const handleDelete = async () => {
    setDeleting(true);
    setMessage(null);
    try {
      await deleteAccount(user, api.deleteMe);
    } catch (err) {
      console.error("Failed to delete the account:", err);
      setMessage(deleteErrorMessage(err));
      setDeleting(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">Your data</CardTitle>
        <CardDescription>
          Your account keeps your votes, your district and the bills you follow. We never store your email,
          name or address. To see, change or remove your votes, go to{" "}
          <Link href="/my-votes" className="font-medium text-foreground underline underline-offset-2">
            My votes
          </Link>
          .
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" onClick={handleDownload} disabled={downloading || deleting}>
            {downloading ? "Preparing..." : "Download my data"}
          </Button>
          {!confirming && (
            <Button
              variant="outline"
              className="text-destructive border-destructive hover:bg-destructive/10"
              onClick={() => setConfirming(true)}
            >
              Delete my account
            </Button>
          )}
        </div>

        {confirming && (
          <div role="group" aria-labelledby="delete-account-heading" className="space-y-3 rounded-lg border border-destructive p-4">
            <h3 id="delete-account-heading" className="font-medium text-foreground">
              Delete your account?
            </h3>
            <p className="text-sm text-muted-foreground">
              This deletes your votes, your district and the bills you follow, and removes your sign-in. It
              happens right away and can&apos;t be undone. Download your data first if you want a copy. You may
              be asked to sign in again to confirm it&apos;s you.
            </p>
            <div className="flex flex-wrap gap-2">
              <Button variant="destructive" onClick={handleDelete} disabled={deleting}>
                {deleting ? "Deleting..." : "Delete my account"}
              </Button>
              <Button variant="ghost" onClick={() => setConfirming(false)} disabled={deleting}>
                Cancel
              </Button>
            </div>
          </div>
        )}

        {message && (
          <p role="alert" className="text-sm text-destructive">
            {message}
          </p>
        )}
      </CardContent>
    </Card>
  );
}
