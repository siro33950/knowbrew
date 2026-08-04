#!/usr/bin/env node

import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";

const execute = promisify(execFile);
const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(scriptDirectory, "..");
const inputDirectory = path.resolve(repositoryRoot, process.argv[2] || "build-artifacts");
const distDirectory = path.resolve(repositoryRoot, process.argv[3] || "dist");
const version = process.env.KNOWBREW_VERSION?.trim();

if (!version || !/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/.test(version)) {
  throw new Error("KNOWBREW_VERSION must be a valid semantic version");
}

const platforms = [
  { goos: "darwin", goarch: "amd64", binary: "knowbrew", format: "tar.gz" },
  { goos: "darwin", goarch: "arm64", binary: "knowbrew", format: "tar.gz" },
  { goos: "linux", goarch: "amd64", binary: "knowbrew", format: "tar.gz" },
  { goos: "linux", goarch: "arm64", binary: "knowbrew", format: "tar.gz" },
  { goos: "windows", goarch: "amd64", binary: "knowbrew.exe", format: "zip" },
  { goos: "windows", goarch: "arm64", binary: "knowbrew.exe", format: "zip" },
];

async function copyReleaseFiles(stageDirectory, binarySource, binaryName) {
  await fs.mkdir(stageDirectory, { recursive: true });
  await Promise.all([
    fs.copyFile(path.join(repositoryRoot, "LICENSE"), path.join(stageDirectory, "LICENSE")),
    fs.copyFile(path.join(repositoryRoot, "README.ja.md"), path.join(stageDirectory, "README.ja.md")),
    fs.copyFile(path.join(repositoryRoot, "README.md"), path.join(stageDirectory, "README.md")),
    fs.copyFile(binarySource, path.join(stageDirectory, binaryName)),
  ]);
  await fs.chmod(path.join(stageDirectory, binaryName), 0o755);
}

async function archive(stageDirectory, outputPath, format, binaryName) {
  const files = ["LICENSE", "README.ja.md", "README.md", binaryName];
  if (format === "zip") {
    await execute("zip", ["-q", "-j", outputPath, ...files], {
      cwd: stageDirectory,
    });
    return;
  }
  await execute("tar", ["-czf", outputPath, "-C", stageDirectory, ...files]);
}

async function sha256(file) {
  const hash = crypto.createHash("sha256");
  hash.update(await fs.readFile(file));
  return hash.digest("hex");
}

await fs.rm(distDirectory, { recursive: true, force: true });
await fs.mkdir(distDirectory, { recursive: true });

const artifacts = [];
const archives = [];
for (const platform of platforms) {
  const source = path.join(
    inputDirectory,
    `knowbrew-${platform.goos}-${platform.goarch}`,
    platform.binary,
  );
  const binaryDirectory = path.join(
    distDirectory,
    `knowbrew_${platform.goos}_${platform.goarch}`,
  );
  const destination = path.join(binaryDirectory, platform.binary);
  await fs.mkdir(binaryDirectory, { recursive: true });
  await fs.copyFile(source, destination);
  await fs.chmod(destination, 0o755);

  artifacts.push({
    name: platform.binary,
    path: path.relative(repositoryRoot, destination),
    goos: platform.goos,
    goarch: platform.goarch,
    type: "Binary",
    extra: { ID: "knowbrew" },
  });

  const stageDirectory = path.join(
    distDirectory,
    ".archive",
    `${platform.goos}-${platform.goarch}`,
  );
  await copyReleaseFiles(stageDirectory, source, platform.binary);
  const archiveName = `knowbrew_${version}_${platform.goos}_${platform.goarch}.${platform.format}`;
  const archivePath = path.join(distDirectory, archiveName);
  await archive(stageDirectory, archivePath, platform.format, platform.binary);
  archives.push({ name: archiveName, path: archivePath });
}

await fs.rm(path.join(distDirectory, ".archive"), { recursive: true, force: true });
await fs.writeFile(
  path.join(distDirectory, "artifacts.json"),
  `${JSON.stringify({ artifacts }, null, 2)}\n`,
);
await fs.writeFile(
  path.join(distDirectory, "metadata.json"),
  `${JSON.stringify(
    {
      project_name: "knowbrew",
      version,
      tag: `v${version}`,
      commit: process.env.GITHUB_SHA || "",
    },
    null,
    2,
  )}\n`,
);

const checksums = [];
for (const item of archives.sort((left, right) => left.name.localeCompare(right.name))) {
  checksums.push(`${await sha256(item.path)}  ${item.name}`);
}
await fs.writeFile(path.join(distDirectory, "checksums.txt"), `${checksums.join("\n")}\n`);

console.log(`Assembled ${platforms.length} release targets for knowbrew ${version}`);
