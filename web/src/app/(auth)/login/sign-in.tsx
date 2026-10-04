"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { afterSignInPath } from "@/lib/auth/onboarding";
import { useUser } from "@/lib/auth/provider";
import { enabledProviders, safeNext, type ProviderId } from "@/lib/auth/providers";
import { useDeviceVotes } from "@/lib/votes/hooks";
import { ProviderIcon } from "./provider-icon";

/** The sign-in card's frame; the Suspense fallback renders it without buttons. */
export function SignInCard({ children }: { children?: React.ReactNode }) {
  return (
    <Card className="w-full max-w-md">
      <CardHeader className="text-center">
        <CardTitle className="text-2xl">Sign in to Just a Bill</CardTitle>
        <CardDescription>
          Keep your district and votes on every device. You can read, vote and compare without an
          account.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {children}
        <p className="mt-6 text-center text-xs text-muted-foreground">
          By signing in, you agree to our{" "}
          <Link href="/terms" className="underline underline-offset-2 hover:text-foreground">
            Terms of Service
          </Link>{" "}
          and{" "}
          <Link href="/privacy" className="underline underline-offset-2 hover:text-foreground">
            Privacy Policy
          </Link>
          . We never see your password, and we don&apos;t store your email or name.
        </p>
      </CardContent>
    </Card>
  );
}

/**
 * One sign-in page with a button for each provider turned on in this build (#55, #652;
 * NEXT_PUBLIC_AUTH_PROVIDERS): a popup on desktop and a redirect on phones (the auth store picks).
 * With none turned on, it says sign-in isn't available. Once signed in, it goes back to ?next=
 * (same-site paths only), through onboarding when that's due (lib/auth/onboarding.ts).
 */
export function SignIn() {
  const router = useRouter();
  const next = safeNext(useSearchParams().get("next"));
  const { status, account, signIn, signInError, retry } = useUser();
  const [pending, setPending] = useState<ProviderId | null>(null);
  const deviceVoteCount = Object.keys(useDeviceVotes()).length;
  const providers = enabledProviders();

  // A new account, or votes on this device to add, go through onboarding first (#138).
  useEffect(() => {
    if (status === "signed-in" && account) router.replace(afterSignInPath(account, next, deviceVoteCount));
  }, [status, account, next, deviceVoteCount, router]);

  if (status === "disabled" || providers.length === 0) {
    return (
      <SignInCard>
        <p className="text-center text-sm text-muted-foreground">Sign-in isn&apos;t available right now.</p>
      </SignInCard>
    );
  }

  const start = async (id: ProviderId) => {
    setPending(id);
    try {
      await signIn(id);
    } finally {
      setPending(null);
    }
  };

  const busy = status === "loading" || status === "signed-in" || pending !== null;

  return (
    <SignInCard>
      <div className="space-y-3">
        {providers.map((p) => (
          <Button
            key={p.id}
            variant="outline"
            className="w-full"
            onClick={() => void start(p.id)}
            disabled={busy}
          >
            <ProviderIcon id={p.id} className="mr-3 h-5 w-5" />
            {pending === p.id ? "Signing in..." : `Continue with ${p.label}`}
          </Button>
        ))}
      </div>

      {status === "signed-in" && (
        <p role="status" className="mt-4 text-center text-sm text-muted-foreground">
          Signed in. Taking you back...
        </p>
      )}
      {status === "error" && (
        <div role="alert" className="mt-4 text-center text-sm text-destructive">
          <p>You&apos;re signed in, but we couldn&apos;t load your account.</p>
          <Button variant="ghost" size="sm" className="mt-2" onClick={retry}>
            Try again
          </Button>
        </div>
      )}
      {signInError && (
        <p role="alert" className="mt-4 text-center text-sm text-destructive">
          {signInError}
        </p>
      )}
    </SignInCard>
  );
}
