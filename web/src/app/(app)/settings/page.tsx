import { notFound } from "next/navigation";
import { accountsEnabled } from "@/lib/accounts";
import { AccountSettings } from "./account-settings";

// Static: the account loads in the browser (AccountSettings), so the server never reads a cookie
// or a token here (#74, #137).
export default function SettingsPage() {
  // Settings are an account screen: with accounts off (#215) they don't exist.
  if (!accountsEnabled()) notFound();

  return (
    <div className="mx-auto max-w-2xl px-4 py-8 sm:px-6 lg:px-8">
      <div className="mb-8">
        <h1 className="text-2xl font-semibold tracking-tight text-foreground sm:text-3xl">Settings</h1>
        <p className="mt-2 text-muted-foreground">Manage your account and your district.</p>
      </div>
      <AccountSettings />
    </div>
  );
}
