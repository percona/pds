import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMockLifecycle, renderWithProviders } from "@/test/utils";
import { LifecyclePanel } from "./lifecycle-panel";

function stubApi() {
  const fetchMock = vi.fn<(input: string, init?: RequestInit) => Promise<Response>>((input) => {
    const path = String(input);
    const body = path.endsWith("/updates/preview")
      ? { replacesNetwork: false, replacesNodeGroup: false, changes: [] }
      : path.endsWith("/runs")
        ? { run: { id: 3 }, provisioning: { phases: [] } }
        : createMockLifecycle();

    return Promise.resolve(
      new Response(JSON.stringify(body), {
        status: path.endsWith("/runs") ? 202 : 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

function call(fetchMock: ReturnType<typeof stubApi>, suffix: string) {
  return fetchMock.mock.calls.find(([path]) => String(path).endsWith(suffix));
}

afterEach(() => vi.unstubAllGlobals());

describe("LifecyclePanel", () => {
  it("says the environment is up to date when nothing is pending", () => {
    stubApi();

    renderWithProviders(<LifecyclePanel environment="orders" lifecycle={createMockLifecycle()} />);

    expect(screen.getByText("PDS 1.4.0 is up to date.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Upgrade now" })).not.toBeInTheDocument();
    expect(screen.queryByText("Changed outside PDS")).not.toBeInTheDocument();
  });

  it("upgrades early", async () => {
    const fetchMock = stubApi();
    const user = userEvent.setup();

    renderWithProviders(
      <LifecyclePanel
        environment="orders"
        lifecycle={createMockLifecycle({ pendingChartVersion: "1.5.0" })}
      />,
    );

    expect(screen.getByText("PDS 1.5.0 is available.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Upgrade now" }));

    await waitFor(() => expect(call(fetchMock, "/runs")).toBeDefined());
    expect(JSON.parse(String(call(fetchMock, "/runs")?.[1]?.body))).toEqual({
      kind: "upgrade_pds",
    });
  });

  it("defers once for up to 30 days", async () => {
    const fetchMock = stubApi();
    const user = userEvent.setup();

    renderWithProviders(
      <LifecyclePanel
        environment="orders"
        lifecycle={createMockLifecycle({ pendingChartVersion: "1.5.0" })}
      />,
    );

    const days = screen.getByLabelText("Defer for");
    expect(
      within(days)
        .getAllByRole("option")
        .map((option) => option.textContent),
    ).toEqual(["7 days", "14 days", "30 days"]);
    await user.selectOptions(days, "30");
    await user.click(screen.getByRole("button", { name: "Defer" }));

    await waitFor(() => expect(call(fetchMock, "/upgrades/defer")).toBeDefined());
    expect(JSON.parse(String(call(fetchMock, "/upgrades/defer")?.[1]?.body))).toEqual({ days: 30 });
  });

  it("shows a deferral and offers no second one", () => {
    stubApi();

    renderWithProviders(
      <LifecyclePanel
        environment="orders"
        lifecycle={createMockLifecycle({
          pendingChartVersion: "1.5.0",
          deferred: true,
          deferredUntil: "2026-12-10T00:00:00Z",
        })}
      />,
    );

    expect(screen.getByText(/Deferred until/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Defer" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Upgrade now" })).toBeInTheDocument();
  });

  it("says a version failed here and was rolled back", () => {
    stubApi();

    renderWithProviders(
      <LifecyclePanel
        environment="orders"
        lifecycle={createMockLifecycle({ skippedChartVersion: "1.5.0" })}
      />,
    );

    expect(
      screen.getByText(/PDS 1.5.0 failed on this environment and was rolled back/),
    ).toBeInTheDocument();
  });

  it("shows template revision N, latest M and opens the change preview", async () => {
    const fetchMock = stubApi();
    const user = userEvent.setup();

    renderWithProviders(
      <LifecyclePanel
        environment="orders"
        lifecycle={createMockLifecycle({
          templateRevision: 2,
          latestTemplateRevision: 3,
          templateOutOfDate: true,
          templateSummary: "Template revision 2, latest 3",
        })}
      />,
    );

    expect(screen.getByText("Template revision 2, latest 3")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Review update" }));

    expect(await screen.findByRole("dialog")).toBeInTheDocument();
    await waitFor(() => expect(call(fetchMock, "/updates/preview")).toBeDefined());
  });

  it("offers a Kubernetes upgrade when one is available", async () => {
    stubApi();
    const user = userEvent.setup();

    renderWithProviders(
      <LifecyclePanel
        environment="orders"
        lifecycle={createMockLifecycle({ kubernetesUpgradeAvailable: true })}
      />,
    );

    expect(screen.getByText("Kubernetes 1.31 can be upgraded.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Upgrade Kubernetes" }));

    expect(await screen.findByLabelText("Kubernetes version")).toBeInTheDocument();
  });

  it("lists what changed outside PDS and offers no repair", () => {
    stubApi();

    renderWithProviders(
      <LifecyclePanel
        environment="orders"
        lifecycle={createMockLifecycle({
          drift: [
            { resource: "NodeGroup", detail: "Instance type is m5.xlarge, PDS set m5.large" },
            { resource: "BackupBucket", detail: "Versioning is off" },
          ],
          driftCheckedAt: "2026-11-20T09:00:00Z",
        })}
      />,
    );

    const list = screen.getByRole("list", { name: "Changed outside PDS" });
    expect(list).toHaveTextContent("NodeGroup");
    expect(list).toHaveTextContent("Instance type is m5.xlarge");
    expect(list).toHaveTextContent("BackupBucket");
    expect(screen.getByText(/PDS reports these and does not change them/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /repair|fix|revert/i })).not.toBeInTheDocument();
  });

  it("edits the maintenance window", async () => {
    const fetchMock = stubApi();
    const user = userEvent.setup();

    renderWithProviders(<LifecyclePanel environment="orders" lifecycle={createMockLifecycle()} />);

    expect(screen.getByText("Sunday at 02:00 Europe/Berlin")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Edit window" }));
    await user.selectOptions(screen.getByLabelText("Day"), "3");
    await user.selectOptions(screen.getByLabelText("Hour"), "4");
    const zone = screen.getByLabelText("Time zone");
    await user.clear(zone);
    await user.type(zone, "UTC");
    await user.click(screen.getByRole("button", { name: "Save window" }));

    await waitFor(() => expect(call(fetchMock, "/maintenance-window")).toBeDefined());
    expect(call(fetchMock, "/maintenance-window")?.[1]?.method).toBe("PUT");
    expect(JSON.parse(String(call(fetchMock, "/maintenance-window")?.[1]?.body))).toEqual({
      day: 3,
      hour: 4,
      timeZone: "UTC",
    });
  });
});
