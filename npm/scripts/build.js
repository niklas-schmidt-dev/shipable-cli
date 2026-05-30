"use strict";

const { spawnSync } = require("node:child_process");
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");

const packageRoot = path.resolve(__dirname, "..");
const repoRoot = path.resolve(packageRoot, "..");
const distRoot = path.join(packageRoot, "dist");
const packageJSON = JSON.parse(
  fs.readFileSync(path.join(packageRoot, "package.json"), "utf8")
);

const targets = [
  { dir: "darwin-arm64", goos: "darwin", goarch: "arm64", executable: "shipable" },
  { dir: "darwin-x64", goos: "darwin", goarch: "amd64", executable: "shipable" },
  { dir: "linux-arm64", goos: "linux", goarch: "arm64", executable: "shipable" },
  { dir: "linux-x64", goos: "linux", goarch: "amd64", executable: "shipable" },
  { dir: "win32-arm64", goos: "windows", goarch: "arm64", executable: "shipable.exe" },
  { dir: "win32-x64", goos: "windows", goarch: "amd64", executable: "shipable.exe" }
];

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: repoRoot,
    env: process.env,
    stdio: "inherit",
    ...options
  });
  if (result.error) {
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(" ")} exited with ${result.status}`);
  }
}

function commandOutput(command, args) {
  const result = spawnSync(command, args, {
    cwd: repoRoot,
    encoding: "utf8",
    env: process.env
  });
  if (result.status !== 0 || result.error) {
    return "";
  }
  return result.stdout.trim();
}

function buildDate() {
  const sourceDateEpoch = process.env.SOURCE_DATE_EPOCH;
  if (sourceDateEpoch && /^\d+$/.test(sourceDateEpoch)) {
    return new Date(Number(sourceDateEpoch) * 1000).toISOString();
  }
  return new Date().toISOString();
}

function sha256(filePath) {
  return crypto.createHash("sha256").update(fs.readFileSync(filePath)).digest("hex");
}

const version = process.env.SHIPABLE_CLI_VERSION || packageJSON.version;
const commit =
  process.env.GITHUB_SHA ||
  process.env.SHIPABLE_CLI_COMMIT ||
  commandOutput("git", ["rev-parse", "--short=12", "HEAD"]) ||
  "unknown";
const builtAt = process.env.SHIPABLE_CLI_BUILD_DATE || buildDate();
const pkg = "github.com/niklas-schmidt-dev/shipable-cli/internal/shipablecli";
const ldflagParts = [
  "-s",
  "-w",
  "-X",
  `${pkg}.version=${version}`,
  "-X",
  `${pkg}.commit=${commit}`,
  "-X",
  `${pkg}.buildDate=${builtAt}`
];
// Optionally override the source-default public WorkOS device-flow client id
// and WorkOS API base for staging/test release builds. The production default is
// committed in source so source-built installs such as Homebrew work too.
const workosClientID = process.env.SHIPABLE_WORKOS_CLIENT_ID || "";
if (workosClientID) {
  ldflagParts.push("-X", `${pkg}.defaultWorkOSClientID=${workosClientID}`);
}
const workosAPIURL = process.env.SHIPABLE_WORKOS_API_URL || "";
if (workosAPIURL) {
  ldflagParts.push("-X", `${pkg}.defaultWorkOSAPIURL=${workosAPIURL}`);
}
// Optional: point the TUI's "official" backend somewhere other than the
// hardcoded https://api.shipable.de default (e.g. a staging build).
const officialAPIURL = process.env.SHIPABLE_OFFICIAL_API_URL || "";
if (officialAPIURL) {
  ldflagParts.push("-X", `${pkg}.officialAPIURL=${officialAPIURL}`);
}
const ldflags = ldflagParts.join(" ");

fs.rmSync(distRoot, { force: true, recursive: true });
fs.mkdirSync(distRoot, { recursive: true });

const sums = [];
for (const target of targets) {
  const targetDir = path.join(distRoot, target.dir);
  const outputPath = path.join(targetDir, target.executable);
  fs.mkdirSync(targetDir, { recursive: true });
  run("go", [
    "build",
    "-trimpath",
    "-buildvcs=false",
    "-ldflags",
    ldflags,
    "-o",
    outputPath,
    "./cmd/shipable"
  ], {
    env: {
      ...process.env,
      CGO_ENABLED: "0",
      GOARCH: target.goarch,
      GOOS: target.goos
    }
  });
  if (!target.executable.endsWith(".exe")) {
    fs.chmodSync(outputPath, 0o755);
  }
  const relativePath = path.relative(packageRoot, outputPath).split(path.sep).join("/");
  sums.push(`${sha256(outputPath)}  ${relativePath}`);
}

fs.writeFileSync(path.join(distRoot, "SHA256SUMS"), `${sums.sort().join("\n")}\n`);
console.log(`Built @shipable/cli ${version} for ${targets.length} targets`);
