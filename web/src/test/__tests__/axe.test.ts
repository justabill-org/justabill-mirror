// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { axeViolations } from "../axe";

describe("axeViolations", () => {
  it("reports a real violation, so an empty result means something", async () => {
    const root = document.createElement("div");
    root.innerHTML = '<input type="text"><button type="button"></button>';
    document.body.appendChild(root);
    const violations = await axeViolations(root);
    root.remove();
    expect(violations.map((v) => v.split(":")[0]).sort()).toEqual(["button-name", "label"]);
  });
});
