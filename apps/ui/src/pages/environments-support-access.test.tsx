import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";

type Window = {
  state: "opening" | "open" | "closed";
  level: "readOnly" | "admin";
  expiresAt: string;
  caseReference?: string;
  closedReason?: string;
  extendingTo?: string;
  shareNote?: string;
};

type Api = {
  role?: "admin" | "user";
  window?: Window | null;
  summary?: { state: string; level: string; expiresAt: string };
  /** The status the support access read answers with, when it is not 200. */
  readStatus?: number;
};

const hoursFromNow = (hours: number) => new Date(Date.now() + hours * 3_600_000).toISOString();

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type Call = { method: string; path: string; body?: unknown };

/** Answers the routes the environments page reads and records every call it makes. */
function stubApi(api: Api) {
  const calls: Call[] = [];
  const state = { window: api.window ?? null };
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input).split("?")[0];
      const method = init?.method ?? "GET";
      const body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
      calls.push({ method, path, body });

      if (path === "/api/auth/session") {
        return Promise.resolve(
          jsonResponse({ username: "ada", role: api.role ?? "admin", team: "payments" }),
        );
      }
      if (path === "/api/connections") {
        return Promise.resolve(jsonResponse({ servicenow: { configured: false } }));
      }
      if (path.startsWith("/api/activity")) {
        return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
      }
      if (path === "/api/environments") {
        return Promise.resolve(
          jsonResponse({
            hub: true,
            environments: [
              {
                name: "spoke-a",
                status: "connected",
                lastSeen: new Date().toISOString(),
                ...(api.summary ? { supportAccess: api.summary } : {}),
              },
            ],
          }),
        );
      }
      if (path === "/api/environments/spoke-a/support-access") {
        if (method === "POST") {
          const request = body as { level: Window["level"]; hours: number; caseReference: string };
          state.window = {
            state: "opening",
            level: request.level,
            expiresAt: hoursFromNow(request.hours),
            ...(request.caseReference ? { caseReference: request.caseReference } : {}),
          };

          return Promise.resolve(jsonResponse({ window: state.window }, 201));
        }
        if (method === "PATCH") {
          const request = body as { hours: number };
          state.window = state.window && {
            ...state.window,
            extendingTo: hoursFromNow(request.hours),
          };

          return Promise.resolve(jsonResponse({ window: state.window }));
        }
        if (method === "DELETE") {
          state.window = state.window && {
            ...state.window,
            state: "closed",
            closedReason: "ended",
          };

          return Promise.resolve(new Response(null, { status: 204 }));
        }
        if (api.readStatus) {
          return Promise.resolve(
            jsonResponse({ code: "ErrNotFound", message: "That environment does not exist." }, 404),
          );
        }

        return Promise.resolve(jsonResponse({ window: state.window }));
      }

      return Promise.resolve(jsonResponse({}));
    }),
  );

  return { calls, state };
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

const optionStates = () =>
  within(screen.getByLabelText("Extend access"))
    .getAllByRole("option")
    .map((option) => [option.textContent, (option as HTMLOptionElement).disabled]);

async function openPanel(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole("link", { name: /spoke-a/ }));
  await user.click(await screen.findByRole("tab", { name: "Support access" }));
}

