import { describe, expect, it } from "vitest";
import { isNameConfirmed } from "@/features/databases";

describe("isNameConfirmed", () => {
  it("accepts the database name typed exactly", () => {
    expect(isNameConfirmed("orders-prod", "orders-prod")).toBe(true);
  });

  // A name pasted out of the header or a terminal brings whitespace with it,
  // and that is a paste artifact rather than a different database.
  it("accepts a name with whitespace around it", () => {
    expect(isNameConfirmed("  orders-prod\n", "orders-prod")).toBe(true);
  });

  it("refuses anything that is not the name", () => {
    expect(isNameConfirmed("", "orders-prod")).toBe(false);
    expect(isNameConfirmed("orders", "orders-prod")).toBe(false);
    expect(isNameConfirmed("orders-prod-2", "orders-prod")).toBe(false);
    expect(isNameConfirmed("orders prod", "orders-prod")).toBe(false);
  });

  // Kubernetes object names are lowercase, so a case difference is a different
  // name rather than a typing style.
  it("refuses a name in the wrong case", () => {
    expect(isNameConfirmed("Orders-Prod", "orders-prod")).toBe(false);
  });
});
