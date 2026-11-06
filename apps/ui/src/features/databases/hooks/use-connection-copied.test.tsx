import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { useConnectionCopied } from "@/features/databases";

beforeEach(() => {
  window.localStorage.clear();
});

describe("useConnectionCopied", () => {
  it("starts false, so the connection card leads the overview", () => {
    const { result } = renderHook(() => useConnectionCopied("pds.orders-dev"));

    expect(result.current.copied).toBe(false);
  });

  it("remembers the copy for the next visit", () => {
    const { result } = renderHook(() => useConnectionCopied("pds.orders-dev"));

    act(() => result.current.markCopied());

    expect(result.current.copied).toBe(true);

    const next = renderHook(() => useConnectionCopied("pds.orders-dev"));

    expect(next.result.current.copied).toBe(true);
  });

  it("keeps the memory per database", () => {
    const { result } = renderHook(() => useConnectionCopied("pds.orders-dev"));

    act(() => result.current.markCopied());

    const other = renderHook(() => useConnectionCopied("pds.carts-staging"));

    expect(other.result.current.copied).toBe(false);
  });

  it("stays usable when storage is unavailable", () => {
    const storage = window.localStorage;
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: {
        getItem: () => {
          throw new Error("blocked");
        },
        setItem: () => {
          throw new Error("blocked");
        },
      },
    });

    const { result } = renderHook(() => useConnectionCopied("pds.orders-dev"));

    expect(result.current.copied).toBe(false);
    act(() => result.current.markCopied());
    expect(result.current.copied).toBe(true);

    Object.defineProperty(window, "localStorage", { configurable: true, value: storage });
  });
});
