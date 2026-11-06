import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { sessionKey } from "@/features/auth";
import { RootRoutes } from "@/routes/root";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubAdapter(handler: (path: string, init?: RequestInit) => Response) {
  const mock = vi.fn((input: RequestInfo | URL, init?: RequestInit) =>
    Promise.resolve(handler(String(input), init)),
  );
  vi.stubGlobal("fetch", mock);

  return mock;
}

/** The claimable link every happy path here opens. */
function stubClaimableInvite(claim: () => Response) {
  return stubAdapter((path, init) => {
    if (path.startsWith("/api/auth/invite") && init?.method === "POST") {
      return claim();
    }
    if (path.startsWith("/api/auth/invite")) {
      return jsonResponse({ username: "acme" });
    }

    return jsonResponse({ code: "ErrValidation", message: "Sign in to continue." }, 401);
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStorage.clear();
});

describe("ClaimInvitePage", () => {
  it("names the username the link sets a password for", async () => {
    stubClaimableInvite(() => jsonResponse({ username: "acme" }));

    renderWithProviders(<RootRoutes />, { route: "/claim?token=a-real-token" });

    expect(
      await screen.findByText("This link sets the password for acme.", { exact: false }),
    ).toBeInTheDocument();
  });

  it("sets the password and sends the customer to sign in with their username filled in", async () => {
    const fetchMock = stubClaimableInvite(() => jsonResponse({ username: "acme" }));

    renderWithProviders(<RootRoutes />, { route: "/claim?token=a-real-token" });

    await userEvent.type(await screen.findByLabelText("Password"), "a long enough secret");
    await userEvent.type(screen.getByLabelText("Password again"), "a long enough secret");
    await waitFor(() => expect(screen.getByRole("button", { name: "Set password" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Set password" }));

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        ([input, init]) => String(input).startsWith("/api/auth/invite") && init?.method === "POST",
      );
      expect(posted).toBeDefined();
      expect(JSON.parse(String(posted?.[1]?.body))).toEqual({
        token: "a-real-token",
        password: "a long enough secret",
      });
    });

    // Claiming does not sign them in. They arrive at the form with the username
    // already filled in and use the password they just chose.
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(await screen.findByLabelText("Username")).toHaveValue("acme");
  });

  // A leftover session for another username used to bounce the sign in page
  // straight into the app. Claiming acme in a browser already signed in as
  // default has to land on the form for acme, not the control plane as default.
  it("does not keep a leftover session when the link is for another username", async () => {
    stubAdapter((path, init) => {
      if (path.startsWith("/api/auth/invite") && init?.method === "POST") {
        return jsonResponse({ username: "acme" });
      }
      if (path.startsWith("/api/auth/invite")) {
        return jsonResponse({ username: "acme" });
      }
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

      return jsonResponse({ code: "ErrValidation", message: "Sign in to continue." }, 401);
    });

    const { queryClient } = renderWithProviders(<RootRoutes />, {
      route: "/claim?token=a-real-token",
    });

    await userEvent.type(await screen.findByLabelText("Password"), "a long enough secret");
    await userEvent.type(screen.getByLabelText("Password again"), "a long enough secret");
    await waitFor(() => expect(screen.getByRole("button", { name: "Set password" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Set password" }));

    // The session can settle before the claim answers and navigates, so the
    // form is awaited first. Then the leftover session has to have settled:
    // before that the form is on screen anyway, and after it the bug opens the
    // app as default.
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    await waitFor(() => {
      expect(queryClient.getQueryState(sessionKey)?.status).toBe("success");
    });
    expect(screen.getByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(await screen.findByLabelText("Username")).toHaveValue("acme");
    expect(screen.queryByTestId("sidebar")).not.toBeInTheDocument();
  });

  it("refuses to submit until both passwords match and are long enough", async () => {
    const fetchMock = stubClaimableInvite(() => jsonResponse({ username: "acme" }));

    renderWithProviders(<RootRoutes />, { route: "/claim?token=a-real-token" });

    await userEvent.type(await screen.findByLabelText("Password"), "a long enough secret");
    await userEvent.type(screen.getByLabelText("Password again"), "a different secret");
    await userEvent.tab();

    expect(await screen.findByText("Both passwords have to match.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Set password" })).toBeDisabled();

    await userEvent.clear(screen.getByLabelText("Password"));
    await userEvent.type(screen.getByLabelText("Password"), "short");
    await userEvent.tab();

    expect(await screen.findByText("Use at least 12 characters.")).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.filter(
        ([input, init]) => String(input).startsWith("/api/auth/invite") && init?.method === "POST",
      ),
    ).toHaveLength(0);
  });

  it.each([
    {
      name: "already used",
      status: 409,
      message: "That invite link has already been used. Ask Percona for a new one.",
    },
    {
      name: "expired",
      status: 409,
      message: "That invite link has expired. Ask Percona for a new one.",
    },
    { name: "never issued", status: 404, message: "That invite link is not valid." },
  ])("shows the adapter's reason when the link is $name", async ({ status, message }) => {
    stubAdapter((path) => {
      if (path.startsWith("/api/auth/invite")) {
        return jsonResponse({ code: "ErrConflict", message }, status);
      }

      return jsonResponse({ code: "ErrValidation", message: "Sign in to continue." }, 401);
    });

    renderWithProviders(<RootRoutes />, { route: "/claim?token=a-spent-token" });

    expect(
      await screen.findByRole("heading", { name: "This link cannot be used" }),
    ).toBeInTheDocument();
    expect(await screen.findByText(message)).toBeInTheDocument();
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
  });

  it("treats a link with no token at all as unusable without asking the adapter", async () => {
    const fetchMock = stubAdapter(() =>
      jsonResponse({ code: "ErrValidation", message: "Sign in to continue." }, 401),
    );

    renderWithProviders(<RootRoutes />, { route: "/claim" });

    expect(
      await screen.findByRole("heading", { name: "This link cannot be used" }),
    ).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.filter(([input]) => String(input).startsWith("/api/auth/invite")),
    ).toHaveLength(0);
  });

  // An adapter that did not answer says nothing about the link. Reporting it as
  // unusable would have the customer throw away a link that is fine.
  it("does not blame the link when the control plane cannot be reached", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))),
    );

    renderWithProviders(<RootRoutes />, { route: "/claim?token=a-real-token" });

    expect(
      await screen.findByRole("heading", { name: "The control plane could not be reached" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "This link cannot be used" }),
    ).not.toBeInTheDocument();
  });

  // The link is what a customer with no password has, so it cannot sit behind
  // the gate that sends people to a form they cannot use yet.
  it("opens without a session", async () => {
    stubClaimableInvite(() => jsonResponse({ username: "acme" }));

    renderWithProviders(<RootRoutes />, { route: "/claim?token=a-real-token" });

    expect(await screen.findByLabelText("Password")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Sign in" })).not.toBeInTheDocument();
  });
});

