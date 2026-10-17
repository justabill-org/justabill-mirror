import { Navbar } from "@/components/layout/navbar";
import { MAIN_ID } from "@/components/layout/skip-link";
import { Footer } from "@/components/layout/footer";
import { accountsEnabled } from "@/lib/accounts";

// No cookies or headers here: reading either would make every page in the group render per
// request. The navbar reads the signed-in user in the browser (useUser, #74).
export default function AppLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  // accountsEnabled() reads only the build-time flag, so the group stays static. With accounts off
  // (#215) the navbar has no account links.
  return (
    <div className="flex min-h-screen flex-col">
      <Navbar accounts={accountsEnabled()} />
      <main id={MAIN_ID} tabIndex={-1} className="flex-1">{children}</main>
      <Footer />
    </div>
  );
}
