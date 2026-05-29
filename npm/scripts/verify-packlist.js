"use strict";

const { spawnSync } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

const packageRoot = path.resolve(__dirname, "..");
const packageJSON = JSON.parse(
  fs.readFileSync(path.join(packageRoot, "package.json"), "utf8")
);

const allowedFiles = new Set([
  "README.md",
  "bin/shipable.js",
  "dist/SHA256SUMS",
  "dist/darwin-arm64/shipable",
  "dist/darwin-x64/shipable",
  "dist/linux-arm64/shipable",
  "dist/linux-x64/shipable",
  "dist/win32-arm64/shipable.exe",
  "dist/win32-x64/shipable.exe",
  "LICENSE",
  "package.json"
]);

const deniedPatterns = [
  /(^|\/)\.env($|[./])/,
  /(^|\/)\.git($|\/)/,
  /(^|\/)\.npmrc$/,
  /(^|\/)\.netrc$/,
  /(^|\/)config\.json$/,
  /(^|\/)credentials(\.json)?$/,
  /(^|\/)id_(rsa|dsa|ecdsa|ed25519)$/,
  /\.(key|pem|p12|pfx|crt|cer)$/i
];

const lifecycleScripts = [
  "preinstall",
  "install",
  "postinstall",
  "prepublish",
  "prepublishOnly",
  "prepare",
  "prepack",
  "postpack",
  "publish",
  "postpublish"
];

const dependencyFields = [
  "dependencies",
  "devDependencies",
  "optionalDependencies",
  "peerDependencies",
  "bundleDependencies",
  "bundledDependencies"
];

function fail(message) {
  console.error(message);
  process.exit(1);
}

for (const scriptName of lifecycleScripts) {
  if (packageJSON.scripts?.[scriptName]) {
    fail(`Refusing package lifecycle script: ${scriptName}`);
  }
}

for (const field of dependencyFields) {
  const value = packageJSON[field];
  if (Array.isArray(value) ? value.length > 0 : value && Object.keys(value).length > 0) {
    fail(`Refusing npm dependency field: ${field}`);
  }
}

const result = spawnSync("npm", ["pack", "--dry-run", "--json"], {
  cwd: packageRoot,
  encoding: "utf8"
});
if (result.error) {
  throw result.error;
}
if (result.status !== 0) {
  process.stderr.write(result.stderr);
  process.stdout.write(result.stdout);
  process.exit(result.status);
}

let pack;
try {
  pack = JSON.parse(result.stdout.trim());
} catch (error) {
  fail(`Could not parse npm pack JSON: ${error.message}\n${result.stdout}`);
}

const files = (pack[0]?.files || []).map((file) => file.path).sort();
for (const file of files) {
  if (!allowedFiles.has(file)) {
    fail(`Unexpected file in npm package: ${file}`);
  }
  if (deniedPatterns.some((pattern) => pattern.test(file))) {
    fail(`Denied file pattern in npm package: ${file}`);
  }
}

for (const file of allowedFiles) {
  if (!files.includes(file)) {
    fail(`Expected file missing from npm package: ${file}`);
  }
}

console.log(`Verified ${files.length} npm package files`);
