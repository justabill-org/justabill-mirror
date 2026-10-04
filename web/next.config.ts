import type { NextConfig } from "next";

import { signInConfig } from "./src/lib/auth/config";
import { firebaseAuthRewrites } from "./src/lib/auth/rewrites";
import { assertVercelBuildEnv } from "./src/lib/build-env";
import { securityHeaders } from "./src/lib/security-headers";
import { cardTracingIncludes } from "./src/lib/share-assets";

assertVercelBuildEnv(process.env);

const nextConfig: NextConfig = {
  // For web/Dockerfile and compose; Vercel's builder uses its own output.
  output: "standalone",
  poweredByHeader: false,
  // The share card images read their Geist TTF files and the logo from disk at runtime (#88, #706):
  // every route that renders the card gets them in its function bundle.
  outputFileTracingIncludes: cardTracingIncludes(),
  // Rep cards show members' Congress.gov photos (#667) through next/image, which fetches, resizes
  // and caches them, so browsers load them from our origin and the CSP's img-src stays 'self'.
  // Only member images: MEMBER_PHOTO_ORIGIN and MEMBER_PHOTO_PATH in src/lib/scorecard.ts.
  images: {
    // The object form, which reads plainly; Next 16 also accepts `new URL(...)`.
    remotePatterns: [{ protocol: "https", hostname: "www.congress.gov", pathname: "/img/member/**", search: "" }],
    // Each photo is a few KB and changes once a term; the default (4 hours) refetches it daily.
    minimumCacheTTL: 2678400, // 31 days
  },
  // Static headers (no nonce) so cached and static pages keep working (#74). Not on Firebase's
  // sign-in helper pages under /__/auth/: they come from Firebase with their own headers, and
  // ours (frame-ancestors 'none', X-Frame-Options DENY) would block the SDK's iframe.
  async headers() {
    return [{ source: "/:path((?!__/auth/).*)", headers: securityHeaders(process.env) }];
  },
  // Redirect sign-in on our own domain (#55, #137): proxy, never redirect, /__/auth/* to Firebase.
  async rewrites() {
    return firebaseAuthRewrites(signInConfig()?.projectId);
  },
};

export default nextConfig;
