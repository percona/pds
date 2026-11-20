import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/utils";
import { AwsAccountsSection } from "./accounts-section";

function account(overrides: Record<string, unknown> = {}) {
  return {
    id: "cc_1",
    accountId: "123456789012",
    alias: "payments",
    state: "ready",
    allowedRegions: ["eu-west-1", "us-east-1"],
    stackRegion: "eu-west-1",
    templateVersion: 2,
    latestTemplateVersion: 2,
    createdAt: "2026-11-01T00:00:00Z",
    environments: ["orders"],
    ...overrides,
  };
}

function stubApi(accounts: unknown[], failList = false) {
  const calls: { path: string; init?: RequestInit }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input).split("?")[0];
      calls.push({ path, init });
      const reply = (body: unknown, status = 200) =>
        Promise.resolve(
          new Response(status === 204 ? null : JSON.stringify(body), {
            status,
            headers: { "Content-Type": "application/json" },
          }),
        );
      if (path === "/api/cloud-connections" && init?.method === "GET") {
        return failList
          ? reply({ code: "ErrNotImplemented", message: "Not set up." }, 501)
          : reply({ cloudConnections: accounts });
      }
      if (init?.method === "DELETE") {
        return reply(null, 204);
      }
      if (path === "/api/cloud-connection-tokens") {
        return reply(
          { id: "tok", command: "curl connect", expiresAt: "", allowedRegions: [] },
          201,
        );
      }

      return reply({});
    }),
  );

  return calls;
}

afterEach(() => vi.unstubAllGlobals());

describe("AwsAccountsSection", () => {
  it("lists each account by alias and ID with its state in words", async () => {
    stubApi([
      account(),
      account({
        id: "cc_2",
        alias: "",
        accountId: "210987654321",
        state: "unreachable",
        reason: "PDS cannot assume the role.",
        environments: [],
      }),
      account({
        id: "cc_3",
        alias: "labs",
        accountId: "111111111111",
        state: "out_of_date",
        templateVersion: 1,
        environments: [],
      }),
    ]);

    renderWithProviders(<AwsAccountsSection />);

    expect(await screen.findByRole("heading", { name: "AWS accounts" })).toBeInTheDocument();
    const rows = await screen.findAllByRole("listitem");
    expect(rows).toHaveLength(3);
    expect(within(rows[0]).getByText("payments")).toBeInTheDocument();
    expect(within(rows[0]).getByText("123456789012")).toBeInTheDocument();
    expect(within(rows[0]).getByText("Connected")).toBeInTheDocument();
    expect(within(rows[1]).getByText("Unreachable")).toBeInTheDocument();
    expect(within(rows[2]).getByText("Out of date")).toBeInTheDocument();
  });

  it("offers Fix only for an account that does not work, and opens the command in place", async () => {
    stubApi([
      account(),
      account({
        id: "cc_2",
        alias: "labs",
        state: "unreachable",
        reason: "PDS cannot assume the role.",
        environments: [],
      }),
    ]);
    const user = userEvent.setup();

    renderWithProviders(<AwsAccountsSection />);

    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[0]).queryByRole("button", { name: "Fix" })).not.toBeInTheDocument();
    await user.click(within(rows[1]).getByRole("button", { name: "Fix" }));

    expect(within(rows[1]).getByText("PDS cannot assume the role.")).toBeInTheDocument();
    expect(within(rows[1]).getByRole("button", { name: "Create command" })).toBeInTheDocument();
  });

  it("opens the fix an environment row linked to", async () => {
    stubApi([account({ id: "cc_2", alias: "labs", state: "unreachable", environments: [] })]);

    renderWithProviders(<AwsAccountsSection />, { route: "/environments?fix=cc_2" });

    expect(await screen.findByRole("button", { name: "Create command" })).toBeInTheDocument();
  });

  it("shows the details of an account", async () => {
    stubApi([account()]);
    const user = userEvent.setup();

    renderWithProviders(<AwsAccountsSection />);
    await user.click(await screen.findByRole("button", { name: "Details" }));

    expect(screen.getByText("eu-west-1, us-east-1")).toBeInTheDocument();
    expect(screen.getByText("orders")).toBeInTheDocument();
  });

  it("removes an account after its alias is typed", async () => {
    const calls = stubApi([account({ environments: [] })]);
    const user = userEvent.setup();

    renderWithProviders(<AwsAccountsSection />);
    await user.click(await screen.findByRole("button", { name: "Remove" }));
    const confirm = screen.getByRole("button", { name: "Remove AWS account" });
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText("Account name"), "payments");
    await user.click(confirm);

    await waitFor(() =>
      expect(
        calls.some(
          (call) => call.init?.method === "DELETE" && call.path === "/api/cloud-connections/cc_1",
        ),
      ).toBe(true),
    );
    const removal = calls.find((call) => call.init?.method === "DELETE");
    expect(JSON.parse(String(removal?.init?.body))).toEqual({ confirm: "payments" });
  });

  it("asks for the account ID when the account has no alias", async () => {
    stubApi([account({ alias: "", environments: [] })]);
    const user = userEvent.setup();

    renderWithProviders(<AwsAccountsSection />);
    await user.click(await screen.findByRole("button", { name: "Remove" }));

    expect(screen.getByText("Type 123456789012 to confirm.")).toBeInTheDocument();
  });

  it("shows an empty state that connects the first account", async () => {
    stubApi([]);

    renderWithProviders(<AwsAccountsSection />);

    expect(await screen.findByText("No AWS accounts yet")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Connect an AWS account" })).toHaveAttribute(
      "href",
      "/environments/aws-accounts/new",
    );
  });

  it("stays out of the way when the control plane cannot connect accounts", async () => {
    stubApi([], true);

    renderWithProviders(<AwsAccountsSection />);

    await waitFor(() => expect(screen.queryByText("No AWS accounts yet")).not.toBeInTheDocument());
    expect(screen.queryByRole("heading", { name: "AWS accounts" })).not.toBeInTheDocument();
  });
});
