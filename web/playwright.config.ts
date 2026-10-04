import path from "node:path";
import { defineConfig, devices } from "@playwright/test";
import { authEmulatorTopology } from "./e2e/auth-emulator";
import { EXPERIMENTS_NOW } from "./e2e/helpers";

// Browser smoke tests (docs/design/84-e2e-smoke-tests.md): the production web build against the
// real API on a Spanner emulator database seeded by db/cmd/e2e-seed. `task e2e` builds and seeds
// everything and runs them; CI's Web E2E job does the same in the Playwright image.
//
// Build the web app first with NEXT_PUBLIC_API_URL set to the API URL below: Next.js inlines it.

const CI = !!process.env.CI;
const env = (key: string, fallback: string) => process.env[key] || fallback;

// Not 8080 and 3000, so a dev API or `next dev` on the same machine doesn't collide with the run.
const API_PORT = env("E2E_API_PORT", "18080");
const WEB_PORT = env("E2E_WEB_PORT", "13000");
const API_URL = `http://localhost:${API_PORT}`;
const WEB_URL = `http://localhost:${WEB_PORT}`;
// The Census geocoder stub (e2e/stubs/census.mjs), which answers every address with CA-12.
const CENSUS_PORT = env("E2E_CENSUS_PORT", "18089");
const CENSUS_URL = `http://localhost:${CENSUS_PORT}`;
// The OTLP metrics stub (e2e/stubs/otlp.mjs), which the web server exports its metrics to.
const OTLP_PORT = env("E2E_OTLP_PORT", "18090");
const OTLP_URL = `http://localhost:${OTLP_PORT}`;

// The Firebase Auth emulator, for the sign-in tests (e2e/sign-in.spec.ts, `task e2e:sign-in`). When
// it's set the API runs with accounts on and verifies the emulator's tokens; the web build must
// then have accounts on too (NEXT_PUBLIC_ACCOUNTS_ENABLED and the NEXT_PUBLIC_FIREBASE_* values).
const AUTH_EMULATOR = process.env.E2E_AUTH_EMULATOR_HOST;
// The browser reaches the emulator at the host inlined in the web build, which must be on the pages'
// own site (#794: popup sign-ins stall with a cross-site emulator), or the run stops here. When the
// API's emulator is elsewhere (CI: auth-emulator:9099, another container), e2e/forward.mjs listens on
// the browser's localhost port and passes connections on to it.
const AUTH_TOPOLOGY = AUTH_EMULATOR
  ? authEmulatorTopology(AUTH_EMULATOR, process.env.NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST, WEB_URL)
  : undefined;
const authForwarder = AUTH_TOPOLOGY?.forward
  ? [
      {
        name: "auth-emulator",
        command: `node e2e/forward.mjs ${AUTH_TOPOLOGY.forward.port} ${AUTH_TOPOLOGY.forward.target}`,
        // The emulator's root answers once the forwarder passes connections on.
        url: `http://${AUTH_TOPOLOGY.browserHost}/`,
        reuseExistingServer: false,
        timeout: 20_000,
      },
    ]
  : [];
const accountsEnv: Record<string, string> = AUTH_EMULATOR
  ? {
      ACCOUNTS_ENABLED: "true",
      AUTH_PROJECT_ID: env("E2E_AUTH_PROJECT_ID", "demo-justabill"),
      FIREBASE_AUTH_EMULATOR_HOST: AUTH_EMULATOR,
      APP_CHECK_MODE: "off",
    }
  : { ACCOUNTS_ENABLED: "false" };

// The API binary `task e2e` and CI build (go build -o api/bin/e2e-api ./cmd/server). It runs from
// its own directory: the API reads .env from its working directory and the parent, and the
// repo's .env (real keys, the dev database, Redis) must not reach it.
const API_BIN = path.resolve(__dirname, env("E2E_API_BIN", "../api/bin/e2e-api"));

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  forbidOnly: CI,
  retries: CI ? 1 : 0,
  // A test that only passes on its retry fails the run; the retry's trace shows why it flaked.
  failOnFlakyTests: CI,
  // In CI, "github" annotates each failed or flaky test with its error, so a job's annotations say
  // whether a test timed out or broke.
  reporter: CI ? [["list"], ["github"], ["html", { open: "never" }]] : [["list"]],
  use: {
    baseURL: WEB_URL,
    trace: "on-first-retry",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: [
    ...authForwarder,
    {
      name: "census",
      command: "node e2e/stubs/census.mjs",
      url: `${CENSUS_URL}/health`,
      reuseExistingServer: false,
      timeout: 10_000,
      env: { E2E_CENSUS_PORT: CENSUS_PORT },
    },
    {
      name: "otlp",
      command: "node e2e/stubs/otlp.mjs",
      url: `${OTLP_URL}/health`,
      reuseExistingServer: false,
      timeout: 10_000,
      env: { E2E_OTLP_PORT: OTLP_PORT },
    },
    {
      name: "api",
      command: JSON.stringify(API_BIN),
      cwd: path.dirname(API_BIN),
      url: `${API_URL}/health`,
      reuseExistingServer: false,
      timeout: 60_000,
      stdout: "pipe",
      env: {
        PORT: API_PORT,
        APP_ENV: "development",
        SPANNER_EMULATOR_HOST: env("SPANNER_EMULATOR_HOST", "localhost:9010"),
        SPANNER_PROJECT: env("SPANNER_PROJECT", "justabill-local"),
        SPANNER_INSTANCE: env("SPANNER_INSTANCE", "test-instance"),
        // Its own variable: SPANNER_DATABASE is the dev database in Taskfile.yml.
        SPANNER_DATABASE: env("E2E_SPANNER_DATABASE", "justabill_e2e"),
        // No Redis: the cache is optional, and cached community counts would leak between tests.
        REDIS_URL: "",
        ...accountsEnv,
        TRUSTED_PROXY_HOPS: "0",
        CORS_ORIGIN: WEB_URL,
        // find-my-reps asks the stub, never the Census Bureau.
        CENSUS_GEOCODER_URL: `${CENSUS_URL}/geocoder/geographies/onelineaddress`,
      },
    },
    {
      name: "web",
      command: `npm run start -- --port ${WEB_PORT}`,
      url: WEB_URL,
      reuseExistingServer: false,
      timeout: 60_000,
      env: {
        NEXT_PUBLIC_API_URL: API_URL,
        NEXT_TELEMETRY_DISABLED: "1",
        // Metrics only (no traces or logs), exported every second, for e2e/metrics.spec.ts.
        OTEL_EXPORTER_OTLP_METRICS_ENDPOINT: `${OTLP_URL}/v1/metrics`,
        OTEL_METRIC_EXPORT_INTERVAL: "1000",
        // While an experiment is in the registry, its proxy assigns arms as on its first day, so a visit
        // to its page lands in either arm (e2e/experiments.spec.ts).
        JAB_EXPERIMENTS_NOW: EXPERIMENTS_NOW,
      },
    },
  ],
});
