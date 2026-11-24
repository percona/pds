import { describe, expect, it } from "vitest";
import { memberChoices, removedMembersNote, scaleChanges } from "./scale";

const current = { cpu: 1, memory: 2, storage: 20, replicas: 1 };

describe("scaleChanges", () => {
  it("sends nothing when nothing moved", () => {
    expect(scaleChanges(current, current)).toEqual({});
  });

  it("sends only the field that moved", () => {
    expect(scaleChanges(current, { ...current, cpu: 4 })).toEqual({ cpu: 4 });
    expect(scaleChanges(current, { ...current, storage: 50 })).toEqual({ storage: 50 });
    expect(scaleChanges(current, { ...current, replicas: 3 })).toEqual({ replicas: 3 });
  });

  it("sends every field that moved", () => {
    expect(scaleChanges(current, { cpu: 2, memory: 8, storage: 100, replicas: 3 })).toEqual({
      cpu: 2,
      memory: 8,
      storage: 100,
      replicas: 3,
    });
  });
});

describe("memberChoices", () => {
  // Galera decides quorum by majority, so the adapter accepts one member or an
  // odd number of them. The picker must not offer a count that is refused.
  it("offers one member and the odd counts that hold quorum", () => {
    expect(memberChoices).toEqual([1, 3, 5]);
  });
});

describe("removedMembersNote", () => {
  const sentence = "Storage of the removed members stays until the database is deleted.";

  it("tells a MongoDB database that loses members that their storage stays", () => {
    expect(removedMembersNote("mongodb", 3, 1)).toBe(sentence);
    expect(removedMembersNote("mongodb", 5, 3)).toBe(sentence);
  });

  it("says nothing when no members are removed", () => {
    expect(removedMembersNote("mongodb", 1, 3)).toBeNull();
    expect(removedMembersNote("mongodb", 3, 3)).toBeNull();
  });

  it("says nothing for an engine that gives its storage back", () => {
    expect(removedMembersNote("mysql", 3, 1)).toBeNull();
    expect(removedMembersNote("postgresql", 3, 1)).toBeNull();
  });
});
