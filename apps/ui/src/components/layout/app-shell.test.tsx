import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AppShell } from "@/components/layout/app-shell";
import { createMockAlert, createMockAlertReport, renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** The sidebar asks the adapter whether support and activity are connected on every render. */
function stubSupport(response: Response) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: string) => {
      const path = String(input);
      if (path === "/api/connections") {
        return Promise.resolve(
          jsonResponse({
            servicenow: { configured: false },
            assistant: { configured: false },
            cursor: { configured: false },
          }),
        );
      }
      if (path === "/api/environments") {
        return Promise.resolve(jsonResponse({ environments: [] }));
      }
      if (path.startsWith("/api/activity")) {
        return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
      }
      // The sidebar names who is signed in, so the shell needs a session the
      // same way it needs the environment list.
      if (path === "/api/auth/session") {
        return Promise.resolve(jsonResponse({ username: "acme" }));
      }

      return Promise.resolve(response);
    }),
  );
}

function renderShell() {
  return renderWithProviders(
    <AppShell>
      <p>page body</p>
    </AppShell>,
  );
}

beforeEach(() => {
  stubSupport(jsonResponse({ tickets: [] }));
});

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

describe("AppShell", () => {
  it("renders the product name and primary navigation", async () => {
    renderShell();

    expect(screen.getByRole("link", { name: "PDS" })).toHaveAttribute("href", "/");
    expect(screen.queryByText("MySQL on this Kubernetes cluster")).not.toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Main" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Environments" })).toHaveAttribute(
      "href",
      "/environments",
    );
    expect(screen.queryByRole("link", { name: "Databases" })).not.toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Activity" })).toHaveAttribute(
      "href",
      "/activity",
    );
    expect(screen.queryByRole("link", { name: "Chats" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Chat$/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Ask PDS" })).toBeInTheDocument();
  });

  it("keeps Environments in the sidebar while environment discovery is failing", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) => {
        const path = String(input);
        if (path === "/api/connections") {
          return Promise.resolve(
            jsonResponse({
              servicenow: { configured: false },
              assistant: { configured: false },
              cursor: { configured: false },
            }),
          );
        }
        if (path === "/api/environments") {
          return Promise.resolve(
            jsonResponse(
              { code: "ErrDownstream", message: "the git backend is not answering" },
              502,
            ),
          );
        }
        if (path.startsWith("/api/activity")) {
          return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
        }

        return Promise.resolve(jsonResponse({ tickets: [] }));
      }),
    );

    renderShell();

    await waitFor(() => {
      expect(screen.getByRole("link", { name: "Environments" })).toBeInTheDocument();
    });
    expect(screen.queryByRole("link", { name: "Databases" })).not.toBeInTheDocument();
    expect(screen.queryByText("MySQL on this Kubernetes cluster")).not.toBeInTheDocument();
  });

  it("adds support to the navigation once the adapter reports it is connected", async () => {
    renderShell();

    expect(await screen.findByRole("link", { name: "Support" })).toHaveAttribute(
      "href",
      "/support",
    );
  });

  // No ServiceNow Secret means no tickets to list, so the entry is left out
  // rather than leading to a page that only says so.
  it("leaves support out of the navigation when the adapter answers 501", async () => {
    stubSupport(
      jsonResponse(
        { code: "ErrNotImplemented", message: "Support tickets are not configured." },
        501,
      ),
    );
    renderShell();

    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Environments" })).toBeInTheDocument(),
    );
    expect(screen.queryByRole("button", { name: /^Chat$/ })).not.toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByRole("link", { name: "Support" })).not.toBeInTheDocument(),
    );
  });

  it("orders navigation as environments, alerts, AI agents, activity, then support", async () => {
    renderShell();

    const nav = await screen.findByRole("navigation", { name: "Main" });
    await within(nav).findByRole("link", { name: "Support" });
    const items = within(nav).getAllByRole("link");
    expect(items.map((link) => link.textContent?.trim())).toEqual([
      "Environments",
      "Alerts",
      "AI agents",
      "Activity",
      "Support",
    ]);
    expect(within(nav).queryAllByRole("button")).toEqual([]);
  });

  it("renders a sidebar that is w-64 wide", () => {
    renderShell();

    expect(screen.getByTestId("sidebar").className).toContain("w-64");
  });

  it("constrains page content to max-w-6xl with px-6", () => {
    renderShell();

    const content = screen.getByTestId("app-content");
    expect(content.className).toContain("max-w-6xl");
    expect(content.className).toContain("mx-auto");
    expect(content.className).toContain("px-6");
    expect(content).toHaveTextContent("page body");
  });

  it("renders children inside the main landmark", () => {
    renderShell();

    expect(screen.getByRole("main")).toHaveTextContent("page body");
  });

  it("collapses the sidebar to icons and remembers the choice", async () => {
    renderShell();

    await userEvent.click(await screen.findByRole("button", { name: "Collapse sidebar" }));

    const sidebar = screen.getByTestId("sidebar");
    expect(sidebar.className).toContain("w-16");
    expect(sidebar.className).not.toContain("w-64");
    expect(screen.getByRole("main").className).toContain("pl-16");
    expect(window.localStorage.getItem("pds-sidebar")).toBe("collapsed");

    const environments = screen.getByRole("link", { name: "Environments" });
    expect(within(environments).getByRole("tooltip", { hidden: true })).toHaveTextContent(
      "Environments",
    );

    await userEvent.click(screen.getByRole("button", { name: "Expand sidebar" }));

    expect(screen.getByTestId("sidebar").className).toContain("w-64");
    expect(screen.getByRole("main").className).toContain("pl-64");
    expect(window.localStorage.getItem("pds-sidebar")).toBe("expanded");
  });

  it("starts collapsed when that choice was remembered", () => {
    window.localStorage.setItem("pds-sidebar", "collapsed");

    renderShell();

    expect(screen.getByTestId("sidebar").className).toContain("w-16");
    expect(screen.getByRole("main").className).toContain("pl-16");
  });

  it("keeps the critical count visible when the sidebar is collapsed", async () => {
    const report = createMockAlertReport({
      alerts: [createMockAlert({ id: "a" }), createMockAlert({ id: "b" })],
    });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) =>
        Promise.resolve(
          String(input) === "/api/alerts"
            ? jsonResponse(report)
            : jsonResponse({ environments: [] }),
        ),
      ),
    );

    renderShell();

    expect(await screen.findByText("2 critical alerts firing")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Collapse sidebar" }));
    expect(screen.getByText("2 critical alerts firing")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
  });

  it("opens the sidebar as a drawer on a phone", async () => {
    vi.stubGlobal(
      "matchMedia",
      (query: string) =>
        ({
          matches: query.includes("max-width"),
          media: query,
          addEventListener: () => {},
          removeEventListener: () => {},
          addListener: () => {},
          removeListener: () => {},
          dispatchEvent: () => false,
          onchange: null,
        }) as MediaQueryList,
    );

    renderShell();

    expect(screen.queryByTestId("sidebar")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Collapse sidebar" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Open menu" }));

    const sidebar = screen.getByTestId("sidebar");
    expect(sidebar.className).toContain("w-64");
    expect(screen.getByRole("button", { name: "Close menu" })).toHaveFocus();
    expect(screen.getByRole("link", { name: "Environments" })).toBeVisible();

    await userEvent.click(screen.getByTestId("sidebar-scrim"));

    expect(screen.queryByTestId("sidebar")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open menu" })).toHaveFocus();
  });

  it("opens chat over the current page instead of leaving it", async () => {
    renderShell();

    await userEvent.click(screen.getByRole("button", { name: "Ask PDS" }));

    expect(screen.getByRole("region", { name: "Chat" })).toBeInTheDocument();
    expect(screen.getByRole("main")).toHaveTextContent("page body");
  });

  it("links Alerts and counts only firing criticals", async () => {
    const report = createMockAlertReport({
      alerts: [
        createMockAlert({ id: "a" }),
        createMockAlert({ id: "b" }),
        createMockAlert({ id: "c", severity: "warning" }),
      ],
    });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) =>
        Promise.resolve(
          String(input) === "/api/alerts"
            ? jsonResponse(report)
            : jsonResponse({ environments: [] }),
        ),
      ),
    );

    renderShell();

    const link = await screen.findByRole("link", { name: /^Alerts/ });
    expect(link).toHaveAttribute("href", "/alerts");
    expect(await within(link).findByText("2 critical alerts firing")).toBeInTheDocument();
  });

  it("shows no count while alerts have not been read", async () => {
    renderShell();

    const link = await screen.findByRole("link", { name: /^Alerts/ });
    expect(within(link).queryByText(/critical alert/)).not.toBeInTheDocument();
  });

  it("shows no count when no critical alert is firing", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) =>
        Promise.resolve(
          String(input) === "/api/alerts"
            ? jsonResponse(
                createMockAlertReport({ alerts: [createMockAlert({ severity: "warning" })] }),
              )
            : jsonResponse({ environments: [] }),
        ),
      ),
    );

    renderShell();

    const link = await screen.findByRole("link", { name: /^Alerts/ });
    await waitFor(() => expect(link).toBeInTheDocument());
    expect(within(link).queryByText(/critical alert/)).not.toBeInTheDocument();
  });
});
