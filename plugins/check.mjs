// Checks plugins/pds and the two marketplace files at the repository root against the rules that
// no tool enforces: `claude plugin validate` does not read the Agent Skills limits, and Cursor
// publishes only a schema.
//
//   node plugins/check.mjs [--base <git ref>]
//
// With --base the plugin's version must also have changed since that ref whenever a file under
// plugins/pds did.
import { execFileSync } from "node:child_process";
import { existsSync, lstatSync, readdirSync, readFileSync, readlinkSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const PLUGIN = "plugins/pds";
const MARKETPLACE_FILES = [".claude-plugin/marketplace.json", ".cursor-plugin/marketplace.json"];
const NAME = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$/;
const SKILL_NAME = /^[a-z0-9]+(-[a-z0-9]+)*$/;
const SEMVER = /^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/;
const DEFAULT_URL = "${PDS_URL:-https://pds.percona.com}/mcp";
const FIXED_URL = "https://pds.percona.com/mcp";

// Keys the manifests of this plugin may carry. Claude Code strips others with a warning, and
// Cursor's own plugin checks refuse them.
const MANIFEST_KEYS = [
  "name",
  "version",
  "description",
  "author",
  "homepage",
  "repository",
  "license",
  "keywords",
];
// The only keys Cursor's own marketplace checks allow on an entry.
const CURSOR_ENTRY_KEYS = ["name", "source", "description", "minClientVersions"];

function readJson(root, path, problems) {
  try {
    return JSON.parse(readFileSync(join(root, path), "utf8"));
  } catch (error) {
    problems.push(`${path} cannot be read as JSON: ${error.message}`);
    return undefined;
  }
}

function checkManifests(root, problems) {
  const manifests = {};
  for (const folder of [".claude-plugin", ".cursor-plugin"]) {
    const path = `${PLUGIN}/${folder}/plugin.json`;
    const manifest = readJson(root, path, problems);
    if (manifest === undefined) continue;
    manifests[folder] = manifest;
    if (typeof manifest.name !== "string" || !NAME.test(manifest.name)) {
      problems.push(`${path}: name must be lowercase kebab-case`);
    }
    if (typeof manifest.version !== "string" || !SEMVER.test(manifest.version)) {
      problems.push(`${path}: version must be a semantic version`);
    }
    for (const key of Object.keys(manifest)) {
      if (!MANIFEST_KEYS.includes(key))
        problems.push(`${path}: "${key}" is not a key this plugin uses`);
    }
    for (const [key, value] of Object.entries(manifest)) {
      if (typeof value === "string" && /(^|\/)\.\.(\/|$)/.test(value)) {
        problems.push(`${path}: "${key}" climbs out of the plugin`);
      }
    }
  }
  const [claude, cursor] = [manifests[".claude-plugin"], manifests[".cursor-plugin"]];
  if (claude && cursor) {
    if (claude.name !== cursor.name) problems.push("the two manifests have different names");
    if (claude.version !== cursor.version) {
      problems.push(`the manifest versions differ: ${claude.version} and ${cursor.version}`);
    }
  }
  return manifests;
}

function checkMarketplaces(root, manifests, problems) {
  const files = [
    { folder: ".claude-plugin", source: `./${PLUGIN}`, mustStartWithDot: true },
    { folder: ".cursor-plugin", source: PLUGIN, mustStartWithDot: false },
  ];
  for (const { folder, source, mustStartWithDot } of files) {
    const path = `${folder}/marketplace.json`;
    const marketplace = readJson(root, path, problems);
    if (marketplace === undefined) continue;
    if (typeof marketplace.name !== "string" || !marketplace.name || !marketplace.owner?.name) {
      problems.push(`${path}: name and owner.name are required`);
    }
    const manifest = manifests[folder];
    for (const entry of marketplace.plugins ?? []) {
      if (manifest && entry.name !== manifest.name) {
        problems.push(
          `${path}: entry "${entry.name}" does not match the manifest name "${manifest.name}"`,
        );
      }
      if (mustStartWithDot && !String(entry.source).startsWith("./")) {
        problems.push(`${path}: source must start with ./ for Claude Code`);
      }
      if (entry.source !== source) problems.push(`${path}: source must be ${source}`);
      if (!existsSync(join(root, String(entry.source), folder, "plugin.json"))) {
        problems.push(`${path}: ${entry.source} has no ${folder}/plugin.json`);
      }
      if (folder === ".cursor-plugin") {
        for (const key of Object.keys(entry)) {
          if (!CURSOR_ENTRY_KEYS.includes(key))
            problems.push(`${path}: "${key}" is not allowed on a Cursor entry`);
        }
      } else if ("version" in entry) {
        problems.push(`${path}: the version belongs in plugin.json only`);
      }
    }
    if (!marketplace.plugins?.length) problems.push(`${path}: lists no plugin`);
  }
}

function checkMcpConfig(root, problems) {
  const claude = readJson(root, `${PLUGIN}/.mcp.json`, problems)?.mcpServers;
  const cursor = readJson(root, `${PLUGIN}/mcp.json`, problems)?.mcpServers;
  if (!claude || !cursor) return;

  if (claude.pds?.type !== "http" || claude.pds?.url !== DEFAULT_URL) {
    problems.push(
      `${PLUGIN}/.mcp.json: pds must be type http and read ${DEFAULT_URL}, with the PDS_URL:-https://pds.percona.com default`,
    );
  }
  if (cursor.pds?.url !== FIXED_URL) {
    problems.push(
      `${PLUGIN}/mcp.json: pds must be the fixed address ${FIXED_URL}, because Cursor has no default form`,
    );
  }
  for (const [file, servers] of [
    [".mcp.json", claude],
    ["mcp.json", cursor],
  ]) {
    const data = servers["pds-data"];
    const args = data?.args ?? [];
    if (data?.command !== "npx" || args.length !== 3 || args[0] !== "-y" || args[2] !== "data") {
      problems.push(`${PLUGIN}/${file}: pds-data must run npx -y <package>@<version> data`);
    } else if (!/^@gas-lab\/pds-mcp@\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(args[1])) {
      problems.push(
        `${PLUGIN}/${file}: pds-data must name @gas-lab/pds-mcp at an exact version, not "${args[1]}"`,
      );
    }
    if (data && Object.keys(data).some((key) => !["command", "args"].includes(key))) {
      problems.push(`${PLUGIN}/${file}: pds-data takes no settings to fill in`);
    }
  }
  if (JSON.stringify(claude["pds-data"]) !== JSON.stringify(cursor["pds-data"])) {
    problems.push(`${PLUGIN}: the two MCP files must run the same data server`);
  }
}

// The body of a file with front matter, and the front matter's own lines.
function frontMatter(text) {
  const match = /^---\n([\s\S]*?)\n---\n([\s\S]*)$/.exec(text);
  return match ? { front: match[1], body: match[2] } : undefined;
}

function checkSkills(root, problems) {
  const skills = join(root, PLUGIN, "skills");
  if (existsSync(join(root, PLUGIN, "commands"))) {
    problems.push(`${PLUGIN}/commands: the plugin ships skills and MCP prompts, never commands`);
  }
  if (!existsSync(skills)) {
    problems.push(`${PLUGIN}/skills is missing`);
    return;
  }
  for (const folder of readdirSync(skills)) {
    const path = `${PLUGIN}/skills/${folder}/SKILL.md`;
    if (!existsSync(join(root, path))) {
      problems.push(`${path} is missing`);
      continue;
    }
    const parts = frontMatter(readFileSync(join(root, path), "utf8"));
    if (!parts) {
      problems.push(`${path} has no front matter`);
      continue;
    }
    const name = /^name: (.*)$/m.exec(parts.front)?.[1];
    const quoted = /^description: (.*)$/m.exec(parts.front)?.[1];
    if (name !== folder) problems.push(`${path}: name "${name}" is not its folder "${folder}"`);
    if (folder.length > 64 || !SKILL_NAME.test(folder)) {
      problems.push(
        `${path}: the name is at most 64 characters of lowercase letters, digits and single hyphens`,
      );
    }
    let description = quoted ?? "";
    try {
      description = JSON.parse(description);
    } catch {
      // A plain scalar is measured as written.
    }
    if (!description) problems.push(`${path}: description is empty`);
    if (description.length > 1024)
      problems.push(`${path}: description is ${description.length}, over 1024 characters`);
    if (parts.body.split("\n").length >= 500)
      problems.push(`${path}: the body is 500 lines or more`);
  }
}

function checkRule(root, problems) {
  const path = `${PLUGIN}/rules/use-pds.mdc`;
  if (!existsSync(join(root, path))) {
    problems.push(`${path} is missing`);
    return;
  }
  const parts = frontMatter(readFileSync(join(root, path), "utf8"));
  if (!parts || !/^alwaysApply: true$/m.test(parts.front)) {
    problems.push(`${path} must have front matter with alwaysApply: true`);
  }
}

function filesUnder(directory) {
  const found = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) found.push(...filesUnder(path));
    else found.push(path);
  }
  return found;
}

