"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useState } from "react";
import { buttonClasses } from "@/components/ui/button";
import { Logo } from "@/components/layout/logo";
import { useUser } from "@/lib/auth/provider";

const navLinks = [
  { href: "/bills", label: "Bills" },
  { href: "/vote", label: "Vote" },
  { href: "/scorecard", label: "Scorecard" },
  { href: "/my-votes", label: "My votes" },
];

interface NavbarProps {
  /** Whether to show Sign in and Settings (NEXT_PUBLIC_ACCOUNTS_ENABLED). */
  accounts?: boolean;
  /**
   * A call to action after the links (#661): the home page's "Start voting". Every page has the same
   * logo and links; only this button differs.
   */
  cta?: { href: string; label: string };
}

export function Navbar({ accounts = false, cta }: NavbarProps) {
  // Null outside the App Router (e.g. a unit test rendering the home page): no link is active.
  const pathname = usePathname() ?? "";
  // The session is read in the browser only, so the layout stays cacheable (#74). While it's
  // still loading neither link shows, so a signed-in visitor never sees "Sign in" flash.
  const { status } = useUser();
  let accountLink: "settings" | "login" | null = null;
  if (accounts && status !== "loading") {
    accountLink = status === "signed-in" || status === "error" ? "settings" : "login";
  }
  const [mobileMenuOpen, setMobileMenuOpen] = useState(false);

  return (
    <header className="sticky top-0 z-50 border-b border-border bg-background/95 backdrop-blur supports-[backdrop-filter]:bg-background/60">
      <nav aria-label="Main" className="mx-auto flex h-16 max-w-7xl items-center justify-between px-4 sm:px-6 lg:px-8">
        <Logo />

        {/* Desktop Navigation */}
        <div className="hidden items-center gap-1 md:flex">
          {navLinks.map((link) => {
            const isActive = pathname === link.href || pathname.startsWith(`${link.href}/`);
            return (
              <Link
                key={link.href}
                href={link.href}
                aria-current={isActive ? "page" : undefined}
                className={`rounded-lg px-4 py-2 text-sm font-medium transition-colors ${
                  isActive
                    ? "bg-muted text-foreground"
                    : "text-muted-foreground hover:bg-muted hover:text-foreground"
                }`}
              >
                {link.label}
              </Link>
            );
          })}
        </div>

        {/* Desktop Auth */}
        <div className="hidden items-center gap-3 md:flex">
          {accountLink === null ? null : accountLink === "settings" ? (
            <Link href="/settings" className={buttonClasses({ variant: "ghost", size: "sm" })}>
              <UserIcon className="mr-2 h-4 w-4" />
              Settings
            </Link>
          ) : (
            <Link href="/login" className={buttonClasses({ variant: "ghost", size: "sm" })}>
              Sign in
            </Link>
          )}
          {cta && (
            <Link href={cta.href} className={buttonClasses({ size: "sm" })}>
              {cta.label}
            </Link>
          )}
        </div>

        {/* Mobile Menu Button */}
        <button
          type="button"
          className="rounded-lg p-2 text-muted-foreground hover:bg-muted hover:text-foreground md:hidden"
          onClick={() => setMobileMenuOpen(!mobileMenuOpen)}
          aria-expanded={mobileMenuOpen}
          aria-label="Toggle navigation menu"
        >
          {mobileMenuOpen ? (
            <XIcon className="h-6 w-6" />
          ) : (
            <MenuIcon className="h-6 w-6" />
          )}
        </button>
      </nav>

      {/* Mobile Navigation */}
      {mobileMenuOpen && (
        <div className="border-t border-border bg-background md:hidden">
          <div className="space-y-1 px-4 py-3">
            {navLinks.map((link) => {
              const isActive = pathname === link.href || pathname.startsWith(`${link.href}/`);
              return (
                <Link
                  key={link.href}
                  href={link.href}
                  aria-current={isActive ? "page" : undefined}
                  onClick={() => setMobileMenuOpen(false)}
                  className={`block rounded-lg px-4 py-2.5 text-base font-medium transition-colors ${
                    isActive
                      ? "bg-muted text-foreground"
                      : "text-muted-foreground hover:bg-muted hover:text-foreground"
                  }`}
                >
                  {link.label}
                </Link>
              );
            })}
            {(accounts || cta) && <div className="my-2 border-t border-border" />}
            {accountLink === null ? null : accountLink === "settings" ? (
              <Link
                href="/settings"
                onClick={() => setMobileMenuOpen(false)}
                className="block rounded-lg px-4 py-2.5 text-base font-medium text-muted-foreground hover:bg-muted hover:text-foreground"
              >
                Settings
              </Link>
            ) : (
              <Link
                href="/login"
                onClick={() => setMobileMenuOpen(false)}
                className="block rounded-lg px-4 py-2.5 text-base font-medium text-muted-foreground hover:bg-muted hover:text-foreground"
              >
                Sign in
              </Link>
            )}
            {cta && (
              <Link
                href={cta.href}
                onClick={() => setMobileMenuOpen(false)}
                className="mt-2 block rounded-lg bg-primary px-4 py-2.5 text-center text-base font-medium text-primary-foreground"
              >
                {cta.label}
              </Link>
            )}
          </div>
        </div>
      )}
    </header>
  );
}

function MenuIcon({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={2}
    >
      <path strokeLinecap="round" strokeLinejoin="round" d="M4 6h16M4 12h16M4 18h16" />
    </svg>
  );
}

function XIcon({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={2}
    >
      <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
    </svg>
  );
}

function UserIcon({ className }: { className?: string }) {
  return (
    <svg
      className={className}
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={2}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M16 7a4 4 0 11-8 0 4 4 0 018 0zM12 14a7 7 0 00-7 7h14a7 7 0 00-7-7z"
      />
    </svg>
  );
}
