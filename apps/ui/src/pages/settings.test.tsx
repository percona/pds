import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

type Calls = Array<{ method: string; path: string; body: unknown }>;

const admin = {
  username: "acme",
  user: "Ada",
  organization: "Acme",
  team: "default",
  role: "admin",
};

/** Stubs the control plane for Settings. The session names whatever the last rename set. */
function stubAdapter(
  options: {
    session?: Record<string, unknown>;
    write?: () => Response;
    ai?: { access: string; passwords: boolean; sqlWrites: boolean };
  } = {},
) {
  const calls: Calls = [];
  let session = options.session ?? admin;
  let ai = options.ai ?? { access: "read", passwords: false, sqlWrites: false };
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      const method = init?.method ?? "GET";
      if (path === "/api/auth/session") {
        return Promise.resolve(jsonResponse(session));
      }
      if (path === "/api/organization/ai" && method === "GET") {
        return Promise.resolve(jsonResponse(ai));
      }
      if (method !== "GET") {
        const body = init?.body ? JSON.parse(String(init.body)) : undefined;
        calls.push({ method, path, body });
        const refusal = options.write?.();
        if (refusal) {
          return Promise.resolve(refusal);
        }
        if (path === "/api/organization/ai") {
          ai = body as typeof ai;

          return Promise.resolve(jsonResponse(ai));
        }
        session = { ...session, organization: (body as { name: string }).name };

        return Promise.resolve(new Response(null, { status: 204 }));
      }

      return Promise.resolve(jsonResponse({}));
    }),
  );

  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Settings screen", () => {
  it("renames the organization, starting from its current name", async () => {
    const calls = stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/settings" });

    expect(await screen.findByRole("heading", { level: 1, name: "Settings" })).toBeVisible();
    const field = await screen.findByLabelText("Organization name");
    await waitFor(() => expect(field).toHaveValue("Acme"));
    const save = screen.getByRole("button", { name: "Save" });
    expect(save).toBeDisabled();

    await userEvent.clear(field);
    await userEvent.type(field, "  Acme Corp  ");
    await userEvent.click(save);

    await waitFor(() =>
      expect(calls).toEqual([
        { method: "PATCH", path: "/api/organization", body: { name: "Acme Corp" } },
      ]),
    );
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Organization renamed"));
    await waitFor(() =>
      expect(screen.getByLabelText("Organization name")).toHaveValue("Acme Corp"),
    );
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("holds back a name that is empty or too long", async () => {
    stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/settings" });

    const field = await screen.findByLabelText("Organization name");
    await waitFor(() => expect(field).toHaveValue("Acme"));
    await userEvent.clear(field);
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();

    await userEvent.click(field);
    await userEvent.paste("a".repeat(201));
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(screen.getByText("Use 200 characters or fewer.")).toBeVisible();
  });

  it("shows what the control plane refused", async () => {
    stubAdapter({
      write: () =>
        jsonResponse(
          {
            code: "ErrValidation",
            message: "Give the organization a name of up to 200 characters.",
          },
          400,
        ),
    });
    renderWithProviders(<AppRoutes />, { route: "/settings" });

    const field = await screen.findByLabelText("Organization name");
    await waitFor(() => expect(field).toHaveValue("Acme"));
    await userEvent.type(field, "x");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Give the organization a name of up to 200 characters.",
    );
  });

  it("is not there for a user", async () => {
    stubAdapter({ session: { ...admin, role: "user" } });
    renderWithProviders(<AppRoutes />, { route: "/settings" });

    await waitFor(() =>
      expect(screen.queryByLabelText("Organization name")).not.toBeInTheDocument(),
    );
    expect(screen.queryByRole("heading", { level: 1, name: "Settings" })).not.toBeInTheDocument();
  });

  it("shows what agents and the Chat may do and saves a change", async () => {
    const calls = stubAdapter();
    renderWithProviders(<AppRoutes />, { route: "/settings" });

    const group = await screen.findByRole("radiogroup", {
      name: "The most agents and the Chat may do",
    });
    expect(within(group).getByRole("radio", { name: /Read only/ })).toBeChecked();
    const passwords = screen.getByRole("checkbox", {
      name: "Let local agents fetch database passwords",
    });
    const sql = screen.getByRole("checkbox", { name: "Let local agents change data with SQL" });
    expect(passwords).not.toBeChecked();
    expect(sql).not.toBeChecked();
    const save = screen.getByRole("button", { name: "Save changes" });
    expect(save).toBeDisabled();

    await userEvent.click(within(group).getByRole("radio", { name: /Read and change/ }));
    await userEvent.click(passwords);
    await userEvent.click(save);

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "PUT",
          path: "/api/organization/ai",
          body: { access: "change", passwords: true, sqlWrites: false },
        },
      ]),
    );
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("AI settings saved"));
    expect(within(group).getByRole("radio", { name: /Read and change/ })).toBeChecked();
    expect(screen.getByRole("button", { name: "Save changes" })).toBeDisabled();
  });

  it("shows what the control plane refused when saving AI settings", async () => {
    stubAdapter({
      write: () =>
        jsonResponse(
          { code: "ErrValidation", message: "Choose read, change, or change and delete." },
          400,
        ),
    });
    renderWithProviders(<AppRoutes />, { route: "/settings" });

    const group = await screen.findByRole("radiogroup", {
      name: "The most agents and the Chat may do",
    });
    await userEvent.click(within(group).getByRole("radio", { name: /Read, change and delete/ }));
    await userEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(await screen.findByText("Choose read, change, or change and delete.")).toBeVisible();
  });
});
