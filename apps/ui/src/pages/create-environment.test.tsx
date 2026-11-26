import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";

function account(overrides: Record<string, unknown> = {}) {
  return {
    id: "cc_1",
    accountId: "123456789012",
    alias: "payments",
    state: "ready",
    allowedRegions: ["eu-west-1", "us-east-1"],
    stackRegion: "eu-west-1",
    templateVersion: 1,
    latestTemplateVersion: 1,
    createdAt: "2026-11-01T00:00:00Z",
    environments: [],
    ...overrides,
  };
}

type Call = { path: string; init?: RequestInit };

function stubApi(accounts: unknown[], create?: () => Response) {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input).split("?")[0];
      calls.push({ path, init });
      const json = (value: unknown, status = 200) =>
        new Response(JSON.stringify(value), {
          status,
          headers: { "Content-Type": "application/json" },
        });
      if (path === "/api/environments") {
        return Promise.resolve(
          json({ hub: true, environments: [{ name: "taken", status: "connected" }] }),
        );
      }
      if (path === "/api/cloud-connections" && init?.method === "GET") {
        return Promise.resolve(json({ cloudConnections: accounts }));
      }
      if (/^\/api\/cloud-connections\/[^/]+\/environments$/.test(path)) {
        return Promise.resolve(
          create?.() ??
            json({ environment: { name: "orders", status: "pending" }, run: { id: 1 } }, 202),
        );
      }
      if (path === "/api/cloud-connection-tokens") {
        return Promise.resolve(
          json(
            { id: "tok", command: "curl connect --token ccn_1", expiresAt: "", allowedRegions: [] },
            201,
          ),
        );
      }
      if (path === "/api/connect") {
        return Promise.resolve(
          json({ hubUrl: "https://hub.example", gitUrl: "https://git.example" }),
        );
      }

      return Promise.resolve(json({}));
    }),
  );

  return calls;
}

afterEach(() => vi.unstubAllGlobals());

async function name(user: ReturnType<typeof userEvent.setup>, value: string) {
  await user.type(await screen.findByLabelText("Environment name"), value);
}

describe("Create an environment", () => {
  it("builds the environment in an AWS account by default, in five steps", async () => {
    stubApi([account()]);

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });

    for (const title of [
      "1. Name this environment",
      "2. Choose how to create it",
      "3. Choose the AWS account",
      "4. Choose the region",
      "5. Create the environment",
    ]) {
      expect(await screen.findByRole("heading", { name: title })).toBeInTheDocument();
    }
    expect(screen.getByRole("link", { name: "Run a command yourself instead" })).toHaveAttribute(
      "href",
      "/environments/new?mode=command",
    );
  });

  it("keeps the command path for someone who runs it themselves", async () => {
    stubApi([]);

    renderWithProviders(<AppRoutes />, { route: "/environments/new?mode=command" });

    expect(await screen.findByRole("heading", { name: "2. Run this command" })).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Let PDS create it in your AWS account instead" }),
    ).toHaveAttribute("href", "/environments/new");
  });

  it("offers the command itself when the Team has no AWS account yet", async () => {
    stubApi([]);

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });

    const step = (
      await screen.findByRole("heading", { name: "3. Choose the AWS account" })
    ).closest("section") as HTMLElement;
    expect(await within(step).findByRole("button", { name: "Create command" })).toBeInTheDocument();
    expect(screen.getByText("Choose an AWS account to pick a region.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create environment" })).toBeDisabled();
  });

  it("lists the accounts, and a broken one opens its fix in place", async () => {
    stubApi([
      account(),
      account({
        id: "cc_2",
        alias: "labs",
        accountId: "210987654321",
        state: "unreachable",
        reason: "PDS cannot assume the role.",
      }),
    ]);
    const user = userEvent.setup();

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });
    await user.click(await screen.findByRole("radio", { name: /labs/ }));

    expect(screen.getByText("PDS cannot assume the role.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create command" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create environment" })).toBeDisabled();
  });

  it("connects another account last, and offers the command", async () => {
    stubApi([account()]);
    const user = userEvent.setup();

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });
    const radios = await screen.findAllByRole("radio");
    expect(radios.at(-1)).toHaveAccessibleName(/Connect another AWS account/);
    await user.click(radios.at(-1) as HTMLElement);

    expect(screen.getByRole("button", { name: "Create command" })).toBeInTheDocument();
  });

  it("creates the environment in the chosen account and region, then shows its progress", async () => {
    const calls = stubApi([account()]);
    const user = userEvent.setup();

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });
    await name(user, "orders");
    await user.click(await screen.findByRole("radio", { name: /payments/ }));
    await user.selectOptions(screen.getByLabelText("Region"), "us-east-1");
    await user.click(screen.getByRole("button", { name: "Create environment" }));

    await waitFor(() =>
      expect(calls.some((call) => call.path === "/api/cloud-connections/cc_1/environments")).toBe(
        true,
      ),
    );
    const post = calls.find((call) => call.path === "/api/cloud-connections/cc_1/environments");
    expect(JSON.parse(String(post?.init?.body))).toEqual({ name: "orders", region: "us-east-1" });
    expect(
      await screen.findByRole("heading", { level: 1, name: "Environments" }),
    ).toBeInTheDocument();
  });

  it("offers every region for an account that may build anywhere", async () => {
    const calls = stubApi([account({ allowedRegions: ["*"] })]);
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments/new" });
    await name(user, "orders");
    await user.click(await screen.findByRole("radio", { name: /payments/ }));
    expect(screen.getByRole("option", { name: "ap-southeast-2" })).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText("Region"), "ap-southeast-2");
    await user.click(screen.getByRole("button", { name: "Create environment" }));
    await waitFor(() =>
      expect(calls.some((call) => call.path === "/api/cloud-connections/cc_1/environments")).toBe(
        true,
      ),
    );
    const post = calls.find((call) => call.path === "/api/cloud-connections/cc_1/environments");
    expect(JSON.parse(String(post?.init?.body))).toEqual({
      name: "orders",
      region: "ap-southeast-2",
    });
  });
  it("starts from the account a connect page handed over", async () => {
    stubApi([account(), account({ id: "cc_2", alias: "labs", accountId: "210987654321" })]);

    renderWithProviders(<AppRoutes />, { route: "/environments/new?account=cc_2" });

    expect(await screen.findByRole("radio", { name: /labs/ })).toBeChecked();
  });

  it("refuses a name that is taken or not a name, before anything is sent", async () => {
    stubApi([account()]);
    const user = userEvent.setup();

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });
    await user.click(await screen.findByRole("radio", { name: /payments/ }));
    await name(user, "taken");

    expect(
      await screen.findByText("An environment with that name already exists. Pick another."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create environment" })).toBeDisabled();
  });

  it("shows why the control plane refused and keeps the form", async () => {
    stubApi(
      [account()],
      () =>
        new Response(
          JSON.stringify({
            code: "ErrConflict",
            message:
              "This AWS account is not ready. Open it, check the connection, then try again.",
          }),
          { status: 409, headers: { "Content-Type": "application/json" } },
        ),
    );
    const user = userEvent.setup();

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });
    await name(user, "orders");
    await user.click(await screen.findByRole("radio", { name: /payments/ }));
    await user.click(screen.getByRole("button", { name: "Create environment" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("This AWS account is not ready");
    expect(screen.getByRole("heading", { name: "5. Create the environment" })).toBeInTheDocument();
  });
});
