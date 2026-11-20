import { afterEach, describe, expect, it, vi } from "vitest";
import { createManagedEnvironment, deleteManagedEnvironment, retryRun } from "./api";

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
});
