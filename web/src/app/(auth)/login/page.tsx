import type { Metadata } from "next";
import { Suspense } from "react";
import { SignIn, SignInCard } from "./sign-in";

export const metadata: Metadata = {
  title: "Sign in - Just a Bill",
  robots: { index: false },
};

// Static: SignIn reads ?next= and the Firebase session in the browser. useSearchParams needs the
// Suspense boundary, so the prerendered HTML holds the card without its buttons.
export default function LoginPage() {
  return (
    <Suspense fallback={<SignInCard />}>
      <SignIn />
    </Suspense>
  );
}
