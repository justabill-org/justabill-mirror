import { configDefaults, defineConfig } from "vitest/config";
import path from "path";

export default defineConfig({
  test: {
    environment: "node",
    // jsdom + axe tests take 3-5s on a busy CI runner, right at vitest's 5s default, and flaked
    // main (run 36728995924, rep-card's axe test). A hang still fails, just later.
    testTimeout: 20_000,
    // Files share a worker instead of each starting its own, which was most of the suite's time
    // (#564: about half). The setup file resets modules, env, globals and timers between files.
    isolate: false,
    setupFiles: ["./src/test/setup.ts"],
    // e2e/ holds the Playwright smoke tests (npm run test:e2e), which need a browser and the API.
    exclude: [...configDefaults.exclude, "e2e/**"],
    coverage: {
      provider: "v8",
      include: ["src/**/*.{ts,tsx}"],
      exclude: ["src/**/__tests__/**", "src/**/*.test.{ts,tsx}", "src/**/*.d.ts"],
      reporter: ["text-summary", "json-summary", "html"],
      // Floors, not targets (docs/design/172-testing-linting.md): the hard minimum, raised by hand
      // now and then, never in a feature PR. CI also fails a PR whose coverage drops below main's
      // (.github/scripts/coverage-vs-main.sh). The long-term target is 70% statements.
      thresholds: {
        statements: 36,
        branches: 30,
        functions: 30,
        lines: 34,
      },
    },
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
      // Next.js resolves `server-only` itself (no package to install): an empty module on the
      // server, a build error in client code. Tests run as the server, so they get the empty one.
      "server-only": path.resolve(__dirname, "node_modules/next/dist/compiled/server-only/empty.js"),
    },
  },
});
