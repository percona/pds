import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { sessionKey } from "@/features/auth";
import { RootRoutes } from "@/routes/root";
import { notifySignedOut } from "@/lib/signed-out";
import { visit } from "@/lib/visit";
import { renderWithProviders } from "@/test/utils";

vi.mock("@/lib/visit", () => ({ visit: vi.fn() }));

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/**
 * Stubs the adapter with nobody signed in. The handler is called per request
 * rather than returning one Response, because a Response body can only be
 * read once.
 */
function stubAdapter(handler: (path: string, init?: RequestInit) => Response) {
  const mock = vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
    Promise.resolve(handler(String(input), init)),
  );
  vi.stubGlobal("fetch", mock);

  return mock;
}

const signedOut = jsonResponse({ code: "ErrValidation", message: "Sign in to continue." }, 401);

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

async function submit() {
  await userEvent.type(await screen.findByLabelText("Username"), "acme");
  await userEvent.type(screen.getByLabelText("Password"), "a long enough secret");
  await userEvent.click(screen.getByRole("button", { name: "Sign in" }));
}

describe("LoginPage", () => {
  it("signs in and sends the username and password to the adapter", async () => {
    const fetchMock = stubAdapter((path, init) => {
      if (path === "/api/auth/login" && init?.method === "POST") {
        return jsonResponse({ username: "acme" });
      }
      if (path === "/api/auth/session") {
        return signedOut;
      }

      return jsonResponse({});
    });

    renderWithProviders(<RootRoutes />, { route: "/login" });

    await userEvent.type(await screen.findByLabelText("Username"), "acme");
    await userEvent.type(screen.getByLabelText("Password"), "a long enough secret");
    await waitFor(() => expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        ([input, init]) => String(input) === "/api/auth/login" && init?.method === "POST",
      );
      expect(posted).toBeDefined();
      expect(JSON.parse(String(posted?.[1]?.body))).toEqual({
        username: "acme",
        password: "a long enough secret",
      });
    });
  });

  it("shows the adapter's one message when the sign in is refused", async () => {
    stubAdapter((path, init) => {
      if (path === "/api/auth/login" && init?.method === "POST") {
        return jsonResponse(
          { code: "ErrValidation", message: "That username and password do not match." },
          401,
        );
      }

      return signedOut;
    });

    renderWithProviders(<RootRoutes />, { route: "/login" });

    await userEvent.type(await screen.findByLabelText("Username"), "acme");
    await userEvent.type(screen.getByLabelText("Password"), "the wrong secret");
    await userEvent.click(screen.getByRole("button", { name: "Sign in" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "That username and password do not match.",
    );
  });

  it("says there is no sign up, because Percona creates the organization", async () => {
    stubAdapter(() => signedOut);

    renderWithProviders(<RootRoutes />, { route: "/login" });

    expect(await screen.findByText(/There is no sign up/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /sign up/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /create account/i })).not.toBeInTheDocument();
  });

  it("refuses a username that is not a DNS label before asking the adapter", async () => {
    const fetchMock = stubAdapter(() => signedOut);

    renderWithProviders(<RootRoutes />, { route: "/login" });

    await userEvent.type(await screen.findByLabelText("Username"), "Acme Corp");
    await userEvent.type(screen.getByLabelText("Password"), "a long enough secret");
    await userEvent.tab();

    expect(
      await screen.findByText("Use lowercase letters, numbers, and dashes. Start with a letter."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeDisabled();
    expect(
      fetchMock.mock.calls.filter(([input]) => String(input) === "/api/auth/login"),
    ).toHaveLength(0);
  });

  it("prefills the username a customer has just set a password for", async () => {
    stubAdapter(() => signedOut);

    renderWithProviders(<RootRoutes />, { route: "/login?username=acme" });

    expect(await screen.findByLabelText("Username")).toHaveValue("acme");
  });

  it("sends a signed in visitor home when they open sign in again", async () => {
    stubAdapter((path) => {
      if (path === "/api/auth/session") {
        return jsonResponse({ username: "default" });
      }
      if (path === "/api/features") {
        return jsonResponse({ flags: {} });
      }
      if (path === "/api/environments") {
        return jsonResponse({ environments: [] });
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [] });
      }
      if (path === "/api/cloud-connections") {
        return jsonResponse({ cloudConnections: [] });
      }
      if (path === "/api/connections") {
        return jsonResponse({
          servicenow: { configured: false },
          assistant: { configured: false },
          cursor: { configured: false },
        });
      }
      // The shell reads these as soon as it mounts. A 401 signs the visitor
      // out again, so the sidebar can appear and then leave before the
      // assertion.
      if (path.startsWith("/api/activity")) {
        return jsonResponse({ entries: [], cursor: "", truncated: false });
      }
      if (path === "/api/alerts") {
        return jsonResponse({ environments: [], alerts: [], criticals: 0 });
      }
      if (path.startsWith("/api/support/tickets")) {
        return jsonResponse({ tickets: [] });
      }

      return signedOut;
    });

    renderWithProviders(<RootRoutes />, { route: "/login" });

    expect(await screen.findByRole("heading", { name: "Home" })).toBeInTheDocument();
    expect(screen.getByTestId("sidebar")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Sign in" })).not.toBeInTheDocument();
  });

  // /login?username=acme is where the claim page sends people. A leftover
  // session for somebody else must not skip the form.
  it("shows the form when the username in the link is not the signed in one", async () => {
    stubAdapter((path) => {
      if (path === "/api/auth/session") {
        return jsonResponse({ username: "default" });
      }
      if (path === "/api/features") {
        return jsonResponse({ flags: {} });
      }
      if (path === "/api/environments") {
        return jsonResponse({ environments: [] });
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [] });
      }
      if (path === "/api/connections") {
        return jsonResponse({
          servicenow: { configured: false },
          assistant: { configured: false },
          cursor: { configured: false },
        });
      }

      return signedOut;
    });

    const { queryClient } = renderWithProviders(<RootRoutes />, { route: "/login?username=acme" });

    await waitFor(() => {
      expect(queryClient.getQueryState(sessionKey)?.status).toBe("success");
    });
    expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.getByLabelText("Username")).toHaveValue("acme");
    expect(screen.queryByTestId("sidebar")).not.toBeInTheDocument();
  });
});

