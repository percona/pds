import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

describe("ConnectEnvironmentPage", () => {
  it("stays on the connect page when the environment read fails", async () => {
    const errors: unknown[] = [];
    const consoleError = vi.spyOn(console, "error").mockImplementation((...args) => {
      errors.push(args[0]);
    });

    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
        if (path === "/api/environments") {
          return Promise.resolve(
            jsonResponse({ code: "ErrNotImplemented", message: "Not a hub." }, 501),
          );
        }
        if (path === "/api/connect") {
          return Promise.resolve(
            jsonResponse({
              hubUrl: "https://hub.example",
              gitUrl: "https://git.example/hub/pds.git",
              clusterRegion: "eu-west-1",
            }),
          );
        }

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });

    expect(
      await screen.findByRole("heading", { level: 1, name: "Create an environment" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("heading", { level: 1, name: "Databases" })).not.toBeInTheDocument();

    expect(
      errors.filter((error) => String(error).includes("fewer hooks than expected")),
    ).toHaveLength(0);

    consoleError.mockRestore();
  });

  it("prefills the name from the link and links a connected environment to its Update", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
        if (path === "/api/environments") {
          return Promise.resolve(
            jsonResponse({
              hub: true,
              hubUrl: "https://hub.example",
              environments: [
                { name: "test", status: "connected", lastSeen: new Date().toISOString() },
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

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments/new?name=test" });

    expect(await screen.findByLabelText("Environment name")).toHaveValue("test");
    expect(await screen.findByText(/test is already connected/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "update it in place" })).toHaveAttribute(
      "href",
      "/environments/test/update",
    );
    // The full join command stays the repair path.
    expect(screen.getByRole("button", { name: "Create command" })).toBeEnabled();
  });

  it("mints a token and prints one command that creates, installs, and joins", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const path = String(input).split("?")[0];
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
            jsonResponse({ hub: true, hubUrl: "https://hub.example", environments: [] }),
          );
        }
        if (path === "/api/connect") {
          return Promise.resolve(
            jsonResponse({
              hub: true,
              hubUrl: "https://hub.example",
              gitUrl: "https://git.example/hub/pds.git",
              namespace: "pds",
            }),
          );
        }
        if (path === "/api/connect/tokens" && (init?.method ?? "GET") === "POST") {
          return Promise.resolve(
            jsonResponse(
              {
                environment: "eks-testing",
                token: "jnt_abc",
                command:
                  "curl -fsSL https://hub.example/api/internal/connect/bootstrap.sh | bash -s -- --token jnt_abc",
                hubUrl: "https://hub.example",
                region: "us-west-2",
              },
              201,
            ),
          );
        }
        if (path.startsWith("/api/activity")) {
          return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
        }

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });

    expect(
      await screen.findByRole("heading", { level: 1, name: "Create an environment" }),
    ).toBeInTheDocument();
    expect(screen.getByText("1. Name this environment")).toBeInTheDocument();
    expect(screen.getByText("2. Run this command")).toBeInTheDocument();
    expect(screen.getByText("3. Wait for it to appear")).toBeInTheDocument();
    expect(screen.queryByText("Create the environment")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Hub URL")).not.toBeInTheDocument();

    await userEvent.type(screen.getByLabelText("Environment name"), "eks-testing");
    // The hub mints its own operator token and the UI reads it back, so there
    // is nothing here to paste.
    expect(screen.queryByLabelText("Hub operator token")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Create command" }));

    expect(
      await screen.findByText(
        "curl -fsSL https://hub.example/api/internal/connect/bootstrap.sh | bash -s -- --token jnt_abc",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/create-environment\.sh/)).not.toBeInTheDocument();
    expect(screen.getByText(/already runs Percona Database Service/)).toBeInTheDocument();
    expect(screen.getByText(/asks which AWS region/)).toBeInTheDocument();
    expect(screen.getByText("us-west-2")).toBeInTheDocument();
    expect(screen.getByText(/join secret/)).toBeInTheDocument();
    expect(screen.queryByText(/valid for a day/)).not.toBeInTheDocument();

    const minted = vi.mocked(fetch).mock.calls.find(([input, init]) => {
      return String(input).split("?")[0] === "/api/connect/tokens" && init?.method === "POST";
    });
    // No credential is typed here. A hub that refuses the mint answers 401,
    // and the request helper fetches the token and repeats the call.
    expect(minted?.[1]?.headers).not.toMatchObject({ Authorization: expect.any(String) });
  });

  it("does not mint a command for a name the router already uses", async () => {
    const posts: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
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
            jsonResponse({ hub: true, hubUrl: "https://hub.example", environments: [] }),
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
        if (path === "/api/connect/tokens") {
          posts.push(path);

          return Promise.resolve(jsonResponse({ code: "ErrValidation", message: "no" }, 400));
        }
        if (path.startsWith("/api/activity")) {
          return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
        }

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });

    expect(
      await screen.findByRole("heading", { level: 1, name: "Create an environment" }),
    ).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Environment name"), "connect");

    expect(
      await screen.findByText("That name is taken by a page in this app. Pick another."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create command" })).toBeDisabled();
    expect(posts).toEqual([]);
  });

  it("offers Open environment once the named spoke heartbeats", async () => {
    let environments: { name: string; status: string }[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
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
            jsonResponse({ hub: true, hubUrl: "https://hub.example", environments }),
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
        if (path.startsWith("/api/activity")) {
          return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
        }

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });

    await userEvent.type(await screen.findByLabelText("Environment name"), "eks-testing");

    expect(screen.queryByRole("link", { name: "Open environment" })).not.toBeInTheDocument();

    environments = [{ name: "eks-testing", status: "connected" }];
    await userEvent.click(screen.getByRole("button", { name: "Check now" }));

    expect(await screen.findByRole("link", { name: "Open environment" })).toHaveAttribute(
      "href",
      "/environments/eks-testing",
    );
    expect(screen.getByText("eks-testing is connected.")).toBeInTheDocument();
  });

  it("says an environment cannot join when there is no public address", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
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
          return Promise.resolve(jsonResponse({ hub: true, environments: [] }));
        }
        if (path === "/api/connect") {
          return Promise.resolve(jsonResponse({ hub: true }));
        }
        if (path.startsWith("/api/activity")) {
          return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
        }

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });

    expect(
      await screen.findByText(
        "This control plane has no public address yet, so an environment cannot join. Contact Percona support.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create command" })).not.toBeInTheDocument();
  });

  it("says an environment cannot join when environment configuration cannot be stored", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
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
            jsonResponse({ hub: true, hubUrl: "https://hub.example", environments: [] }),
          );
        }
        if (path === "/api/connect") {
          return Promise.resolve(jsonResponse({ hub: true, hubUrl: "https://hub.example" }));
        }
        if (path.startsWith("/api/activity")) {
          return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
        }

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });

    expect(
      await screen.findByText(
        "This control plane cannot store configuration for an environment yet, so an environment cannot join. Contact Percona support.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Create command" })).not.toBeInTheDocument();
  });

  it("mints a command without naming the Git host behind this control plane", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const path = String(input).split("?")[0];
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
            jsonResponse({
              hub: true,
              hubUrl: "https://hub.example",
              githubOrg: "percona-cloud",
              environments: [],
            }),
          );
        }
        if (path === "/api/connect") {
          return Promise.resolve(
            jsonResponse({
              hub: true,
              hubUrl: "https://hub.example",
              githubOrg: "percona-cloud",
              clusterRegion: "us-west-2",
            }),
          );
        }
        if (path === "/api/connect/tokens" && (init?.method ?? "GET") === "POST") {
          return Promise.resolve(
            jsonResponse(
              {
                environment: "eks-testing",
                token: "jnt_abc",
                command:
                  "curl -fsSL https://hub.example/api/internal/connect/bootstrap.sh | bash -s -- --token jnt_abc",
                hubUrl: "https://hub.example",
                region: "us-west-2",
              },
              201,
            ),
          );
        }
        if (path.startsWith("/api/activity")) {
          return Promise.resolve(jsonResponse({ entries: [], cursor: "", truncated: false }));
        }

        return Promise.resolve(jsonResponse({}));
      }),
    );

    renderWithProviders(<AppRoutes />, { route: "/environments/new" });

    // The org backing this control plane is ours. The page offers the command
    // without saying where the configuration for that environment is kept.
    expect(await screen.findByRole("button", { name: "Create command" })).toBeInTheDocument();
    expect(screen.queryByText(/GitHub/)).not.toBeInTheDocument();
    expect(screen.queryByText("percona-cloud")).not.toBeInTheDocument();

    await userEvent.type(screen.getByLabelText("Environment name"), "eks-testing");
    await userEvent.click(screen.getByRole("button", { name: "Create command" }));

    expect(
      await screen.findByText(
        "curl -fsSL https://hub.example/api/internal/connect/bootstrap.sh | bash -s -- --token jnt_abc",
      ),
    ).toBeInTheDocument();
  });
});