function checkPaths(root, files, problems) {
  const plugin = resolve(root, PLUGIN);
  for (const file of files) {
    if (!lstatSync(file).isSymbolicLink()) continue;
    const target = resolve(dirname(file), readlinkSync(file));
    if (relative(plugin, target).startsWith("..")) {
      problems.push(
        `${relative(root, file)} leaves the plugin: a git install copies only ${PLUGIN}`,
      );
    }
  }
}

// What the customer copy rules in AGENTS.md ban in a string a customer reads. The plugin's files
// are all read by a customer or their agent.
const BANNED = [
  [/—|–/, "an em dash or an en dash"],
  [/\b(PMM|Flux|spoke|hub|adapter)\b/i, "a name for what Percona runs"],
  [/(?<!Kubernetes |EKS )\bclusters?\b/i, "a bare cluster"],
];

function checkCopy(root, files, problems) {
  for (const file of files) {
    if (lstatSync(file).isSymbolicLink()) continue;
    const text = readFileSync(file, "utf8");
    for (const [pattern, what] of BANNED) {
      const hit = pattern.exec(text);
      if (hit) problems.push(`${relative(root, file)}: customer copy has ${what} ("${hit[0]}")`);
    }
  }
}

export function checkPlugin(root) {
  const problems = [];
  const manifests = checkManifests(root, problems);
  checkMarketplaces(root, manifests, problems);
  checkMcpConfig(root, problems);
  checkSkills(root, problems);
  checkRule(root, problems);
  if (existsSync(join(root, PLUGIN))) {
    const files = filesUnder(join(root, PLUGIN));
    checkPaths(root, files, problems);
    checkCopy(root, [...files, ...MARKETPLACE_FILES.map((file) => join(root, file))], problems);
  }
  return problems;
}

