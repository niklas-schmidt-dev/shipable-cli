"use strict";

const fs = require("node:fs");
const path = require("node:path");

const packageRoot = path.resolve(__dirname, "..");
const packageJSON = JSON.parse(
  fs.readFileSync(path.join(packageRoot, "package.json"), "utf8")
);

const rawTag = process.argv[2] || process.env.GITHUB_REF_NAME || "";
const tag = rawTag.replace(/^refs\/tags\//, "");
const expected = `v${packageJSON.version}`;

if (tag !== expected) {
  console.error(`Release tag ${tag || "<missing>"} does not match ${expected}`);
  process.exit(1);
}

console.log(`Release tag ${tag} matches @shipable/cli ${packageJSON.version}`);
