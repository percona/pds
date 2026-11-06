import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { OrganizationSwitcher } from "@/features/auth";
import type { Session } from "@/features/auth";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const acme = { id: 1, name: "Acme", role: "admin", current: true } as const;
const initech = { id: 2, name: "Initech", role: "user", current: false } as const;

function sessionOf(overrides: Partial<Session>): Session {
  return { username: "acme", user: "Ada", organization: "Acme", team: "default", ...overrides };
}

/** Answers the session with `session`, and any write with `write`. */
function stubAdapter(
  session: Session,
  write: () => Response = () => new Response(null, { status: 204 }),
) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === "/api/auth/session" && init?.method === "GET") {
      return Promise.resolve(jsonResponse(session));
    }

    return Promise.resolve(write());
  });
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

function writes(fetchMock: ReturnType<typeof stubAdapter>) {
  return fetchMock.mock.calls
    .filter(([, init]) => init?.method === "POST")
    .map(([input, init]) => ({
      path: String(input),
      body: JSON.parse(String(init?.body)) as unknown,
    }));
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("OrganizationSwitcher visibility", () => {
  it("is a switcher for an admin, even of one organization", async () => {
    stubAdapter(sessionOf({ role: "admin", organizations: [acme] }));

    renderWithProviders(<OrganizationSwitcher />);

    expect(await screen.findByRole("button", { name: /Acme/ })).toBeInTheDocument();
  });

  it("is a switcher for a person in several organizations", async () => {
    stubAdapter(sessionOf({ role: "user", organizations: [{ ...acme, role: "user" }, initech] }));

    renderWithProviders(<OrganizationSwitcher />);

    expect(await screen.findByRole("button", { name: /Acme/ })).toBeInTheDocument();
  });

  it("is a static label for a user of one organization", async () => {
    stubAdapter(sessionOf({ role: "user", organizations: [{ ...acme, role: "user" }] }));

    renderWithProviders(<OrganizationSwitcher />);

    expect(await screen.findByText("Acme")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("is a static label for a session with no organizations to switch between", async () => {
    stubAdapter(sessionOf({ role: "admin" }));

    renderWithProviders(<OrganizationSwitcher />);

    expect(await screen.findByText("Acme")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("shows nothing for a session that has no organization at all", async () => {
    stubAdapter({ username: "acme" });

    const { container } = renderWithProviders(<OrganizationSwitcher />);

    await waitFor(() => expect(container).toBeEmptyDOMElement());
  });
});

describe("OrganizationSwitcher menu", () => {
  const session = sessionOf({ role: "admin", organizations: [acme, initech] });

  it("lists the organizations and marks the current one", async () => {
    stubAdapter(session);
    renderWithProviders(<OrganizationSwitcher />);

    await userEvent.click(await screen.findByRole("button", { name: /Acme/ }));

    const dialog = await screen.findByRole("dialog", { name: "Switch organization" });
    expect(dialog).toBeVisible();
    expect(screen.getByRole("button", { name: /Initech/ })).not.toHaveAttribute("aria-current");
    expect(screen.getAllByRole("button", { name: /Acme/ }).at(-1)).toHaveAttribute(
      "aria-current",
      "true",
    );
  });

  it("moves the session into the organization that was picked", async () => {
    const fetchMock = stubAdapter(session);
    renderWithProviders(<OrganizationSwitcher />);

    await userEvent.click(await screen.findByRole("button", { name: /Acme/ }));
    await userEvent.click(await screen.findByRole("button", { name: /Initech/ }));

    await waitFor(() =>
      expect(writes(fetchMock)).toEqual([
        { path: "/api/auth/organization", body: { organizationId: 2 } },
      ]),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("says why a switch was refused and stays open", async () => {
    stubAdapter(session, () =>
      jsonResponse({ code: "ErrNotFound", message: "That organization does not exist." }, 404),
    );
    renderWithProviders(<OrganizationSwitcher />);

    await userEvent.click(await screen.findByRole("button", { name: /Acme/ }));
    await userEvent.click(await screen.findByRole("button", { name: /Initech/ }));

    expect(await screen.findByRole("alert")).toHaveTextContent("That organization does not exist.");
    expect(screen.getByRole("dialog")).toBeVisible();
  });

  it("creates another organization from its name and moves into it", async () => {
    const fetchMock = stubAdapter(session);
    renderWithProviders(<OrganizationSwitcher />);

    await userEvent.click(await screen.findByRole("button", { name: /Acme/ }));
    await userEvent.click(await screen.findByRole("button", { name: "Create organization" }));
    const create = screen.getByRole("button", { name: "Create organization" });
    expect(create).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Organization name"), "Hooli");
    await userEvent.click(create);

    await waitFor(() =>
      expect(writes(fetchMock)).toEqual([
        { path: "/api/auth/organizations", body: { name: "Hooli" } },
      ]),
    );
  });

  it("shows the limit the control plane names when it refuses a new organization", async () => {
    stubAdapter(session, () =>
      jsonResponse(
        {
          code: "ErrConflict",
          message:
            "You have created as many organizations as one person can. Contact Percona support.",
        },
        409,
      ),
    );
    renderWithProviders(<OrganizationSwitcher />);

    await userEvent.click(await screen.findByRole("button", { name: /Acme/ }));
    await userEvent.click(await screen.findByRole("button", { name: "Create organization" }));
    await userEvent.type(screen.getByLabelText("Organization name"), "Hooli");
    await userEvent.click(screen.getByRole("button", { name: "Create organization" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "as many organizations as one person can",
    );
  });
});
