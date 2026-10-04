// Tests for license-check.mjs: node --test .github/scripts/license-check.test.mjs
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";

import { checkGoReport, checkNpmLock, compilePolicy, parseSPDX } from "./license-check.mjs";

const policy = () =>
  compilePolicy({
    allow: ["MIT", "Apache-2.0", "BSD-3-Clause"],
    allowUnmodified: { licenses: ["MPL-2.0"], reason: "unmodified" },
    exceptions: [
      { ecosystem: "npm", packages: ["@img/sharp-libvips-*"], license: "LGPL-3.0-or-later", reason: "linked" },
      { ecosystem: "npm", packages: ["caniuse-lite"], license: "CC-BY-4.0", reason: "data" },
    ],
  });

const lock = (packages) => ({ lockfileVersion: 3, packages: { "": { name: "web" }, ...packages } });

test("parseSPDX handles AND, OR, WITH and parentheses", () => {
  assert.deepEqual(parseSPDX("MIT"), { id: "MIT" });
  assert.deepEqual(parseSPDX("(MIT OR GPL-3.0) AND Apache-2.0"), {
    op: "AND",
    left: { op: "OR", left: { id: "MIT" }, right: { id: "GPL-3.0" } },
    right: { id: "Apache-2.0" },
  });
  assert.deepEqual(parseSPDX("GPL-2.0 WITH Classpath-exception-2.0"), { id: "GPL-2.0 WITH Classpath-exception-2.0" });
  for (const bad of ["", "(MIT", "MIT AND", "MIT)", "OR MIT", "MIT WITH"]) {
    assert.throws(() => parseSPDX(bad), undefined, bad);
  }
});

test("npm: allowed, MPL-2.0 and excepted packages pass", () => {
  const got = checkNpmLock(
    policy(),
    lock({
      "node_modules/a": { version: "1.0.0", license: "MIT" },
      "node_modules/b": { version: "1.0.0", license: "(MIT OR GPL-3.0-only)", dev: true },
      "node_modules/axe-core": { version: "4.0.0", license: "MPL-2.0", dev: true },
      "node_modules/@img/sharp-libvips-linux-x64": { version: "1.2.0", license: "LGPL-3.0-or-later", optional: true },
      "node_modules/caniuse-lite": { version: "1.0.0", license: "CC-BY-4.0" },
      "node_modules/x/node_modules/c": { version: "2.0.0", license: "Apache-2.0 AND MIT" },
      "packages/workspace": { name: "workspace" },
      "node_modules/workspace": { resolved: "packages/workspace", link: true },
    }),
    "web/package-lock.json",
  );
  assert.deepEqual(got, []);
});

test("npm: a package outside the allowlist fails with its name, version and license", () => {
  const got = checkNpmLock(
    policy(),
    lock({
      "node_modules/gpl-thing": { version: "3.1.4", license: "GPL-3.0-only", dev: true },
      "node_modules/half": { version: "1.0.0", license: "MIT AND AGPL-3.0-only" },
      "node_modules/none": { version: "1.0.0" },
      "node_modules/closed": { version: "1.0.0", license: "UNLICENSED" },
      "node_modules/legacy": { version: "1.0.0", license: { type: "MIT" } },
      // The exception covers only the license it names, and only on the packages it names.
      "node_modules/@img/sharp-libvips-linux-x64": { version: "1.2.0", license: "GPL-3.0-only" },
      "node_modules/@img/other": { version: "1.0.0", license: "LGPL-3.0-or-later" },
      // An alias is checked under the real package's name.
      "node_modules/alias": { name: "real-gpl", version: "1.0.0", license: "GPL-2.0-only" },
    }),
    "web/package-lock.json",
  );
  assert.equal(got.length, 8, got.join("\n"));
  for (const want of [
    "gpl-thing@3.1.4 is licensed GPL-3.0-only; GPL-3.0-only isn't on the allowlist",
    "half@1.0.0 is licensed MIT AND AGPL-3.0-only; AGPL-3.0-only isn't",
    "none@1.0.0 has no license",
    "closed@1.0.0 has UNLICENSED",
    "legacy@1.0.0 has no license",
    "@img/sharp-libvips-linux-x64@1.2.0 is licensed GPL-3.0-only",
    "@img/other@1.0.0 is licensed LGPL-3.0-or-later",
    "real-gpl@1.0.0 is licensed GPL-2.0-only",
  ]) {
    assert.ok(
      got.some((f) => f.startsWith("web/package-lock.json: ") && f.includes(want)),
      `missing "${want}" in\n${got.join("\n")}`,
    );
  }
});

