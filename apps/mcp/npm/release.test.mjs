import { execFileSync, spawnSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { buildPackage, buildServerJson, distTag, versionFromTag } from "./release.mjs";
import { binaryPath } from "./package/launch.mjs";

// The few matchers these checks use, over node:assert, so the tests run with `node --test` and
// need no package.
function expect(actual, message) {
  return {
    toBe: (expected) => assert.equal(actual, expected, message),
    toEqual: (expected) => assert.deepEqual(actual, expected, message),
    toThrow: (expected) =>
      assert.throws(
        actual,
        typeof expected === "string" ? (error) => error.message.includes(expected) : expected,
      ),
    not: { toBe: (expected) => assert.notEqual(actual, expected, message) },
  };
}

const targets = [
  { goos: "darwin", goarch: "arm64", dir: "pds-mcp_darwin_arm64_v8.0", package: "darwin-arm64" },
  { goos: "darwin", goarch: "amd64", dir: "pds-mcp_darwin_amd64_v1", package: "darwin-x64" },
  { goos: "linux", goarch: "arm64", dir: "pds-mcp_linux_arm64_v8.0", package: "linux-arm64" },
  { goos: "linux", goarch: "amd64", dir: "pds-mcp_linux_amd64_v1", package: "linux-x64" },
];

// What GoReleaser leaves in dist: one build directory per target and artifacts.json listing them
// next to archives and checksums that the packages must ignore.
function fakeDist() {
  const dist = mkdtempSync(join(tmpdir(), "pds-mcp-dist-"));
  const artifacts = [];
  for (const target of targets) {
    mkdirSync(join(dist, target.dir));
    const path = join(dist, target.dir, "pds-mcp");
    writeFileSync(path, `binary for ${target.goos} ${target.goarch}\n`);
    artifacts.push({
      name: "pds-mcp",
      path,
      goos: target.goos,
      goarch: target.goarch,
      type: "Binary",
    });
    artifacts.push({
      name: `pds-mcp_${target.dir}`,
      path: `${path}.upload`,
      goos: target.goos,
      goarch: target.goarch,
      type: "Uploadable binary",
    });
  }
  artifacts.push({ name: "checksums.txt", path: join(dist, "checksums.txt"), type: "Checksum" });
  const file = join(dist, "artifacts.json");
  writeFileSync(file, JSON.stringify(artifacts));
  return { dist, file };
}

function distOptions(version = "1.0.0") {
  const { file } = fakeDist();
  return { version, artifacts: file, outDir: mkdtempSync(join(tmpdir(), "pds-mcp-out-")) };
}

const readManifest = (dir) => JSON.parse(readFileSync(join(dir, "package.json"), "utf8"));

describe("the version of a release", () => {
  it("is the mcp-v tag without its prefix", () => {
    expect(versionFromTag("mcp-v1.2.3")).toBe("1.2.3");
    expect(versionFromTag("mcp-v1.2.3-rc.1")).toBe("1.2.3-rc.1");
    expect(versionFromTag("v0.4.0")).toBe("0.4.0");
  });

  it("rejects a tag that is not SemVer", () => {
    expect(() => versionFromTag("mcp-v1.2")).toThrow(/not a SemVer version/);
    expect(() => versionFromTag("mcp-vlatest")).toThrow(/not a SemVer version/);
  });

  it("puts a prerelease on next and a stable release on latest", () => {
    expect(distTag("mcp-v1.2.3")).toBe("latest");
    expect(distTag("mcp-v1.2.3-rc.1")).toBe("next");
  });

  it("reaches the one npm package and no other", () => {
    const { name, dir } = buildPackage(distOptions(versionFromTag("mcp-v1.2.3-rc.1")));

    expect(name).toBe("@gas-lab/pds-mcp");
    const manifest = readManifest(dir);
    expect(manifest.name).toBe("@gas-lab/pds-mcp");
    expect(manifest.version).toBe("1.2.3-rc.1");
    expect(readdirSync(join(dir, ".."))).toEqual(["pds-mcp"]);
  });

  it("reaches the binary", () => {
    const out = mkdtempSync(join(tmpdir(), "pds-mcp-bin-"));
    const binary = join(out, "pds-mcp");
    const version = versionFromTag("mcp-v1.2.3-rc.1");
    execFileSync(
      "go",
      ["build", "-ldflags", `-X main.version=${version}`, "-o", binary, "./cmd/pds-mcp"],
      {
        cwd: new URL("..", import.meta.url),
      },
    );
    expect(execFileSync(binary, ["version"], { encoding: "utf8" })).toBe("pds-mcp 1.2.3-rc.1\n");
  });
});

describe("the package", () => {
  it("carries every platform's binary, executable, in that platform's folder", () => {
    const { dir } = buildPackage(distOptions());
    for (const target of targets) {
      const binary = join(dir, "bin", target.package, "pds-mcp");
      expect(readFileSync(binary, "utf8")).toBe(`binary for ${target.goos} ${target.goarch}\n`);
      expect(statSync(binary).mode & 0o777).toBe(0o755);
    }
  });

  it("depends on nothing and names no platform restriction", () => {
    const manifest = readManifest(buildPackage(distOptions()).dir);
    expect(manifest.optionalDependencies).toBe(undefined);
    expect(manifest.dependencies).toBe(undefined);
    expect(manifest.os).toBe(undefined);
    expect(manifest.cpu).toBe(undefined);
    expect(manifest.scripts).toBe(undefined);
    expect(manifest.bin).toEqual({ "pds-mcp": "bin/pds-mcp.js" });
  });

  // npm trusted publishing accepts a publish only from the repository the manifest names.
  it("names the repository that publishes it", () => {
    const manifest = readManifest(buildPackage(distOptions()).dir);
    expect(manifest.repository.url, "repository url").toBe(
      "git+https://github.com/percona/pds-ops.git",
    );
  });

  it("ships the launcher and the licence", () => {
    const { dir } = buildPackage(distOptions());
    for (const path of ["bin/pds-mcp.js", "launch.mjs", "platform.mjs", "LICENSE"]) {
      expect(existsSync(join(dir, path)), path).toBe(true);
    }
  });

  it("packs into one tarball that holds the four binaries with their mode", () => {
    const { dir } = buildPackage(distOptions("1.0.0"));
    const tarballs = mkdtempSync(join(tmpdir(), "pds-mcp-tgz-"));
    execFileSync("npm", ["pack", "--pack-destination", tarballs, "--loglevel=error"], {
      cwd: dir,
      stdio: "ignore",
    });
    expect(readdirSync(tarballs)).toEqual(["gas-lab-pds-mcp-1.0.0.tgz"]);
    const listing = execFileSync("tar", ["-tvzf", join(tarballs, "gas-lab-pds-mcp-1.0.0.tgz")], {
      encoding: "utf8",
    })
      .trim()
      .split("\n");
    for (const target of targets) {
      const line = listing.find((entry) => entry.endsWith(`package/bin/${target.package}/pds-mcp`));
      expect(line?.startsWith("-rwxr-xr-x")).toBe(true);
    }
  });

  it("is not built when a platform has no binary", () => {
    const options = distOptions();
    const kept = JSON.parse(readFileSync(options.artifacts, "utf8")).filter(
      (a) => !(a.goos === "linux" && a.goarch === "arm64"),
    );
    writeFileSync(options.artifacts, JSON.stringify(kept));
    expect(() => buildPackage(options)).toThrow(/linux arm64/);
  });
});

describe("the launcher", () => {
  for (const [os, cpu, folder] of [
    ["darwin", "arm64", "bin/darwin-arm64"],
    ["darwin", "x64", "bin/darwin-x64"],
    ["linux", "arm64", "bin/linux-arm64"],
    ["linux", "x64", "bin/linux-x64"],
  ]) {
    it(`picks the folder for ${os} on ${cpu}`, () => {
      expect(binaryPath(os, cpu, "/installed/pds-mcp")).toBe(
        `/installed/pds-mcp/${folder}/pds-mcp`,
      );
    });
  }

  for (const [os, cpu] of [
    ["win32", "x64"],
    ["linux", "ia32"],
    ["freebsd", "arm64"],
  ]) {
    it(`stops with a clear message on ${os} ${cpu}`, () => {
      expect(() => binaryPath(os, cpu, "/installed/pds-mcp")).toThrow(
        `pds-mcp does not run on ${os} ${cpu}. It runs on macOS and Linux, on arm64 and x64.`,
      );
    });
  }

  it("runs the binary of its platform and passes the arguments and exit code through", () => {
    const { dir } = buildPackage(distOptions());
    const own = join(dir, "bin", `${process.platform}-${process.arch}`, "pds-mcp");
    if (!existsSync(own)) return;
    writeFileSync(own, '#!/bin/sh\necho "args: $*"\nexit 7\n');

    const run = spawnSync(process.execPath, [join(dir, "bin", "pds-mcp.js"), "version", "--flag"], {
      encoding: "utf8",
    });
    expect(run.stdout).toBe("args: version --flag\n");
    expect(run.status).toBe(7);
  });
});

// The part of JSON Schema that the registry's server.json schema uses, so the check runs with
// `node --test` and no package. It returns the paths that break the schema.
function schemaErrors(schema, value, node = schema, path = "$") {
  if (node.$ref !== undefined) {
    const target = node.$ref
      .replace(/^#\//, "")
      .split("/")
      .reduce((at, key) => at[key], schema);
    return schemaErrors(schema, value, target, path);
  }
  const errors = [];
  for (const part of node.allOf ?? []) errors.push(...schemaErrors(schema, value, part, path));
  if (node.anyOf !== undefined) {
    const attempts = node.anyOf.map((part) => schemaErrors(schema, value, part, path));
    if (!attempts.some((attempt) => attempt.length === 0))
      errors.push(`${path}: matches no option`);
  }
  if (node.type !== undefined) {
    const actual = Array.isArray(value) ? "array" : value === null ? "null" : typeof value;
    if (actual !== node.type) return [`${path}: is ${actual}, not ${node.type}`];
  }
  if (node.enum !== undefined && !node.enum.includes(value)) errors.push(`${path}: not allowed`);
  if (node.const !== undefined && value !== node.const) errors.push(`${path}: not ${node.const}`);
  if (node.not !== undefined && schemaErrors(schema, value, node.not, path).length === 0) {
    errors.push(`${path}: matches a forbidden shape`);
  }
  if (typeof value === "string") {
    if (node.minLength !== undefined && value.length < node.minLength)
      errors.push(`${path}: short`);
    if (node.maxLength !== undefined && value.length > node.maxLength) errors.push(`${path}: long`);
    if (node.pattern !== undefined && !new RegExp(node.pattern).test(value)) {
      errors.push(`${path}: does not match ${node.pattern}`);
    }
  }
  if (Array.isArray(value) && node.items !== undefined) {
    value.forEach((item, index) =>
      errors.push(...schemaErrors(schema, item, node.items, `${path}[${index}]`)),
    );
  }
  if (typeof value === "object" && value !== null && !Array.isArray(value)) {
    for (const key of node.required ?? []) {
      if (!(key in value)) errors.push(`${path}: lacks ${key}`);
    }
    for (const [key, part] of Object.entries(node.properties ?? {})) {
      if (key in value) errors.push(...schemaErrors(schema, value[key], part, `${path}.${key}`));
    }
    if (typeof node.additionalProperties === "object") {
      for (const [key, item] of Object.entries(value)) {
        if (!(key in (node.properties ?? {}))) {
          errors.push(...schemaErrors(schema, item, node.additionalProperties, `${path}.${key}`));
        }
      }
    }
  }
  return errors;
}

const registrySchema = JSON.parse(readFileSync(new URL("./server.schema.json", import.meta.url)));
const readServer = () => JSON.parse(readFileSync(new URL("../server.json", import.meta.url)));
const readPackage = () =>
  JSON.parse(readFileSync(new URL("./package/package.json", import.meta.url)));

describe("the registry listing", () => {
  it("is a document the registry's schema accepts", () => {
    expect(schemaErrors(registrySchema, readServer())).toEqual([]);
    expect(schemaErrors(registrySchema, buildServerJson({ version: "1.2.3" }))).toEqual([]);
  });

  it("is checked by a validator that refuses what the schema refuses", () => {
    const tooLong = { ...readServer(), description: "x".repeat(101) };
    expect(schemaErrors(registrySchema, tooLong).length).not.toBe(0);
    const badName = { ...readServer(), name: "pds-mcp" };
    expect(schemaErrors(registrySchema, badName).length).not.toBe(0);
    const noVersion = { ...readServer(), version: undefined };
    delete noVersion.version;
    expect(schemaErrors(registrySchema, noVersion).length).not.toBe(0);
  });

  it("carries the version of the release on the listing and on its package", () => {
    for (const tag of ["mcp-v1.2.3", "mcp-v1.2.3-rc.1"]) {
      const version = versionFromTag(tag);
      const listing = buildServerJson({ version });
      expect(listing.version).toBe(version);
      expect(listing.packages.map((p) => p.version)).toEqual([version]);
    }
  });

  it("names the package that the release packs, at the version it packs", () => {
    const packed = buildPackage(distOptions("1.2.3-rc.1"));
    const listing = buildServerJson({ version: "1.2.3-rc.1" });

    expect(listing.packages.map((p) => [p.registryType, p.identifier, p.version])).toEqual([
      ["npm", packed.name, readManifest(packed.dir).version],
    ]);
    expect(listing.packages[0].transport).toEqual({ type: "stdio" });
  });

  it("is the name the packed package claims, which is how the registry checks ownership", () => {
    const packed = buildPackage(distOptions("1.2.3"));

    expect(readManifest(packed.dir).mcpName).toBe(buildServerJson({ version: "1.2.3" }).name);
  });

  it("offers the control plane's /mcp over streamable HTTP, at an address a deployment can change", () => {
    const [remote, ...others] = buildServerJson({ version: "1.2.3" }).remotes;
    expect(others).toEqual([]);
    expect(remote.type).toBe("streamable-http");
    expect(remote.url).toBe("https://{host}/mcp");
    expect(remote.variables.host.default).toBe("pds.percona.com");
  });

  it("uses words the customer copy rules allow", () => {
    const listing = buildServerJson({ version: "1.2.3" });
    const text = JSON.stringify([
      listing.title,
      listing.description,
      listing.remotes[0].variables,
      listing.packages[0].environmentVariables,
    ]);
    expect(/[\u2013\u2014]|PMM|Flux|\bhub\b|adapter|spoke|!/i.test(text)).toBe(false);
    expect(/\bclusters?\b/i.test(text.replace(/Kubernetes cluster|EKS cluster/g, ""))).toBe(false);
  });

  it("is not stamped when its name differs from the package's mcpName", () => {
    const dir = mkdtempSync(join(tmpdir(), "pds-mcp-listing-"));
    const manifest = join(dir, "package.json");
    writeFileSync(
      manifest,
      JSON.stringify({ ...readPackage(), mcpName: "io.github.other/pds-mcp" }),
    );
    expect(() => buildServerJson({ version: "1.2.3", manifest })).toThrow(/mcpName/);

    const noName = { ...readPackage() };
    delete noName.mcpName;
    writeFileSync(manifest, JSON.stringify(noName));
    expect(() => buildServerJson({ version: "1.2.3", manifest })).toThrow(/mcpName/);
  });

  it("is not stamped when it names a package the release does not pack", () => {
    const dir = mkdtempSync(join(tmpdir(), "pds-mcp-listing-"));
    const server = join(dir, "server.json");
    const listing = readServer();
    listing.packages[0].identifier = "@gas-lab/other";
    writeFileSync(server, JSON.stringify(listing));
    expect(() => buildServerJson({ version: "1.2.3", server })).toThrow(/@gas-lab\/other/);
  });

  it("is written by the release script for a tag", () => {
    const dir = mkdtempSync(join(tmpdir(), "pds-mcp-listing-"));
    const out = join(dir, "server.json");
    execFileSync(process.execPath, [
      new URL("./release.mjs", import.meta.url).pathname,
      "server-json",
      "mcp-v1.2.3-rc.1",
      out,
    ]);
    const written = JSON.parse(readFileSync(out, "utf8"));
    expect(written.version).toBe("1.2.3-rc.1");
    expect(written.packages[0].version).toBe("1.2.3-rc.1");

    const bad = spawnSync(
      process.execPath,
      [new URL("./release.mjs", import.meta.url).pathname, "server-json", "mcp-vlatest", out],
      { encoding: "utf8" },
    );
    expect(bad.status).toBe(1);
    expect(bad.stderr).toBe('The release tag "mcp-vlatest" is not a SemVer version.\n');
  });
});