describe("the gate around the app", () => {
  it("sends a signed out visitor from a product page to sign in", async () => {
    stubAdapter(() => signedOut);

    renderWithProviders(<RootRoutes />, { route: "/databases" });

    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(screen.queryByTestId("sidebar")).not.toBeInTheDocument();
  });

  it("renders the product once the adapter names a user", async () => {
    stubAdapter((path) => {
      if (path === "/api/auth/session") {
        return jsonResponse({ username: "acme" });
      }
      if (path === "/api/features") {
        return jsonResponse({ flags: {} });
      }
      if (path === "/api/environments") {
        return jsonResponse({ environments: [] });
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [] });
      }
      if (path === "/api/connections") {
        return jsonResponse({
          servicenow: { configured: false },
          assistant: { configured: false },
          cursor: { configured: false },
        });
      }

      return jsonResponse({});
    });

    renderWithProviders(<RootRoutes />, { route: "/databases" });

    expect(await screen.findByTestId("sidebar")).toBeInTheDocument();
    expect(await screen.findByText("acme")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Sign in" })).not.toBeInTheDocument();
  });

  // A surface that is off has to be off on the first frame, so the flag set is
  // read before any screen mounts rather than alongside it.
  it("applies the flag set it reads after signing in", async () => {
    stubAdapter((path) => {
      if (path === "/api/auth/session") {
        return jsonResponse({ username: "acme" });
      }
      if (path === "/api/features") {
        return jsonResponse({ flags: { databases: false } });
      }
      if (path === "/api/environments") {
        return jsonResponse({ environments: [] });
      }

      return jsonResponse({});
    });

    renderWithProviders(<RootRoutes />, { route: "/databases" });

    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeInTheDocument();
  });

  it("says so when the control plane cannot be reached at all", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))),
    );

    renderWithProviders(<RootRoutes />, { route: "/databases" });

    expect(
      await screen.findByText("The control plane could not be reached", { exact: false }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Sign in" })).not.toBeInTheDocument();
  });

  // A logout the adapter refused leaves the session row and the cookie live.
  // Showing the login page over a session that still works would be the button
  // lying about what it did.
  it("stays signed in when the adapter refuses the sign out", async () => {
    stubAdapter((path, init) => {
      if (path === "/api/auth/logout" && init?.method === "POST") {
        return jsonResponse({ code: "ErrInternal", message: "Signing out did not finish." }, 500);
      }
      if (path === "/api/auth/session") {
        return jsonResponse({ username: "acme" });
      }
      if (path === "/api/features") {
        return jsonResponse({ flags: {} });
      }
      if (path === "/api/environments") {
        return jsonResponse({ environments: [] });
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [] });
      }
      if (path === "/api/connections") {
        return jsonResponse({
          servicenow: { configured: false },
          assistant: { configured: false },
          cursor: { configured: false },
        });
      }

      return jsonResponse({});
    });

    renderWithProviders(<RootRoutes />, { route: "/databases" });

    await userEvent.click(await screen.findByRole("button", { name: "Sign out" }));

    expect(await screen.findByTestId("sidebar")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Sign in" })).not.toBeInTheDocument();
  });

  it("drops the last user's cached data when the session runs out", async () => {
    let signedIn = true;
    stubAdapter((path) => {
      if (path === "/api/auth/session") {
        return signedIn ? jsonResponse({ username: "acme" }) : signedOut;
      }
      if (path === "/api/features") {
        return jsonResponse({ flags: {} });
      }
      if (path === "/api/environments") {
        return jsonResponse({ environments: [] });
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [] });
      }

      return jsonResponse({});
    });

    const { queryClient } = renderWithProviders(<RootRoutes />, { route: "/databases" });
    expect(await screen.findByText("acme")).toBeInTheDocument();
    queryClient.setQueryData(["chat-conversation", "c1"], { title: "acme's private chat" });

    signedIn = false;
    notifySignedOut();

    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(queryClient.getQueryData(["chat-conversation", "c1"])).toBeUndefined();
    expect(queryClient.getQueryData(sessionKey)).toBeNull();
  });

  it("signs out and shows the login screen again", async () => {
    // The adapter stops reporting a session once the row is gone, so the stub
    // has to as well, or the test would prove only that a button was clicked.
    let signedIn = true;
    const fetchMock = stubAdapter((path, init) => {
      if (path === "/api/auth/logout" && init?.method === "POST") {
        signedIn = false;

        return new Response(null, { status: 204 });
      }
      if (path === "/api/auth/session") {
        return signedIn ? jsonResponse({ username: "acme" }) : signedOut;
      }
      if (path === "/api/features") {
        return jsonResponse({ flags: {} });
      }
      if (path === "/api/environments") {
        return jsonResponse({ environments: [] });
      }
      if (path.startsWith("/api/databases")) {
        return jsonResponse({ databases: [] });
      }
      if (path === "/api/connections") {
        return jsonResponse({
          servicenow: { configured: false },
          assistant: { configured: false },
          cursor: { configured: false },
        });
      }

      return jsonResponse({});
    });

    renderWithProviders(<RootRoutes />, { route: "/databases" });

    await userEvent.click(await screen.findByRole("button", { name: "Sign out" }));

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([input, init]) => String(input) === "/api/auth/logout" && init?.method === "POST",
        ),
      ).toBe(true),
    );
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
  });

  describe("coming back from an app that asked to sign in", () => {
    const next = "/oauth/authorize?client_id=https%3A%2F%2Fagent.example%2Fc.json&state=abc";

    function signInAs(route: string) {
      const fetchMock = stubAdapter((path, init) => {
        if (path === "/api/auth/login" && init?.method === "POST") {
          return jsonResponse({ username: "acme" });
        }
        if (path === "/api/auth/session") {
          return signedOut;
        }

        return jsonResponse({});
      });
      vi.mocked(visit).mockClear();

      renderWithProviders(<RootRoutes />, { route });

      return fetchMock;
    }

    it("returns to the authorization request after signing in", async () => {
      signInAs(`/login?next=${encodeURIComponent(next)}`);

      await submit();

      await waitFor(() => expect(visit).toHaveBeenCalledWith(next));
    });

    it.each([
      ["another site", "https://evil.example/oauth/authorize?x=1"],
      ["a protocol relative address", "//evil.example/oauth/authorize?x=1"],
      ["a path that is not an authorization request", "/oauth/token?x=1"],
    ])("does not follow %s", async (_name, target) => {
      const fetchMock = signInAs(`/login?next=${encodeURIComponent(target)}`);

      await submit();

      await waitFor(() =>
        expect(
          fetchMock.mock.calls.some(
            ([input, init]) => String(input) === "/api/auth/login" && init?.method === "POST",
          ),
        ).toBe(true),
      );
      expect(visit).not.toHaveBeenCalled();
    });
  });
});
