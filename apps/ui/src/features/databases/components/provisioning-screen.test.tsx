import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ProvisioningScreen } from "@/features/databases";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

function step(id: string) {
  return screen.getByTestId(`provisioning-step-${id}`);
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ProvisioningScreen", () => {
  it("names the database and states the expected wait", () => {
    renderWithProviders(
      <ProvisioningScreen
        database={createMockDatabase({ name: "orders-dev", status: "provisioning" })}
        onRetry={vi.fn()}
      />,
    );

    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Creating orders-dev");
    expect(screen.getByText("Usually 6 to 12 minutes")).toBeInTheDocument();
  });

  it("shows each step with icon and text, not color alone", () => {
    renderWithProviders(
      <ProvisioningScreen
        database={createMockDatabase({ status: "initializing" })}
        onRetry={vi.fn()}
      />,
    );

    expect(step("volumes")).toHaveTextContent("Creating volumes");
    expect(step("volumes")).toHaveTextContent("Done");
    expect(step("database")).toHaveTextContent("In progress");
    expect(step("app-user")).toHaveTextContent("Waiting");
    expect(step("volumes").querySelector("svg")).not.toBeNull();
  });

  it("expands a step to its last platform event", async () => {
    renderWithProviders(
      <ProvisioningScreen
        database={createMockDatabase({ status: "initializing" })}
        events={{ database: "Waiting for pod orders-dev-0 to become ready" }}
        onRetry={vi.fn()}
      />,
    );

    const toggle = within(step("database")).getByRole("button");
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    await userEvent.click(toggle);

    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(step("database")).toHaveTextContent("Waiting for pod orders-dev-0 to become ready");
  });

  it("says so when a step has no event yet", async () => {
    renderWithProviders(
      <ProvisioningScreen
        database={createMockDatabase({ status: "provisioning" })}
        onRetry={vi.fn()}
      />,
    );

    await userEvent.click(within(step("volumes")).getByRole("button"));

    expect(step("volumes")).toHaveTextContent("No event recorded yet.");
  });

  it("states the failed step and the reason without a stack trace", () => {
    renderWithProviders(
      <ProvisioningScreen
        database={createMockDatabase({
          name: "orders-dev",
          status: "failed",
          message: "No node has enough memory for 3 members.",
        })}
        failedStep="volumes"
        onRetry={vi.fn()}
      />,
    );

    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(
      "Could not create orders-dev",
    );
    expect(screen.getByText("Creating volumes failed.")).toBeInTheDocument();
    expect(screen.getByText("No node has enough memory for 3 members.")).toBeInTheDocument();
    expect(step("volumes")).toHaveTextContent("Failed");
  });

  it("offers a retry and a support route on a failure", async () => {
    const onRetry = vi.fn();
    renderWithProviders(
      <ProvisioningScreen
        database={createMockDatabase({ name: "orders-dev", status: "failed" })}
        onRetry={onRetry}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(onRetry).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByRole("button", { name: "Contact support" }));
    expect(await screen.findByRole("dialog")).toBeInTheDocument();
  });

  it("opens the support dialog pre-filled with the database it is about", async () => {
    const database = createMockDatabase({
      name: "orders-dev",
      profile: "prod",
      status: "failed",
      message: "No node has enough memory for 3 members.",
    });
    renderWithProviders(
      <ProvisioningScreen
        database={database}
        failedStep="database"
        events={{ database: "Pod orders-dev-0 restarted 5 times" }}
        onRetry={vi.fn()}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Contact support" }));

    expect(await screen.findByLabelText("Subject")).toHaveValue(
      "Cannot create database orders-dev",
    );

    const description = screen.getByLabelText<HTMLTextAreaElement>("Description");
    expect(description.value).toContain(`Environment: ${database.environment}`);
  });

  it("sends ticket context with environment type when the ticket is sent", async () => {
    const fetchMock = vi.fn().mockImplementation((input: string, init?: RequestInit) => {
      if (String(input) === "/api/support/tickets" && init?.method === "POST") {
        return Promise.resolve(
          new Response(JSON.stringify({ number: "CS0061234" }), {
            status: 201,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }

      return Promise.resolve(new Response(JSON.stringify({}), { status: 200 }));
    });
    vi.stubGlobal("fetch", fetchMock);

    renderWithProviders(
      <ProvisioningScreen
        database={createMockDatabase({
          name: "orders-dev",
          profile: "prod",
          members: 3,
          status: "failed",
          message: "No node has enough memory for 3 members.",
        })}
        failedStep="database"
        events={{ database: "Pod orders-dev-0 restarted 5 times" }}
        onRetry={vi.fn()}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Contact support" }));
    await userEvent.click(screen.getByRole("button", { name: "Send ticket" }));

    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([url, init]) => String(url) === "/api/support/tickets" && init?.method === "POST",
        ),
      ).toBe(true),
    );

    const [, init] = fetchMock.mock.calls.find(
      ([url, requestInit]) =>
        String(url) === "/api/support/tickets" && requestInit?.method === "POST",
    ) as [string, RequestInit];

    expect(JSON.parse(String(init.body))).toMatchObject({
      context: {
        database: "orders-dev",
        profile: "prod",
        clusterType: "cluster",
        members: 3,
        errorMessage: "Pod orders-dev-0 restarted 5 times",
      },
    });
  });

  it("offers Try again, Contact support and Delete on a failure", () => {
    renderWithProviders(
      <ProvisioningScreen
        database={createMockDatabase({ name: "orders-dev", status: "failed" })}
        failedStep="database"
        onRetry={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Contact support" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete" })).toBeInTheDocument();
  });

  it("does not offer a retry while the database is still coming up", () => {
    renderWithProviders(
      <ProvisioningScreen
        database={createMockDatabase({ status: "provisioning" })}
        onRetry={vi.fn()}
      />,
    );

    expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Contact support" })).not.toBeInTheDocument();
  });

  it("does not call a create lost while the failure is still unsettled", () => {
    renderWithProviders(
      <ProvisioningScreen
        database={createMockDatabase({ status: "failed", message: "smart update: failed" })}
        failed={false}
        onRetry={() => {}}
      />,
    );

    expect(screen.queryByText(/Could not create/)).not.toBeInTheDocument();
    expect(screen.queryByText(/failed\./)).not.toBeInTheDocument();
  });
});
