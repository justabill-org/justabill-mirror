// What `#server-key` (package.json "imports") resolves to outside React Server Components: in the
// browser and in the server render of client components. It sends no key, so the web server's key
// (lib/server-key.ts) never reaches client code, not even as a module in the bundle, and neither
// does the visitor's IP.

/** Returns `init` unchanged: only server components and route handlers send the web server's key. */
export function withServerKey(init: RequestInit = {}): RequestInit {
  return init;
}

/** Returns `init` unchanged: only server components send the visitor's IP (lib/server-key.ts). */
export function withVisitor(init: RequestInit = {}): Promise<RequestInit> {
  return Promise.resolve(init);
}
