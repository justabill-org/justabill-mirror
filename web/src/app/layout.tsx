import type { Metadata, Viewport } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import { PreviewBanner } from "@/components/layout/preview-banner";
import { WebAnalytics } from "@/components/layout/web-analytics";
import { analyticsEnabled } from "@/lib/analytics";
import { AuthProvider } from "@/lib/auth/provider";
import { AccountVotesProvider } from "@/lib/votes/provider";
import { siteUrl } from "@/lib/site";
import "./globals.css";

const geistSans = Geist({
  variable: "--font-geist-sans",
  subsets: ["latin"],
});

const geistMono = Geist_Mono({
  variable: "--font-geist-mono",
  subsets: ["latin"],
});

export const metadata: Metadata = {
  metadataBase: siteUrl(),
  title: "Just a Bill: what Congress is doing, in plain language",
  description:
    "Read bills in Congress in plain language, say how you would vote, and see how your representatives voted.",
  openGraph: {
    title: "Just a Bill",
    description: "Read bills in Congress in plain language, say how you would vote, and see how your representatives voted.",
    type: "website",
  },
};

export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#ffffff" },
    { media: "(prefers-color-scheme: dark)", color: "#0f172a" },
  ],
  width: "device-width",
  initialScale: 1,
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body
        className={`${geistSans.variable} ${geistMono.variable} font-sans antialiased bg-background text-foreground`}
      >
        {/* Vercel previews show example data until the API opens to them at go-public (#678). */}
        <PreviewBanner />
        {/* The session is followed in the browser; pages render the same for everyone (#74). Votes
            go to the account when signed in, else stay in this browser (#138). */}
        <AuthProvider>
          <AccountVotesProvider>{children}</AccountVotesProvider>
        </AuthProvider>
        {analyticsEnabled(process.env) && <WebAnalytics />}
      </body>
    </html>
  );
}
