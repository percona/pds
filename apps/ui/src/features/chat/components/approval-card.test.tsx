import { act, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/utils";
import type { ChatApproval } from "../types";
import { ApprovalCard } from "./approval-card";

const scale: ChatApproval = {
  id: "a1",
  tool: "scale_database",
  title: "Scale a database",
  database: "orders-dev",
  destructive: false,
  lines: [
    { label: "Database", value: "spoke-a/pds.orders-dev" },
    { label: "CPU cores", value: "2" },
    { label: "Memory (GB)", value: "8" },
  ],
  expiresAt: "2099-01-01T00:00:00Z",
};

const remove: ChatApproval = {
  id: "a2",
  tool: "delete_database",
  title: "Delete a database",
  database: "orders-dev",
  destructive: true,
  lines: [{ label: "Database", value: "spoke-a/pds.orders-dev" }],
  expiresAt: "2099-01-01T00:00:00Z",
};

afterEach(() => {
  vi.useRealTimers();
});

describe("ApprovalCard", () => {
  it("shows the change and every argument it will be made with", () => {
    renderWithProviders(<ApprovalCard approval={scale} status="open" onDecide={vi.fn()} />);

    expect(screen.getByText("Scale a database")).toBeInTheDocument();
    expect(screen.getByText("Database")).toBeInTheDocument();
    expect(screen.getByText("spoke-a/pds.orders-dev")).toBeInTheDocument();
    expect(screen.getByText("CPU cores")).toBeInTheDocument();
    expect(screen.getByText("Memory (GB)")).toBeInTheDocument();
    expect(screen.getByText("8")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Reject" })).toBeEnabled();
    expect(screen.queryByLabelText(/Type orders-dev/)).not.toBeInTheDocument();
  });

  it("sends the answer the person gives", async () => {
    const onDecide = vi.fn().mockResolvedValue(undefined);
    renderWithProviders(<ApprovalCard approval={scale} status="open" onDecide={onDecide} />);

    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(onDecide).toHaveBeenLastCalledWith({ approve: true });

    await userEvent.click(screen.getByRole("button", { name: "Reject" }));
    expect(onDecide).toHaveBeenLastCalledWith({ approve: false });
  });

  it("keeps approve disabled for a delete until the database name is typed", async () => {
    const onDecide = vi.fn().mockResolvedValue(undefined);
    renderWithProviders(<ApprovalCard approval={remove} status="open" onDecide={onDecide} />);

    const approve = screen.getByRole("button", { name: "Approve" });
    const name = screen.getByLabelText("Type orders-dev to confirm");
    expect(approve).toBeDisabled();

    await userEvent.type(name, "orders");
    expect(approve).toBeDisabled();
    await userEvent.type(name, "-dev");
    expect(approve).toBeEnabled();

    await userEvent.click(approve);
    expect(onDecide).toHaveBeenCalledWith({ approve: true, name: "orders-dev" });
  });

  it("lets a delete be rejected without the name", async () => {
    const onDecide = vi.fn().mockResolvedValue(undefined);
    renderWithProviders(<ApprovalCard approval={remove} status="open" onDecide={onDecide} />);

    await userEvent.click(screen.getByRole("button", { name: "Reject" }));

    expect(onDecide).toHaveBeenCalledWith({ approve: false });
  });

  it("says what went wrong when the answer is refused, and stays open", async () => {
    const onDecide = vi.fn().mockRejectedValue(new Error("That approval expired."));
    renderWithProviders(<ApprovalCard approval={scale} status="open" onDecide={onDecide} />);

    await userEvent.click(screen.getByRole("button", { name: "Approve" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("That approval expired.");
    expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();
  });

  it("shows an expired approval as expired, with nothing to press", () => {
    renderWithProviders(<ApprovalCard approval={scale} status="expired" onDecide={vi.fn()} />);

    expect(screen.getByText("This approval expired. Nothing changed.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reject" })).not.toBeInTheDocument();
  });

  it("expires on its own when the time runs out", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-10-28T09:00:00Z"));
    const soon: ChatApproval = { ...scale, expiresAt: "2026-10-28T09:15:00Z" };
    renderWithProviders(<ApprovalCard approval={soon} status="open" onDecide={vi.fn()} />);

    expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();
    act(() => {
      vi.advanceTimersByTime(15 * 60 * 1000);
    });

    expect(screen.getByText("This approval expired. Nothing changed.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it.each([
    ["approved", "Approved. The change is being made."],
    ["rejected", "Rejected. Nothing changed."],
    ["cancelled", "Cancelled. Nothing changed."],
  ] as const)("shows an answered approval as %s", (status, text) => {
    renderWithProviders(<ApprovalCard approval={scale} status={status} onDecide={vi.fn()} />);

    expect(screen.getByText(text)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });
});
