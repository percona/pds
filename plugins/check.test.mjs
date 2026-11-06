import { execFileSync } from "node:child_process";
import {
  cpSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";
import { afterEach, describe, it } from "node:test";

import { checkPlugin, checkVersionBump } from "./check.mjs";

const repository = join(dirname(fileURLToPath(import.meta.url)), "..");
const scratch = [];

// A copy of the real plugin and both marketplace files, so a case changes one thing in a tree that
// otherwise passes.
function copyOfRepository() {
  const root = mkdtempSync(join(tmpdir(), "pds-plugin-"));
  scratch.push(root);
  cpSync(join(repository, "plugins/pds"), join(root, "plugins/pds"), { recursive: true });
  for (const folder of [".claude-plugin", ".cursor-plugin"]) {
    cpSync(join(repository, folder), join(root, folder), { recursive: true });
  }
  return root;
}

function edit(root, file, change) {
  const path = join(root, file);
  writeFileSync(path, change(readFileSync(path, "utf8")));
}

function editJson(root, file, change) {
  edit(root, file, (text) => {
    const value = JSON.parse(text);
    change(value);
    return `${JSON.stringify(value, null, 2)}\n`;
  });
}

function problems(root) {
  return checkPlugin(root).join("\n");
}

afterEach(() => {
  for (const root of scratch.splice(0)) rmSync(root, { recursive: true, force: true });
});

describe("the plugin as shipped", () => {
  it("passes every check", () => {
    assert.deepEqual(checkPlugin(repository), []);
  });

  it("reads PDS_URL with the hosted default in Claude Code and a fixed address in Cursor", () => {
    const claude = JSON.parse(readFileSync(join(repository, "plugins/pds/.mcp.json"), "utf8"));
    const cursor = JSON.parse(readFileSync(join(repository, "plugins/pds/mcp.json"), "utf8"));
    assert.equal(claude.mcpServers.pds.type, "http");
    assert.equal(claude.mcpServers.pds.url, "${PDS_URL:-https://pds.percona.com}/mcp");
    assert.equal(cursor.mcpServers.pds.url, "https://pds.percona.com/mcp");
  });

  it("has no commands folder", () => {
    assert.throws(() => readFileSync(join(repository, "plugins/pds/commands")), { code: "ENOENT" });
  });
});

describe("manifests", () => {
  it("fails when the two manifests carry different versions", () => {
    const root = copyOfRepository();
    editJson(root, "plugins/pds/.cursor-plugin/plugin.json", (m) => (m.version = "9.9.9"));
    assert.match(problems(root), /versions differ/);
  });

  it("fails on a key the Claude Code manifest does not know", () => {
    const root = copyOfRepository();
    editJson(root, "plugins/pds/.claude-plugin/plugin.json", (m) => (m.rules = "./rules"));
    assert.match(problems(root), /claude-plugin\/plugin.json.*"rules"/);
  });

  it("fails on a name that is not kebab-case", () => {
    const root = copyOfRepository();
    editJson(root, "plugins/pds/.cursor-plugin/plugin.json", (m) => (m.name = "PDS_Plugin"));
    assert.match(problems(root), /name/);
  });

  it("fails when a marketplace entry has another name than its manifest", () => {
    const root = copyOfRepository();
    editJson(root, ".claude-plugin/marketplace.json", (m) => (m.plugins[0].name = "other"));
    assert.match(problems(root), /entry "other" does not match/);
  });

  it("fails when the Claude Code source loses its ./", () => {
    const root = copyOfRepository();
    editJson(root, ".claude-plugin/marketplace.json", (m) => (m.plugins[0].source = "plugins/pds"));
    assert.match(problems(root), /must start with \.\//);
  });

  it("fails when the Cursor entry carries a key its schema does not allow", () => {
    const root = copyOfRepository();
    editJson(root, ".cursor-plugin/marketplace.json", (m) => (m.plugins[0].category = "database"));
    assert.match(problems(root), /"category"/);
  });
});

describe("MCP config", () => {
  it("fails when the Claude Code remote entry loses its default", () => {
    const root = copyOfRepository();
    editJson(root, "plugins/pds/.mcp.json", (m) => (m.mcpServers.pds.url = "${PDS_URL}/mcp"));
    assert.match(problems(root), /PDS_URL:-https:\/\/pds.percona.com/);
  });

  it("fails when the Cursor remote entry is not the fixed address", () => {
    const root = copyOfRepository();
    editJson(root, "plugins/pds/mcp.json", (m) => (m.mcpServers.pds.url = "${env:PDS_URL}/mcp"));
    assert.match(problems(root), /https:\/\/pds.percona.com\/mcp/);
  });

  it("fails when the data server is not at an exact version", () => {
    const root = copyOfRepository();
    editJson(
      root,
      "plugins/pds/.mcp.json",
      (m) => (m.mcpServers["pds-data"].args = ["-y", "@gas-lab/pds-mcp@latest", "data"]),
    );
    assert.match(problems(root), /exact version/);
  });

  it("fails when the two files pin different versions", () => {
    const root = copyOfRepository();
    editJson(
      root,
      "plugins/pds/mcp.json",
      (m) => (m.mcpServers["pds-data"].args = ["-y", "@gas-lab/pds-mcp@7.7.7", "data"]),
    );
    assert.match(problems(root), /same data server/);
  });

  it("fails when the data server is given settings to fill in", () => {
    const root = copyOfRepository();
    editJson(
      root,
      "plugins/pds/.mcp.json",
      (m) => (m.mcpServers["pds-data"].env = { PDS_URL: "${PDS_URL}" }),
    );
    assert.match(problems(root), /no settings/);
  });
});

describe("the skill", () => {
  it("fails on a name that is not its folder", () => {
    const root = copyOfRepository();
    edit(root, "plugins/pds/skills/databases/SKILL.md", (t) =>
      t.replace("name: databases", "name: other"),
    );
    assert.match(problems(root), /name "other" is not its folder/);
  });

  it("fails on a name over 64 characters", () => {
    const root = copyOfRepository();
    const long = "a".repeat(65);
    cpSync(join(root, "plugins/pds/skills/databases"), join(root, `plugins/pds/skills/${long}`), {
      recursive: true,
    });
    rmSync(join(root, "plugins/pds/skills/databases"), { recursive: true });
    edit(root, `plugins/pds/skills/${long}/SKILL.md`, (t) =>
      t.replace("name: databases", `name: ${long}`),
    );
    assert.match(problems(root), /64 characters/);
  });

  it("fails on a description over 1024 characters", () => {
    const root = copyOfRepository();
    edit(root, "plugins/pds/skills/databases/SKILL.md", (t) =>
      t.replace(/^description: .*$/m, `description: "${"a".repeat(1030)}"`),
    );
    assert.match(problems(root), /1024 characters/);
  });

  it("fails on a body of 500 lines or more", () => {
    const root = copyOfRepository();
    edit(root, "plugins/pds/skills/databases/SKILL.md", (t) => `${t}\n${"line\n".repeat(500)}`);
    assert.match(problems(root), /500 lines/);
  });

  it("fails when a commands folder appears", () => {
    const root = copyOfRepository();
    mkdirSync(join(root, "plugins/pds/commands"));
    writeFileSync(join(root, "plugins/pds/commands/x.md"), "x\n");
    assert.match(problems(root), /commands/);
  });

  it("fails when the Cursor rule is not always on", () => {
    const root = copyOfRepository();
    edit(root, "plugins/pds/rules/use-pds.mdc", (t) =>
      t.replace("alwaysApply: true", "alwaysApply: false"),
    );
    assert.match(problems(root), /alwaysApply: true/);
  });
});

describe("paths", () => {
  it("fails on a link that leaves the plugin", () => {
    const root = copyOfRepository();
    symlinkSync(join(root, "README.md"), join(root, "plugins/pds/outside.md"));
    assert.match(problems(root), /outside.md leaves the plugin/);
  });

  it("fails on a manifest path that climbs out", () => {
    const root = copyOfRepository();
    editJson(root, "plugins/pds/.cursor-plugin/plugin.json", (m) => (m.skills = "../skills"));
    assert.match(problems(root), /climbs out/);
  });
});

describe("copy", () => {
  for (const [name, text] of [
    ["an em dash", "one — two"],
    ["an en dash separator", "one – two"],
    ["the hub", "Ask the hub."],
    ["the adapter", "The adapter answers."],
    ["PMM", "PMM shows it."],
    ["Flux", "Flux applies it."],
    ["a spoke", "A spoke connects."],
    ["a bare cluster", "Pick a cluster."],
    ["bare clusters", "Both clusters."],
  ]) {
    it(`rejects ${name}`, () => {
      const root = copyOfRepository();
      edit(root, "plugins/pds/rules/use-pds.mdc", (t) => `${t}\n${text}\n`);
      assert.match(problems(root), /customer copy/);
    });
  }

  it("lets Kubernetes cluster and EKS cluster stand", () => {
    const root = copyOfRepository();
    edit(
      root,
      "plugins/pds/rules/use-pds.mdc",
      (t) => `${t}\nYour Kubernetes cluster and your EKS cluster.\n`,
    );
    assert.doesNotMatch(problems(root), /customer copy/);
  });
});

describe("version bump", () => {
  function git(root, ...args) {
    return execFileSync("git", ["-c", "user.name=t", "-c", "user.email=t@example.com", ...args], {
      cwd: root,
      encoding: "utf8",
    });
  }

  function repositoryWithBase() {
    const root = copyOfRepository();
    git(root, "init", "-q", "-b", "main");
    git(root, "add", ".");
    git(root, "commit", "-q", "-m", "base");
    git(root, "switch", "-q", "-c", "change");
    return root;
  }

  function bump(root, version) {
    for (const folder of [".claude-plugin", ".cursor-plugin"]) {
      editJson(root, `plugins/pds/${folder}/plugin.json`, (m) => (m.version = version));
    }
  }

  it("passes when nothing under plugins/pds changed", () => {
    const root = repositoryWithBase();
    writeFileSync(join(root, "unrelated.txt"), "x\n");
    git(root, "add", ".");
    git(root, "commit", "-q", "-m", "other");
    assert.deepEqual(checkVersionBump(root, "main"), []);
  });

  it("fails when a file under plugins/pds changed and the version did not", () => {
    const root = repositoryWithBase();
    edit(root, "plugins/pds/rules/use-pds.mdc", (t) => `${t}\nMore.\n`);
    git(root, "commit", "-q", "-am", "edit");
    assert.match(checkVersionBump(root, "main").join("\n"), /changed without a new version/);
  });

  it("passes when the same change bumps both manifests", () => {
    const root = repositoryWithBase();
    edit(root, "plugins/pds/rules/use-pds.mdc", (t) => `${t}\nMore.\n`);
    bump(root, "99.0.0");
    git(root, "commit", "-q", "-am", "edit");
    assert.deepEqual(checkVersionBump(root, "main"), []);
  });

  it("passes for a plugin the base does not have", () => {
    const root = copyOfRepository();
    git(root, "init", "-q", "-b", "main");
    git(root, "commit", "-q", "--allow-empty", "-m", "base");
    git(root, "switch", "-q", "-c", "change");
    git(root, "add", ".");
    git(root, "commit", "-q", "-m", "add");
    assert.deepEqual(checkVersionBump(root, "main"), []);
  });
});
