import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ConsentOrganization, ConsentRequest } from "@/features/agent-consent";
import { visit } from "@/lib/visit";
import { renderWithProviders } from "@/test/utils";
import { AuthorizeAgentPage } from "./authorize-agent";

vi.mock("@/lib/visit", () => ({ visit: vi.fn() }));

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function organization(overrides: Partial<ConsentOrganization> = {}): ConsentOrganization {
  return {
    id: 1,
    name: "Acme",
    role: "user",
    teams: [{ id: 5, name: "payments" }],
    ownTeamId: 5,
    access: "change_delete",
    passwords: false,
    sqlWrites: false,
    ...overrides,
  };
}

function consent(overrides: Partial<ConsentRequest> = {}): ConsentRequest {
  return {
    client: {
      name: "Claude Code",
      host: "claude.example",
      kind: "cimd",
      redirectHost: "claude.example",
      loopback: false,
    },
    expiresAt: "2026-10-29T12:10:00Z",
    organizations: [organization()],
    ...overrides,
  };
}

type Call = { path: string; body: Record<string, unknown> };

/** Answers the consent routes and records what the page posted. */
function stubConsent(request: ConsentRequest | Response, decision?: (call: Call) => Response) {
  const calls: Call[] = [];
  const mock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : {};
    calls.push({ path, body });
    if (path === "/api/agent-consent/lookup") {
      return Promise.resolve(request instanceof Response ? request.clone() : jsonResponse(request));
    }
    const answer =
      decision?.({ path, body }) ??
      jsonResponse({ redirectUrl: "http://127.0.0.1:43211/callback?code=c0de&state=s" });

    return Promise.resolve(answer);
  });
  vi.stubGlobal("fetch", mock);

  return calls;
}

const route = "/authorize-agent?request=parked-token";

