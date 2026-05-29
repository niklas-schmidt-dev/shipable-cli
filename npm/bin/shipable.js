#!/usr/bin/env node
"use strict";

const { spawnSync } = require("node:child_process");
const os = require("node:os");
const path = require("node:path");

const TARGETS = Object.freeze({
  "darwin-arm64": { dir: "darwin-arm64", executable: "shipable" },
  "darwin-x64": { dir: "darwin-x64", executable: "shipable" },
  "linux-arm64": { dir: "linux-arm64", executable: "shipable" },
  "linux-x64": { dir: "linux-x64", executable: "shipable" },
  "win32-arm64": { dir: "win32-arm64", executable: "shipable.exe" },
  "win32-x64": { dir: "win32-x64", executable: "shipable.exe" }
});

function platformTarget(platform = process.platform, arch = process.arch) {
  const key = `${platform}-${arch}`;
  const target = TARGETS[key];
  if (!target) {
    throw new Error(
      `Unsupported platform ${key}. Supported targets: ${Object.keys(TARGETS)
        .sort()
        .join(", ")}`
    );
  }
  return target;
}

function binaryPathFor(platform = process.platform, arch = process.arch) {
  const target = platformTarget(platform, arch);
  return path.join(__dirname, "..", "dist", target.dir, target.executable);
}

function exitCodeForSignal(signal) {
  const signalNumber = os.constants.signals?.[signal];
  if (Number.isInteger(signalNumber)) {
    return 128 + signalNumber;
  }
  return 1;
}

function run(argv = process.argv.slice(2), options = {}) {
  const binary = options.binary || binaryPathFor(options.platform, options.arch);
  const result = spawnSync(binary, argv, { stdio: "inherit" });
  if (result.error) {
    console.error(`shipable: ${result.error.message}`);
    return 1;
  }
  if (result.signal) {
    return exitCodeForSignal(result.signal);
  }
  return result.status ?? 1;
}

if (require.main === module) {
  process.exit(run());
}

module.exports = {
  binaryPathFor,
  platformTarget,
  run
};
