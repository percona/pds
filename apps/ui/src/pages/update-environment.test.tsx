import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ALL_TEAMS } from "@/lib/acting-team";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type Mint = { name: string };

function stubHub(mints: Mint[] = [], environments?: unknown[], session?: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input).split("?")[0];
      if (path === "/api/auth/session" && session) {
        return Promise.resolve(jsonResponse(session));
      }
      if (path === "/api/teams") {
        return Promise.resolve(jsonResponse({ teams: [{ name: "alpha" }, { name: "beta" }] }));
      }
      if (path === "/api/environments") {
        return Promise.resolve(
          jsonResponse({
            hub: true,
            environments: environments ?? [
              { name: "spoke-a", status: "connected", lastSeen: new Date().toISOString() },
            ],
          }),
        );
      }
      if (path === "/api/connect") {
        return Promise.resolve(
          jsonResponse({
            hub: true,
            hubUrl: "https://hub.example",
            gitUrl: "https://git.example/hub/pds.git",
          }),
        );
      }
      if (path === "/api/connect/tokens" && init?.method === "POST") {
        mints.push(JSON.parse(String(init.body)) as Mint);

        return Promise.resolve(
          jsonResponse(
            {
              environment: "spoke-a",
              token: "jnt_abc",
              hubUrl: "https://hub.example",
              command:
                "curl -fsSL 'https://hub.example/api/internal/connect/bootstrap.sh' | bash -s -- --token 'jnt_abc'",
            },
            201,
          ),
        );
      }

      return Promise.resolve(jsonResponse({}));
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStorage.clear();
  window.localStorage.clear();
});

const person = (team: string, role: "admin" | "user") => ({
  username: "ada",
  user: "Ada",
  organization: "Acme",
  team,
  role,
  organizations: [{ id: 1, name: "Acme", role, current: true }],
});

