import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cancelAwsConnectCommand,
  fetchAwsAccount,
  fetchAwsAccounts,
  issueAwsConnectCommand,
  removeAwsAccount,
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

describe("AWS account calls", () => {
  it("lists the accounts and tolerates an answer with none", async () => {
    vi.stubGlobal("fetch", reply({}));

    expect(await fetchAwsAccounts()).toEqual([]);
  });

  it("reads one account with its steps", async () => {
    const fetchMock = reply({ id: "cc_1", accountId: "1", steps: [] });
    vi.stubGlobal("fetch", fetchMock);

    const account = await fetchAwsAccount("cc_1");

    expect(account.id).toBe("cc_1");
    expect(fetchMock.mock.calls[0][0]).toBe("/api/cloud-connections/cc_1");
  });

  it("asks for a connect command limited to regions", async () => {
    const fetchMock = reply(
      {
        id: "tok",
        command: "curl x",
        expiresAt: "2026-11-20T11:00:00Z",
        allowedRegions: ["eu-west-1"],
      },
      201,
    );
    vi.stubGlobal("fetch", fetchMock);

    const issued = await issueAwsConnectCommand(["eu-west-1"]);

    expect(issued.command).toBe("curl x");
    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/cloud-connection-tokens");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ allowedRegions: ["eu-west-1"] });
  });

  it("asks for all regions with the wildcard alone", async () => {
    const fetchMock = reply({ id: "tok", command: "c", expiresAt: "", allowedRegions: ["*"] }, 201);
    vi.stubGlobal("fetch", fetchMock);
    await issueAwsConnectCommand(["*"]);
    const [, init] = fetchMock.mock.calls[0];
    expect(JSON.parse(String(init?.body))).toEqual({ allowedRegions: ["*"] });
  });
  it("cancels a connect command", async () => {
    const fetchMock = reply(null, 204);
    vi.stubGlobal("fetch", fetchMock);

    await cancelAwsConnectCommand("tok");

    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/cloud-connection-tokens/tok");
    expect(init?.method).toBe("DELETE");
  });

  it("removes an account with the name that was typed", async () => {
    const fetchMock = reply(null, 204);
    vi.stubGlobal("fetch", fetchMock);

    await removeAwsAccount("cc_1", "payments");

    const [path, init] = fetchMock.mock.calls[0];
    expect(path).toBe("/api/cloud-connections/cc_1");
    expect(JSON.parse(String(init?.body))).toEqual({ confirm: "payments" });
  });
});
