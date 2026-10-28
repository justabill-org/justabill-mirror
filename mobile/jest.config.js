// Jest on Linux, no device or simulator (docs/design/1091-phone-apps.md, Testing).
/** @type {import('jest').Config} */
module.exports = {
  preset: "jest-expo",
  testMatch: ["<rootDir>/{app,src}/**/*.test.{ts,tsx}"],
  // tsconfig.json's paths, in its order: the specific @/lib/api first.
  moduleNameMapper: {
    "^@/lib/api$": "<rootDir>/src/lib/api.ts",
    "^@/(.*)$": "<rootDir>/../web/src/$1",
  },
  // Shared modules under ../web/src/lib find clsx and tailwind-merge here.
  modulePaths: ["<rootDir>/node_modules"],
  collectCoverageFrom: ["app/**/*.{ts,tsx}", "src/**/*.{ts,tsx}", "!src/test/**", "!**/__tests__/**", "!**/*.d.ts"],
  coverageReporters: ["json-summary", "text", "lcov"],
  // The floors (the design's Testing section): 80% in total and 70% in each directory. CI's
  // coverage-vs-main.sh also fails a PR whose totals drop below main's.
  coverageThreshold: {
    global: { statements: 80, branches: 80, functions: 80, lines: 80 },
    "./app/": { statements: 70, branches: 70, functions: 70, lines: 70 },
    "./src/lib/": { statements: 70, branches: 70, functions: 70, lines: 70 },
  },
};