beforeEach(() => {
  vi.mocked(visit).mockClear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("AuthorizeAgentPage", () => {
  it("names who is asking and flags the name as the app's own claim", async () => {
    stubConsent(consent());

    renderWithProviders(<AuthorizeAgentPage />, { route });

    expect(await screen.findByRole("heading", { name: /Allow an app/ })).toBeInTheDocument();
    const asking = await screen.findByRole("region", { name: "Who is asking" });
    expect(within(asking).getByText("Claude Code")).toBeInTheDocument();
    expect(within(asking).getByText(/Name chosen by the app/)).toBeInTheDocument();
    expect(within(asking).getAllByText("claude.example").length).toBeGreaterThan(0);
  });

  it("warns when the app is on this computer and shows where the answer goes", async () => {
    stubConsent(
      consent({
        client: {
          name: "Test Agent",
          host: "agent.example",
          kind: "cimd",
          redirectHost: "127.0.0.1",
          loopback: true,
        },
      }),
    );

    renderWithProviders(<AuthorizeAgentPage />, { route });

    const asking = await screen.findByRole("region", { name: "Who is asking" });
    expect(within(asking).getByText("127.0.0.1")).toBeInTheDocument();
    expect(within(asking).getByText(/runs on this computer/i)).toBeInTheDocument();
  });

  it("says a registered app has no address to verify", async () => {
    stubConsent(
      consent({
        client: {
          name: "Cursor",
          host: "",
          kind: "dcr",
          redirectHost: "cursor.example",
          loopback: false,
        },
      }),
    );

    renderWithProviders(<AuthorizeAgentPage />, { route });

    const asking = await screen.findByRole("region", { name: "Who is asking" });
    expect(within(asking).getByText(/registered itself/i)).toBeInTheDocument();
  });

  it("pre-selects read and the person's own team", async () => {
    stubConsent(
      consent({
        organizations: [
          organization({
            role: "admin",
            teams: [
              { id: 5, name: "payments" },
              { id: 6, name: "search" },
            ],
            ownTeamId: 6,
          }),
        ],
      }),
    );

    renderWithProviders(<AuthorizeAgentPage />, { route });

    expect(await screen.findByRole("radio", { name: /Read only/ })).toBeChecked();
    expect(screen.getByRole("combobox", { name: "Team" })).toHaveValue("6");
  });

  it("does not offer tiers above what the organization allows", async () => {
    stubConsent(consent({ organizations: [organization({ access: "change" })] }));

    renderWithProviders(<AuthorizeAgentPage />, { route });

    expect(await screen.findByRole("radio", { name: /Read only/ })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Read and change$/ })).toBeInTheDocument();
    expect(screen.queryByRole("radio", { name: /delete/ })).not.toBeInTheDocument();
  });

  it("offers one tier when the limit is read", async () => {
    stubConsent(consent({ organizations: [organization({ access: "read" })] }));

    renderWithProviders(<AuthorizeAgentPage />, { route });

    await screen.findByRole("radio", { name: /Read only/ });
    expect(screen.getAllByRole("radio")).toHaveLength(1);
  });

  it("shows no password or SQL box unless the control plane offers one", async () => {
    stubConsent(consent());

    renderWithProviders(<AuthorizeAgentPage />, { route });

    await screen.findByRole("radio", { name: /Read only/ });
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  });

  it("shows each box only when offered, and unticked", async () => {
    stubConsent(
      consent({
        client: {
          name: "PDS local client",
          host: "",
          kind: "local",
          redirectHost: "127.0.0.1",
          loopback: true,
        },
        organizations: [organization({ passwords: true, sqlWrites: false })],
      }),
    );

    renderWithProviders(<AuthorizeAgentPage />, { route });

    const passwords = await screen.findByRole("checkbox", {
      name: /fetch database passwords into this computer's keychain/,
    });
    expect(passwords).not.toBeChecked();
    expect(
      screen.queryByRole("checkbox", { name: /change data with SQL/ }),
    ).not.toBeInTheDocument();
  });

  it("allows with the organization, team, tier and boxes that were chosen", async () => {
    const calls = stubConsent(
      consent({
        client: {
          name: "PDS local client",
          host: "",
          kind: "local",
          redirectHost: "127.0.0.1",
          loopback: true,
        },
        organizations: [
          organization({
            role: "admin",
            teams: [
              { id: 5, name: "payments" },
              { id: 6, name: "search" },
            ],
            passwords: true,
            sqlWrites: true,
          }),
        ],
      }),
    );

    renderWithProviders(<AuthorizeAgentPage />, { route });
    await userEvent.selectOptions(await screen.findByRole("combobox", { name: "Team" }), "6");
    await userEvent.click(screen.getByRole("radio", { name: /Read and change$/ }));
    await userEvent.click(screen.getByRole("checkbox", { name: /change data with SQL/ }));
    await userEvent.click(screen.getByRole("button", { name: "Allow access" }));

    await waitFor(() =>
      expect(visit).toHaveBeenCalledWith("http://127.0.0.1:43211/callback?code=c0de&state=s"),
    );
    const approve = calls.find((call) => call.path === "/api/agent-consent/approve");
    expect(approve?.body).toEqual({
      request: "parked-token",
      organizationId: 1,
      teamId: 6,
      access: "change",
      passwords: false,
      sqlWrites: true,
    });
  });

  it("lets a person in several organizations choose one, and each has its own choices", async () => {
    const calls = stubConsent(
      consent({
        organizations: [
          organization({ id: 1, name: "Acme", access: "change_delete" }),
          organization({
            id: 2,
            name: "Globex",
            access: "read",
            teams: [{ id: 9, name: "core" }],
            ownTeamId: 9,
          }),
        ],
      }),
    );

    renderWithProviders(<AuthorizeAgentPage />, { route });
    const choice = await screen.findByRole("combobox", { name: "Organization" });
    expect(screen.getAllByRole("radio")).toHaveLength(3);

    await userEvent.selectOptions(choice, "2");

    expect(screen.getAllByRole("radio")).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Allow access" }));
    await waitFor(() => expect(visit).toHaveBeenCalled());
    const approve = calls.find((call) => call.path === "/api/agent-consent/approve");
    expect(approve?.body).toMatchObject({ organizationId: 2, teamId: 9, access: "read" });
  });

  it("does not ask which organization when the person has one", async () => {
    stubConsent(consent());

    renderWithProviders(<AuthorizeAgentPage />, { route });

    await screen.findByRole("radio", { name: /Read only/ });
    expect(screen.queryByRole("combobox", { name: "Organization" })).not.toBeInTheDocument();
    expect(screen.getByText("Acme")).toBeInTheDocument();
  });

  it("cancels by telling the control plane and following its redirect", async () => {
    const calls = stubConsent(consent(), () =>
      jsonResponse({ redirectUrl: "https://claude.example/cb?error=access_denied" }),
    );

    renderWithProviders(<AuthorizeAgentPage />, { route });
    await userEvent.click(await screen.findByRole("button", { name: "Cancel" }));

    await waitFor(() =>
      expect(visit).toHaveBeenCalledWith("https://claude.example/cb?error=access_denied"),
    );
    expect(calls.map((call) => call.path)).toEqual([
      "/api/agent-consent/lookup",
      "/api/agent-consent/deny",
    ]);
    expect(calls[1].body).toEqual({ request: "parked-token" });
  });

  it("decides nothing until the person clicks", async () => {
    const calls = stubConsent(consent());

    renderWithProviders(<AuthorizeAgentPage />, { route });
    await screen.findByRole("button", { name: "Allow access" });

    expect(calls.map((call) => call.path)).toEqual(["/api/agent-consent/lookup"]);
    expect(visit).not.toHaveBeenCalled();
  });

  it("will not follow a redirect that is not http or https", async () => {
    stubConsent(consent(), () => jsonResponse({ redirectUrl: "javascript:alert(1)" }));

    renderWithProviders(<AuthorizeAgentPage />, { route });
    await userEvent.click(await screen.findByRole("button", { name: "Allow access" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/will not open/i);
    expect(visit).not.toHaveBeenCalled();
  });

  it("keeps the choices and shows the refusal when the control plane refuses them", async () => {
    stubConsent(consent(), () =>
      jsonResponse(
        {
          code: "ErrValidation",
          message:
            "Those permissions are more than this organization or this app allows. Choose less.",
        },
        422,
      ),
    );

    renderWithProviders(<AuthorizeAgentPage />, { route });
    await userEvent.click(await screen.findByRole("button", { name: "Allow access" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/Choose less/);
    expect(screen.getByRole("button", { name: "Allow access" })).toBeEnabled();
    expect(visit).not.toHaveBeenCalled();
  });

  it("says so when the person belongs to no organization that can sign the app in", async () => {
    stubConsent(consent({ organizations: [] }));

    renderWithProviders(<AuthorizeAgentPage />, { route });

    expect(await screen.findByRole("alert")).toHaveTextContent(/do not belong to an organization/);
  });

  it("says the request is gone when it expired or was answered", async () => {
    stubConsent(
      jsonResponse(
        {
          code: "ErrNotFound",
          message:
            "That sign in request has expired or was already answered. Start again from the app.",
        },
        404,
      ),
    );

    renderWithProviders(<AuthorizeAgentPage />, { route });

    expect(await screen.findByText(/no longer waiting/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Allow access" })).not.toBeInTheDocument();
  });

  it("has nothing to decide without a request token", async () => {
    const calls = stubConsent(consent());

    renderWithProviders(<AuthorizeAgentPage />, { route: "/authorize-agent" });

    expect(await screen.findByText(/no longer waiting/i)).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("does not render a decision inside a frame", async () => {
    const calls = stubConsent(consent());
    vi.stubGlobal("top", {});

    renderWithProviders(<AuthorizeAgentPage />, { route });

    expect(await screen.findByText(/must be opened in its own window/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Allow access" })).not.toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});
