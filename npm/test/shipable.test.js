"use strict";

const assert = require("node:assert/strict");
const path = require("node:path");
const test = require("node:test");

const shim = require("../bin/shipable.js");

test("maps supported Node platforms to bundled binaries", () => {
  assert.deepEqual(shim.platformTarget("linux", "x64"), {
    dir: "linux-x64",
    executable: "shipable"
  });
  assert.deepEqual(shim.platformTarget("darwin", "arm64"), {
    dir: "darwin-arm64",
    executable: "shipable"
  });
  assert.deepEqual(shim.platformTarget("win32", "x64"), {
    dir: "win32-x64",
    executable: "shipable.exe"
  });
});

test("rejects unsupported platforms", () => {
  assert.throws(
    () => shim.platformTarget("freebsd", "x64"),
    /Unsupported platform freebsd-x64/
  );
});

test("resolves binary path inside package dist directory", () => {
  const binary = shim.binaryPathFor("linux", "arm64");
  assert.equal(
    binary,
    path.resolve(__dirname, "..", "dist", "linux-arm64", "shipable")
  );
});