function git(root, ...args) {
  return execFileSync("git", args, {
    cwd: root,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
}

// The version a ref holds, or undefined when the ref has no such manifest. A manifest that is
// there and cannot be read is an error, not a skip.
function versionAt(root, ref, folder) {
  const path = `${PLUGIN}/${folder}/plugin.json`;
  try {
    git(root, "cat-file", "-e", `${ref}:${path}`);
  } catch {
    return undefined;
  }
  return JSON.parse(git(root, "show", `${ref}:${path}`)).version;
}

// A client keeps the plugin it has until the version string changes, so any change under
// plugins/pds, a generated file included, needs a new one.
export function checkVersionBump(root, base) {
  const mergeBase = git(root, "merge-base", base, "HEAD").trim();
  if (versionAt(root, mergeBase, ".claude-plugin") === undefined) return [];
  const changed = git(root, "diff", "--name-only", mergeBase, "--", PLUGIN).trim();
  if (!changed) return [];
  const problems = [];
  for (const folder of [".claude-plugin", ".cursor-plugin"]) {
    const before = versionAt(root, mergeBase, folder);
    const now = JSON.parse(readFileSync(join(root, PLUGIN, folder, "plugin.json"), "utf8")).version;
    if (before === now) {
      problems.push(
        `${PLUGIN} changed without a new version in ${folder}/plugin.json (still ${now})`,
      );
    }
  }
  return problems;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const root = join(dirname(fileURLToPath(import.meta.url)), "..");
  const base = process.argv[2] === "--base" ? process.argv[3] : undefined;
  const problems = [...checkPlugin(root), ...(base ? checkVersionBump(root, base) : [])];
  for (const problem of problems) console.error(problem);
  if (problems.length > 0) process.exit(1);
  console.log("plugins/pds passes");
}
