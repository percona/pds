import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { CreateDatabaseForm } from "@/features/databases";
import { renderWithProviders } from "@/test/utils";

const mongodbNameError =
  "Use 22 characters or fewer for MongoDB. Its members' object names are built from it.";

function submitButton() {
  return screen.getByRole("button", { name: "Create database" });
}

describe("CreateDatabaseForm with MongoDB offered", () => {
  it("hides the Access choice for MongoDB and shows it again for MySQL", async () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);
    expect(screen.getByRole("radiogroup", { name: "Access" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: /MongoDB/ }));
    expect(screen.queryByRole("radiogroup", { name: "Access" })).not.toBeInTheDocument();
    expect(screen.queryByText("Allowed addresses")).not.toBeInTheDocument();
    expect(
      screen.getByText(
        "Access from outside the Kubernetes cluster is not available for MongoDB yet.",
      ),
    ).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: /MySQL/ }));
    expect(screen.getByRole("radiogroup", { name: "Access" })).toBeInTheDocument();
  });

  it("sends no access for MongoDB", async () => {
    const onSubmit = vi.fn();
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={onSubmit} />);

    await userEvent.click(screen.getByRole("radio", { name: /MongoDB/ }));
    await userEvent.type(screen.getByLabelText("Name"), "orders");
    await waitFor(() => expect(submitButton()).toBeEnabled());
    await userEvent.click(submitButton());

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith({
        name: "orders",
        environment: "spoke-a",
        engine: "mongodb",
        profile: "dev",
      }),
    );
  });

  it("ignores a public choice made for another engine", async () => {
    const onSubmit = vi.fn();
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={onSubmit} />);

    await userEvent.click(screen.getByRole("radio", { name: "Public" }));
    await userEvent.click(screen.getByRole("radio", { name: /MongoDB/ }));
    await userEvent.type(screen.getByLabelText("Name"), "orders");
    await userEvent.clear(screen.getByLabelText("Name"));
    await userEvent.type(screen.getByLabelText("Name"), "orders");
    await waitFor(() => expect(submitButton()).toBeEnabled());
    await userEvent.click(submitButton());

    await waitFor(() => expect(onSubmit).toHaveBeenCalledTimes(1));
    expect(onSubmit.mock.calls[0]?.[0]).not.toHaveProperty("access");
    expect(onSubmit.mock.calls[0]?.[0]).not.toHaveProperty("sourceRanges");
  });

  it("refuses a MongoDB name of 23 characters and takes 22", async () => {
    renderWithProviders(<CreateDatabaseForm environment="spoke-a" onSubmit={vi.fn()} />);

    await userEvent.click(screen.getByRole("radio", { name: /MongoDB/ }));
    await userEvent.type(screen.getByLabelText("Name"), "a".repeat(23));
    await userEvent.tab();

    expect(await screen.findByText(mongodbNameError)).toBeInTheDocument();
    expect(submitButton()).toBeDisabled();

    await userEvent.clear(screen.getByLabelText("Name"));
    await userEvent.type(screen.getByLabelText("Name"), "a".repeat(22));
    await waitFor(() => expect(submitButton()).toBeEnabled());
  });
});
