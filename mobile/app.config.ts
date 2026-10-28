// The app's Expo config (docs/design/1091-phone-apps.md). EAS generates the native projects from
// it, so nothing under ios/ or android/ is committed.
import type { ConfigContext, ExpoConfig } from "expo/config";

/** The App Store bundle ID and the Play package name: justabill.io reversed. */
export const APP_ID = "io.justabill.app";

export default function appConfig({ config }: ConfigContext): ExpoConfig {
  return {
    ...config,
    name: "Just a Bill",
    slug: "justabill",
    scheme: "justabill",
    orientation: "portrait",
    // Light and dark follow the system setting.
    userInterfaceStyle: "automatic",
    ios: {
      bundleIdentifier: APP_ID,
      // iPhone only; on an iPad it runs scaled.
      supportsTablet: false,
      usesAppleSignIn: true,
    },
    android: {
      package: APP_ID,
    },
    plugins: ["expo-router"],
  };
}
