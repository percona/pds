import { chmodSync, copyFileSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { binaryFolder, packageName, platforms } from "./package/platform.mjs";

// SemVer 2.0.0 without build metadata, which npm dist-tags and the release tags do not use.
const semver =
  /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-(0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*)?$/;

const here = dirname(fileURLToPath(import.meta.url));

export function versionFromTag(releaseTag) {
  const version = releaseTag.replace(/^(?:mcp-v|v)/, "");
  if (!semver.test(version)) {
    throw new Error(`The release tag "${releaseTag}" is not a SemVer version.`);
  }
  return version;
}

// A prerelease never moves latest, so a person who installs without a version is not handed one.
export function distTag(releaseTag) {
  return versionFromTag(releaseTag).includes("-") ? "next" : "latest";
}

function write(dir, name, content) {
  mkdirSync(dirname(join(dir, name)), { recursive: true });
  writeFileSync(join(dir, name), content);
}

const json = (value) => `${JSON.stringify(value, null, 2)}\n`;

// artifacts is the artifacts.json GoReleaser writes to dist. Lays out the one package under
// outDir: the launcher, and each platform's binary in the folder the launcher picks it from.
export function buildPackage({ version, artifacts, outDir }) {
  const built = JSON.parse(readFileSync(artifacts, "utf8")).filter(
    (artifact) => artifact.type === "Binary",
  );
  rmSync(outDir, { recursive: true, force: true });

  const dir = join(outDir, packageName.replace(/^@[^/]+\//, ""));
  for (const platform of platforms) {
    const binary = built.find(
      (artifact) => artifact.goos === platform.goos && artifact.goarch === platform.goarch,
    );
    if (binary === undefined) {
      throw new Error(`GoReleaser built no binary for ${platform.goos} ${platform.goarch}.`);
    }
    const target = join(dir, binaryFolder(platform.os, platform.cpu), "pds-mcp");
    mkdirSync(dirname(target), { recursive: true });
    copyFileSync(binary.path, target);
    chmodSync(target, 0o755);
  }

  const template = JSON.parse(readFileSync(join(here, "package", "package.json"), "utf8"));
  for (const file of ["bin/pds-mcp.js", "launch.mjs", "platform.mjs"]) {
    copyFileSync(join(here, "package", file), join(dir, file));
  }
  chmodSync(join(dir, "bin", "pds-mcp.js"), 0o755);
  copyFileSync(join(here, "..", "..", "..", "LICENSE"), join(dir, "LICENSE"));
  write(dir, "package.json", json({ ...template, version }));
  return { name: packageName, dir };
}

// The MCP Registry listing for a release: server.json with the release's version on the listing and
// on its npm package. The registry accepts the listing only when the package it names carries
// the same name as mcpName, and only when the package is the one the release publishes, so a
// mismatch stops here rather than at the registry.
export function buildServerJson({
  version,
  server = join(here, "..", "server.json"),
  manifest = join(here, "package", "package.json"),
}) {
  const listing = JSON.parse(readFileSync(server, "utf8"));
  const { mcpName } = JSON.parse(readFileSync(manifest, "utf8"));
  if (mcpName !== listing.name) {
    throw new Error(
      `The package's mcpName is "${mcpName}" and server.json is named "${listing.name}".`,
    );
  }
  for (const entry of listing.packages) {
    if (entry.registryType !== "npm" || entry.identifier !== packageName) {
      throw new Error(
        `server.json names ${entry.identifier}, which this release does not publish.`,
      );
    }
    entry.version = version;
  }
  return { ...listing, version };
}

const usage =
  "Usage: node npm/release.mjs version <tag> | dist-tag <tag> | build <tag> <artifacts.json> <outDir> | server-json <tag> <outFile>\n";

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [command, tag, artifacts, outDir] = process.argv.slice(2);
  try {
    if (command === "version" && tag !== undefined) {
      process.stdout.write(`${versionFromTag(tag)}\n`);
    } else if (command === "dist-tag" && tag !== undefined) {
      process.stdout.write(`${distTag(tag)}\n`);
    } else if (command === "server-json" && artifacts !== undefined) {
      writeFileSync(artifacts, json(buildServerJson({ version: versionFromTag(tag) })));
    } else if (command === "build" && outDir !== undefined) {
      const { dir } = buildPackage({ version: versionFromTag(tag), artifacts, outDir });
      process.stdout.write(`${dir}\n`);
    } else {
      process.stderr.write(usage);
      process.exit(1);
    }
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exit(1);
  }
}
