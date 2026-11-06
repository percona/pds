import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { failureSettleMs, useProvisioningEvents } from "@/features/databases";
import type { Database } from "@/features/databases";
import { createMockDatabase } from "@/test/utils";

function renderHistory(initial: Database) {
  return renderHook(({ database }) => useProvisioningEvents(database), {
    initialProps: { database: initial },
  });
}

describe("useProvisioningEvents", () => {
  it("records the operator message against the step that was running", () => {
    const { result } = renderHistory(
      createMockDatabase({ status: "provisioning", message: "Waiting for volume to bind" }),
    );

    expect(result.current.events).toEqual({ volumes: "Waiting for volume to bind" });
    expect(result.current.lastActiveStep).toBe("volumes");
  });

  it("keeps the event of a finished step when the next step starts", () => {
    const { result, rerender } = renderHistory(
      createMockDatabase({ status: "provisioning", message: "Volume orders-dev-pvc bound" }),
    );

    rerender({
      database: createMockDatabase({
        status: "initializing",
        message: "Starting pod orders-dev-0",
      }),
    });

    expect(result.current.events).toEqual({
      volumes: "Volume orders-dev-pvc bound",
      database: "Starting pod orders-dev-0",
    });
    expect(result.current.lastActiveStep).toBe("database");
  });

  it("remembers the last running step so a failure can name it", () => {
    const { result, rerender } = renderHistory(
      createMockDatabase({ status: "provisioning", message: "Waiting for volume to bind" }),
    );

    rerender({
      database: createMockDatabase({
        status: "failed",
        message: "No StorageClass can satisfy the claim.",
      }),
    });

    expect(result.current.lastActiveStep).toBe("volumes");
  });

  it("holds no events for a database the adapter reports without a message", () => {
    const { result } = renderHistory(createMockDatabase({ status: "pending" }));

    expect(result.current.events).toEqual({});
    expect(result.current.lastActiveStep).toBe("volumes");
  });

  describe("failure settling", () => {
    beforeEach(() => {
      vi.useFakeTimers();
    });

    afterEach(() => {
      vi.useRealTimers();
    });

    it("does not settle a failure the moment it appears", () => {
      const { result } = renderHistory(createMockDatabase({ status: "failed" }));

      expect(result.current.failureSettled).toBe(false);
    });

    it("settles a failure that holds", () => {
      const { result } = renderHistory(createMockDatabase({ status: "failed" }));

      act(() => {
        vi.advanceTimersByTime(failureSettleMs);
      });

      expect(result.current.failureSettled).toBe(true);
    });

    it("clears a failure the operator recovered from before it settled", () => {
      const { result, rerender } = renderHistory(createMockDatabase({ status: "failed" }));

      act(() => {
        vi.advanceTimersByTime(failureSettleMs / 2);
      });
      rerender({ database: createMockDatabase({ status: "provisioning" }) });
      act(() => {
        vi.advanceTimersByTime(failureSettleMs);
      });

      expect(result.current.failureSettled).toBe(false);
    });
  });
});
