import { renderRouter, screen } from "expo-router/testing-library";

// The app's real routes, through Expo Router: the entry the bundles start from renders a screen.
describe("the app's routes", () => {
  it("open on the home screen", async () => {
    const router = await renderRouter("./app", { initialUrl: "/" });
    expect(router.getPathname()).toBe("/");
    expect(screen.getByRole("header", { name: "Just a Bill" })).toBeOnTheScreen();
  });
});
