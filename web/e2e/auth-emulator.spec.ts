import { expect, test } from "@playwright/test";
import { authEmulatorTopology } from "./auth-emulator";

// playwright.config.ts's check of where the browser reaches the Auth emulator (#794). No browser:
// the config runs it before any test, so these cases are the guard's only coverage.

const WEB_URL = "http://localhost:13000";

test.describe("authEmulatorTopology", () => {
  test("forwards the browser's localhost port to an emulator in another container (CI)", () => {
    expect(authEmulatorTopology("auth-emulator:9099", "localhost:9099", WEB_URL)).toEqual({
      browserHost: "localhost:9099",
      forward: { port: "9099", target: "auth-emulator:9099" },
    });
  });

  test("starts no forwarder when the API's emulator is on localhost too (task e2e:sign-in)", () => {
    expect(authEmulatorTopology("localhost:9099", "localhost:9099", WEB_URL)).toEqual({
      browserHost: "localhost:9099",
    });
  });

  test("stops the run when the browser's emulator is on another site than the pages", () => {
    for (const browserHost of ["auth-emulator:9099", "127.0.0.1:9099"]) {
      expect(() => authEmulatorTopology("auth-emulator:9099", browserHost, WEB_URL)).toThrow(
        /another site than the pages \(localhost\).*issues\/794/,
      );
    }
  });

  test("stops the run when the build's emulator host isn't given", () => {
    for (const browserHost of [undefined, "", " "]) {
      expect(() => authEmulatorTopology("auth-emulator:9099", browserHost, WEB_URL)).toThrow(
        /NEXT_PUBLIC_FIREBASE_AUTH_EMULATOR_HOST is not set/,
      );
    }
  });

  test("stops the run when the browser and the API would use different emulators on localhost", () => {
    expect(() => authEmulatorTopology("localhost:9099", "localhost:9100", WEB_URL)).toThrow(/different emulators/);
  });

  test("rejects a host without a port, or with a scheme or path", () => {
    for (const bad of ["localhost", "http://localhost:9099", "localhost:9099/x"]) {
      expect(() => authEmulatorTopology("auth-emulator:9099", bad, WEB_URL)).toThrow(/is not host:port/);
      expect(() => authEmulatorTopology(bad, "localhost:9099", WEB_URL)).toThrow(/is not host:port/);
    }
  });
});
