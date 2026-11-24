import { describe, expect, it } from "vitest";
import {
  createNameLimits,
  createPageDescription,
  emptyListDescription,
  engines,
  hasAccessChoice,
  visibleEngines,
} from "./engines";

describe("engines", () => {
  it("offers only the engines marked enabled", () => {
    expect(visibleEngines).toEqual(engines.filter((engine) => engine.enabled));
    expect(visibleEngines.map((engine) => engine.id)).toEqual(["mysql", "postgresql", "mongodb"]);
  });

  it("offers MongoDB", () => {
    expect(engines.find((engine) => engine.id === "mongodb")).toMatchObject({
      label: "MongoDB",
      enabled: true,
    });
    expect(visibleEngines.map((engine) => engine.id)).toContain("mongodb");
  });

  it("limits a MongoDB name to 22 characters and gives it no Access choice", () => {
    expect(createNameLimits.mongodb?.limit).toBe(22);
    expect(hasAccessChoice("mongodb")).toBe(false);
    expect(hasAccessChoice("mysql")).toBe(true);
    expect(hasAccessChoice("postgresql")).toBe(true);
  });

  it("names the environment from the engines that are offered", () => {
    expect(createPageDescription).toBe(
      "A MySQL, PostgreSQL, or MongoDB database on this Kubernetes cluster.",
    );
    expect(emptyListDescription).toBe(
      "Create a MySQL, PostgreSQL, or MongoDB database on this Kubernetes cluster.",
    );
  });
});
