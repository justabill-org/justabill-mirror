// @vitest-environment jsdom
import path from "node:path";

import { cleanup, render, screen } from "@testing-library/react";
import { ESLint } from "eslint";
import { afterEach, describe, expect, it } from "vitest";

import { SAMPLE_DATA_LABEL, SampleData, sample } from "../sample";

afterEach(cleanup);

const webRoot = path.resolve(__dirname, "../../..");

async function restrictedImports(code: string, filePath: string) {
  const eslint = new ESLint({ cwd: webRoot });
  const [result] = await eslint.lintText(code, { filePath: path.join(webRoot, filePath) });
  return result.messages.filter((m) => m.ruleId === "no-restricted-imports");
}

describe("sample", () => {
  it("returns the value unchanged", () => {
    const value = { title: "A sample bill" };
    expect(sample(value)).toBe(value);
  });
});

describe("SampleData", () => {
  it("shows a visible Sample data label with an accessible name", () => {
    render(
      <SampleData>
        <span>42 cosponsors</span>
      </SampleData>,
    );
    const note = screen.getByRole("note", { name: SAMPLE_DATA_LABEL });
    expect(note.textContent).toBe(SAMPLE_DATA_LABEL);
    expect(SAMPLE_DATA_LABEL.startsWith("Sample data")).toBe(true);
    expect(screen.getByText("42 cosponsors")).toBeTruthy();
  });
});

describe("the ESLint ban on sample data", () => {
  it.each([
    ["the alias, in a component", 'import { sample } from "@/prototype/sample";\nexport const x = sample(1);\n', "src/components/x.tsx"],
    ["a relative path, in a page", 'import { sample } from "../../prototype/sample";\nexport const x = sample(1);\n', "src/app/bills/x.tsx"],
    ["the alias, in lib/obs", 'import { sample } from "@/prototype/sample";\nexport const x = sample(1);\n', "src/lib/obs/x.ts"],
  ])("reports no-restricted-imports for %s", async (_name, code, file) => {
    const messages = await restrictedImports(code, file);
    expect(messages).toHaveLength(1);
    expect(messages[0].message).toContain("prototype previews only");
  }, 30_000);

  it("still bans the OTel SDK outside lib/obs", async () => {
    const messages = await restrictedImports('import "@vercel/otel";\n', "src/components/x.tsx");
    expect(messages).toHaveLength(1);
  }, 30_000);

  it("allows other imports", async () => {
    const messages = await restrictedImports('import { cn } from "@/lib/utils";\nexport const x = cn;\n', "src/components/x.tsx");
    expect(messages).toHaveLength(0);
  }, 30_000);
});
