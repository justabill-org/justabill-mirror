import { describe, it, expect } from "vitest";
import { cn, htmlToText } from "../utils";

describe("cn", () => {
  it("joins conditional class names", () => {
    expect(cn("flex", false && "hidden", undefined, "p-6")).toBe("flex p-6");
  });

  it("lets later classes override conflicting earlier ones", () => {
    expect(cn("flex flex-col space-y-2 p-6 pb-0", "p-4")).toBe(
      "flex flex-col space-y-2 p-4",
    );
    expect(cn("text-sm text-muted-foreground", "text-lg")).toBe(
      "text-muted-foreground text-lg",
    );
    expect(cn("text-foreground", "text-muted-foreground")).toBe(
      "text-muted-foreground",
    );
  });

  it("merges Tailwind 4 class names", () => {
    expect(cn("shadow-sm", "shadow-xs")).toBe("shadow-xs");
    expect(cn("rounded-sm", "rounded-xs")).toBe("rounded-xs");
    expect(cn("bg-red-500", "bg-(--brand)")).toBe("bg-(--brand)");
    expect(cn("outline-none", "outline-hidden")).toBe("outline-hidden");
  });
});

// Vitest runs in node, so there's no document: this is the server fallback. The DOMParser path
// is covered by utils-dom.test.ts.
describe("htmlToText without a document", () => {
  it("keeps an escaped script tag as text instead of stripping it into markup", () => {
    expect(htmlToText("&lt;script&gt;alert(1)&lt;/script&gt;")).toBe("<script>alert(1)</script>");
  });

  it("strips real tags before decoding, so nothing decoded is treated as a tag", () => {
    expect(htmlToText("<b>Sec. 2.</b> &lt;b&gt;bold&lt;/b&gt;")).toBe("Sec. 2. <b>bold</b>");
    expect(htmlToText("<scr<script>ipt>x")).toBe("ipt>x");
    expect(htmlToText("Sec. 3 <script")).toBe("Sec. 3");
  });

  it("decodes each entity once", () => {
    expect(htmlToText("&amp;lt; &quot;a&quot; &#39;b&#39;&nbsp;c &copy;")).toBe("&lt; \"a\" 'b' c &copy;");
  });

  it("trims whitespace", () => {
    expect(htmlToText("  <p> Title </p>  ")).toBe("Title");
  });
});
