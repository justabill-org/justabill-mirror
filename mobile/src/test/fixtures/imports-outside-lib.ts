// Imports a shared module mustn't have: web code outside src/lib, the web's server key, and a path
// that isn't there (src/__tests__/shared-imports.test.ts). Data for the walker: tsc and ESLint skip
// src/test/fixtures, since a fresh checkout has none of the web's packages.
import * as nav from "@/components/layout/skip-link";
import * as key from "#server-key";
import * as missing from "@/lib/no-such-module";
import { ApiError } from "@/lib/api";

export const fixture = { nav, key, missing, ApiError };