describe("ClaimInvitePage for an Organization invitation", () => {
  const preview = {
    organization: "Acme",
    team: "payments",
    role: "user",
    email: "g***e@example.com",
    anyEmail: false,
  };

  function stubOrganizationInvite(options: {
    methods: string[];
    preview?: Record<string, unknown>;
    claim?: () => Response;
  }) {
    return stubAdapter((path, init) => {
      if (path === "/api/auth/methods") {
        return jsonResponse({ methods: options.methods });
      }
      if (path.startsWith("/api/auth/invite") && init?.method === "POST") {
        return options.claim?.() ?? jsonResponse({ username: "grace" });
      }
      if (path.startsWith("/api/auth/invite")) {
        return jsonResponse(options.preview ?? preview);
      }

      return jsonResponse({ code: "ErrValidation", message: "Sign in to continue." }, 401);
    });
  }

  it("names the organization, team and role, and shows the masked address", async () => {
    stubOrganizationInvite({ methods: ["oidc"] });

    renderWithProviders(<RootRoutes />, { route: "/claim?token=a-real-token" });

    expect(await screen.findByRole("heading", { name: "Join Acme" })).toBeInTheDocument();
    expect(screen.getByText("You are invited as a user in the payments team.")).toBeInTheDocument();
    expect(screen.getByText("g***e@example.com")).toBeInTheDocument();
  });

  it("says anyone with the link can join when no address was given", async () => {
    stubOrganizationInvite({
      methods: ["oidc"],
      preview: { ...preview, email: undefined, anyEmail: true },
    });

    renderWithProviders(<RootRoutes />, { route: "/claim?token=a-real-token" });

    expect(await screen.findByText("Anyone with this link can join.")).toBeInTheDocument();
  });

  it("offers the providers and carries the link through the sign in", async () => {
    stubOrganizationInvite({ methods: ["oidc"] });

    renderWithProviders(<RootRoutes />, { route: "/claim?token=a%2Breal-token" });

    expect(await screen.findByRole("link", { name: "Continue with Google" })).toHaveAttribute(
      "href",
      "/api/auth/oidc/start?provider=google&invite=a%2Breal-token",
    );
    expect(screen.getByRole("link", { name: "Continue with GitHub" })).toHaveAttribute(
      "href",
      "/api/auth/oidc/start?provider=github&invite=a%2Breal-token",
    );
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
  });

  it("asks for a login and a password where there is no provider", async () => {
    const fetchMock = stubOrganizationInvite({ methods: ["password"] });

    renderWithProviders(<RootRoutes />, { route: "/claim?token=a-real-token" });

    await userEvent.type(await screen.findByLabelText("Login"), "grace");
    await userEvent.type(screen.getByLabelText("Password"), "a long enough secret");
    await userEvent.type(screen.getByLabelText("Password again"), "a long enough secret");
    await waitFor(() => expect(screen.getByRole("button", { name: "Set password" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Set password" }));

    await waitFor(() => {
      const posted = fetchMock.mock.calls.find(
        ([input, init]) => String(input).startsWith("/api/auth/invite") && init?.method === "POST",
      );
      expect(JSON.parse(String(posted?.[1]?.body))).toEqual({
        token: "a-real-token",
        login: "grace",
        password: "a long enough secret",
      });
    });
    expect(await screen.findByRole("heading", { name: "Sign in" })).toBeInTheDocument();
    expect(await screen.findByLabelText("Username")).toHaveValue("grace");
  });

  it("shows why a login was refused and keeps the form", async () => {
    stubOrganizationInvite({
      methods: ["password"],
      claim: () =>
        jsonResponse(
          { code: "ErrConflict", message: "That login is already taken. Choose another." },
          409,
        ),
    });

    renderWithProviders(<RootRoutes />, { route: "/claim?token=a-real-token" });

    await userEvent.type(await screen.findByLabelText("Login"), "grace");
    await userEvent.type(screen.getByLabelText("Password"), "a long enough secret");
    await userEvent.type(screen.getByLabelText("Password again"), "a long enough secret");
    await waitFor(() => expect(screen.getByRole("button", { name: "Set password" })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: "Set password" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "That login is already taken. Choose another.",
    );
    expect(screen.getByLabelText("Login")).toBeInTheDocument();
  });
});
