// @vitest-environment jsdom
import { describe, it, expect } from "vitest";
import { htmlToText } from "../utils";

describe("htmlToText in the browser", () => {
  it("returns the text content, with escaped markup kept as text", () => {
    expect(htmlToText("<b>Sec. 2.</b> &lt;script&gt;alert(1)&lt;/script&gt;")).toBe(
      "Sec. 2. <script>alert(1)</script>"
    );
  });
});
