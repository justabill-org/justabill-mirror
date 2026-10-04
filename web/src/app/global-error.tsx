"use client";

import { ErrorPage, type ErrorPageProps } from "@/components/layout/error-page";
import { Logo } from "@/components/layout/logo";
import "./globals.css";

// Errors in the root layout itself (#73). It replaces the root layout, so it brings its own
// <html>, <body> and styles; the page title comes from React's <title>. It shows the logo but not
// the full navbar (#661): this page must render even when the navbar is what failed.
export default function GlobalError(props: ErrorPageProps) {
  return (
    <html lang="en">
      <body className="font-sans antialiased bg-background text-foreground">
        <title>Something went wrong | Just a Bill</title>
        <header className="border-b border-border">
          <div className="mx-auto flex h-16 max-w-7xl items-center px-4 sm:px-6 lg:px-8">
            <Logo />
          </div>
        </header>
        <main>
          <ErrorPage {...props} />
        </main>
      </body>
    </html>
  );
}
