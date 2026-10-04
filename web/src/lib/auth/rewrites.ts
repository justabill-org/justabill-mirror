/**
 * Proxies Firebase's sign-in helper pages (/__/auth/handler, /__/auth/iframe) through our own
 * domain, so redirect sign-in works in browsers that block third-party storage (Firebase's
 * "Option 3"; the providers' redirect URIs are https://<domain>/__/auth/handler). It must be a
 * rewrite, not a redirect: the pages have to be served from the app's own origin, path unchanged.
 * None when sign-in is off in the build (no project ID).
 */
export function firebaseAuthRewrites(projectId: string | undefined) {
  if (!projectId) return [];
  return [
    {
      source: "/__/auth/:path*",
      destination: `https://${projectId}.firebaseapp.com/__/auth/:path*`,
    },
  ];
}
