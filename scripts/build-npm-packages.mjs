#!/usr/bin/env node

import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDirectory = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(scriptDirectory, "..");
const distDirectory = path.resolve(repositoryRoot, process.argv[2] || "dist");
const outputDirectory = path.join(distDirectory, "npm");

const platforms = [
  { goos: "darwin", goarch: "amd64", npmOS: "darwin", npmCPU: "x64" },
  { goos: "darwin", goarch: "arm64", npmOS: "darwin", npmCPU: "arm64" },
  { goos: "linux", goarch: "amd64", npmOS: "linux", npmCPU: "x64" },
  { goos: "linux", goarch: "arm64", npmOS: "linux", npmCPU: "arm64" },
  { goos: "windows", goarch: "amd64", npmOS: "win32", npmCPU: "x64" },
  { goos: "windows", goarch: "arm64", npmOS: "win32", npmCPU: "arm64" },
].map((platform) => ({
  ...platform,
  suffix: `${platform.npmOS}-${platform.npmCPU}`,
  packageName: `@knowbrew/${platform.npmOS}-${platform.npmCPU}`,
  binaryName: platform.npmOS === "win32" ? "knowbrew.exe" : "knowbrew",
}));

function normalizedVersion(metadata) {
  const raw = process.env.KNOWBREW_NPM_VERSION || metadata.version || metadata.tag;
  if (typeof raw !== "string" || raw.trim() === "") {
    throw new Error(
      "dist/metadata.json does not contain a version; set KNOWBREW_NPM_VERSION",
    );
  }
  const version = raw.trim().replace(/^v/, "");
  if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error(`cannot use ${JSON.stringify(raw)} as an npm package version`);
  }
  return version;
}

function packageMetadata(name, version, description) {
  return {
    name,
    version,
    description,
    license: "MIT",
    repository: {
      type: "git",
      url: "https://github.com/siro33950/knowbrew.git",
    },
    homepage: "https://github.com/siro33950/knowbrew",
    bugs: {
      url: "https://github.com/siro33950/knowbrew/issues",
    },
    publishConfig: {
      access: "public",
    },
  };
}

async function readJSON(file) {
  return JSON.parse(await fs.readFile(file, "utf8"));
}

async function resolveArtifactPath(artifact) {
  const candidates = path.isAbsolute(artifact.path)
    ? [artifact.path]
    : [
        path.resolve(repositoryRoot, artifact.path),
        path.resolve(distDirectory, artifact.path),
      ];
  for (const candidate of candidates) {
    try {
      const stat = await fs.stat(candidate);
      if (stat.isFile()) {
        return candidate;
      }
    } catch (error) {
      if (error.code !== "ENOENT") {
        throw error;
      }
    }
  }
  throw new Error(`GoReleaser artifact is missing: ${artifact.path}`);
}

async function writePackageJSON(directory, contents) {
  await fs.writeFile(
    path.join(directory, "package.json"),
    `${JSON.stringify(contents, null, 2)}\n`,
  );
}

const metadata = await readJSON(path.join(distDirectory, "metadata.json"));
const version = normalizedVersion(metadata);
const artifactDocument = await readJSON(path.join(distDirectory, "artifacts.json"));
const artifacts = Array.isArray(artifactDocument)
  ? artifactDocument
  : artifactDocument.artifacts;
if (!Array.isArray(artifacts)) {
  throw new Error("dist/artifacts.json does not contain an artifact list");
}
const binaries = artifacts.filter(
  (artifact) =>
    String(artifact.type).toLowerCase() === "binary" &&
    (!artifact.extra?.ID || artifact.extra.ID === "knowbrew"),
);

await fs.rm(outputDirectory, { recursive: true, force: true });
await fs.mkdir(outputDirectory, { recursive: true });

const optionalDependencies = {};
for (const platform of platforms) {
  const matches = binaries.filter(
    (artifact) =>
      artifact.goos === platform.goos && artifact.goarch === platform.goarch,
  );
  if (matches.length !== 1) {
    throw new Error(
      `expected one ${platform.goos}/${platform.goarch} binary, found ${matches.length}`,
    );
  }

  const packageDirectory = path.join(outputDirectory, `knowbrew-${platform.suffix}`);
  await fs.mkdir(packageDirectory, { recursive: true });
  const sourceBinary = await resolveArtifactPath(matches[0]);
  const destinationBinary = path.join(packageDirectory, platform.binaryName);
  await fs.copyFile(sourceBinary, destinationBinary);
  await fs.chmod(destinationBinary, 0o755);
  await fs.copyFile(
    path.join(repositoryRoot, "LICENSE"),
    path.join(packageDirectory, "LICENSE"),
  );
  await writePackageJSON(packageDirectory, {
    ...packageMetadata(
      platform.packageName,
      version,
      `knowbrew binary for ${platform.npmOS}-${platform.npmCPU}`,
    ),
    os: [platform.npmOS],
    cpu: [platform.npmCPU],
    files: [platform.binaryName, "LICENSE"],
  });
  optionalDependencies[platform.packageName] = version;
}

const mainDirectory = path.join(outputDirectory, "knowbrew");
await fs.mkdir(path.join(mainDirectory, "bin"), { recursive: true });
await fs.copyFile(
  path.join(repositoryRoot, "npm", "bin", "knowbrew.js"),
  path.join(mainDirectory, "bin", "knowbrew.js"),
);
await fs.chmod(path.join(mainDirectory, "bin", "knowbrew.js"), 0o755);
await fs.copyFile(
  path.join(repositoryRoot, "README.md"),
  path.join(mainDirectory, "README.md"),
);
await fs.copyFile(
  path.join(repositoryRoot, "LICENSE"),
  path.join(mainDirectory, "LICENSE"),
);
await writePackageJSON(mainDirectory, {
  ...packageMetadata(
    "knowbrew",
    version,
    "Brew durable knowledge from Claude Code and Codex session logs",
  ),
  bin: {
    knowbrew: "bin/knowbrew.js",
  },
  files: ["bin/knowbrew.js", "README.md", "LICENSE"],
  engines: {
    node: ">=18",
  },
  optionalDependencies,
});

console.log(`Built 7 npm packages for knowbrew ${version} in ${outputDirectory}`);
