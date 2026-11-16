import { readFileSync, readdirSync } from "node:fs";
import { join, relative, sep } from "node:path";
import { createElement } from "react";
import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { App } from "../App";
// Landing is its own npm project, so this is a relative import of the console reader.
import {
  bannedNameHits,
  bareClusterHits,
  visibleCopyTexts,
} from "../../../apps/ui/src/lib/visible-copy";

const roots = [join(process.cwd(), "src"), join(process.cwd(), "index.html")];
const srcDir = join(process.cwd(), "src");
const emDash = "—";
const enDash = "–";

function collect(path: string): string[] {
  if (/\.(ts|tsx|css|html)$/.test(path)) return [path];

  return readdirSync(path, { withFileTypes: true }).flatMap((entry) =>
    entry.isDirectory() || /\.(ts|tsx|css|html)$/.test(entry.name)
      ? collect(join(path, entry.name))
      : [],
  );
}

function collectSourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return collectSourceFiles(path);
    return /\.(ts|tsx)$/.test(entry.name) ? [path] : [];
  });
}

describe("copy lint", () => {
  const files = roots.flatMap(collect).filter((path) => !path.endsWith("copy-lint.test.ts"));

  it("finds files to check", () => {
    expect(files.length).toBeGreaterThan(5);
  });

  it("has zero em-dashes and zero en-dashes", () => {
    const offenders = files.filter((path) => {
      const contents = readFileSync(path, "utf8");
      return contents.includes(emDash) || contents.includes(enDash);
    });

    expect(offenders).toEqual([]);
  });

  it("has no exclamation marks in visible copy", () => {
    const offenders = files
      .filter((path) => path.endsWith(".tsx") && !path.endsWith(".test.tsx"))
      .filter((path) => {
        const contents = readFileSync(path, "utf8");
        // JSX text that ends in "!" before a tag, or a sentence string that does.
        return />[^<>{}]*[A-Za-z]![^<>{}=]*</.test(contents) || /"[A-Z][^"]*!"/.test(contents);
      });

    expect(offenders).toEqual([]);
  });

  // OpenEverest, Everest and everest-system may be named. These may not: a
  // customer is not told what Percona runs for them. The check reads visible
  // text only, so a comment, an import, an identifier or a route can say them.
  it("names no component the customer is not told about", () => {
    const offenders = visibleSources().flatMap(({ file, texts }) =>
      bannedNameHits(texts)
        .filter((found) => found !== "PMM")
        .map((found) => ({ file, found })),
    );

    expect(offenders).toEqual([]);
  });

  it("names no bare cluster in visible text", () => {
    const offenders = visibleSources().flatMap(({ file, texts }) =>
      bareClusterHits(texts).map((found) => ({ file, found })),
    );

    expect(offenders).toEqual([]);
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

  it("still rejects a banned name when a customer can read it", () => {
    const texts = visibleCopyTexts(`export function Page() {
      return <p>Open the hub and ask PMM.</p>;
    }`);

    expect(bannedNameHits(texts)).toEqual(["the hub", "PMM"]);
  });

  // body, summary, q, a and eyebrow are not text props, so the source reader
  // leaves them out. The rendered page is what a customer reads.
  it("names no bare cluster or banned name on the rendered page", () => {
    const { container } = render(createElement(App));
    const text = container.textContent ?? "";

    expect(bareClusterHits([text])).toEqual([]);
    expect(bannedNameHits([text]).filter((found) => found !== "PMM")).toEqual([]);
  });

  // The landing page may name PMM once, as the open source monitoring project in
  // the Built on list. Nowhere else may it.
  it("names PMM only as the linked monitoring project", () => {
    const { container } = render(createElement(App));
    const named = [...container.querySelectorAll("a")].filter((link) =>
      /\bPMM\b/.test(link.textContent ?? ""),
    );

    expect(named).toHaveLength(1);
    expect(named[0]).toHaveAttribute("href", "https://www.percona.com/monitoring/");
    expect((container.textContent ?? "").match(/\bPMM\b/g)).toHaveLength(1);
  });
});

function visibleSources(): { file: string; texts: string[] }[] {
  return collectSourceFiles(srcDir)
    .filter((path) => !/\.test\.tsx?$/.test(path) && !path.includes(`${sep}test${sep}`))
    .map((path) => {
      const source = readFileSync(path, "utf8");
      return { file: relative(srcDir, path), texts: visibleCopyTexts(source, path) };
    });
}
