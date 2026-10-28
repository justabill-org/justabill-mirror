// Metro bundles the web's shared modules from ../web/src/lib (docs/design/1091-phone-apps.md). Expo's
// Metro config resolves tsconfig.json's `paths`, so @/lib/api is src/lib/api.ts and any other @/ name
// a file under ../web/src.
const path = require("node:path");
const { getDefaultConfig } = require("expo/metro-config");

const config = getDefaultConfig(__dirname);

config.watchFolders = [...(config.watchFolders ?? []), path.resolve(__dirname, "../web/src/lib")];
// A shared module's packages (clsx, tailwind-merge) come from this module's node_modules, since
// ../web/node_modules may not be installed.
config.resolver.nodeModulesPaths = [path.resolve(__dirname, "node_modules")];

module.exports = config;
