import { screen } from "@testing-library/react";
import { useQuery } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { Link } from "react-router";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

function Probe() {
  const { data } = useQuery({ queryKey: ["probe"], queryFn: () => "query ready" });
  return (
    <div>
      <span>{data ?? "loading"}</span>
      <Link to="/databases">Databases</Link>
    </div>
  );
}

describe("renderWithProviders", () => {
  it("supplies a query client and a router", async () => {
    renderWithProviders(<Probe />);

    expect(screen.getByRole("link", { name: "Databases" })).toHaveAttribute("href", "/databases");
    expect(await screen.findByText("query ready")).toBeInTheDocument();
  });
});

describe("createMockDatabase", () => {
  it("returns a database shaped the way the adapter returns one", () => {
    expect(createMockDatabase()).toEqual({
      id: "pds.test-db",
      name: "test-db",
      engine: "mysql",
      profile: "dev",
      namespace: "pds",
      status: "ready",
      members: 1,
      cpu: 1,
      memoryGb: 2,
      storageGb: 20,
      environment: "spoke-a",
    });
  });

  it("applies overrides", () => {
    expect(createMockDatabase({ name: "orders-prod", status: "provisioning" })).toMatchObject({
      name: "orders-prod",
      status: "provisioning",
    });
  });
});
