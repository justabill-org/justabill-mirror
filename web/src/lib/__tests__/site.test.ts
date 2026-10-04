import { describe, it, expect } from "vitest";
import { siteUrl } from "../site";

describe("siteUrl", () => {
  it("parses an http(s) URL", () => {
    expect(siteUrl("https://justabill.example")?.host).toBe("justabill.example");
    expect(siteUrl("http://localhost:3000")?.origin).toBe("http://localhost:3000");
  });

  it.each(["", "justabill.example", "ftp://justabill.example", "javascript:alert(1)"])(
    "returns undefined for %j",
    (value) => {
      expect(siteUrl(value)).toBeUndefined();
    }
  );
});
