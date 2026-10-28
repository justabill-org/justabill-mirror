import { Stack } from "expo-router";

// The shared web modules, in the bundle from the start so `expo export` proves Metro resolves their
// aliases (CI's Mobile Tests). The screens that use them replace this import.
import "../src/shared";

/** The root navigator. The tabs (Bills, Vote, My votes, Scorecard, Settings) replace it in story 2. */
export default function RootLayout() {
  return <Stack screenOptions={{ title: "Just a Bill" }} />;
}
