import { describe, expect, it } from "vitest";
import { assetUrl, basePath, routerBasename } from "./base-path";

describe("base path", () => {
  it("mounts the console under /app", () => {
    expect(basePath).toBe("/app/");
    expect(routerBasename).toBe("/app");
  });

  it("builds public asset URLs under the mount", () => {
    expect(assetUrl("percona-mark.svg")).toBe("/app/percona-mark.svg");
  });
});
