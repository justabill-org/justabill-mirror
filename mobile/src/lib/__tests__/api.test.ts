import { isVoteCapReached } from "../../shared";
import { ApiError, getMember, getMemberPositions, getReps, getRepsAt, NotImplementedError } from "../api";

describe("ApiError", () => {
  it("reads the code from a JSON error body", () => {
    expect(new ApiError(429, "Too Many Requests", '{"code":"daily_vote_cap","message":"x"}').code).toBe("daily_vote_cap");
  });

  it.each([
    ["a body that isn't JSON", "upstream connect error"],
    ["JSON without a code", '{"message":"x"}'],
    ["a code that isn't a string", '{"code":7}'],
    ["JSON null", "null"],
  ])("has no code for %s", (_name, body) => {
    expect(new ApiError(500, "Internal Server Error", body).code).toBeUndefined();
  });

  // The shared vote backend checks `instanceof ApiError` on the class @/lib/api names: in the app,
  // that must be this one.
  it("is the class the shared modules check", () => {
    expect(isVoteCapReached(new ApiError(429, "Too Many Requests", '{"code":"daily_vote_cap"}'))).toBe(true);
    expect(isVoteCapReached(new ApiError(429, "Too Many Requests", '{"code":"rate_limited"}'))).toBe(false);
  });
});

describe("the placeholders", () => {
  it.each([
    ["getMember", () => getMember("A000360")],
    ["getMemberPositions", () => getMemberPositions("A000360", 119)],
    ["getReps", () => getReps("1600 Pennsylvania Ave NW, Washington, DC")],
    ["getRepsAt", () => getRepsAt(38.9, -77.04)],
  ])("%s rejects until the client exists", async (name, call) => {
    await expect(call()).rejects.toThrow(new NotImplementedError(name));
  });
});
