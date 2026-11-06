import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  Feature,
  allFeaturesOn,
  featureFlagNames,
  fetchFeatureFlags,
  readFeatureFlags,
  useFeature,
} from "@/features/flags";
import { renderHookWithProviders, renderWithProviders } from "@/test/utils";

afterEach(() => {
  vi.unstubAllGlobals();
});

/**
 * The adapter owns the names and the UI reads them, so the two lists are one
 * contract. Reading the Go source is the only check that fails when a surface
 * is added to one half and forgotten in the other.
 */
function adapterFlagNames(): string[] {
  const source = readFileSync(
    resolve(process.cwd(), "../../adapter/internal/features/features.go"),
    "utf8",
  );
  const constBlock = source.split("const (")[1]?.split(")")[0] ?? "";

  return [...constBlock.matchAll(/=\s*"([a-z-]+)"/g)].map((match) => match[1]);
}

describe("flag names", () => {
  it("matches the set the adapter publishes", () => {
    expect(featureFlagNames.toSorted()).toEqual(adapterFlagNames().toSorted());
  });

  it("covers every surface the ticket names", () => {
    expect([...featureFlagNames]).toEqual([
      "databases",
      "clusters",
      "backups",
      "connections",
      "support",
      "support-access",
      "chat",
      "assistant",
      "metrics",
      "activity",
      "mcp",
    ]);
  });

  it("defaults every surface on", () => {
    const flags = allFeaturesOn();

    for (const name of featureFlagNames) {
      expect(flags[name]).toBe(true);
    }
  });
});

describe("readFeatureFlags", () => {
  it("keeps the values the adapter sent", () => {
    const flags = readFeatureFlags({ chat: false, databases: true });

    expect(flags.chat).toBe(false);
    expect(flags.databases).toBe(true);
  });

  it("leaves a name the adapter did not mention on", () => {
    const flags = readFeatureFlags({ chat: false });

    expect(flags.metrics).toBe(true);
  });

  it("ignores a name that is not a surface", () => {
    const flags = readFeatureFlags({ "not-a-surface": false }) as Record<string, boolean>;

    expect(flags["not-a-surface"]).toBeUndefined();
    expect(flags.databases).toBe(true);
  });

  it("ignores a value that is not a boolean", () => {
    const flags = readFeatureFlags({ chat: "false" });

    expect(flags.chat).toBe(true);
  });

  it("falls back to the whole product for a body that is not an object", () => {
    expect(readFeatureFlags(null)).toEqual(allFeaturesOn());
    expect(readFeatureFlags("nope")).toEqual(allFeaturesOn());
  });
});

describe("fetchFeatureFlags", () => {
  it("reads the flags the adapter publishes", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ flags: { chat: false } }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    await expect(fetchFeatureFlags()).resolves.toEqual({ ...allFeaturesOn(), chat: false });
  });

  it("leaves the whole product on when the adapter has no flag route", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: "ErrNotFound", message: "gone" }), {
          status: 404,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    await expect(fetchFeatureFlags()).resolves.toEqual(allFeaturesOn());
  });

  it("gives the read a deadline, because it sits ahead of the first render", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ flags: {} }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await fetchFeatureFlags();

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(init.signal).toBeInstanceOf(AbortSignal);
  });

  it("fails when the read times out, rather than answering for the adapter", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockRejectedValue(new DOMException("timed out", "TimeoutError")),
    );

    await expect(fetchFeatureFlags()).rejects.toThrow();
  });

  it("fails when the read is aborted", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new DOMException("aborted", "AbortError")));

    await expect(fetchFeatureFlags()).rejects.toThrow();
  });

  it("fails when the adapter cannot be reached", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("network")));

    await expect(fetchFeatureFlags()).rejects.toThrow();
  });

  it("fails when the adapter answers with a server error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: "ErrInternal", message: "boom" }), {
          status: 500,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );

    await expect(fetchFeatureFlags()).rejects.toThrow();
  });
});

describe("useFeature", () => {
  it("reads a surface that is on", () => {
    const { result } = renderHookWithProviders(() => useFeature("chat"));

    expect(result.current).toBe(true);
  });

  it("reads a surface that is off", () => {
    const { result } = renderHookWithProviders(() => useFeature("chat"), {
      flags: { chat: false },
    });

    expect(result.current).toBe(false);
  });
});

describe("Feature", () => {
  it("renders its children when the surface is on", () => {
    renderWithProviders(<Feature name="backups">Backups</Feature>);

    expect(screen.getByText("Backups")).toBeInTheDocument();
  });

  it("renders nothing when the surface is off", () => {
    renderWithProviders(<Feature name="backups">Backups</Feature>, {
      flags: { backups: false },
    });

    expect(screen.queryByText("Backups")).not.toBeInTheDocument();
  });

  it("renders a fallback when the surface is off", () => {
    renderWithProviders(
      <Feature name="backups" fallback={<span>Not available</span>}>
        Backups
      </Feature>,
      { flags: { backups: false } },
    );

    expect(screen.getByText("Not available")).toBeInTheDocument();
  });
});
