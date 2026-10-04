"use client";

import Link from "next/link";
import { useState } from "react";
import { ImportLocalVotes } from "@/components/account/import-local-votes";
import { Button, buttonClasses } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useUser } from "@/lib/auth/provider";
import { FollowedBills } from "./followed-bills";
import { SettingsForm } from "./settings-form";
import { YourData } from "./your-data";

/** The signed-in part of /settings; it reads the user in the browser only. */
export function AccountSettings() {
  const user = useUser();
  const { status, account, getIdToken, signOut, retry, setAccount } = user;
  const [signingOut, setSigningOut] = useState(false);

  if (status === "loading") {
    return (
      <div className="space-y-4" aria-busy="true" aria-label="Loading your account">
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }

  if (status === "error") {
    return (
      <Card>
        <CardContent className="py-8 text-center">
          <p className="text-muted-foreground">We couldn&apos;t load your account.</p>
          <Button variant="outline" className="mt-4" onClick={retry}>
            Try again
          </Button>
        </CardContent>
      </Card>
    );
  }

  if (status !== "signed-in" || !account) {
    return (
      <Card className="text-center">
        <CardContent className="py-16">
          <h2 className="text-lg font-semibold text-foreground">Sign in to see your settings</h2>
          <p className="mt-2 text-muted-foreground">
            You can read, vote and compare without an account. Signing in keeps your district with
            you on every device.
          </p>
          <div className="mt-6 flex justify-center">
            <Link href="/login?next=/settings" className={buttonClasses()}>
              Sign in
            </Link>
          </div>
        </CardContent>
      </Card>
    );
  }

  const handleSignOut = async () => {
    setSigningOut(true);
    try {
      await signOut();
    } finally {
      setSigningOut(false);
    }
  };

  return (
    <div className="space-y-6">
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">Your district</CardTitle>
          <CardDescription>Used to match you with your representatives</CardDescription>
        </CardHeader>
        <CardContent>
          <SettingsForm user={account} getIdToken={getIdToken} onSaved={setAccount} />
        </CardContent>
      </Card>

      <ImportLocalVotes />

      <FollowedBills user={user} />

      <YourData user={user} />

      <Card>
        <CardHeader>
          <CardTitle className="text-lg">Account</CardTitle>
        </CardHeader>
        <CardContent>
          <Button
            variant="outline"
            className="text-destructive border-destructive hover:bg-destructive/10"
            onClick={handleSignOut}
            disabled={signingOut}
          >
            {signingOut ? "Signing out..." : "Sign out"}
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}
