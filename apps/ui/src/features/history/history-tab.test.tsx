import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/utils";
import { HistoryTab, JoinHistory } from "./history-tab";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("HistoryTab", () => {
  it("shows an empty history", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(jsonResponse({ entries: [], truncated: false }))),
    );

    renderWithProviders(<HistoryTab databaseId="spoke-a/pds.orders" />);

    expect(await screen.findByText("No history yet.")).toBeInTheDocument();
    expect(screen.queryByText("Usage")).not.toBeInTheDocument();
  });

  it("shows backup size and duration", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) =>
        Promise.resolve(
          jsonResponse(
            String(url).includes("/usage")
              ? { samples: [] }
              : {
                  entries: [
                    {
                      id: "1",
                      kind: "backup",
                      at: "2026-09-24T12:00:00Z",
                      outcome: "succeeded",
                      sizeBytes: 4096,
                      durationMs: 90000,
                    },
                  ],
                  truncated: false,
                },
          ),
        ),
      ),
    );

    renderWithProviders(<HistoryTab databaseId="spoke-a/pds.orders" />);

    expect(await screen.findByText("Backup succeeded")).toBeInTheDocument();
    expect(screen.getByText("4096 bytes 90000 ms")).toBeInTheDocument();
  });
});

describe("JoinHistory", () => {
  it("shows a phase and its output", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        jsonResponse({
          runs: [
            {
              runId: "run-1",
              phases: [
                { phase: "installing", outcome: "failed", durationMs: 60000, output: "gave up" },
              ],
            },
          ],
        }),
      ),
    );

    renderWithProviders(<JoinHistory environment="spoke-a" />);

    expect(await screen.findByText("run-1")).toBeInTheDocument();
    expect(screen.getByText(/installing failed/)).toBeInTheDocument();
    expect(screen.getByText("gave up")).toBeInTheDocument();
  });
});
