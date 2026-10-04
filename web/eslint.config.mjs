import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";

// Prototype sample data never merges (docs/design/565-prototype-review-loop.md): importing the
// helper fails lint, so a prototype has to replace every sample with real API data first.
const sampleData = {
  group: ["@/prototype/sample", "**/prototype/sample"],
  message: "Sample data is for prototype previews only; replace it with real API data before merging.",
};

const eslintConfig = defineConfig([
  ...nextVitals,
  ...nextTs,
  // Only lib/obs sets up OpenTelemetry (docs/design/53-observability.md, Decision 3): the rest of
  // the app uses the OTel API (@opentelemetry/api, api-logs) and lib/obs's helpers.
  {
    files: ["**/*.{js,mjs,ts,tsx}"],
    ignores: ["src/lib/obs/**", "**/__tests__/**", "**/*.test.{ts,tsx}"],
    rules: {
      "no-restricted-imports": [
        "error",
        {
          patterns: [
            {
              group: [
                "@vercel/otel",
                "@opentelemetry/sdk-*",
                "@opentelemetry/exporter-*",
                "@opentelemetry/resources",
                "@opentelemetry/context-*",
                "@opentelemetry/instrumentation*",
                "@opentelemetry/core",
              ],
              message: "Only src/lib/obs sets up OpenTelemetry; use @opentelemetry/api and @/lib/obs.",
            },
            sampleData,
          ],
        },
      ],
    },
  },
  // lib/obs may import the OTel SDK, but not sample data.
  {
    files: ["src/lib/obs/**/*.{js,mjs,ts,tsx}"],
    ignores: ["**/__tests__/**", "**/*.test.{ts,tsx}"],
    rules: { "no-restricted-imports": ["error", { patterns: [sampleData] }] },
  },
  // Override default ignores of eslint-config-next.
  globalIgnores([
    // Default ignores of eslint-config-next:
    ".next/**",
    "out/**",
    "build/**",
    "next-env.d.ts",
    // Vitest's HTML coverage report (npm run test:coverage).
    "coverage/**",
    // Playwright's HTML report and traces (npm run test:e2e).
    "playwright-report/**",
    "test-results/**",
  ]),
]);

export default eslintConfig;
