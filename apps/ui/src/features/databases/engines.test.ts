import { describe, expect, it } from "vitest";
import { createPageDescription, emptyListDescription, engines, visibleEngines } from "./engines";

describe("engines", () => {
  it("offers only the engines marked enabled", () => {
    expect(visibleEngines).toEqual(engines.filter((engine) => engine.enabled));
    expect(visibleEngines.map((engine) => engine.id)).toEqual(["mysql", "postgresql"]);
  });

  it("names the environment from the engines that are offered", () => {
    expect(createPageDescription).toBe(
      "A MySQL or PostgreSQL database on this Kubernetes cluster.",
    );
    expect(emptyListDescription).toBe(
      "Create a MySQL or PostgreSQL database on this Kubernetes cluster.",
    );
  });
});
