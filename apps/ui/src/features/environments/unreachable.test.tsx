import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { UnreachableBanner } from "@/features/environments";
import { renderWithProviders } from "@/test/utils";

describe("UnreachableBanner", () => {
  // Create, scale, schedule and delete are commits. They succeed while the
  // environment is offline and Flux applies them when it reconnects. Saying they
  // "wait" reads as refused, which is the opposite of what happens.
  it("says the Git-backed changes are applied on reconnect", () => {
    renderWithProviders(<UnreachableBanner lastSeen="2 minutes ago" />);

    expect(screen.getByText(/applied when it reconnects/i)).toBeInTheDocument();
  });

  it("says backups and restores are the ones that cannot run", () => {
    renderWithProviders(<UnreachableBanner lastSeen="2 minutes ago" />);

    expect(screen.getByText(/backups and restores/i)).toBeInTheDocument();
  });

  it("names when it was last seen", () => {
    renderWithProviders(<UnreachableBanner lastSeen="2 minutes ago" />);

    expect(screen.getByText(/2 minutes ago/)).toBeInTheDocument();
  });

  // An environment that has never beaten is still being built. Nothing can be
  // queued for it, because nothing will pull what the hub writes.
  it("says nothing can be queued for an environment that has never reported", () => {
    renderWithProviders(<UnreachableBanner />);

    expect(screen.getByText(/no heartbeat yet/i)).toBeInTheDocument();
    expect(screen.queryByText(/applied when it reconnects/i)).not.toBeInTheDocument();
  });
});
