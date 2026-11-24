import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { CreateDatabaseForm } from "@/features/databases";
import { renderWithProviders } from "@/test/utils";

function submitButton() {
  return screen.getByRole("button", { name: "Create database" });
}

function accessOption(name: "Private" | "Public") {
  return screen.getByRole("radio", { name });
}

function nameField() {
  return screen.getByLabelText("Name");
}

const postgresqlNameError =
  "Use 37 characters or fewer for PostgreSQL. Every member's labels carry this name, and a longer one is refused.";

describe("CreateDatabaseForm", () => {
  it("sends cancel back to that environment", () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    expect(screen.getByRole("link", { name: "Cancel" })).toHaveAttribute(
      "href",
      "/environments/spoke-a",
    );
  });

  it("puts the profile picker before the name field", () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    const position = screen
      .getByRole("radio", { name: "Dev" })
      .compareDocumentPosition(nameField());

    expect(position & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getByText("1 member, on-demand backups")).toBeInTheDocument();
  });

  it("keeps submit disabled until the form is valid", () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    expect(submitButton()).toBeDisabled();
  });

  it("enables submit once a valid name is typed", async () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    await userEvent.type(nameField(), "orders-dev");

    await waitFor(() => expect(submitButton()).toBeEnabled());
  });

  it("reports an invalid name on blur and keeps submit disabled", async () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    await userEvent.type(nameField(), "Orders_Dev");
    await userEvent.tab();

    expect(
      await screen.findByText(
        "Use lowercase letters, numbers, and dashes. Start and end with a letter or number.",
      ),
    ).toBeInTheDocument();
    expect(submitButton()).toBeDisabled();
    expect(nameField()).toHaveAttribute("aria-invalid", "true");
  });

  it("submits the name with the default profile", async () => {
    const onSubmit = vi.fn();
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={onSubmit} />);

    await userEvent.type(nameField(), "orders-dev");
    await waitFor(() => expect(submitButton()).toBeEnabled());
    await userEvent.click(submitButton());

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        name: "orders-dev",
        environment: "spoke-a",
        engine: "mysql",
        profile: "dev",
        access: "private",
      }),
    );
  });

  it("submits the profile the user picked", async () => {
    const onSubmit = vi.fn();
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={onSubmit} />);

    await userEvent.click(screen.getByRole("radio", { name: "Prod" }));
    await userEvent.type(nameField(), "orders-prod");
    await waitFor(() => expect(submitButton()).toBeEnabled());
    await userEvent.click(submitButton());

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        name: "orders-prod",
        environment: "spoke-a",
        engine: "mysql",
        profile: "prod",
        access: "private",
      }),
    );
  });

  it("offers MySQL, PostgreSQL and MongoDB and picks MySQL first", () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    expect(screen.getByRole("radio", { name: /MySQL/ })).toBeChecked();
    expect(screen.getByRole("radio", { name: /PostgreSQL/ })).not.toBeChecked();
    expect(screen.getByRole("radio", { name: /MongoDB/ })).not.toBeChecked();
    expect(screen.getByText("Not sure which to pick?")).toBeInTheDocument();
  });

  it("keeps the advanced overrides collapsed until they are asked for", async () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    const advanced = screen.getByRole("button", { name: "Advanced" });
    expect(advanced).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByLabelText("CPU cores")).not.toBeInTheDocument();

    await userEvent.click(advanced);

    expect(advanced).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByLabelText("CPU cores")).toBeInTheDocument();
    expect(screen.getByLabelText("Memory in GB")).toBeInTheDocument();
    expect(screen.getByLabelText("Storage in GB")).toBeInTheDocument();
  });

  // The adapter manages one namespace and rejects every other value, so a
  // namespace input could only ever succeed with a name the browser cannot
  // know. The overrides are CPU, memory, and storage.
  it("offers no namespace override", async () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    await userEvent.click(screen.getByRole("button", { name: "Advanced" }));

    expect(screen.queryByLabelText("Namespace")).not.toBeInTheDocument();
  });

  it("shows the selected profile's effective sizing in Advanced", async () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    await userEvent.click(screen.getByRole("button", { name: "Advanced" }));

    expect(screen.getByText("Dev uses 1 core. Allowed 1 to 64.")).toBeInTheDocument();
    expect(screen.getByText("Dev uses 2 GB. Allowed 1 to 512.")).toBeInTheDocument();
    expect(screen.getByText("Dev uses 20 GB. Allowed 10 to 16000.")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: "Prod" }));

    expect(screen.getByText("Prod uses 4 cores. Allowed 1 to 64.")).toBeInTheDocument();
    expect(screen.getByText("Prod uses 16 GB. Allowed 1 to 512.")).toBeInTheDocument();
    expect(screen.getByText("Prod uses 250 GB. Allowed 10 to 16000.")).toBeInTheDocument();
  });

  it("submits the advanced overrides the user set", async () => {
    const onSubmit = vi.fn();
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={onSubmit} />);

    await userEvent.type(nameField(), "orders-dev");
    await userEvent.click(screen.getByRole("button", { name: "Advanced" }));
    await userEvent.type(screen.getByLabelText("CPU cores"), "4");
    await userEvent.type(screen.getByLabelText("Storage in GB"), "50");

    await waitFor(() => expect(submitButton()).toBeEnabled());
    await userEvent.click(submitButton());

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        name: "orders-dev",
        environment: "spoke-a",
        engine: "mysql",
        profile: "dev",
        access: "private",
        cpu: 4,
        storage: 50,
      }),
    );
  });

  it("states the bounds when an override is out of range", async () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    await userEvent.type(nameField(), "orders-dev");
    await userEvent.click(screen.getByRole("button", { name: "Advanced" }));
    await userEvent.type(screen.getByLabelText("CPU cores"), "128");
    await userEvent.tab();

    expect(await screen.findByText("CPU must be between 1 and 64 cores.")).toBeInTheDocument();
    await waitFor(() => expect(submitButton()).toBeDisabled());
  });

  it("keeps the label and shows a spinner while the create is in flight", () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} submitting />);

    const submit = screen.getByRole("button", { name: "Creating..." });
    expect(submit).toBeDisabled();
    expect(submit).toHaveAttribute("aria-busy", "true");
  });

  it("shows the adapter message when the create is rejected", () => {
    renderWithProviders(
      <CreateDatabaseForm
        environment="spoke-a"
        onSubmit={vi.fn()}
        error="A database named `orders-dev` already exists."
      />,
    );

    expect(screen.getByText("A database named `orders-dev` already exists.")).toBeInTheDocument();
  });

  it("describes the engines", () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    expect(screen.getByText("The usual choice for existing MySQL apps.")).toBeInTheDocument();
    expect(screen.getByText("The usual choice for existing PostgreSQL apps.")).toBeInTheDocument();
    expect(
      screen.getByText(
        "Percona Database Service can run any of these engines. Match the one your application already uses.",
      ),
    ).toBeInTheDocument();
  });

  it("submits PostgreSQL with private access by default", async () => {
    const onSubmit = vi.fn();
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={onSubmit} />);

    await userEvent.click(screen.getByRole("radio", { name: /PostgreSQL/ }));
    await userEvent.type(nameField(), "reports");
    await waitFor(() => expect(submitButton()).toBeEnabled());
    await userEvent.click(submitButton());

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        name: "reports",
        environment: "spoke-a",
        engine: "postgresql",
        profile: "dev",
        access: "private",
      }),
    );
  });

  it("refuses a PostgreSQL name the adapter would refuse, and keeps it for MySQL", async () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    await userEvent.click(screen.getByRole("radio", { name: /PostgreSQL/ }));
    await userEvent.type(nameField(), "a".repeat(38));
    await userEvent.tab();

    expect(await screen.findByText(postgresqlNameError)).toBeInTheDocument();
    expect(submitButton()).toBeDisabled();

    await userEvent.click(screen.getByRole("radio", { name: /MySQL/ }));
    await waitFor(() => expect(submitButton()).toBeEnabled());
    await waitFor(() => expect(screen.queryByText(postgresqlNameError)).not.toBeInTheDocument());
    expect(nameField()).not.toHaveAttribute("aria-invalid", "true");
  });

  it("checks a name typed for MySQL again when PostgreSQL is picked", async () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    await userEvent.type(nameField(), "a".repeat(38));
    await userEvent.tab();
    await waitFor(() => expect(submitButton()).toBeEnabled());

    await userEvent.click(screen.getByRole("radio", { name: /PostgreSQL/ }));

    expect(await screen.findByText(postgresqlNameError)).toBeInTheDocument();
    expect(nameField()).toHaveAttribute("aria-invalid", "true");
    expect(submitButton()).toBeDisabled();
  });

  it("does not report a name left empty when only the engine changes", async () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    await userEvent.click(screen.getByRole("radio", { name: /PostgreSQL/ }));
    await userEvent.click(screen.getByRole("radio", { name: /MySQL/ }));

    expect(screen.getByRole("radio", { name: /MySQL/ })).toBeChecked();
    expect(screen.queryByText("Enter a name.")).not.toBeInTheDocument();
    expect(nameField()).not.toHaveAttribute("aria-invalid", "true");
  });

  it("sends the public access choice and its allowed addresses for PostgreSQL", async () => {
    const onSubmit = vi.fn();
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={onSubmit} />);

    await userEvent.click(screen.getByRole("radio", { name: /PostgreSQL/ }));
    await userEvent.type(nameField(), "reports");
    await userEvent.click(accessOption("Public"));
    await userEvent.clear(screen.getByLabelText("Allowed addresses"));
    await userEvent.type(screen.getByLabelText("Allowed addresses"), "203.0.113.7");
    await waitFor(() => expect(submitButton()).toBeEnabled());
    await userEvent.click(submitButton());

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        name: "reports",
        environment: "spoke-a",
        engine: "postgresql",
        profile: "dev",
        access: "public",
        sourceRanges: ["203.0.113.7"],
      }),
    );
  });

  describe("access", () => {
    it("is a private or public switch with no checkbox", () => {
      renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

      const group = screen.getByRole("radiogroup", { name: "Access" });
      expect(group).toContainElement(accessOption("Private"));
      expect(group).toContainElement(accessOption("Public"));
      expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    });

    it("defaults to private and sends it", async () => {
      const onSubmit = vi.fn();
      renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={onSubmit} />);

      expect(accessOption("Private")).toBeChecked();
      expect(accessOption("Public")).not.toBeChecked();
      expect(screen.queryByLabelText("Allowed addresses")).not.toBeInTheDocument();

      await userEvent.type(nameField(), "orders-dev");
      await waitFor(() => expect(submitButton()).toBeEnabled());
      await userEvent.click(submitButton());

      await waitFor(() =>
        expect(onSubmit).toHaveBeenCalledWith({
          name: "orders-dev",
          environment: "spoke-a",
          engine: "mysql",
          profile: "dev",
          access: "private",
        }),
      );
    });

    it("states the cost and where each choice is reachable from", async () => {
      renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

      expect(
        screen.getByText(
          "Adds a load balancer in your cloud account, billed by your cloud provider.",
        ),
      ).toBeInTheDocument();
      expect(
        screen.getByText("Reachable from your VPC and the networks connected to it."),
      ).toBeInTheDocument();

      await userEvent.click(accessOption("Public"));

      expect(screen.getByText("Reachable from the internet.")).toBeInTheDocument();
    });

    it("submits the allowed addresses of a public database", async () => {
      const onSubmit = vi.fn();
      renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={onSubmit} />);

      await userEvent.type(nameField(), "orders-dev");
      await userEvent.click(accessOption("Public"));
      const addresses = screen.getByLabelText("Allowed addresses");
      expect(addresses).toHaveValue("0.0.0.0/0");
      await userEvent.clear(addresses);
      await userEvent.type(addresses, "203.0.113.7, 10.0.0.0/8");

      await waitFor(() => expect(submitButton()).toBeEnabled());
      await userEvent.click(submitButton());

      await waitFor(() =>
        expect(onSubmit).toHaveBeenCalledWith({
          name: "orders-dev",
          environment: "spoke-a",
          engine: "mysql",
          profile: "dev",
          access: "public",
          sourceRanges: ["203.0.113.7", "10.0.0.0/8"],
        }),
      );
    });

    it("defaults a public database to every address and refuses a blank list", async () => {
      const onSubmit = vi.fn();
      renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={onSubmit} />);

      await userEvent.type(nameField(), "orders-dev");
      await userEvent.click(accessOption("Public"));

      expect(screen.getByLabelText("Allowed addresses")).toHaveValue("0.0.0.0/0");
      expect(
        screen.getByText(
          "Any address on the internet can connect. Replace 0.0.0.0/0 to limit who can.",
        ),
      ).toBeInTheDocument();

      await waitFor(() => expect(submitButton()).toBeEnabled());
      await userEvent.click(submitButton());

      await waitFor(() =>
        expect(onSubmit).toHaveBeenCalledWith({
          name: "orders-dev",
          environment: "spoke-a",
          engine: "mysql",
          profile: "dev",
          access: "public",
          sourceRanges: ["0.0.0.0/0"],
        }),
      );

      await userEvent.clear(screen.getByLabelText("Allowed addresses"));
      await userEvent.tab();

      expect(screen.getByText("Enter at least one address or range.")).toBeInTheDocument();
      expect(submitButton()).toBeDisabled();
    });

    it("names the entry that is not an address and keeps submit disabled", async () => {
      renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

      await userEvent.type(nameField(), "orders-dev");
      await userEvent.click(accessOption("Public"));
      const addresses = screen.getByLabelText("Allowed addresses");
      await userEvent.clear(addresses);
      await userEvent.type(addresses, "10.0.0.0/8, office");
      await userEvent.tab();

      expect(
        await screen.findByText("`office` is not an IP address or CIDR range."),
      ).toBeInTheDocument();
      await waitFor(() => expect(submitButton()).toBeDisabled());
      expect(
        screen.queryByText(
          "Any address on the internet can connect. Replace 0.0.0.0/0 to limit who can.",
        ),
      ).not.toBeInTheDocument();
    });

    it("names a bad entry even while another field is still wrong", async () => {
      renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

      await userEvent.click(accessOption("Public"));
      const addresses = screen.getByLabelText("Allowed addresses");
      await userEvent.clear(addresses);
      await userEvent.type(addresses, "office");
      await userEvent.tab();

      expect(
        await screen.findByText("`office` is not an IP address or CIDR range."),
      ).toBeInTheDocument();
    });

    it("forgets the allow list when switched back to private", async () => {
      const onSubmit = vi.fn();
      renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={onSubmit} />);

      await userEvent.type(nameField(), "orders-dev");
      await userEvent.click(accessOption("Public"));
      await userEvent.type(screen.getByLabelText("Allowed addresses"), "office");
      await userEvent.click(accessOption("Private"));

      expect(screen.queryByLabelText("Allowed addresses")).not.toBeInTheDocument();
      await waitFor(() => expect(submitButton()).toBeEnabled());
      await userEvent.click(submitButton());

      await waitFor(() =>
        expect(onSubmit).toHaveBeenCalledWith({
          name: "orders-dev",
          environment: "spoke-a",
          engine: "mysql",
          profile: "dev",
          access: "private",
        }),
      );
    });

    it("moves between the choices with the arrow keys", async () => {
      renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

      accessOption("Private").focus();
      await userEvent.keyboard("{ArrowRight}");

      expect(accessOption("Public")).toBeChecked();
    });
  });
});
