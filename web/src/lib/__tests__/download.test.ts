// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { downloadJson } from "../download";

// The vote export's save (#872): the file goes through a temporary object URL, revoked once the
// link is clicked so the export doesn't stay in memory.

const created: Blob[] = [];
const createObjectURL = vi.fn((blob: Blob) => {
  created.push(blob);
  return "blob:http://localhost/export-1";
});
const revokeObjectURL = vi.fn();

afterEach(() => {
  created.length = 0;
  createObjectURL.mockClear();
  revokeObjectURL.mockClear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

/** jsdom has no object URLs: stub a URL whose statics record them (setup.ts unstubs globals). */
function stubUrls() {
  vi.stubGlobal(
    "URL",
    class extends URL {
      static createObjectURL = createObjectURL;
      static revokeObjectURL = revokeObjectURL;
    }
  );
}

describe("downloadJson", () => {
  it("saves the text as a JSON file under the name given, then revokes the URL", async () => {
    stubUrls();
    const clicked: { href: string; download: string; revokedBefore: boolean }[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push({ href: this.href, download: this.download, revokedBefore: revokeObjectURL.mock.calls.length > 0 });
    });

    downloadJson('{"votes":[]}', "just-a-bill-votes.json");

    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(created[0].type).toBe("application/json");
    expect(await created[0].text()).toBe('{"votes":[]}');
    expect(clicked).toEqual([
      { href: "blob:http://localhost/export-1", download: "just-a-bill-votes.json", revokedBefore: false },
    ]);
    expect(revokeObjectURL.mock.calls).toEqual([["blob:http://localhost/export-1"]]);
  });

  it("leaves no link in the page", () => {
    stubUrls();
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    downloadJson("{}", "x.json");
    expect(document.querySelectorAll("a")).toHaveLength(0);
  });
});
