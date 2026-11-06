import { describe, expect, it } from "vitest";
import { createMockDatabase } from "@/test/utils";
import {
  copiesHere,
  maxSourceRanges,
  parseSourceRanges,
  reachabilityOf,
  visibilityOf,
} from "./access";

describe("reachabilityOf", () => {
  it("reads a database with no access block as internal", () => {
    expect(reachabilityOf(createMockDatabase())).toBe("internal");
  });

  it("reads an internal database as internal", () => {
    expect(reachabilityOf(createMockDatabase({ access: { external: false } }))).toBe("internal");
  });

  it("is pending while an external database has no address", () => {
    expect(reachabilityOf(createMockDatabase({ access: { external: true } }))).toBe("pending");
  });

  it("is external once the address exists", () => {
    expect(
      reachabilityOf(createMockDatabase({ access: { external: true, address: "198.51.100.20" } })),
    ).toBe("external");
  });
});

describe("visibilityOf", () => {
  it("reads the visibility the adapter reports", () => {
    expect(
      visibilityOf(createMockDatabase({ access: { external: true, visibility: "private" } })),
    ).toBe("private");
    expect(
      visibilityOf(createMockDatabase({ access: { external: true, visibility: "public" } })),
    ).toBe("public");
    expect(
      visibilityOf(createMockDatabase({ access: { external: false, visibility: "cluster" } })),
    ).toBe("cluster");
  });

  it("reads an older adapter's external database as public, since only public existed", () => {
    expect(visibilityOf(createMockDatabase({ access: { external: true } }))).toBe("public");
  });

  it("reads a database with no access block as environment", () => {
    expect(visibilityOf(createMockDatabase())).toBe("cluster");
  });
});

describe("copiesHere", () => {
  const privateLb = { external: true, visibility: "private" as const, address: "10.0.4.12" };
  const publicLb = { external: true, visibility: "public" as const, address: "198.51.100.20" };

  it("offers copy only for a public database", () => {
    expect(copiesHere(createMockDatabase({ access: publicLb }))).toBe(true);
    expect(copiesHere(createMockDatabase({ environment: "spoke-a", access: publicLb }))).toBe(true);
    expect(copiesHere(createMockDatabase({ access: privateLb }))).toBe(false);
    expect(copiesHere(createMockDatabase({ environment: "spoke-a", access: privateLb }))).toBe(
      false,
    );
  });

  it("copies nothing before the address exists", () => {
    expect(
      copiesHere(createMockDatabase({ access: { external: true, visibility: "private" } })),
    ).toBe(false);
    expect(copiesHere(createMockDatabase())).toBe(false);
  });
});

describe("parseSourceRanges", () => {
  it.each([
    ["", []],
    ["   ", []],
    ["10.0.0.0/8", ["10.0.0.0/8"]],
    ["203.0.113.7", ["203.0.113.7"]],
    ["10.0.0.0/8, 203.0.113.7", ["10.0.0.0/8", "203.0.113.7"]],
    [
      "10.0.0.0/8\n192.168.0.0/16  172.16.0.0/12",
      ["10.0.0.0/8", "192.168.0.0/16", "172.16.0.0/12"],
    ],
    ["2001:db8::/32", ["2001:db8::/32"]],
    ["2001:db8::1", ["2001:db8::1"]],
  ])("reads %j", (text, ranges) => {
    expect(parseSourceRanges(text)).toEqual({ ranges });
  });

  // The adapter parses with Go's netip, which refuses leading zeroes, so the
  // form refuses them too rather than letting the create fail server side.
  it.each([
    "office",
    "10.0.0.0/33",
    "256.0.0.1",
    "10.0.0",
    "10.0.0.0/",
    "2001:db8::/129",
    "001.002.003.004",
    "10.0.0.01",
    "10.0.0.0/08",
  ])("names %j as the entry at fault", (bad) => {
    expect(parseSourceRanges(`10.0.0.0/8, ${bad}`)).toEqual({
      ranges: [],
      error: `\`${bad}\` is not an IP address or CIDR range.`,
    });
  });

  it("caps the list", () => {
    const text = Array.from({ length: maxSourceRanges + 1 }, (_, i) => `10.0.${i}.0/24`).join(",");

    expect(parseSourceRanges(text)).toEqual({
      ranges: [],
      error: `Allow at most ${maxSourceRanges} ranges.`,
    });
  });
});
