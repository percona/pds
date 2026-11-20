import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createManagedEnvironment,
  deferUpgrade,
  deleteManagedEnvironment,
  fetchRemains,
  previewUpdate,
  putMaintenanceWindow,
  retryRun,
  startRun,
} from "./api";

function reply(body: unknown, status = 200) {
  return vi.fn<(input: string, init?: RequestInit) => Promise<Response>>(() =>
    Promise.resolve(
      new Response(status === 204 ? null : JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("managed environment calls", () => {
  it("creates an environment through an AWS account", async () => {
    const fetchMock = reply({ environment: { name: "orders" }, run: { id: 4 } }, 202);
    vi.stubGlobal("fetch", fetchMock);

    const created = await createManagedEnvironment("cc_1", { name: "orders", region: "eu-west-1" });

    expect(created.run.id).toBe(4);
    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/cloud-connections/cc_1/environments");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ name: "orders", region: "eu-west-1" });
  });

  it("retries a failed run by its id", async () => {
    const fetchMock = reply({ run: { id: 4 }, provisioning: { phases: [] } }, 202);
    vi.stubGlobal("fetch", fetchMock);

    await retryRun("orders", 4);

    expect(fetchMock.mock.calls[0][0]).toBe("/api/environments/orders/runs/4/retry");
    expect(fetchMock.mock.calls[0][1]?.method).toBe("POST");
  });

  it("sends the typed name to delete", async () => {
    const fetchMock = reply({ run: { id: 5 }, provisioning: {} }, 202);
    vi.stubGlobal("fetch", fetchMock);

    await deleteManagedEnvironment("orders", "orders");

    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/environments/orders");
    expect(init?.method).toBe("DELETE");
    expect(JSON.parse(String(init?.body))).toEqual({ confirm: "orders" });
  });

  it("forgets the environment in PDS only when asked", async () => {
    const fetchMock = reply(null, 204);
    vi.stubGlobal("fetch", fetchMock);

    await deleteManagedEnvironment("orders", "orders", { pdsOnly: true });

    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      confirm: "orders",
      scope: "pds_only",
    });
  });

  it("starts a run of a kind with the Kubernetes version and the node group confirmation", async () => {
    const fetchMock = reply({ run: { id: 7 }, provisioning: { phases: [] } }, 202);
    vi.stubGlobal("fetch", fetchMock);

    await startRun("orders", {
      kind: "upgrade_kubernetes",
      kubernetesVersion: "1.32",
      confirmNodeGroupReplace: true,
    });

    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/environments/orders/runs");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({
      kind: "upgrade_kubernetes",
      kubernetesVersion: "1.32",
      confirmNodeGroupReplace: true,
    });
  });

  it("defers an upgrade for a number of days", async () => {
    const fetchMock = reply({ deferred: true });
    vi.stubGlobal("fetch", fetchMock);

    await deferUpgrade("orders", 14);

    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/environments/orders/upgrades/defer");
    expect(JSON.parse(String(init?.body))).toEqual({ days: 14 });
  });

  it("replaces the maintenance window", async () => {
    const fetchMock = reply({});
    vi.stubGlobal("fetch", fetchMock);

    await putMaintenanceWindow("orders", { day: 0, hour: 2, timeZone: "Europe/Berlin" });

    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/environments/orders/maintenance-window");
    expect(init?.method).toBe("PUT");
    expect(JSON.parse(String(init?.body))).toEqual({ day: 0, hour: 2, timeZone: "Europe/Berlin" });
  });

  it("previews an update, with a Kubernetes version when there is one", async () => {
    const fetchMock = reply({ replacesNetwork: false, replacesNodeGroup: false, changes: [] });
    vi.stubGlobal("fetch", fetchMock);

    await previewUpdate("orders", "1.32");
    await previewUpdate("orders");

    expect(fetchMock.mock.calls[0][0]).toBe("/api/environments/orders/updates/preview");
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toEqual({
      kubernetesVersion: "1.32",
    });
    expect(JSON.parse(String(fetchMock.mock.calls[1][1]?.body))).toEqual({});
  });

  it("reads what Remove from PDS only leaves in the AWS account", async () => {
    const fetchMock = reply({
      accountId: "123456789012",
      region: "eu-west-1",
      stackName: "pds-orders",
      eksName: "pds-orders",
      bucket: "pds-orders-backups",
    });
    vi.stubGlobal("fetch", fetchMock);

    const remains = await fetchRemains("orders");

    expect(fetchMock.mock.calls[0][0]).toBe("/api/environments/orders/remains");
    expect(remains.eksName).toBe("pds-orders");
  });
});
