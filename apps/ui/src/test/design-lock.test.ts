import { readFileSync, readdirSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

// The design locks in AGENTS.md, checked against the source so a change that
// breaks one fails here instead of waiting for a reviewer to notice.

const root = process.cwd();
const srcDir = join(root, "src");
const e2eDir = join(root, "e2e");

function collect(dir: string, pattern: RegExp): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return collect(path, pattern);
    return pattern.test(entry.name) ? [path] : [];
  });
}

const isTest = (path: string) => /\.(test|spec)\.tsx?$/.test(path);
const sourceFiles = collect(srcDir, /\.tsx?$/).filter((path) => !isTest(path));
const testFiles = [...collect(srcDir, /\.(test|spec)\.tsx?$/), ...collect(e2eDir, /\.spec\.ts$/)];

// Class names live in string and template literals. Reading only those, with
// template placeholders cut out, keeps a variable that happens to be called
// `rounded` from reading as a class.
function classTokens(source: string): string[] {
  const literals = source.match(/"(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*'|`(?:[^`\\]|\\.)*`/g) ?? [];
  return literals
    .map((literal) => literal.slice(1, -1).replace(/\$\{[^}]*\}/g, " "))
    .flatMap((literal) => literal.split(/[\s"'`{}$]+/))
    .map((token) => token.split(":").pop() ?? "")
    .filter(Boolean);
}

type Offence = { file: string; found: string };

function offences(files: string[], find: (source: string) => string[]): Offence[] {
  return files.flatMap((path) =>
    find(readFileSync(path, "utf8")).map((found) => ({ file: relative(root, path), found })),
  );
}

const palette =
  "red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose|slate|gray|zinc|neutral|stone|black|white";
const paletteClass = new RegExp(
  `^(bg|text|border|ring|fill|stroke|from|via|to|outline|divide|accent|caret|decoration|shadow)-(${palette})(-\\d{2,3})?(/\\d+)?$`,
);

describe("design locks", () => {
  it("finds files to check", () => {
    expect(sourceFiles.length).toBeGreaterThan(20);
    expect(testFiles.length).toBeGreaterThan(20);
  });

  it("uses only the 8px, 12px and full radii", () => {
    const allowed = new Set(["rounded-md", "rounded-lg", "rounded-full"]);
    const found = offences(sourceFiles, (source) =>
      classTokens(source).filter((token) => /^rounded(-|$)/.test(token) && !allowed.has(token)),
    );
    expect(found).toEqual([]);
  });

  it("sets nothing above text-2xl", () => {
    const found = offences(sourceFiles, (source) =>
      classTokens(source).filter((token) => /^text-([3-9]xl)$/.test(token)),
    );
    expect(found).toEqual([]);
  });

  it("colors through theme tokens, never a raw palette class or hex value", () => {
    const found = offences(sourceFiles, (source) => [
      ...classTokens(source).filter((token) => paletteClass.test(token)),
      ...(source.match(/#[0-9a-fA-F]{6}\b|#[0-9a-fA-F]{3}\b/g) ?? []),
    ]);
    expect(found).toEqual([]);
  });

  it("uses no serif type", () => {
    const found = offences(sourceFiles, (source) =>
      classTokens(source).filter((token) => token === "font-serif"),
    );
    expect(found).toEqual([]);
  });

  it("draws icons from lucide-react at strokeWidth 1.5", () => {
    const found = offences(sourceFiles, (source) => [
      ...(source.match(
        /from "(react-icons[^"]*|@heroicons\/[^"]*|@radix-ui\/react-icons|@tabler\/icons-react|@phosphor-icons\/[^"]*|@mui\/icons-material[^"]*)"/g,
      ) ?? []),
      ...(source.match(/strokeWidth=\{(?!1\.5\})[^}]*\}/g) ?? []),
    ]);
    expect(found).toEqual([]);
  });

  it("renders no raw HTML", () => {
    const found = offences(sourceFiles, (source) => source.match(/dangerouslySetInnerHTML/g) ?? []);
    expect(found).toEqual([]);
  });

  it("leaves no focused, skipped or todo tests", () => {
    const found = offences(
      testFiles,
      (source) =>
        source.match(
          /\b(it|test|describe)\.(only|skip|todo)\b|\b(xit|xtest|xdescribe|fit|fdescribe)\(/g,
        ) ?? [],
    );
    expect(found).toEqual([]);
  });
});
