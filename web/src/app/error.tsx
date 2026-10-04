"use client";

import { ErrorPage, type ErrorPageProps } from "@/components/layout/error-page";
import { Footer } from "@/components/layout/footer";
import { Navbar } from "@/components/layout/navbar";
import { accountsEnabled } from "@/lib/accounts";

// Errors outside the (app) group, e.g. the home page and share pages (#73), with the same header
// and footer as every other page (#661).
export default function Error(props: ErrorPageProps) {
  return (
    <div className="flex min-h-screen flex-col">
      <Navbar accounts={accountsEnabled()} />
      <main className="flex-1">
        <ErrorPage {...props} />
      </main>
      <Footer />
    </div>
  );
}
