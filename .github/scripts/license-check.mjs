#!/usr/bin/env node
// Checks every dependency's license against the allowlist in .github/license-policy.json.
//
//   node .github/scripts/license-check.mjs --npm web/package-lock.json --go db=<report.csv> ...
//   task license:check   # runs go-licenses for each Go module, then this
//
// npm: every package in the lockfile, dev and optional ones too, by its `license` field (an SPDX
// expression). Go: the CSV of `go-licenses report` (package,license URL,license), one per module.
// A license passes when it's in `allow` or `allowUnmodified`, or an exception names the package
// and that license. AND needs every part to pass, OR any one. A package with no license, or one
// go-licenses can't classify (Unknown), fails unless an exception names it: read its license and
// add an exception in a PR, whose license then stands in for the missing one.
// Exits 1 and prints a GitHub error annotation for each failure.
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";

const policyFile = ".github/license-policy.json";

/** Tokenizes and parses an SPDX license expression into a tree of {op, left, right} and {id}. */
export function parseSPDX(expr) {
  const tokens = String(expr).match(/\(|\)|[^\s()]+/g) ?? [];
  let pos = 0;
  const peek = () => tokens[pos];
  const next = () => tokens[pos++];

  function primary() {
    const tok = next();
    if (tok === undefined) throw new Error(`unexpected end of license expression "${expr}"`);
    if (tok === "(") {
      const node = or();
      if (next() !== ")") throw new Error(`unbalanced parentheses in license expression "${expr}"`);
      return node;
    }
    if (tok === ")" || /^(AND|OR|WITH)$/i.test(tok)) {
      throw new Error(`unexpected "${tok}" in license expression "${expr}"`);
    }
    // "X WITH exception" stays one identifier: it passes only if listed as written.
    if (peek()?.toUpperCase() === "WITH") {
      next();
      const exc = next();
      if (exc === undefined || exc === "(" || exc === ")") {
        throw new Error(`WITH needs an exception in license expression "${expr}"`);
      }
      return { id: `${tok} WITH ${exc}` };
    }
    return { id: tok };
  }
  function and() {
    let node = primary();
    while (peek()?.toUpperCase() === "AND") {
      next();
      node = { op: "AND", left: node, right: primary() };
    }
    return node;
  }
  function or() {
    let node = and();
    while (peek()?.toUpperCase() === "OR") {
      next();
      node = { op: "OR", left: node, right: and() };
    }
    return node;
  }

  if (tokens.length === 0) throw new Error("empty license expression");
  const tree = or();
  if (pos !== tokens.length) throw new Error(`unexpected "${tokens[pos]}" in license expression "${expr}"`);
  return tree;
}

function matches(pattern, name) {
  return pattern.endsWith("*") ? name.startsWith(pattern.slice(0, -1)) : name === pattern;
}

/** Builds the check from the policy: returns allowed(ecosystem, pkg, licenseId) and the exceptions used. */
export function compilePolicy(policy) {
  const allowed = new Set([...(policy.allow ?? []), ...(policy.allowUnmodified?.licenses ?? [])]);
  const exceptions = (policy.exceptions ?? []).map((e) => {
    if (!e.ecosystem || !e.license || !e.reason || !Array.isArray(e.packages) || e.packages.length === 0) {
      throw new Error(`${policyFile}: each exception needs ecosystem, packages, license and reason`);
    }
    return { ...e, used: new Set() };
  });
  return {
    exceptions,
    allows(ecosystem, pkg, id) {
      if (allowed.has(id)) return true;
      for (const e of exceptions) {
        if (e.ecosystem !== ecosystem || e.license !== id) continue;
        const hit = e.packages.find((p) => matches(p, pkg));
        if (hit) {
          e.used.add(hit);
          return true;
        }
      }
      return false;
    },
    /** Returns the license an exception gives a package whose own license is missing or Unknown. */
    declared(ecosystem, pkg) {
      for (const e of exceptions) {
        if (e.ecosystem !== ecosystem) continue;
        const hit = e.packages.find((p) => matches(p, pkg));
        if (hit) {
          e.used.add(hit);
          return e.license;
        }
      }
      return null;
    },
  };
}

/** Returns the license IDs that keep the expression from passing, or [] when it passes. */
export function denied(tree, allows) {
  if (tree.id) return allows(tree.id) ? [] : [tree.id];
  const l = denied(tree.left, allows);
  const r = denied(tree.right, allows);
  if (tree.op === "AND") return [...l, ...r];
  return l.length === 0 || r.length === 0 ? [] : [...l, ...r];
}

