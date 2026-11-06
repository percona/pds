import { useQuery } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useSession, type Session } from "@/features/auth";
import { requestJson } from "@/lib/api";
import { renderWithProviders } from "@/test/utils";
import { ActingTeamProvider, useActingTeam } from "../acting-team";
import { useTeamToCreateIn } from "../team-field";
import { TeamSelector } from "./team-selector";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const acme = { id: 1, name: "Acme", role: "admin", current: true } as const;

function sessionOf(overrides: Partial<Session>): Session {
  return {
    username: "acme",
    user: "Ada",
    organization: "Acme",
    team: "default",
    role: "admin",
    organizations: [acme],
    ...overrides,
  };
}

const teamNames = ["default", "Payments", "Search"];

function stubAdapter(session: Session, teams = teamNames) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const path = String(input);
    if (path === "/api/auth/session") {
      return Promise.resolve(jsonResponse(session));
    }
    if (path === "/api/teams") {
      return Promise.resolve(jsonResponse({ teams: teams.map((name) => ({ name })) }));
    }
    if (path === "/api/users") {
      return Promise.resolve(jsonResponse({ users: [] }));
    }

    return Promise.resolve(jsonResponse({ environments: [] }));
  });
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

/** The paths the console asked a Team's route for, in order. */
function environmentReads(fetchMock: ReturnType<typeof stubAdapter>) {
  return fetchMock.mock.calls
    .map(([input]) => String(input))
    .filter((path) => path.startsWith("/api/environments"));
}

/** Stands in for a screen: it reads the environments the way a page does. */
function Screen() {
  const query = useQuery({
    queryKey: ["environments", "list"],
    queryFn: () => requestJson("/api/environments"),
  });

  return <p>{query.isPending ? "Loading screen" : "Screen loaded"}</p>;
}

/** Opens once the session is known, the way the gate around the console does. */
function SignedIn({ children }: { children: ReactNode }) {
  return useSession().data == null ? null : children;
}

function renderConsole() {
  return renderWithProviders(
    <SignedIn>
      <ActingTeamProvider>
        <TeamSelector />
        <Screen />
      </ActingTeamProvider>
    </SignedIn>,
  );
}

beforeEach(() => {
  window.localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  window.localStorage.clear();
});

describe("TeamSelector for a User", () => {
  it("shows the Team as a label and offers no choice", async () => {
    const fetchMock = stubAdapter(sessionOf({ role: "user", team: "Payments" }));

    renderConsole();

    expect(await screen.findByText("Payments")).toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
    expect(await screen.findByText("Screen loaded")).toBeInTheDocument();
    const paths = fetchMock.mock.calls.map(([input]) => String(input));
    expect(paths).not.toContain("/api/teams");
    expect(environmentReads(fetchMock)).toEqual(["/api/environments"]);
  });

  it("ignores a Team remembered in the browser", async () => {
    window.localStorage.setItem("pds-team:1", "Search");
    const fetchMock = stubAdapter(sessionOf({ role: "user", team: "Payments" }));

    renderConsole();

    await screen.findByText("Screen loaded");
    expect(environmentReads(fetchMock)).toEqual(["/api/environments"]);
  });
});

