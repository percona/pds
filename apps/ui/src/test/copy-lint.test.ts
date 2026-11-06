import { readFileSync, readdirSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { describe, expect, it } from "vitest";
import {
  bannedNameHits,
  bareClusterHits,
  htmlDocumentTitles,
  visibleCopyTexts,
} from "@/lib/visible-copy";

const srcDir = join(process.cwd(), "src");
const emDash = "\u2014";
const enDash = "\u2013";

function collectSourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return collectSourceFiles(path);
    return /\.(ts|tsx|css|html)$/.test(entry.name) ? [path] : [];
  });
}

describe("copy lint", () => {
  it("finds source files to check", () => {
    expect(collectSourceFiles(srcDir).length).toBeGreaterThan(5);
  });

  it("has zero em-dashes and zero en-dashes in source", () => {
    const offenders = collectSourceFiles(srcDir)
      .filter((path) => !path.endsWith("copy-lint.test.ts"))
      .filter((path) => {
        const contents = readFileSync(path, "utf8");
        return contents.includes(emDash) || contents.includes(enDash);
      });

    expect(offenders).toEqual([]);
  });

  // OpenEverest, Everest and everest-system may be named. These may not: a
  // customer is not told what Percona runs for them. The check reads visible
  // text only, so a comment, an import, an identifier or a route can say them.
  it("names no component the customer is not told about", () => {
    const offenders = visibleSources().flatMap(({ file, texts }) =>
      bannedNameHits(texts).map((found) => ({ file, found })),
    );

    expect(offenders).toEqual([]);
  });

  it("names no bare cluster in visible text", () => {
    const offenders = visibleSources().flatMap(({ file, texts }) =>
      bareClusterHits(texts).map((found) => ({ file, found })),
    );

    expect(offenders).toEqual([]);
  });

  it("rejects a one-word Cluster label", () => {
    const texts = visibleCopyTexts(`
      const sourceLabels = { cluster: "Cluster" };
    `);

    expect(bareClusterHits(texts)).toEqual(["Cluster"]);
  });

  it("does not treat a lowercase cluster token as copy", () => {
    const texts = visibleCopyTexts(`
      const access = "cluster";
      const key = ["clusters", "list"];
    `);

    expect(bareClusterHits(texts)).toEqual([]);
  });

  it("rejects a bare cluster in visible text", () => {
    const texts = visibleCopyTexts(`export function Page() {
      return <p>Create a cluster</p>;
    }`);

    expect(bareClusterHits(texts)).toEqual(["cluster"]);
  });

  it("allows a Kubernetes cluster and an EKS cluster", () => {
    const texts = visibleCopyTexts(`export function Page() {
      return <p>Create an EKS cluster and a Kubernetes cluster.</p>;
    }`);

    expect(bareClusterHits(texts)).toEqual([]);
  });

  it("allows a command block to keep its real names", () => {
    const texts = visibleCopyTexts(`export function Page() {
      return <CommandBlock command="eksctl delete cluster --name demo" />;
    }`);

    expect(bareClusterHits(texts)).toEqual([]);
  });

  it("does not treat comments, imports, identifiers or routes as copy", () => {
    const texts = visibleCopyTexts(`
      import { clusters } from "@/features/clusters";
      // the hub spoke PMM Flux cluster
      const path = "/clusters/connect";
      export function Page({ clusters }: { clusters: string }) {
        return <a href="/clusters">{clusters}</a>;
      }
    `);

    expect(texts).toEqual([]);
    expect(bannedNameHits(texts)).toEqual([]);
    expect(bareClusterHits(texts)).toEqual([]);
  });

  it("still rejects a banned name when a customer can read it", () => {
    const texts = visibleCopyTexts(`export function Page() {
      return <p>Open the hub and ask PMM.</p>;
    }`);

    expect(bannedNameHits(texts)).toEqual(["the hub", "PMM"]);
  });
});

function visibleSources(): { file: string; texts: string[] }[] {
  return collectSourceFiles(srcDir)
    .filter((path) => !/\.test\.tsx?$/.test(path) && !path.includes(`${sep}test${sep}`))
    .map((path) => {
      const source = readFileSync(path, "utf8");
      const texts = path.endsWith(".html")
        ? htmlDocumentTitles(source)
        : /\.(ts|tsx)$/.test(path)
          ? visibleCopyTexts(source, path)
          : [];
      return { file: relative(srcDir, path), texts };
    });
}
