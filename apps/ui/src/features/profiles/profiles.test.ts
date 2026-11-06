import { describe, expect, it } from "vitest";
import { findProfile, profiles, resourceBounds } from "@/features/profiles";

describe("profiles", () => {
  it("lists dev, staging, and prod in picker order", () => {
    expect(profiles.map((profile) => profile.id)).toEqual(["dev", "staging", "prod"]);
  });

  it("carries the consequence line each card shows", () => {
    expect(findProfile("dev").consequence).toBe("1 member, on-demand backups");
    expect(findProfile("staging").consequence).toBe("3 members, HA, daily backups");
    expect(findProfile("prod").consequence).toBe("3 members, HA, daily backups");
  });

  it("matches the sizing table the adapter enforces", () => {
    expect(findProfile("dev")).toMatchObject({
      label: "Dev",
      members: 1,
      cpuCores: 1,
      memoryGb: 2,
      storageGb: 20,
    });
    expect(findProfile("staging")).toMatchObject({
      label: "Staging",
      members: 3,
      cpuCores: 2,
      memoryGb: 8,
      storageGb: 100,
    });
    expect(findProfile("prod")).toMatchObject({
      label: "Prod",
      members: 3,
      cpuCores: 4,
      memoryGb: 16,
      storageGb: 250,
    });
  });

  it("mirrors the override bounds the adapter validates against", () => {
    expect(resourceBounds.cpu).toEqual({ min: 1, max: 64 });
    expect(resourceBounds.memory).toEqual({ min: 1, max: 512 });
    expect(resourceBounds.storage).toEqual({ min: 10, max: 16000 });
  });

  it("renders custom for an empty, unknown, or custom profile instead of throwing", () => {
    expect(findProfile("").label).toBe("Custom");
    expect(findProfile("custom").label).toBe("Custom");
    expect(findProfile("huge").label).toBe("Custom");
    expect(findProfile(undefined).consequence).toBe(
      "Sized on the Kubernetes cluster, not from a catalog profile",
    );
  });
});
