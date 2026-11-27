import { describe, expect, it } from "vitest";
import { allRegions, awsRegions, regionChoices, regionsLabel } from "./regions";

describe("regions", () => {
  it("lists real region names only", () => {
    for (const region of awsRegions) {
      expect(region).toMatch(/^[a-z]{2}(-[a-z]+)+-[0-9]+$/);
    }
    expect(new Set(awsRegions).size).toBe(awsRegions.length);
  });

  it("says all regions for the wildcard and lists the rest", () => {
    expect(regionsLabel([allRegions])).toBe("All regions");
    expect(regionsLabel(["eu-west-1", "us-east-1"])).toBe("eu-west-1, us-east-1");
  });

  it("offers every region for an account with all regions, and its own list otherwise", () => {
    expect(regionChoices([allRegions])).toEqual(awsRegions);
    expect(regionChoices(["eu-west-1"])).toEqual(["eu-west-1"]);
  });
});
