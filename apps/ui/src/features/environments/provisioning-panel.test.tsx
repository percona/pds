import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/utils";
import { ProvisioningPanel } from "./provisioning-panel";
import type { Provisioning } from "./types";

const phases = [
  "Creating network and Kubernetes cluster",
  "Connecting to the environment",
  "Installing PDS",
  "Finishing",
];

function provisioning(overrides: Partial<Provisioning> = {}): Provisioning {
  return {
    run: { id: 7, kind: "create", status: "running", startedAt: "2026-11-20T10:00:00Z" },
    progress: "in_progress",
    phases: [
      { name: phases[0], status: "succeeded" },
      { name: phases[1], status: "running" },
      { name: phases[2], status: "pending" },
      { name: phases[3], status: "pending" },
    ],
    ...overrides,
  };
}

afterEach(() => vi.unstubAllGlobals());

describe("ProvisioningPanel", () => {
  it("lists the four phases, each with its words", () => {
    renderWithProviders(<ProvisioningPanel environment="orders" provisioning={provisioning()} />);

    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(4);
    expect(items[0]).toHaveTextContent("Creating network and Kubernetes cluster");
    expect(items[0]).toHaveTextContent("Done");
    expect(items[1]).toHaveTextContent("Running");
    expect(items[2]).toHaveTextContent("Waiting");
  });

  it("says why a run failed and retries only on request", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      void input;

      return Promise.resolve(
        new Response(JSON.stringify({ run: { id: 7 }, provisioning: {} }), {
          status: 202,
          headers: { "Content-Type": "application/json" },
        }),
      );
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    renderWithProviders(
      <ProvisioningPanel
        environment="orders"
        provisioning={provisioning({
          run: { id: 7, kind: "create", status: "failed", startedAt: "2026-11-20T10:00:00Z" },
          progress: "failed",
          phases: [
            { name: phases[0], status: "failed" },
            { name: phases[1], status: "pending" },
            { name: phases[2], status: "pending" },
            { name: phases[3], status: "pending" },
          ],
          failure: {
            sentence:
              "AWS account payments has no room for another VPC in eu-west-1. Ask AWS to raise the quota, then retry.",
            action: "retry",
            refusal: {
              reason: "quota",
              service: "vpc",
              item: "VPCs per Region",
              region: "eu-west-1",
            },
          },
        })}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("has no room for another VPC");
    expect(fetchMock).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(String(fetchMock.mock.calls[0][0])).toBe("/api/environments/orders/runs/7/retry");
  });

  it("shows the sentence of a run paused on its AWS account", () => {
    renderWithProviders(
      <ProvisioningPanel
        environment="orders"
        provisioning={provisioning({
          progress: "waiting",
          message: "Waiting for the AWS connection.",
          phases: [{ name: phases[0], status: "waiting" }],
        })}
      />,
    );

    expect(screen.getByText("Waiting for the AWS connection.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
  });

  it("shows a delete as one line, because it has no phases", () => {
    renderWithProviders(
      <ProvisioningPanel
        environment="orders"
        provisioning={provisioning({
          run: { id: 9, kind: "delete", status: "running", startedAt: "2026-11-20T10:00:00Z" },
          phases: [],
        })}
      />,
    );

    expect(screen.queryAllByRole("listitem")).toHaveLength(0);
    expect(
      screen.getByText("Deleting the environment and what PDS built in AWS."),
    ).toBeInTheDocument();
  });

  it("names what a blocked delete left behind", () => {
    renderWithProviders(
      <ProvisioningPanel
        environment="orders"
        provisioning={provisioning({
          run: { id: 9, kind: "delete", status: "failed", startedAt: "2026-11-20T10:00:00Z" },
          progress: "blocked",
          phases: [],
          failure: {
            sentence: "Delete blocked. Volume vol-1 is still in your account. Retry.",
            action: "retry",
          },
        })}
      />,
    );

    expect(screen.getByRole("alert")).toHaveTextContent("Delete blocked. Volume vol-1");
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});
