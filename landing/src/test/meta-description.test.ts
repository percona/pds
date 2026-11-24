import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

describe("index.html meta description", () => {
  const html = readFileSync(join(process.cwd(), "index.html"), "utf8");
  const description = /<meta\s+name="description"\s+content="([^"]*)"/.exec(html)?.[1] ?? "";

  it("is present", () => {
    expect(description).not.toBe("");
  });

  it.each(["MySQL", "PostgreSQL", "MongoDB"])("names %s, as the page does", (engine) => {
    expect(description).toContain(engine);
  });

  it("does not offer a Kubernetes cluster you already have", () => {
    expect(description).not.toMatch(/already have|existing|your own Kubernetes|join a Kubernetes/i);
  });
});
