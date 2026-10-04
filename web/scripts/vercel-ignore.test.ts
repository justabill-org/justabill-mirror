import { execFileSync, spawnSync } from "node:child_process";
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

const SCRIPT = path.resolve(__dirname, "vercel-ignore.sh");
const BUILD = 1;
const SKIP = 0;

// A throwaway repo shaped like ours: the script runs from web/, as Vercel
// runs it from the project's Root Directory.
let repo: string;
let web: string;

// The environment without git's own variables. A git hook exports GIT_DIR and GIT_INDEX_FILE, and
// the pre-commit hook runs these tests: inherited, they'd aim this test's git, and the script's, at
// the real repo instead of the throwaway one.
function cleanEnv(): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...process.env };
  for (const key of Object.keys(env)) {
    if (key.startsWith("GIT_")) delete env[key];
  }
  return env;
}

function git(...args: string[]): string {
  return execFileSync("git", args, {
    cwd: repo,
    env: cleanEnv(),
    encoding: "utf8",
  }).trim();
}

function commit(file: string, content: string): string {
  const full = path.join(repo, file);
  mkdirSync(path.dirname(full), { recursive: true });
  writeFileSync(full, content);
  git("add", "-A");
  git("commit", "-q", "-m", `change ${file}`);
  return git("rev-parse", "HEAD");
}

function ignoreStep(previousSha?: string, vercelEnv?: string): number | null {
  const env = cleanEnv();
  delete env.VERCEL_GIT_PREVIOUS_SHA;
  delete env.VERCEL_ENV;
  if (previousSha !== undefined) env.VERCEL_GIT_PREVIOUS_SHA = previousSha;
  if (vercelEnv !== undefined) env.VERCEL_ENV = vercelEnv;
  return spawnSync("sh", [SCRIPT], { cwd: web, env, encoding: "utf8" }).status;
}

beforeEach(() => {
  repo = mkdtempSync(path.join(tmpdir(), "vercel-ignore-"));
  web = path.join(repo, "web");
  git("init", "-q");
  git("config", "user.email", "test@example.org");
  git("config", "user.name", "test");
  git("config", "commit.gpgsign", "false");
  commit("web/package.json", "{}");
  commit("api/main.go", "package main");
});

afterEach(() => {
  rmSync(repo, { recursive: true, force: true });
});

describe("vercel-ignore.sh", () => {
  it("builds on a branch's first deployment", () => {
    expect(ignoreStep()).toBe(BUILD);
    expect(ignoreStep("")).toBe(BUILD);
  });

  it("builds when the previous SHA is not in the clone", () => {
    expect(ignoreStep("0123456789abcdef0123456789abcdef01234567")).toBe(BUILD);
  });

  it("builds when web/ changed since the previous deployment", () => {
    const previous = git("rev-parse", "HEAD");
    commit("api/handler.go", "package main");
    commit("web/src/page.tsx", "export {}");
    expect(ignoreStep(previous)).toBe(BUILD);
  });

  it("skips when only files outside web/ changed", () => {
    const previous = git("rev-parse", "HEAD");
    commit("api/handler.go", "package main");
    commit("docs/design/x.md", "# x");
    expect(ignoreStep(previous)).toBe(SKIP);
  });

  it("builds a redeploy of the same commit", () => {
    expect(ignoreStep(git("rev-parse", "HEAD"))).toBe(BUILD);
  });

  // The release deploy's production deployments: a skip would end "Canceled"
  // and fail the apply (#531).
  it("always builds a production deployment", () => {
    const previous = git("rev-parse", "HEAD");
    commit("api/handler.go", "package main");
    expect(ignoreStep(previous, "production")).toBe(BUILD);
  });

  it("still skips a preview when only files outside web/ changed", () => {
    const previous = git("rev-parse", "HEAD");
    commit("api/handler.go", "package main");
    expect(ignoreStep(previous, "preview")).toBe(SKIP);
  });
});