describe("UpdateEnvironmentPage", () => {
  it("mints a token on request and prints the update command with the risk line", async () => {
    const mints: Mint[] = [];
    stubHub(mints);

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a/update" });

    expect(
      await screen.findByRole("heading", { level: 1, name: "Update spoke-a" }),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(
        "Updating in place is not a tested upgrade path. Back up your databases first.",
      ),
    ).toBeInTheDocument();
    // Nothing to tick: the page asks for no acceptance before the command.
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.queryByText(/--update/)).not.toBeInTheDocument();
    expect(mints).toHaveLength(0);

    await userEvent.click(await screen.findByRole("button", { name: "Create command" }));

    expect(
      await screen.findByText(
        "curl -fsSL 'https://hub.example/api/internal/connect/bootstrap.sh' | bash -s -- --update --token 'jnt_abc'",
      ),
    ).toBeInTheDocument();
    expect(mints).toEqual([{ name: "spoke-a" }]);
    expect(screen.getByText(/Treat it like a password/)).toBeInTheDocument();
  });

  it("says where to run the command and that nothing in AWS changes", async () => {
    stubHub();

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a/update" });

    expect(await screen.findByText(/where kubectl points at the Kubernetes cluster/)).toBeVisible();
    expect(screen.getByText(/changes nothing in AWS/)).toBeVisible();
  });

  it("refuses a name that is not an environment name", async () => {
    stubHub();

    renderWithProviders(<AppRoutes />, { route: "/environments/Not%20A%20Name/update" });

    await waitFor(() =>
      expect(screen.getByText(/is not an environment name, so no command is shown/)).toBeVisible(),
    );
    expect(screen.queryByRole("button", { name: "Create command" })).not.toBeInTheDocument();
  });

  it("mints nothing for an environment that is not on the list", async () => {
    const mints: Mint[] = [];
    stubHub(mints);

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-typo/update" });

    expect(
      await screen.findByText(/spoke-typo is not an environment on this control plane/),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: "Create command" })).not.toBeInTheDocument();
    expect(mints).toHaveLength(0);
  });

  it("mints nothing for an environment that has not connected yet", async () => {
    const mints: Mint[] = [];
    stubHub(mints, [{ name: "spoke-joining", status: "unreachable" }]);

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-joining/update" });

    expect(await screen.findByText(/spoke-joining has not connected yet/)).toBeVisible();
    expect(screen.queryByRole("button", { name: "Create command" })).not.toBeInTheDocument();
    expect(mints).toHaveLength(0);
  });

  it("shows the failure when the environment list cannot be read", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
        if (path === "/api/environments") {
          return Promise.resolve(
            jsonResponse({ code: "ErrDownstream", message: "Try again." }, 502),
          );
        }

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments/spoke-a/update" });

    expect(await screen.findByText(/The environment list could not be read/)).toBeVisible();
    expect(screen.queryByText("Reading environments...")).not.toBeInTheDocument();
  });

  it("clears a minted command when the route moves to another environment", async () => {
    stubHub(
      [],
      [
        { name: "spoke-a", status: "connected", lastSeen: new Date().toISOString() },
        { name: "spoke-b", status: "connected", lastSeen: new Date().toISOString() },
      ],
    );

    renderWithProviders(
      <>
        <AppRoutes />
        <Link to="/environments/spoke-b/update">Other environment</Link>
      </>,
      { route: "/environments/spoke-a/update" },
    );

    await userEvent.click(await screen.findByRole("button", { name: "Create command" }));
    expect(await screen.findByText(/--update --token 'jnt_abc'/)).toBeVisible();

    await userEvent.click(screen.getByRole("link", { name: "Other environment" }));

    expect(await screen.findByRole("heading", { level: 1, name: "Update spoke-b" })).toBeVisible();
    expect(screen.queryByText(/--update --token/)).not.toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Create command" })).toBeVisible();
  });

  describe("when two Teams each have an environment of the same name", () => {
    const both = [
      { name: "prod", team: "alpha", status: "connected", lastSeen: new Date().toISOString() },
      { name: "prod", team: "beta", status: "connected", lastSeen: new Date().toISOString() },
    ];

    it("refuses a link that names no Team", async () => {
      const mints: Mint[] = [];
      stubHub(mints, both);

      renderWithProviders(<AppRoutes />, { route: "/environments/prod/update" });

      expect(
        await screen.findByText(/More than one Team has an environment named prod/),
      ).toBeVisible();
      expect(screen.queryByRole("button", { name: "Create command" })).not.toBeInTheDocument();
      expect(mints).toHaveLength(0);
    });

    it("clears a minted command when the link moves to another Team", async () => {
      window.localStorage.setItem("pds-team:1", ALL_TEAMS);
      stubHub([], both, person("alpha", "admin"));

      renderWithProviders(
        <>
          <AppRoutes />
          <Link to="/environments/prod/update?team=beta">Other Team</Link>
        </>,
        { route: "/environments/prod/update?team=alpha" },
      );

      await userEvent.click(await screen.findByRole("button", { name: "Create command" }));
      expect(await screen.findByText(/--update --token 'jnt_abc'/)).toBeVisible();

      await userEvent.click(screen.getByRole("link", { name: "Other Team" }));

      expect(await screen.findByRole("button", { name: "Create command" })).toBeVisible();
      expect(screen.queryByText(/--update --token/)).not.toBeInTheDocument();
    });

    it("takes the Team from the link", async () => {
      window.localStorage.setItem("pds-team:1", ALL_TEAMS);
      stubHub([], both, person("alpha", "admin"));

      renderWithProviders(<AppRoutes />, { route: "/environments/prod/update?team=beta" });

      expect(await screen.findByRole("button", { name: "Create command" })).toBeVisible();
    });
  });

  describe("in the view of a single Team", () => {
    // The list carries no Team on a row here, so the link's Team is checked
    // against the Team the console acts in.
    const unannotated = [{ name: "prod", status: "connected", lastSeen: new Date().toISOString() }];

    it("accepts a link that names the Team the console acts in", async () => {
      stubHub([], unannotated, person("beta", "user"));

      renderWithProviders(<AppRoutes />, { route: "/environments/prod/update?team=beta" });

      expect(await screen.findByRole("button", { name: "Create command" })).toBeVisible();
    });

    it("refuses a link that names another Team", async () => {
      const mints: Mint[] = [];
      stubHub(mints, unannotated, person("beta", "user"));

      renderWithProviders(<AppRoutes />, { route: "/environments/prod/update?team=alpha" });

      expect(
        await screen.findByText(/prod is not an environment on this control plane/),
      ).toBeVisible();
      expect(mints).toHaveLength(0);
    });
  });
});
