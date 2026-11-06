import { execFileSync } from "node:child_process";
import { cpSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import assert from "node:assert/strict";
import { afterEach, describe, it } from "node:test";

import { checkPlugin } from "./check.mjs";
import { checkPinPublished, pinnedVersion, stampPlugin, waitUntilPublished } from "./release.mjs";

const repository = join(dirname(fileURLToPath(import.meta.url)), "..");
const scratch = [];

// A copy of the plugin and both marketplace files, so a case stamps a tree that otherwise passes.
function copyOfRepository() {
  const root = mkdtempSync(join(tmpdir(), "pds-release-"));
  scratch.push(root);
  cpSync(join(repository, "plugins/pds"), join(root, "plugins/pds"), { recursive: true });
  for (const folder of [".claude-plugin", ".cursor-plugin"]) {
    cpSync(join(repository, folder), join(root, folder), { recursive: true });
  }
  setVersion(root, "0.1.0");
  for (const file of MCP_FILES) {
    writeFileSync(
      join(root, file),
      read(root, file).replace(/pds-mcp@\d+\.\d+\.\d+[^"]*/, "pds-mcp@0.1.0"),
    );
  }
  return root;
}

const read = (root, file) => readFileSync(join(root, file), "utf8");
const readJson = (root, file) => JSON.parse(read(root, file));
const MANIFESTS = [
  "plugins/pds/.claude-plugin/plugin.json",
  "plugins/pds/.cursor-plugin/plugin.json",
];
const MCP_FILES = ["plugins/pds/.mcp.json", "plugins/pds/mcp.json"];

function setVersion(root, version) {
  for (const file of MANIFESTS) {
    writeFileSync(
      join(root, file),
      read(root, file).replace(/"version": "[^"]*"/, `"version": "${version}"`),
    );
  }
}

afterEach(() => {
  for (const dir of scratch.splice(0)) rmSync(dir, { recursive: true, force: true });
});

describe("stampPlugin", () => {
  it("moves both manifests and both MCP files to the release version", () => {
    const root = copyOfRepository();
    const result = stampPlugin(root, "mcp-v1.4.0");
    assert.deepEqual(result, { changed: true, version: "1.4.0" });
    for (const file of MANIFESTS) assert.equal(readJson(root, file).version, "1.4.0");
    for (const file of MCP_FILES) {
      assert.deepEqual(readJson(root, file).mcpServers["pds-data"].args, [
        "-y",
        "@gas-lab/pds-mcp@1.4.0",
        "data",
      ]);
    }
    assert.equal(pinnedVersion(root), "1.4.0");
  });

  it("leaves a tree that passes the plugin checks, with the manifests equal", () => {
    const root = copyOfRepository();
    stampPlugin(root, "mcp-v1.4.0");
    assert.deepEqual(checkPlugin(root), []);
  });

  it("changes nothing else in the files", () => {
    const root = copyOfRepository();
    const before = MCP_FILES.map((file) => read(root, file));
    stampPlugin(root, "mcp-v1.4.0");
    MCP_FILES.forEach((file, index) => {
      assert.equal(read(root, file), before[index].replace("pds-mcp@0.1.0", "pds-mcp@1.4.0"));
    });
  });

  it("does not move a prerelease into the plugin", () => {
    const root = copyOfRepository();
    const before = MANIFESTS.map((file) => read(root, file));
    const result = stampPlugin(root, "mcp-v1.4.0-rc.1");
    assert.deepEqual(result, { changed: false, version: "1.4.0-rc.1", skipped: "prerelease" });
    assert.deepEqual(
      MANIFESTS.map((file) => read(root, file)),
      before,
    );
    assert.equal(pinnedVersion(root), "0.1.0");
  });

  it("reports no change when the plugin already holds the release", () => {
    const root = copyOfRepository();
    assert.deepEqual(stampPlugin(root, "mcp-v0.1.0"), { changed: false, version: "0.1.0" });
  });

  it("refuses a release older than the plugin's own version", () => {
    const root = copyOfRepository();
    setVersion(root, "0.3.0");
    assert.throws(() => stampPlugin(root, "mcp-v0.2.9"), /older than the plugin/);
    assert.equal(readJson(root, MANIFESTS[0]).version, "0.3.0");
  });

  it("moves a plugin that is ahead of the pin up to a newer release", () => {
    const root = copyOfRepository();
    setVersion(root, "0.1.1");
    assert.equal(stampPlugin(root, "mcp-v0.2.0").version, "0.2.0");
  });

  it("refuses a manifest pair that already differs", () => {
    const root = copyOfRepository();
    writeFileSync(
      join(root, MANIFESTS[1]),
      read(root, MANIFESTS[1]).replace('"version": "0.1.0"', '"version": "0.1.1"'),
    );
    assert.throws(() => stampPlugin(root, "mcp-v1.0.0"), /differ/);
  });

  it("refuses a tag that is not a version", () => {
    assert.throws(() => stampPlugin(copyOfRepository(), "mcp-vlatest"), /not a SemVer/);
  });
});

describe("waitUntilPublished", () => {
  it("polls until npm serves the version, then returns", async () => {
    const asked = [];
    const slept = [];
    let served = false;
    await waitUntilPublished("1.4.0", {
      lookup: async (version) => {
        asked.push(version);
        if (asked.length === 3) served = true;
        return served;
      },
      sleep: async (ms) => slept.push(ms),
      attempts: 10,
      delayMs: 5000,
    });
    assert.deepEqual(asked, ["1.4.0", "1.4.0", "1.4.0"]);
    assert.deepEqual(slept, [5000, 5000]);
  });

  it("fails when npm never serves the version", async () => {
    await assert.rejects(
      waitUntilPublished("1.4.0", {
        lookup: async () => false,
        sleep: async () => {},
        attempts: 3,
        delayMs: 1,
      }),
      /npm does not serve @gas-lab\/pds-mcp@1.4.0/,
    );
  });
});

describe("checkPinPublished", () => {
  // A repository whose only commit pins the old package name at baseVersion, then the working
  // tree moves to the new name and version.
  function renamedRepository(baseVersion, version) {
    const root = copyOfRepository();
    for (const file of MCP_FILES) {
      writeFileSync(
        join(root, file),
        read(root, file).replace(/@[a-z-]+\/pds-mcp@[^"]*/, `@percona/pds-mcp@${baseVersion}`),
      );
    }
    const git = (...args) => execFileSync("git", args, { cwd: root, stdio: "ignore" });
    git("init", "-q");
    git("add", ".");
    git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "base");
    for (const file of MCP_FILES) {
      writeFileSync(
        join(root, file),
        read(root, file).replace(/@percona\/pds-mcp@[^"]*/, `@gas-lab/pds-mcp@${version}`),
      );
    }
    return root;
  }

  const never = async () => assert.fail("npm was asked");

  it("passes a package rename at the base's version without asking npm", async () => {
    const root = renamedRepository("0.1.0", "0.1.0");
    assert.match(await checkPinPublished(root, "HEAD", never), /is the base's/);
  });

  it("passes a plugin the base does not have, without asking npm", async () => {
    const root = copyOfRepository();
    const git = (...args) => execFileSync("git", args, { cwd: root, stdio: "ignore" });
    git("init", "-q");
    writeFileSync(join(root, "unrelated.txt"), "base\n");
    git("add", "unrelated.txt");
    git(
      "-c",
      "user.name=t",
      "-c",
      "user.email=t@t",
      "commit",
      "-q",
      "-m",
      "base without the plugin",
    );
    assert.match(await checkPinPublished(root, "HEAD", never), /new at the base/);
  });

  it("fails a changed version that npm does not serve", async () => {
    const root = renamedRepository("0.1.0", "0.2.0");
    await assert.rejects(
      checkPinPublished(root, "HEAD", async () => false),
      /does not serve .*0.2.0/,
    );
  });

  it("passes a changed version that npm serves", async () => {
    const root = renamedRepository("0.1.0", "0.2.0");
    assert.match(await checkPinPublished(root, "HEAD", async () => true), /npm serves .*0.2.0/);
  });
});