describe("support access on the environments page", () => {
  it("opens read only for 4 hours by default, with no case number needed", async () => {
    const { calls } = stubApi({});
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);

    expect(await screen.findByRole("radio", { name: /Read only/ })).toBeChecked();
    expect(screen.getByRole("radio", { name: /Admin/ })).not.toBeChecked();
    expect(screen.getByLabelText("Duration")).toHaveValue("4");
    expect(
      within(screen.getByLabelText("Duration"))
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["1 hour", "4 hours", "24 hours", "72 hours"]);

    await user.click(screen.getByRole("button", { name: "Open support access" }));

    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "POST",
        path: "/api/environments/spoke-a/support-access",
        body: { level: "readOnly", hours: 4, caseReference: "" },
      }),
    );
  });

  it("states in plain words what each level allows", async () => {
    stubApi({});
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);

    expect(await screen.findByText(/Cannot change anything/)).toBeInTheDocument();
    expect(screen.getByText(/Can create and change your databases/)).toBeInTheDocument();
  });

  it("tells the customer that admin cannot read secrets", async () => {
    stubApi({});
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);

    // No access level reads Secrets, so the Admin option must say so.
    expect(await screen.findByText(/Can create and change your databases/)).toHaveTextContent(
      /read secrets/,
    );
  });

  it("tells the customer that admin cannot delete a database or a backup", async () => {
    stubApi({});
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);

    expect(await screen.findByText(/Can create and change your databases/)).toHaveTextContent(
      /Cannot delete databases or backups/,
    );
  });

  it("sends the chosen level, duration and trimmed case number", async () => {
    const { calls } = stubApi({});
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);
    await user.click(await screen.findByRole("radio", { name: /Admin/ }));
    await user.selectOptions(screen.getByLabelText("Duration"), "24");
    await user.type(screen.getByLabelText("ServiceNow case number"), "  CS0012345 ");
    await user.click(screen.getByRole("button", { name: "Open support access" }));

    await waitFor(() =>
      expect(calls.find((call) => call.method === "POST")?.body).toEqual({
        level: "admin",
        hours: 24,
        caseReference: "CS0012345",
      }),
    );
  });

  it("says it is starting after opening and polls its own read until it is open", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { calls, state } = stubApi({});
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);
    await user.click(await screen.findByRole("button", { name: "Open support access" }));
    expect(await screen.findByText(/Support access is starting/)).toBeInTheDocument();
    const listReads = () => calls.filter((call) => call.path === "/api/environments").length;
    const before = listReads();

    state.window = state.window && { ...state.window, state: "open" };
    await vi.advanceTimersByTimeAsync(5_000);

    expect(await screen.findByText(/Support access is open/)).toBeInTheDocument();
    expect(screen.queryByText(/Support access is starting/)).not.toBeInTheDocument();
    // The list is not what polls while a window opens: it reads once more when
    // the window changes state, and not on the panel's timer.
    expect(listReads()).toBeLessThanOrEqual(before + 1);
  });

  it("shows the level, the expiry and a live countdown while a window is open", async () => {
    stubApi({
      window: { state: "open", level: "admin", expiresAt: hoursFromNow(3.2), caseReference: "CS1" },
      summary: { state: "open", level: "admin", expiresAt: hoursFromNow(3.2) },
    });
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    // The one-line status sits under the row without opening the panel.
    const status = await screen.findByRole("group", { name: "Support access status" });
    expect(status).toHaveTextContent(/Support access on, admin/);
    expect(status).toHaveTextContent(/3h 1\dm left/);

    await openPanel(user);

    expect(await screen.findByText("CS1")).toBeInTheDocument();
    expect(screen.getByText("Admin")).toBeInTheDocument();
    expect(screen.getAllByText(/3h 1\dm/).length).toBeGreaterThan(0);
  });

  it("says PDS records when access opens and ends, and that the audit log is the customer's", async () => {
    stubApi({ window: { state: "open", level: "readOnly", expiresAt: hoursFromNow(2) } });
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);

    expect(
      await screen.findByText(/PDS records when access opens, extends and ends/),
    ).toBeInTheDocument();
    expect(screen.getByText(/does not record what the engineer does/)).toBeInTheDocument();
    expect(
      screen.getByText(/turn on audit logging on your Kubernetes cluster/),
    ).toBeInTheDocument();
  });

  it("asks before ending access, with a destructive button, and ends it on confirm", async () => {
    const { calls } = stubApi({
      window: { state: "open", level: "readOnly", expiresAt: hoursFromNow(2) },
      summary: { state: "open", level: "readOnly", expiresAt: hoursFromNow(2) },
    });
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);
    await user.click(await screen.findByRole("button", { name: "End access" }));

    const dialog = await screen.findByRole("dialog", { name: "End support access?" });
    expect(calls.some((call) => call.method === "DELETE")).toBe(false);
    await user.click(within(dialog).getByRole("button", { name: "End access" }));

    await waitFor(() =>
      expect(calls).toContainEqual(
        expect.objectContaining({
          method: "DELETE",
          path: "/api/environments/spoke-a/support-access",
        }),
      ),
    );
    expect(await screen.findByText(/Access was ended/)).toBeInTheDocument();
  });

  it.each([
    ["ended", /Access was ended/],
    ["expired", /Access ended when the time ran out/],
    ["removed", /The environment was removed, so access ended/],
    [
      "failed",
      /Support access could not start on this environment\. Try again, or contact Percona support\./,
    ],
  ])("says why a window closed: %s", async (closedReason, message) => {
    stubApi({
      window: { state: "closed", level: "readOnly", expiresAt: hoursFromNow(-1), closedReason },
    });
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);

    expect(await screen.findByText(message)).toBeInTheDocument();
    // A closed window can be opened again.
    expect(screen.getByRole("button", { name: "Open support access" })).toBeInTheDocument();
  });

  it("reads an open window again once it has expired", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { state } = stubApi({
      window: {
        state: "open",
        level: "readOnly",
        expiresAt: new Date(Date.now() + 2_000).toISOString(),
      },
    });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);
    expect(await screen.findByText(/Support access is open/)).toBeInTheDocument();
    state.window = state.window && { ...state.window, state: "closed", closedReason: "expired" };
    await vi.advanceTimersByTimeAsync(4_000);

    expect(await screen.findByText(/Access ended when the time ran out/)).toBeInTheDocument();
  });

  it("shows a member who is not an admin whether access is open, with no actions", async () => {
    stubApi({
      role: "user",
      window: { state: "open", level: "readOnly", expiresAt: hoursFromNow(2) },
      summary: { state: "open", level: "readOnly", expiresAt: hoursFromNow(2) },
    });
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    expect(await screen.findByRole("group", { name: "Support access status" })).toBeInTheDocument();
    await openPanel(user);

    expect(await screen.findByText(/Support access is open/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "End access" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open support access" })).not.toBeInTheDocument();
  });

  it("offers a member who is not an admin no way to open it", async () => {
    stubApi({ role: "user" });
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);

    expect(await screen.findByText(/Support access is off/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open support access" })).not.toBeInTheDocument();
  });

  it("shows nothing for an environment of another Team", async () => {
    stubApi({ readStatus: 404 });
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);

    await waitFor(() =>
      expect(screen.queryByRole("heading", { name: "Support access" })).not.toBeInTheDocument(),
    );
    expect(screen.queryByRole("button", { name: "Open support access" })).not.toBeInTheDocument();
    expect(screen.queryByText(/could not be read/)).not.toBeInTheDocument();
  });

  it("hides the button and the status line when the surface is off", async () => {
    const { calls } = stubApi({
      summary: { state: "open", level: "admin", expiresAt: hoursFromNow(3) },
    });
    renderWithProviders(<AppRoutes />, {
      route: "/environments",
      flags: { "support-access": false },
    });

    await screen.findByText("spoke-a");

    expect(screen.queryByRole("tab", { name: "Support access" })).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Support access status" })).not.toBeInTheDocument();
    expect(calls.some((call) => call.path.endsWith("/support-access"))).toBe(false);
  });

  it("shows no status line when the environment has no active window", async () => {
    stubApi({});
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await screen.findByText("spoke-a");

    expect(screen.queryByRole("group", { name: "Support access status" })).not.toBeInTheDocument();
  });

  describe("extending an open window", () => {
    it("offers each duration as 'Until N hours from now' and disables those that would not end later", async () => {
      stubApi({ window: { state: "open", level: "readOnly", expiresAt: hoursFromNow(3.2) } });
      const user = userEvent.setup();
      renderWithProviders(<AppRoutes />, { route: "/environments" });

      await openPanel(user);
      await screen.findByLabelText("Extend access");

      expect(optionStates()).toEqual([
        ["Until 1 hour from now", true],
        ["Until 4 hours from now", false],
        ["Until 24 hours from now", false],
        ["Until 72 hours from now", false],
      ]);
      expect(screen.getByLabelText("Extend access")).toHaveValue("4");
    });

    it("sends only the hours, and shows the window still ends when it did until the environment confirms", async () => {
      const { calls } = stubApi({
        window: {
          state: "open",
          level: "admin",
          expiresAt: hoursFromNow(3.2),
          caseReference: "CS1",
        },
      });
      const user = userEvent.setup();
      renderWithProviders(<AppRoutes />, { route: "/environments" });

      await openPanel(user);
      await user.selectOptions(await screen.findByLabelText("Extend access"), "24");
      await user.click(screen.getByRole("button", { name: "Extend" }));

      await waitFor(() =>
        expect(calls).toContainEqual({
          method: "PATCH",
          path: "/api/environments/spoke-a/support-access",
          body: { hours: 24 },
        }),
      );
      expect(await screen.findByText(/Extending until/)).toBeInTheDocument();
      expect(screen.getAllByText(/3h 1\dm/).length).toBeGreaterThan(0);
      expect(screen.getByText("CS1")).toBeInTheDocument();
    });

    it("polls while an extension waits and shows the new time once it is confirmed", async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      const { state } = stubApi({
        window: { state: "open", level: "readOnly", expiresAt: hoursFromNow(3.2) },
      });
      const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
      renderWithProviders(<AppRoutes />, { route: "/environments" });

      await openPanel(user);
      await user.selectOptions(await screen.findByLabelText("Extend access"), "24");
      await user.click(screen.getByRole("button", { name: "Extend" }));
      expect(await screen.findByText(/Extending until/)).toBeInTheDocument();

      state.window = state.window && {
        state: "open",
        level: "readOnly",
        expiresAt: hoursFromNow(24),
      };
      await vi.advanceTimersByTimeAsync(5_000);

      await waitFor(() => expect(screen.queryByText(/Extending until/)).not.toBeInTheDocument());
      expect(screen.getAllByText(/23h \d\dm/).length).toBeGreaterThan(0);
    });

    it("shows the refusal the control plane gives", async () => {
      stubApi({ window: { state: "open", level: "readOnly", expiresAt: hoursFromNow(3.2) } });
      const user = userEvent.setup();
      renderWithProviders(<AppRoutes />, { route: "/environments" });
      await openPanel(user);
      const fetchMock = vi.mocked(fetch);
      const answer = fetchMock.getMockImplementation();
      fetchMock.mockImplementation((input, init) =>
        init?.method === "PATCH"
          ? Promise.resolve(
              jsonResponse(
                { code: "ErrValidation", message: "Choose a time up to 72 hours from now." },
                422,
              ),
            )
          : answer!(input, init),
      );

      await user.click(await screen.findByRole("button", { name: "Extend" }));

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "Choose a time up to 72 hours from now.",
      );
    });

    it("offers a member who is not an admin no way to extend, and no extension while opening", async () => {
      stubApi({
        role: "user",
        window: { state: "open", level: "readOnly", expiresAt: hoursFromNow(2) },
      });
      const user = userEvent.setup();
      renderWithProviders(<AppRoutes />, { route: "/environments" });

      await openPanel(user);

      expect(await screen.findByText(/Support access is open/)).toBeInTheDocument();
      expect(screen.queryByLabelText("Extend access")).not.toBeInTheDocument();
    });

    it("offers no extension while the window is still starting", async () => {
      stubApi({ window: { state: "opening", level: "readOnly", expiresAt: hoursFromNow(4) } });
      const user = userEvent.setup();
      renderWithProviders(<AppRoutes />, { route: "/environments" });

      await openPanel(user);

      expect(await screen.findByText(/Support access is starting/)).toBeInTheDocument();
      expect(screen.queryByLabelText("Extend access")).not.toBeInTheDocument();
    });
  });
});

describe("sharing the address with a support engineer", () => {
  const shareNote =
    "Support access is open on environment spoke-a.\nAccess level: Read only.\nPrivate address: pds-1.tail1234.ts.net.";

  it("shows the text the control plane gives and copies it as it is", async () => {
    stubApi({
      window: { state: "open", level: "readOnly", expiresAt: hoursFromNow(2), shareNote },
    });
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);

    expect(
      await screen.findByText(/Private address: pds-1\.tail1234\.ts\.net/),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Copy details" }));
    expect(await navigator.clipboard.readText()).toBe(shareNote);
  });

  it("shows nothing to share when the control plane gives no text", async () => {
    stubApi({ window: { state: "open", level: "readOnly", expiresAt: hoursFromNow(2) } });
    const user = userEvent.setup();
    renderWithProviders(<AppRoutes />, { route: "/environments" });

    await openPanel(user);

    expect(await screen.findByText(/Support access is open/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copy details" })).not.toBeInTheDocument();
  });
});
