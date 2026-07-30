#!/usr/bin/env node

"use strict";

const path = require("node:path");
const { spawnSync } = require("node:child_process");

const supportedPackages = new Map([
  ["darwin-x64", "@knowbrew/darwin-x64"],
  ["darwin-arm64", "@knowbrew/darwin-arm64"],
  ["linux-x64", "@knowbrew/linux-x64"],
  ["linux-arm64", "@knowbrew/linux-arm64"],
  ["win32-x64", "@knowbrew/win32-x64"],
  ["win32-arm64", "@knowbrew/win32-arm64"],
]);

const platformKey = `${process.platform}-${process.arch}`;
const packageName = supportedPackages.get(platformKey);

function installationError(cause) {
  const detail = cause && cause.message ? `\nCause: ${cause.message}` : "";
  return new Error(
    `knowbrew: no binary is installed for ${platformKey}. ` +
      "This platform may be unsupported, or npm optionalDependencies were omitted. " +
      'Reinstall with "npm install -g knowbrew" without --omit=optional.' +
      detail,
  );
}

if (!packageName) {
  console.error(installationError().message);
  process.exit(1);
}

let packagePath;
try {
  packagePath = require.resolve(`${packageName}/package.json`);
} catch (error) {
  console.error(installationError(error).message);
  process.exit(1);
}

const binaryName = process.platform === "win32" ? "knowbrew.exe" : "knowbrew";
const binaryPath = path.join(path.dirname(packagePath), binaryName);
const result = spawnSync(binaryPath, process.argv.slice(2), { stdio: "inherit" });

if (result.error) {
  console.error(`knowbrew: failed to start ${binaryPath}: ${result.error.message}`);
  process.exit(1);
}

process.exit(result.status === null ? 1 : result.status);
