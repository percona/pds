import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { ProfilePicker } from "@/features/profiles";
import type { ProfileId } from "@/features/profiles";

function Harness({ onChange }: { onChange?: (id: ProfileId) => void }) {
  const [value, setValue] = useState<ProfileId>("dev");

  return (
    <ProfilePicker
      value={value}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
    />
  );
}

describe("ProfilePicker", () => {
  it("renders one card per profile with its consequence line", () => {
    render(<Harness />);

    expect(screen.getByRole("radio", { name: "Dev" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Staging" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Prod" })).toBeInTheDocument();
    expect(screen.getByText("1 member, on-demand backups")).toBeInTheDocument();
    expect(screen.getAllByText("3 members, HA, daily backups")).toHaveLength(2);
  });

  it("starts on the profile it is given", () => {
    render(<Harness />);

    expect(screen.getByRole("radio", { name: "Dev" })).toBeChecked();
    expect(screen.getByRole("radio", { name: "Prod" })).not.toBeChecked();
  });

  it("reports the profile the user picks", async () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);

    await userEvent.click(screen.getByRole("radio", { name: "Prod" }));

    expect(onChange).toHaveBeenCalledWith("prod");
    expect(screen.getByRole("radio", { name: "Prod" })).toBeChecked();
    expect(screen.getByRole("radio", { name: "Dev" })).not.toBeChecked();
  });

  it("shows the sizing of each profile as text", () => {
    render(<Harness />);

    expect(screen.getByTestId("profile-card-dev")).toHaveTextContent("1 core");
    expect(screen.getByTestId("profile-card-dev")).toHaveTextContent("2 GB memory");
    expect(screen.getByTestId("profile-card-dev")).toHaveTextContent("20 GB storage");
    expect(screen.getByTestId("profile-card-staging")).toHaveTextContent("2 cores");
    expect(screen.getByTestId("profile-card-staging")).toHaveTextContent("8 GB memory");
    expect(screen.getByTestId("profile-card-staging")).toHaveTextContent("100 GB storage");
    expect(screen.getByTestId("profile-card-prod")).toHaveTextContent("4 cores");
    expect(screen.getByTestId("profile-card-prod")).toHaveTextContent("16 GB memory");
    expect(screen.getByTestId("profile-card-prod")).toHaveTextContent("250 GB storage");
  });
});
