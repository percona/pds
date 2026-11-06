import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConnectCard } from "@/features/databases";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

const connected = createMockDatabase({
  name: "orders-dev",
  status: "ready",
  connection: {
    host: "orders-dev.pds.svc",
    port: 3306,
    username: "app",
    maskedUri: "mysql://app:••••••••@orders-dev.pds.svc:3306/orders-dev",
    secretName: "orders-dev-app-credentials",
    serviceName: "orders-dev",
  },
});

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
});

const publicDatabase = createMockDatabase({
  id: "spoke-a/pds.orders-ext",
  name: "orders-ext",
  access: { external: true, address: "198.51.100.20" },
  connection: {
    host: "198.51.100.20",
    port: 3306,
    username: "root",
  },
});

describe("ConnectCard", () => {
  it("shows the host, port, user, and connection string", () => {
    renderWithProviders(<ConnectCard database={connected} onConnect={() => {}} />);

    expect(screen.getByText("orders-dev.pds.svc")).toBeInTheDocument();
    expect(screen.getByText("3306")).toBeInTheDocument();
    expect(screen.getByText("app")).toBeInTheDocument();
    expect(
      screen.getByText("mysql://app:••••••••@orders-dev.pds.svc:3306/orders-dev"),
    ).toBeInTheDocument();
  });

  it("never puts the password on screen", () => {
    const { container } = renderWithProviders(
      <ConnectCard database={connected} onConnect={() => {}} />,
    );

    expect(container.textContent).not.toContain("secret");
  });

  // Every copy happens in the dialog, so the card offers the way in and
  // nothing else. Two copy paths would be two places a credential can leave.
  it("hands off to the dialog rather than copying anything itself", async () => {
    const onConnect = vi.fn();

    renderWithProviders(<ConnectCard database={connected} onConnect={onConnect} />);

    expect(
      screen.queryByRole("button", { name: "Copy connection string" }),
    ).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Connect" }));

    expect(onConnect).toHaveBeenCalledTimes(1);
  });

  it("states the shape without a string the hub does not have", () => {
    const onHub = createMockDatabase({
      environment: "spoke-a",
      connection: { host: "orders-dev.pds.svc", port: 3306, username: "app" },
    });

    renderWithProviders(<ConnectCard database={onHub} onConnect={() => {}} />);

    expect(screen.getByText("orders-dev.pds.svc")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect" })).toBeInTheDocument();
  });

  it("says why there is nothing to connect to yet instead of showing a blank card", () => {
    renderWithProviders(
      <ConnectCard
        database={createMockDatabase({ name: "orders-dev", status: "provisioning" })}
        onConnect={() => {}}
      />,
    );

    expect(
      screen.getByText(
        "orders-dev is still coming up. Connection details appear once it is ready.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Connect" })).not.toBeInTheDocument();
  });

  describe("reachability", () => {
    it("states an internal database is reachable inside the cluster only", () => {
      renderWithProviders(<ConnectCard database={connected} onConnect={() => {}} />);

      expect(screen.getByText("Reachable inside the Kubernetes cluster only")).toBeInTheDocument();
      expect(screen.getByTestId("reachability-icon-internal")).toBeInTheDocument();
    });

    it("states reachability while the database is still coming up", () => {
      renderWithProviders(
        <ConnectCard
          database={createMockDatabase({ status: "provisioning", access: { external: false } })}
          onConnect={() => {}}
        />,
      );

      expect(screen.getByText("Reachable inside the Kubernetes cluster only")).toBeInTheDocument();
    });

    it("shows the external address and the allow list", () => {
      const external = createMockDatabase({
        access: {
          external: true,
          sourceRanges: ["10.0.0.0/8", "203.0.113.7/32"],
          address: "198.51.100.20",
        },
        connection: { host: "198.51.100.20", port: 3306, username: "app" },
      });

      renderWithProviders(<ConnectCard database={external} onConnect={() => {}} />);

      expect(screen.getByText("Public, reachable from the internet")).toBeInTheDocument();
      expect(screen.getByTestId("reachability-icon-public")).toBeInTheDocument();
      expect(screen.getByText("198.51.100.20")).toBeInTheDocument();
      expect(screen.getByText("mysql://app:••••••••@198.51.100.20:3306")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Copy connection string" })).toBeInTheDocument();
      expect(screen.getByText("10.0.0.0/8, 203.0.113.7/32")).toHaveClass("font-mono");
    });

    it("warns when an external database accepts any address", () => {
      const open = createMockDatabase({
        access: { external: true, address: "198.51.100.20" },
        connection: { host: "198.51.100.20", port: 3306, username: "app" },
      });

      renderWithProviders(<ConnectCard database={open} onConnect={() => {}} />);

      expect(screen.getByText("Any address on the internet can connect.")).toBeInTheDocument();
    });

    it("says the address is pending instead of showing the in-cluster host", () => {
      const pending = createMockDatabase({
        access: { external: true, sourceRanges: ["10.0.0.0/8"] },
        connection: {
          host: "orders-dev-pxc.pds.svc",
          port: 3306,
          username: "app",
          maskedUri: "mysql://app:••••••••@orders-dev-pxc.pds.svc:3306/",
        },
      });

      renderWithProviders(<ConnectCard database={pending} onConnect={() => {}} />);

      expect(screen.getByText("Public address pending")).toBeInTheDocument();
      expect(screen.getByTestId("reachability-icon-pending")).toBeInTheDocument();
      expect(
        screen.getByText(
          "The host appears here once the load balancer has an address. Until then, connect from inside the Kubernetes cluster.",
        ),
      ).toBeInTheDocument();
      expect(screen.queryByText(/orders-dev-pxc\.pds\.svc/)).not.toBeInTheDocument();
      expect(screen.queryByText("Connection string")).not.toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Copy connection string" }),
      ).not.toBeInTheDocument();
      expect(screen.getByText("Pending")).toBeInTheDocument();
    });

    it("says the address is pending before any connection is published", () => {
      renderWithProviders(
        <ConnectCard
          database={createMockDatabase({ status: "provisioning", access: { external: true } })}
          onConnect={() => {}}
        />,
      );

      expect(screen.getByText("Public address pending")).toBeInTheDocument();
    });

    it("states a private database is reachable from the VPC, with no allow list", () => {
      const internalLb = createMockDatabase({
        access: {
          external: true,
          visibility: "private",
          address: "internal-a1b2.elb.amazonaws.com",
        },
        connection: { host: "internal-a1b2.elb.amazonaws.com", port: 3306, username: "app" },
      });

      renderWithProviders(<ConnectCard database={internalLb} onConnect={() => {}} />);

      expect(screen.getByText("Private, reachable from your VPC")).toBeInTheDocument();
      expect(screen.getByTestId("reachability-icon-private")).toBeInTheDocument();
      expect(screen.getByText("internal-a1b2.elb.amazonaws.com")).toBeInTheDocument();
      expect(
        screen.getByText("mysql://app:••••••••@internal-a1b2.elb.amazonaws.com:3306"),
      ).toBeInTheDocument();
      expect(
        screen.getByText(
          "The password stays on the Kubernetes cluster this database runs on. Connect shows how to read it there.",
        ),
      ).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Copy connection string" }),
      ).not.toBeInTheDocument();
      expect(screen.queryByText(/Allowed from/)).not.toBeInTheDocument();
      expect(
        screen.queryByText("Any address on the internet can connect."),
      ).not.toBeInTheDocument();
    });

    it("shows a hub's private database masked, with copy left to the connect dialog", () => {
      const onHub = createMockDatabase({
        environment: "spoke-a",
        access: { external: true, visibility: "private", address: "10.0.4.12" },
        connection: { host: "10.0.4.12", port: 3306, username: "app" },
      });

      renderWithProviders(<ConnectCard database={onHub} onConnect={() => {}} />);

      expect(screen.getByText("Private, reachable from your VPC")).toBeInTheDocument();
      expect(screen.getByText("mysql://app:••••••••@10.0.4.12:3306")).toBeInTheDocument();
      expect(
        screen.getByText(
          "The password stays on the Kubernetes cluster this database runs on. Connect shows how to read it there.",
        ),
      ).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Copy connection string" }),
      ).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Connect" })).toBeInTheDocument();
    });

    it("says a private address is pending", () => {
      renderWithProviders(
        <ConnectCard
          database={createMockDatabase({
            status: "provisioning",
            access: { external: true, visibility: "private" },
          })}
          onConnect={() => {}}
        />,
      );

      expect(screen.getByText("Private address pending")).toBeInTheDocument();
      expect(screen.getByTestId("reachability-icon-pending")).toBeInTheDocument();
    });

    it("copies the real connection string and leaves the password off the page", async () => {
      const writeText = vi.fn().mockResolvedValue(undefined);
      vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify({ uri: "mysql://root:s3cr3t@198.51.100.20:3306" }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        ),
      );

      const { container } = renderWithProviders(
        <ConnectCard database={publicDatabase} onConnect={() => {}} />,
      );

      expect(container.textContent).not.toContain("s3cr3t");
      await userEvent.click(screen.getByRole("button", { name: "Copy connection string" }));

      await waitFor(() =>
        expect(writeText).toHaveBeenCalledWith("mysql://root:s3cr3t@198.51.100.20:3306"),
      );
      expect(container.textContent).not.toContain("s3cr3t");
      expect(toast.success).toHaveBeenCalledWith("Connection string copied");
    });

    it("copies the decoded password and leaves it off the page", async () => {
      const writeText = vi.fn().mockResolvedValue(undefined);
      vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
      vi.stubGlobal(
        "fetch",
        vi.fn().mockResolvedValue(
          new Response(
            JSON.stringify({
              uri: "mysql://root:a%3Eb%23c%7Dd%2Ae%3Ff%21@198.51.100.20:3306/?tls=false",
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        ),
      );

      const { container } = renderWithProviders(
        <ConnectCard database={publicDatabase} onConnect={() => {}} />,
      );

      await userEvent.click(screen.getByRole("button", { name: "Copy password" }));

      await waitFor(() => expect(writeText).toHaveBeenCalledWith("a>b#c}d*e?f!"));
      expect(container.textContent).not.toContain("a>b#c}d*e?f!");
      expect(toast.success).toHaveBeenCalledWith("Password copied");
    });
  });
});