describe("TeamSelector for an admin", () => {
  it("lists every Team of the Organization, with their own chosen", async () => {
    stubAdapter(sessionOf({}));

    renderConsole();

    const select = await screen.findByRole("combobox", { name: "Working in team" });
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(4));
    expect(screen.getByRole("option", { name: "All teams" })).toBeInTheDocument();
    expect(select).toHaveValue("default");
  });

  it("acts in their own Team until they choose another", async () => {
    const fetchMock = stubAdapter(sessionOf({}));

    renderConsole();

    await screen.findByText("Screen loaded");
    expect(environmentReads(fetchMock)).toEqual(["/api/environments"]);
  });

  it("reads the other Team's data after they choose it, and remembers the choice", async () => {
    const fetchMock = stubAdapter(sessionOf({}));
    renderConsole();
    await screen.findByText("Screen loaded");
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(4));

    await userEvent.selectOptions(
      screen.getByRole("combobox", { name: "Working in team" }),
      "Payments",
    );

    await waitFor(() =>
      expect(environmentReads(fetchMock)).toEqual([
        "/api/environments",
        "/api/environments?team=Payments",
      ]),
    );
    expect(screen.getByRole("combobox", { name: "Working in team" })).toHaveValue("Payments");
    expect(window.localStorage.getItem("pds-team:1")).toBe("Payments");
  });

  it("applies the remembered Team before the first read", async () => {
    window.localStorage.setItem("pds-team:1", "Search");
    const fetchMock = stubAdapter(sessionOf({}));

    renderConsole();

    expect(await screen.findByText("Screen loaded")).toBeInTheDocument();
    expect(environmentReads(fetchMock)).toEqual(["/api/environments?team=Search"]);
    expect(screen.getByRole("combobox", { name: "Working in team" })).toHaveValue("Search");
  });

  it("falls back to their own Team when the remembered one no longer exists", async () => {
    window.localStorage.setItem("pds-team:1", "Gone");
    const fetchMock = stubAdapter(sessionOf({}));

    renderConsole();

    await screen.findByText("Screen loaded");
    expect(environmentReads(fetchMock)).toEqual(["/api/environments"]);
    expect(screen.getByRole("combobox", { name: "Working in team" })).toHaveValue("default");
  });

  it("forgets a remembered Team that no longer exists", async () => {
    window.localStorage.setItem("pds-team:1", "Gone");
    stubAdapter(sessionOf({}));

    renderConsole();

    await screen.findByText("Screen loaded");
    await waitFor(() => expect(window.localStorage.getItem("pds-team:1")).toBeNull());
  });

  it("remembers the choice for each Organization on its own", async () => {
    window.localStorage.setItem("pds-team:1", "Search");
    const fetchMock = stubAdapter(
      sessionOf({
        organization: "Initech",
        organizations: [
          { ...acme, current: false },
          { id: 2, name: "Initech", role: "admin", current: true },
        ],
      }),
    );

    renderConsole();

    await screen.findByText("Screen loaded");
    expect(environmentReads(fetchMock)).toEqual(["/api/environments"]);
  });

  it("reads every Team's lists after they choose All teams, and remembers the choice", async () => {
    const fetchMock = stubAdapter(sessionOf({}));
    renderConsole();
    await screen.findByText("Screen loaded");
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(4));

    await userEvent.selectOptions(
      screen.getByRole("combobox", { name: "Working in team" }),
      "All teams",
    );

    await waitFor(() =>
      expect(environmentReads(fetchMock)).toEqual([
        "/api/environments",
        "/api/environments?team=all",
      ]),
    );
    expect(screen.getByRole("combobox", { name: "Working in team" })).toHaveValue("all");
    expect(window.localStorage.getItem("pds-team:1")).toBe("all");
  });

  it("applies a remembered All teams before the first read", async () => {
    window.localStorage.setItem("pds-team:1", "all");
    const fetchMock = stubAdapter(sessionOf({}));

    renderConsole();

    expect(await screen.findByText("Screen loaded")).toBeInTheDocument();
    expect(environmentReads(fetchMock)).toEqual(["/api/environments?team=all"]);
    expect(screen.getByRole("combobox", { name: "Working in team" })).toHaveValue("all");
  });

  it("goes back to one Team after All teams, and the lists name it again", async () => {
    window.localStorage.setItem("pds-team:1", "all");
    const fetchMock = stubAdapter(sessionOf({}));
    renderConsole();
    await screen.findByText("Screen loaded");
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(4));

    await userEvent.selectOptions(
      screen.getByRole("combobox", { name: "Working in team" }),
      "Payments",
    );

    await waitFor(() =>
      expect(environmentReads(fetchMock)).toEqual([
        "/api/environments?team=all",
        "/api/environments?team=Payments",
      ]),
    );
  });
});

describe("useActingTeam", () => {
  function Probe() {
    const { team, allTeams } = useActingTeam();

    return (
      <p>
        {team} {allTeams ? "across all" : "in one"}
      </p>
    );
  }

  it("keeps the admin's own Team as the Team to create in while All teams is chosen", async () => {
    window.localStorage.setItem("pds-team:1", "all");
    stubAdapter(sessionOf({}));

    renderWithProviders(
      <SignedIn>
        <ActingTeamProvider>
          <Probe />
        </ActingTeamProvider>
      </SignedIn>,
    );

    expect(await screen.findByText("default across all")).toBeInTheDocument();
  });

  it("says one Team for a User, whatever was remembered", async () => {
    window.localStorage.setItem("pds-team:1", "all");
    stubAdapter(sessionOf({ role: "user", team: "Payments" }));

    renderWithProviders(
      <SignedIn>
        <ActingTeamProvider>
          <Probe />
        </ActingTeamProvider>
      </SignedIn>,
    );

    expect(await screen.findByText("Payments in one")).toBeInTheDocument();
  });
});

describe("useTeamToCreateIn", () => {
  function Flow({ fixed }: { fixed?: string }) {
    const { team, field } = useTeamToCreateIn(fixed);

    return (
      <div>
        <p>Creating in {team ?? "the acting Team"}</p>
        {field}
      </div>
    );
  }

  function renderFlow(fixed?: string) {
    return renderWithProviders(
      <SignedIn>
        <ActingTeamProvider>
          <Flow fixed={fixed} />
        </ActingTeamProvider>
      </SignedIn>,
    );
  }

  it("asks nothing while the console acts in one Team", async () => {
    stubAdapter(sessionOf({}));

    renderFlow();

    expect(await screen.findByText("Creating in the acting Team")).toBeInTheDocument();
    expect(screen.queryByLabelText("Team")).not.toBeInTheDocument();
  });

  it("asks for a Team while every Team is shown, starting from the admin's own", async () => {
    window.localStorage.setItem("pds-team:1", "all");
    stubAdapter(sessionOf({}));

    renderFlow();

    const field = await screen.findByLabelText("Team");
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    expect(field).toHaveValue("default");
    expect(screen.getByText("Creating in default")).toBeInTheDocument();
  });

  it("uses the Team the flow is fixed to and asks nothing", async () => {
    window.localStorage.setItem("pds-team:1", "all");
    stubAdapter(sessionOf({}));

    renderFlow("Search");

    expect(await screen.findByText("Creating in Search")).toBeInTheDocument();
    expect(screen.queryByLabelText("Team")).not.toBeInTheDocument();
  });

  it("creates in the Team the admin picks", async () => {
    window.localStorage.setItem("pds-team:1", "all");
    stubAdapter(sessionOf({}));
    renderFlow();
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));

    await userEvent.selectOptions(screen.getByLabelText("Team"), "Payments");

    expect(screen.getByText("Creating in Payments")).toBeInTheDocument();
  });
});
