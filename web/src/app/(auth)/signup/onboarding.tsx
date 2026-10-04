"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useState } from "react";
import { ImportLocalVotes } from "@/components/account/import-local-votes";
import { Button, buttonClasses } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { setOnboarded } from "@/lib/auth/onboarding";
import { useUser } from "@/lib/auth/provider";
import { safeNext } from "@/lib/auth/providers";
import { SettingsForm } from "@/app/(app)/settings/settings-form";

/** The page's frame; the Suspense fallback renders it without the steps. */
export function OnboardingFrame({ children }: { children?: React.ReactNode }) {
  return (
    <div className="w-full max-w-xl space-y-6">
      <div className="text-center">
        <h1 className="text-2xl font-bold tracking-tight text-foreground">Welcome to Just a Bill</h1>
        <p className="mt-2 text-muted-foreground">
          Two quick steps, both optional. You can change either later in Settings.
        </p>
      </div>
      {children}
    </div>
  );
}

/**
 * Onboarding after sign-in (#138, replacing the old sign-up form): find your district (only the
 * state and district are kept, never the address), and add this device's votes to the account.
 * Continue goes back to ?next= and marks onboarding done for this account on this device.
 */
export function Onboarding() {
  const router = useRouter();
  const next = safeNext(useSearchParams().get("next"));
  const { status, account, getIdToken, setAccount, retry } = useUser();
  const [importClosed, setImportClosed] = useState(false);

  if (status === "loading") {
    return (
      <OnboardingFrame>
        <div aria-busy="true" aria-label="Loading your account" className="space-y-4">
          <Skeleton className="h-40 w-full" />
        </div>
      </OnboardingFrame>
    );
  }

  if (status === "error") {
    return (
      <OnboardingFrame>
        <Card>
          <CardContent className="py-8 text-center">
            <p role="alert" className="text-muted-foreground">
              You&apos;re signed in, but we couldn&apos;t load your account.
            </p>
            <Button variant="outline" className="mt-4" onClick={retry}>
              Try again
            </Button>
          </CardContent>
        </Card>
      </OnboardingFrame>
    );
  }

  if (status !== "signed-in" || !account) {
    return (
      <OnboardingFrame>
        <Card className="text-center">
          <CardContent className="py-12">
            <p className="text-muted-foreground">
              {status === "disabled" ? "Sign-in isn't available right now." : "Sign in first to set up your account."}
            </p>
            {status !== "disabled" && (
              <Link href={`/login?next=${encodeURIComponent(next)}`} className={buttonClasses({ className: "mt-6" })}>
                Sign in
              </Link>
            )}
          </CardContent>
        </Card>
      </OnboardingFrame>
    );
  }

  const finish = () => {
    setOnboarded(account.id);
    router.push(next);
  };

  return (
    <OnboardingFrame>
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">Find your district</CardTitle>
          <CardDescription>
            So we can show how your representatives voted. We keep only your state and district.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <SettingsForm user={account} getIdToken={getIdToken} onSaved={setAccount} />
        </CardContent>
      </Card>

      {!importClosed && <ImportLocalVotes onDecline={() => setImportClosed(true)} />}

      <div className="flex justify-end">
        <Button onClick={finish}>Continue</Button>
      </div>
    </OnboardingFrame>
  );
}
