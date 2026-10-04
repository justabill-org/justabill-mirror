import { readFileSync } from "node:fs";
import path from "node:path";

import { describe, it, expect } from "vitest";

// web/vercel.json overrides the dashboard. Production is deployed from release tags by the
// release deploy (#365), so a merge to main must not deploy, while every other
// branch keeps its preview.
describe("vercel.json", () => {
  const config = JSON.parse(readFileSync(path.resolve(__dirname, "../../../vercel.json"), "utf8"));

  it("doesn't deploy main", () => {
    expect(config.git.deploymentEnabled).toEqual({ main: false });
  });

  it("keeps the region and the ignored build step", () => {
    expect(config.regions).toEqual(["iad1"]);
    expect(config.ignoreCommand).toBe("sh scripts/vercel-ignore.sh");
  });
});
