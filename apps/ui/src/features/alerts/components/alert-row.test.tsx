import { screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { EnvironmentProvider } from "@/features/environments";
import { ActingTeamProvider } from "@/features/teams";
import { ALL_TEAMS } from "@/lib/acting-team";
import { createMockAlert, renderWithProviders } from "@/test/utils";
import { AlertRow } from "./alert-row";

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function person(team: string, role: "admin" | "user") {
  return {
    username: "ada",
    user: "Ada",
    organization: "Acme",
    team,
    role,
    organizations: [{ id: 1, name: "Acme", role, current: true }],
  };
}

function stub(session: unknown, environments: unknown[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const path = String(input).split("?")[0];
      if (path === "/api/auth/session") {
        return Promise.resolve(jsonResponse(session));
      }
      if (path === "/api/teams") {
        return Promise.resolve(jsonResponse({ teams: [{ name: "alpha" }, { name: "beta" }] }));
      }
      if (path === "/api/environments") {
        return Promise.resolve(jsonResponse({ hub: true, environments }));
      }

      return Promise.resolve(jsonResponse({}));
    }),
  );
}

function renderRow() {
  renderWithProviders(
    <ActingTeamProvider>
      <EnvironmentProvider>
        <ul>
          <AlertRow
            alert={createMockAlert({
              environment: "prod",
              action: { kind: "update_connection", label: "Update connection" },
            })}
          />
        </ul>
      </EnvironmentProvider>
    </ActingTeamProvider>,
  );
}

const connected = (team?: string) => ({
  name: "prod",
  team,
  status: "connected",
  lastSeen: new Date().toISOString(),
});

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

describe("AlertRow Update connection", () => {
  it("opens the update page of the Team the console acts in", async () => {
    stub(person("beta", "user"), [connected()]);

    renderRow();

    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Update connection" })).toHaveAttribute(
        "href",
        "/environments/prod/update?team=beta",
      ),
    );
  });

  it("opens the update page of the Team that owns the environment while every Team is shown", async () => {
    window.localStorage.setItem("pds-team:1", ALL_TEAMS);
    stub(person("alpha", "admin"), [connected("beta")]);

    renderRow();

    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Update connection" })).toHaveAttribute(
        "href",
        "/environments/prod/update?team=beta",
      ),
    );
  });

  it("keeps the acting Team when two Teams each own an environment of that name", async () => {
    window.localStorage.setItem("pds-team:1", ALL_TEAMS);
    stub(person("alpha", "admin"), [connected("alpha"), connected("beta")]);

    renderRow();

    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Update connection" })).toHaveAttribute(
        "href",
        "/environments/prod/update?team=alpha",
      ),
    );
  });
});