function checkOne(policy, ecosystem, where, name, version, found) {
  const label = version ? `${name}@${version}` : name;
  let license = found;
  if (typeof found !== "string" || found.trim() === "" || /^(UNLICENSED|Unknown|NONE|NOASSERTION)$/i.test(found.trim())) {
    // An exception that names the package gives the license someone read in it.
    license = policy.declared(ecosystem, name);
    if (license === null) {
      const shown = typeof found === "string" && found.trim() !== "" ? found : "no license";
      return `${where}: ${label} has ${shown}; read its license and add an exception to ${policyFile} if it's acceptable`;
    }
  }
  let tree;
  try {
    tree = parseSPDX(license);
  } catch (err) {
    return `${where}: ${label}: ${err.message}`;
  }
  const bad = denied(tree, (id) => policy.allows(ecosystem, name, id));
  if (bad.length === 0) return null;
  return `${where}: ${label} is licensed ${license}; ${[...new Set(bad)].join(", ")} isn't on the allowlist in ${policyFile}`;
}

/** Checks an npm lockfile (v2 or v3) and returns the failures. */
export function checkNpmLock(policy, lock, where) {
  const failures = new Set();
  for (const [path, pkg] of Object.entries(lock.packages ?? {})) {
    // "" is our own package; paths without node_modules/ are workspaces; links point at them.
    if (!path.includes("node_modules/") || pkg.link) continue;
    const name = pkg.name ?? path.slice(path.lastIndexOf("node_modules/") + "node_modules/".length);
    const failure = checkOne(policy, "npm", where, name, pkg.version, pkg.license);
    if (failure) failures.add(failure);
  }
  return [...failures];
}

/** Checks the CSV of `go-licenses report` (package,license URL,license) and returns the failures. */
export function checkGoReport(policy, csv, where) {
  const failures = new Set();
  for (const line of csv.split("\n")) {
    if (line.trim() === "") continue;
    const fields = line.split(",");
    if (fields.length < 3) {
      failures.add(`${where}: can't read the go-licenses line "${line}"`);
      continue;
    }
    const failure = checkOne(policy, "go", where, fields[0], "", fields.at(-1).trim());
    if (failure) failures.add(failure);
  }
  return [...failures];
}

function main(argv) {
  const policy = compilePolicy(JSON.parse(readFileSync(policyFile, "utf8")));
  const failures = [];
  const ecosystems = new Set();
  let checked = 0;
  for (let i = 0; i < argv.length; i += 2) {
    const [flag, value] = [argv[i], argv[i + 1]];
    if (value === undefined) throw new Error(`${flag} needs a value`);
    if (flag === "--npm") {
      failures.push(...checkNpmLock(policy, JSON.parse(readFileSync(value, "utf8")), value));
      ecosystems.add("npm");
    } else if (flag === "--go") {
      const eq = value.indexOf("=");
      if (eq < 1) throw new Error(`--go takes <module>=<report.csv>, got ${value}`);
      failures.push(...checkGoReport(policy, readFileSync(value.slice(eq + 1), "utf8"), `${value.slice(0, eq)}/go.mod`));
      ecosystems.add("go");
    } else {
      throw new Error(`unknown flag ${flag}; use --npm <package-lock.json> or --go <module>=<report.csv>`);
    }
    checked++;
  }
  if (checked === 0) throw new Error("nothing to check; pass --npm and --go");

  for (const e of policy.exceptions.filter((e) => ecosystems.has(e.ecosystem))) {
    for (const p of e.packages.filter((p) => !e.used.has(p))) {
      console.log(`::warning file=${policyFile}::exception ${p} (${e.license}) matched no dependency; remove it if it's no longer needed`);
    }
  }
  // Each failure starts with its manifest ("api/go.mod: ..."), which the annotation points at.
  for (const f of failures) console.log(`::error file=${f.slice(0, f.indexOf(": "))}::${f}`);
  if (failures.length > 0) {
    console.log(`${failures.length} dependencies aren't on the license allowlist in ${policyFile}`);
    return 1;
  }
  console.log(`Licenses OK: ${checked} manifests checked against ${policyFile}`);
  return 0;
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    process.exitCode = main(process.argv.slice(2));
  } catch (err) {
    console.log(`::error::license-check: ${err.message}`);
    process.exitCode = 2;
  }
}
