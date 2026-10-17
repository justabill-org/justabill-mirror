import { notFound } from "next/navigation";
import { Footer } from "@/components/layout/footer";
import { Navbar } from "@/components/layout/navbar";
import { MAIN_ID } from "@/components/layout/skip-link";
import { accountsEnabled } from "@/lib/accounts";

export default function AuthLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  // Sign-in and sign-up exist only while accounts are on (#215).
  if (!accountsEnabled()) notFound();

  return (
    <div className="flex min-h-screen flex-col">
      {/* The same header as every page (#661). Accounts are on, or this layout 404s above. */}
      <Navbar accounts />

      {/* Content */}
      <main id={MAIN_ID} tabIndex={-1} className="flex flex-1 items-center justify-center px-4 py-12">
        {children}
      </main>
      <Footer />
    </div>
  );
}
