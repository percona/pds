import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/utils";
import { ConnectAccountPanel } from "./connect-account-panel";

function json(body: unknown, status = 200) {
  return Promise.resolve(
    new Response(status === 204 ? null : JSON.stringify(body), {
      status,
      headers: { "Content-Type": "application/json" },
    }),
  );
}

type Calls = { path: string; init?: RequestInit }[];

function stubApi(onList: () => unknown, onOne: () => unknown) {
  const calls: Calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input).split("?")[0];
      calls.push({ path, init });
      if (path === "/api/cloud-connection-tokens" && init?.method === "POST") {
        return json(
          {
            id: "tok_1",
            command:
              "curl -fsSL 'https://hub.example/api/internal/cloud-connection/connect.sh' | bash -s -- --token 'ccn_abc'",
            expiresAt: new Date(Date.now() + 3600_000).toISOString(),
            allowedRegions: ["eu-west-1"],
          },
          201,
        );
      }
      if (path === "/api/cloud-connection-tokens/tok_1") {
        return json(null, 204);
      }
      if (path === "/api/cloud-connections") {
        return json(onList());
      }
      if (path.startsWith("/api/cloud-connections/")) {
        return json(onOne());
      }

      return json({});
    }),
  );

  return calls;
}

const created = () => new Date().toISOString();

function account(extra: Record<string, unknown> = {}) {
  return {
    id: "cc_1",
    accountId: "123456789012",
    alias: "payments",
    state: "pending",
    allowedRegions: ["eu-west-1"],
    stackRegion: "eu-west-1",
    templateVersion: 1,
    latestTemplateVersion: 1,
    createdAt: created(),
    environments: [],
    ...extra,
  };
}

afterEach(() => vi.unstubAllGlobals());

describe("ConnectAccountPanel", () => {
  it("makes the command only when asked, and shows it to copy", async () => {
    const calls = stubApi(
      () => ({ cloudConnections: [] }),
      () => ({}),
    );
    const user = userEvent.setup();

    renderWithProviders(<ConnectAccountPanel pollMs={20} />);

    expect(calls.some((call) => call.path === "/api/cloud-connection-tokens")).toBe(false);
    await user.click(screen.getByRole("button", { name: "Create command" }));

    expect(await screen.findByText(/--token 'ccn_abc'/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy command" })).toBeInTheDocument();
    expect(screen.getByText(/Treat it like a password/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open CloudShell" })).toHaveAttribute(
      "href",
      expect.stringContaining("cloudshell"),
    );
    expect(screen.getByText("Find the AWS account")).toBeInTheDocument();
    expect(screen.getByText("Create the roles")).toBeInTheDocument();
    expect(screen.getByText("Check that PDS can use them")).toBeInTheDocument();
  });

  it("limits the regions the command may use", async () => {
    const calls = stubApi(
      () => ({ cloudConnections: [] }),
      () => ({}),
    );
    const user = userEvent.setup();

    renderWithProviders(<ConnectAccountPanel pollMs={20} />);
    await user.click(screen.getByRole("checkbox", { name: "Limit the regions" }));
    await user.type(screen.getByLabelText("Regions"), "eu-west-1, us-east-1");
    await user.click(screen.getByRole("button", { name: "Create command" }));

    await screen.findByText(/--token/);
    const mint = calls.find((call) => call.path === "/api/cloud-connection-tokens");
    expect(JSON.parse(String(mint?.init?.body))).toEqual({
      allowedRegions: ["eu-west-1", "us-east-1"],
    });
  });

  it("follows the command to a connected account and says so once", async () => {
    let step = 0;
    stubApi(
      () => ({ cloudConnections: [account()] }),
      () => {
        step += 1;
        const at = new Date().toISOString();
        if (step < 3) {
          return account({ steps: [{ step: "stack", status: "started", at }] });
        }

        return account({
          state: "ready",
          steps: [
            { step: "stack", status: "succeeded", at },
            { step: "protect", status: "succeeded", at },
            { step: "verify", status: "succeeded", at },
          ],
        });
      },
    );
    const onConnected = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(<ConnectAccountPanel pollMs={20} onConnected={onConnected} />);
    await user.click(screen.getByRole("button", { name: "Create command" }));

    expect(await screen.findByText("AWS account payments is connected.")).toBeInTheDocument();
    await waitFor(() => expect(onConnected).toHaveBeenCalledTimes(1));
    expect(onConnected.mock.calls[0][0].id).toBe("cc_1");
  });

  it("names a step that failed and what to do", async () => {
    stubApi(
      () => ({ cloudConnections: [account()] }),
      () => account({ steps: [{ step: "stack", status: "failed", at: new Date().toISOString() }] }),
    );
    const user = userEvent.setup();

    renderWithProviders(<ConnectAccountPanel pollMs={20} />);
    await user.click(screen.getByRole("button", { name: "Create command" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "run the command again as an administrator",
    );
  });

  it("says when the account was connected before", async () => {
    stubApi(
      () => ({
        cloudConnections: [
          account({
            createdAt: "2026-01-01T00:00:00Z",
            signalAt: new Date().toISOString(),
            state: "ready",
          }),
        ],
      }),
      () => account({ createdAt: "2026-01-01T00:00:00Z", signalAt: new Date().toISOString() }),
    );
    const user = userEvent.setup();

    renderWithProviders(<ConnectAccountPanel pollMs={20} />);
    await user.click(screen.getByRole("button", { name: "Create command" }));

    expect(await screen.findByText(/already connected/)).toBeInTheDocument();
  });

  it("cancels the command", async () => {
    const calls = stubApi(
      () => ({ cloudConnections: [] }),
      () => ({}),
    );
    const user = userEvent.setup();

    renderWithProviders(<ConnectAccountPanel pollMs={20} />);
    await user.click(screen.getByRole("button", { name: "Create command" }));
    await screen.findByText(/--token/);
    await user.click(screen.getByRole("button", { name: "Cancel command" }));

    await waitFor(() =>
      expect(
        calls.some(
          (call) =>
            call.path === "/api/cloud-connection-tokens/tok_1" && call.init?.method === "DELETE",
        ),
      ).toBe(true),
    );
    expect(await screen.findByRole("button", { name: "Create command" })).toBeInTheDocument();
  });
});
