import { afterEach, describe, expect, it, vi } from "vitest";
import { enabledProviders, providerNames, unknownProviderIds } from "../auth/providers";

// NEXT_PUBLIC_AUTH_PROVIDERS picks the sign-in buttons (#652). Production lists Google only at
// launch (#133); unset, development gets all three against the Auth emulator.

const ids = (raw: string | undefined) => enabledProviders(raw).map((p) => p.id);

describe("enabledProviders", () => {
  it.each<[string, string | undefined, string[]]>([
    ["unset: all three, for the Auth emulator", undefined, ["google.com", "apple.com", "microsoft.com"]],
    ["Google only", "google.com", ["google.com"]],
    ["in the listed order", "microsoft.com,google.com", ["microsoft.com", "google.com"]],
    ["spaces and blank entries ignored", " apple.com , ,google.com,", ["apple.com", "google.com"]],
    ["each provider once", "google.com,google.com", ["google.com"]],
    ["unknown IDs dropped", "google.com,facebook.com,Apple.com", ["google.com"]],
    ["set but empty: none", "", []],
  ])("%s", (_, raw, want) => {
    expect(ids(raw)).toEqual(want);
  });

  describe("from the build's env", () => {
    afterEach(() => vi.unstubAllEnvs());

    it("reads NEXT_PUBLIC_AUTH_PROVIDERS", () => {
      vi.stubEnv("NEXT_PUBLIC_AUTH_PROVIDERS", "google.com");
      expect(enabledProviders().map((p) => p.label)).toEqual(["Google"]);
    });
  });
});

describe("unknownProviderIds", () => {
  it.each<[string | undefined, string[]]>([
    [undefined, []],
    ["", []],
    ["google.com, microsoft.com", []],
    ["google.com,facebook.com, google", ["facebook.com", "google"]],
  ])("%j", (raw, want) => {
    expect(unknownProviderIds(raw)).toEqual(want);
  });
});

describe("providerNames", () => {
  it.each<[string, string]>([
    ["google.com", "Google"],
    ["google.com,apple.com", "Google or Apple"],
    ["google.com,microsoft.com,apple.com", "Google, Microsoft or Apple"],
  ])("%s reads %j", (raw, want) => {
    expect(providerNames(enabledProviders(raw))).toBe(want);
  });
});
