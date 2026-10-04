import type { Metadata } from "next";
import { Footer } from "@/components/layout/footer";
import { Navbar } from "@/components/layout/navbar";
import { StatusPage } from "@/components/layout/status-page";
import { accountsEnabled } from "@/lib/accounts";

export const metadata: Metadata = {
  title: "Page not found | Just a Bill",
};

// URLs that match no route (#73). notFound() in the (app) pages uses (app)/not-found.tsx.
export default function NotFound() {
  // The same header and footer as every other page (#661).
  return (
    <div className="flex min-h-screen flex-col">
      <Navbar accounts={accountsEnabled()} />
      <main className="flex-1">
        <StatusPage
          code="404"
          title="Page not found"
          description="There's no page at this address. It may have moved, or the link may be mistyped."
        />
      </main>
      <Footer />
    </div>
  );
}