test("npm exceptions don't apply to Go packages", () => {
  const got = checkGoReport(policy(), "caniuse-lite,https://example.com/LICENSE,CC-BY-4.0\n", "api/go.mod");
  assert.equal(got.length, 1);
});

test("go: reads go-licenses CSV, and Unknown fails", () => {
  const csv = [
    "cloud.google.com/go/spanner,https://github.com/googleapis/google-cloud-go/blob/v1/LICENSE,Apache-2.0",
    "github.com/hashicorp/golang-lru/v2,https://github.com/hashicorp/golang-lru/blob/v2/LICENSE,MPL-2.0",
    "example.com/mystery,Unknown,Unknown",
    "example.com/copyleft,https://example.com/LICENSE,GPL-3.0",
    "example.com/dual,https://example.com/LICENSE,MIT",
    "example.com/dual,https://example.com/COPYING,AGPL-3.0",
    "",
  ].join("\n");
  const got = checkGoReport(policy(), csv, "api/go.mod");
  assert.deepEqual(got, [
    "api/go.mod: example.com/mystery has Unknown; read its license and add an exception to .github/license-policy.json if it's acceptable",
    "api/go.mod: example.com/copyleft is licensed GPL-3.0; GPL-3.0 isn't on the allowlist in .github/license-policy.json",
    "api/go.mod: example.com/dual is licensed AGPL-3.0; AGPL-3.0 isn't on the allowlist in .github/license-policy.json",
  ]);
});

test("an exception names the license of a package that has none, or Unknown", () => {
  const p = compilePolicy({
    allow: ["MIT"],
    exceptions: [
      { ecosystem: "go", packages: ["example.com/mystery"], license: "BSD-3-Clause", reason: "read LICENSE.txt" },
      { ecosystem: "npm", packages: ["bare"], license: "MIT", reason: "read its README" },
    ],
  });
  assert.deepEqual(checkGoReport(p, "example.com/mystery,Unknown,Unknown\n", "api/go.mod"), []);
  assert.deepEqual(checkNpmLock(p, lock({ "node_modules/bare": { version: "1.0.0" } }), "web/package-lock.json"), []);
  // Only the ecosystem and packages it names.
  assert.equal(checkGoReport(p, "example.com/other,Unknown,Unknown\n", "api/go.mod").length, 1);
  assert.equal(checkGoReport(p, "bare,Unknown,Unknown\n", "api/go.mod").length, 1);
  assert.deepEqual(
    p.exceptions.map((e) => [...e.used]),
    [["example.com/mystery"], ["bare"]],
  );
});

test("an exception without a reason is rejected", () => {
  assert.throws(
    () => compilePolicy({ allow: [], exceptions: [{ ecosystem: "npm", packages: ["x"], license: "GPL-3.0" }] }),
    /needs ecosystem, packages, license and reason/,
  );
});

test("the CLI fails on a denied package and annotates it", () => {
  const repo = new URL("../../", import.meta.url).pathname;
  const script = join(repo, ".github/scripts/license-check.mjs");
  const dir = mkdtempSync(join(tmpdir(), "license-check-"));
  const bad = join(dir, "package-lock.json");
  writeFileSync(bad, JSON.stringify(lock({ "node_modules/gpl-thing": { version: "1.0.0", license: "GPL-3.0-only" } })));

  let out = "";
  let status = 0;
  try {
    execFileSync("node", [script, "--npm", bad], { cwd: repo, encoding: "utf8" });
  } catch (err) {
    ({ status, stdout: out } = err);
  }
  assert.equal(status, 1);
  assert.match(out, /::error file=[^:]*package-lock\.json::.*gpl-thing@1\.0\.0 is licensed GPL-3\.0-only/);
});
