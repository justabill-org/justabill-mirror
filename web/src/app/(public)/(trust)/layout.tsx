import { Navbar } from "@/components/layout/navbar";
import { MAIN_ID } from "@/components/layout/skip-link";
import { Footer } from "@/components/layout/footer";
import { accountsEnabled } from "@/lib/accounts";

// The trust pages (#75) are static: this layout reads no cookies, so every page prerenders at build
// time. accountsEnabled() is the build-time flag and the navbar reads the session in the browser, so
// the header matches every other page's (#661).
export default function TrustLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-screen flex-col">
      <Navbar accounts={accountsEnabled()} />
      <main id={MAIN_ID} tabIndex={-1} className="flex-1">{children}</main>
      <Footer />
    </div>
  );
}
