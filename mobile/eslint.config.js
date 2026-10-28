// eslint-config-expo, plus every rule of eslint-plugin-react-native-a11y (the design's Testing section).
const { defineConfig } = require("eslint/config");
const expoConfig = require("eslint-config-expo/flat");
const a11y = require("eslint-plugin-react-native-a11y");

module.exports = defineConfig([
  expoConfig,
  {
    files: ["**/*.{js,jsx,ts,tsx}"],
    plugins: { "react-native-a11y": a11y },
    rules: a11y.configs.all.rules,
  },
  { ignores: ["dist/*", ".expo/*", "coverage/*", "src/test/fixtures/*"] },
]);
