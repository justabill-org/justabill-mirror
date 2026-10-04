import type { Metadata } from "next";
import { Suspense } from "react";
import { Onboarding, OnboardingFrame } from "./onboarding";

export const metadata: Metadata = {
  title: "Welcome - Just a Bill",
  robots: { index: false },
};

// The step after a first sign-in (#138): there's no separate sign-up, since signing in with any
// provider creates the account (#55). Static: Onboarding reads ?next= and the
// session in the browser, behind a Suspense boundary for useSearchParams.
export default function SignupPage() {
  return (
    <Suspense fallback={<OnboardingFrame />}>
      <Onboarding />
    </Suspense>
  );
}
