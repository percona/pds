import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RootRoutes } from "@/routes/root";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const signedOut = jsonResponse({ code: "ErrValidation", message: "Sign in to continue." }, 401);

/** A control plane with an identity provider: nobody is signed in, and it offers both ways in. */
function stubIssuerDeployment() {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const path = String(input);
      if (path === "/api/auth/methods") {
        return Promise.resolve(jsonResponse({ methods: ["oidc", "password"] }));
      }

      return Promise.resolve(path === "/api/auth/session" ? signedOut.clone() : jsonResponse({}));
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("LoginPage with an identity provider", () => {
  it("offers Google and GitHub first, each starting that provider's sign in", async () => {
    stubIssuerDeployment();

    renderWithProviders(<RootRoutes />, { route: "/login" });

    const google = await screen.findByRole("link", { name: "Continue with Google" });
    const github = screen.getByRole("link", { name: "Continue with GitHub" });
    expect(google).toHaveAttribute("href", "/api/auth/oidc/start?provider=google");
    expect(github).toHaveAttribute("href", "/api/auth/oidc/start?provider=github");
    // The providers come before the password form in reading order.
    expect(
      google.compareDocumentPosition(screen.getByRole("button", { name: "Use a password" })) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("keeps the password form behind a disclosure", async () => {
    stubIssuerDeployment();

    renderWithProviders(<RootRoutes />, { route: "/login" });

    const toggle = await screen.findByRole("button", { name: "Use a password" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByLabelText("Username")).not.toBeInTheDocument();

    await userEvent.click(toggle);

    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByLabelText("Username")).toBeInTheDocument();
  });

  it("opens the password form when a username is already known", async () => {
    stubIssuerDeployment();

    renderWithProviders(<RootRoutes />, { route: "/login?username=acme" });

    expect(await screen.findByLabelText("Username")).toHaveValue("acme");
  });

  it("does not claim there is no sign up while signing up is possible", async () => {
    stubIssuerDeployment();

    renderWithProviders(<RootRoutes />, { route: "/login?username=acme" });

    await screen.findByLabelText("Username");
    expect(screen.queryByText(/There is no sign up/)).not.toBeInTheDocument();
  });

  it("offers no provider on a control plane without an identity provider", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input);
        if (path === "/api/auth/methods") {
          return Promise.resolve(jsonResponse({ methods: ["password"] }));
        }

        return Promise.resolve(path === "/api/auth/session" ? signedOut.clone() : jsonResponse({}));
      }),
    );

    renderWithProviders(<RootRoutes />, { route: "/login" });

    expect(await screen.findByLabelText("Username")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Continue with/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Use a password" })).not.toBeInTheDocument();
  });
});

describe("Signing up", () => {
  async function openSignUp() {
    stubIssuerDeployment();
    renderWithProviders(<RootRoutes />, { route: "/login" });
    await userEvent.click(await screen.findByRole("link", { name: "Create an organization" }));
  }

  it("asks only for the organization name before the providers", async () => {
    await openSignUp();

    expect(
      screen.getByRole("heading", { level: 1, name: "Create your organization" }),
    ).toBeVisible();
    expect(screen.getByLabelText("Organization name")).toBeInTheDocument();
    expect(screen.queryByLabelText("Username")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Use a password" })).not.toBeInTheDocument();
  });

  it("keeps the providers off until the organization has a name", async () => {
    await openSignUp();

    expect(screen.getByRole("button", { name: "Continue with Google" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Continue with GitHub" })).toBeDisabled();
    expect(screen.queryByRole("link", { name: /Continue with/ })).not.toBeInTheDocument();
  });

  it("sends the typed name with the provider the customer picks", async () => {
    await openSignUp();

    await userEvent.type(screen.getByLabelText("Organization name"), "Analytical Engines");

    expect(screen.getByRole("link", { name: "Continue with Google" })).toHaveAttribute(
      "href",
      "/api/auth/oidc/start?provider=google&organization=Analytical%20Engines",
    );
    expect(screen.getByRole("link", { name: "Continue with GitHub" })).toHaveAttribute(
      "href",
      "/api/auth/oidc/start?provider=github&organization=Analytical%20Engines",
    );
  });

  it("refuses a name over 200 characters and says so", async () => {
    await openSignUp();

    await userEvent.click(screen.getByLabelText("Organization name"));
    await userEvent.paste("a".repeat(201));
    await userEvent.tab();

    expect(await screen.findByText("Use 200 characters or fewer.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Continue with Google" })).toBeDisabled();
  });

  it("goes back to signing in", async () => {
    await openSignUp();

    await userEvent.click(screen.getByRole("link", { name: "Sign in" }));

    expect(await screen.findByRole("heading", { level: 1, name: "Sign in" })).toBeVisible();
  });
});

describe("A sign in that the control plane sent back with a code", () => {
  const cases: Array<[string, string, string]> = [
    ["no_membership", "", "This account does not belong to an organization yet."],
    ["sign_in_failed", "", "Signing in did not finish. Try again."],
    ["email_unverified", "", "Your identity provider has not verified your email address."],
    ["signup_rate_limited", "", "Too many organizations were created from this address."],
    ["signup_unavailable", "", "New organizations cannot be created right now."],
    ["signup_limit_reached", "", "You have created as many organizations as one person can."],
    ["organization_name_invalid", "", "Give the organization a name of up to 200 characters."],
    ["invite_invalid", "", "That invite link is not valid."],
    ["invite_spent", "", "That invite link has already been used."],
    ["invite_expired", "", "That invite link has expired."],
    ["invite_already_member", "", "You already belong to this organization."],
    ["invite_email_mismatch", "a***@example.com", "This invitation was sent to a***@example.com."],
    ["invite_email_unverified", "a***@example.com", "a***@example.com"],
  ];

  it.each(cases)("says what %s means", async (code, invited, sentence) => {
    stubIssuerDeployment();
    const query = new URLSearchParams({ error: code });
    if (invited) {
      query.set("invited", invited);
    }

    renderWithProviders(<RootRoutes />, { route: `/login?${query.toString()}` });

    expect(await screen.findByRole("alert")).toHaveTextContent(sentence);
  });

  it("falls back to a plain sentence for a code it does not know", async () => {
    stubIssuerDeployment();

    renderWithProviders(<RootRoutes />, { route: "/login?error=something_new" });

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Signing in did not finish. Try again.",
    );
  });
});
