// Moves the plugin to an MCP server release, and checks that the version it pins exists.
//
//   node plugins/release.mjs stamp <mcp-v tag>   wait for npm, then stamp the plugin
//   node plugins/release.mjs published           fail when the pinned version is not on npm
//
// The plugin's version is the release version, so a person reads one number for the plugin and the
// binary it runs. A prerelease never moves it: a client keeps the plugin it has until the version
// changes, and the stable plugin must not point at a build npm serves only under `next`.
import { execFile, execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { promisify } from "node:util";
import { fileURLToPath } from "node:url";

import { distTag, versionFromTag } from "../apps/mcp/npm/release.mjs";
import { packageName } from "../apps/mcp/npm/package/platform.mjs";
import { checkPlugin } from "./check.mjs";

const PLUGIN = "plugins/pds";
const MANIFESTS = [`${PLUGIN}/.claude-plugin/plugin.json`, `${PLUGIN}/.cursor-plugin/plugin.json`];
const MCP_FILES = [`${PLUGIN}/.mcp.json`, `${PLUGIN}/mcp.json`];
const ANY_SCOPE_PIN = /@[a-z-]+\/pds-mcp@\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?/;
const PIN = new RegExp(`${packageName}@\\d+\\.\\d+\\.\\d+(?:-[0-9A-Za-z.-]+)?`, "g");
const VERSION_KEY = /("version": ")[^"]*(")/;

const run = promisify(execFile);

function core(version) {
  return version.split("-")[0].split(".").map(Number);
}

function isOlder(version, other) {
  const [a, b] = [core(version), core(other)];
  for (let i = 0; i < 3; i++) if (a[i] !== b[i]) return a[i] < b[i];
  return false;
}

const read = (root, file) => readFileSync(join(root, file), "utf8");

// The version the MCP files pin, which both must agree on.
export function pinnedVersion(root) {
  const pins = new Set(
    MCP_FILES.flatMap((file) => read(root, file).match(PIN) ?? []).map((pin) =>
      pin.split("@").pop(),
    ),
  );
  if (pins.size !== 1)
    throw new Error(`The MCP files pin ${pins.size} versions of ${packageName}.`);
  return [...pins][0];
}

// Rewrites the two manifests and the two MCP files for the release the tag names, and runs the
// plugin checks on the result. A prerelease, or a release the plugin already holds, changes nothing.
export function stampPlugin(root, releaseTag) {
  const version = versionFromTag(releaseTag);
  if (distTag(releaseTag) !== "latest") return { changed: false, version, skipped: "prerelease" };

  const current = new Set(MANIFESTS.map((file) => JSON.parse(read(root, file)).version));
  if (current.size !== 1) {
    throw new Error(`The two manifests differ in version: ${[...current].join(" and ")}.`);
  }
  const [held] = current;
  if (held === version && pinnedVersion(root) === version) return { changed: false, version };
  if (isOlder(version, held)) {
    throw new Error(`The release ${version} is older than the plugin's ${held}.`);
  }

  for (const file of MANIFESTS) {
    writeFileSync(join(root, file), read(root, file).replace(VERSION_KEY, `$1${version}$2`));
  }
  for (const file of MCP_FILES) {
    writeFileSync(join(root, file), read(root, file).replace(PIN, `${packageName}@${version}`));
  }
  const problems = checkPlugin(root);
  if (problems.length > 0)
    throw new Error(`The stamped plugin fails its checks:\n${problems.join("\n")}`);
  return { changed: true, version };
}

async function servedByNpm(version) {
  try {
    await run("npm", ["view", `${packageName}@${version}`, "version", "--prefer-online"]);
    return true;
  } catch {
    return false;
  }
}

// Resolves once npm serves the package at this version.
export async function waitUntilPublished(
  version,
  {
    lookup = servedByNpm,
    sleep = (ms) => new Promise((done) => setTimeout(done, ms)),
    attempts = 30,
    delayMs = 20_000,
  } = {},
) {
  for (let attempt = 1; attempt <= attempts; attempt++) {
    if (await lookup(version)) return;
    if (attempt < attempts) await sleep(delayMs);
  }
  throw new Error(`npm does not serve ${packageName}@${version}.`);
}

// Whether the pinned version differs from the one at the base ref, under whatever package name the
// base used: a pin nobody changed is not this pull request's to prove. "introduced" when the base
// has no plugin at all.
function pinChangedSince(root, base) {
  const mergeBase = execFileSync("git", ["merge-base", base, "HEAD"], {
    cwd: root,
    encoding: "utf8",
  }).trim();
  let atBase;
  try {
    atBase = execFileSync("git", ["show", `${mergeBase}:${MCP_FILES[0]}`], {
      cwd: root,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    });
  } catch {
    // The plugin does not exist at the base: this pull request introduces it, and its first
    // release is what proves the pin.
    return "introduced";
  }
  const before = atBase.match(ANY_SCOPE_PIN)?.[0];
  return before?.split("@").pop() !== pinnedVersion(root);
}

// Fails unless npm serves the pinned version, when this pull request changed the version.
// Renaming the package at the base's version proves nothing new.
export async function checkPinPublished(root, base, lookup = servedByNpm) {
  const version = pinnedVersion(root);
  const changed = base === undefined ? true : pinChangedSince(root, base);
  if (changed === "introduced")
    return `The plugin is new at the base, so its first release proves the pin ${version}.`;
  if (!changed) return `The pin ${version} is the base's.`;
  if (!(await lookup(version))) {
    throw new Error(`npm does not serve ${packageName}@${version}, which the plugin pins.`);
  }
  return `npm serves ${packageName}@${version}`;
}

const usage = "Usage: node plugins/release.mjs stamp <tag> | published [<base ref>]\n";

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const root = join(dirname(fileURLToPath(import.meta.url)), "..");
  const [command, argument] = process.argv.slice(2);
  try {
    if (command === "stamp" && argument !== undefined) {
      const version = versionFromTag(argument);
      if (distTag(argument) === "latest") await waitUntilPublished(version);
      const result = stampPlugin(root, argument);
      const note = result.skipped
        ? `skipped (${result.skipped})`
        : result.changed
          ? "stamped"
          : "unchanged";
      process.stdout.write(`${note} ${result.version}\n`);
    } else if (command === "published") {
      process.stdout.write(`${await checkPinPublished(root, argument)}\n`);
    } else {
      process.stderr.write(usage);
      process.exit(1);
    }
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exit(1);
  }
}
