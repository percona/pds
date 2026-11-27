import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConnectSection } from "@/features/databases";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

function stubCredential(uri: string | null) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    if (String(input).endsWith("/credential") && uri !== null) {
      return Promise.resolve(
        new Response(JSON.stringify({ uri }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    }

    return Promise.resolve(
      new Response(JSON.stringify({ code: "ErrNotFound", message: "No such thing." }), {
        status: 404,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

function stubClipboard() {
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });

  return writeText;
}

/** A database inside the Kubernetes cluster, as the control plane lists it. */
const internal = createMockDatabase({
  id: "spoke-a/pds.orders-dev",
  environment: "spoke-a",
  name: "orders-dev",
  namespace: "pds",
  status: "ready",
  connection: {
    host: "orders-dev-haproxy.pds",
    port: 3306,
    username: "app",
    secretName: "orders-dev-conn",
    serviceName: "orders-dev-haproxy",
  },
});

const publicDatabase = createMockDatabase({
  ...internal,
  access: { external: true, visibility: "public", address: "198.51.100.20" },
  connection: { host: "198.51.100.20", port: 3306, username: "root" },
});

const privateDatabase = createMockDatabase({
  ...internal,
  access: { external: true, visibility: "private", address: "10.0.4.12" },
  connection: { host: "10.0.4.12", port: 3306, username: "root", secretName: "orders-dev-conn" },
});

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.mocked(toast.error).mockClear();
  vi.mocked(toast.success).mockClear();
});

describe("ConnectSection", () => {
  it("splits connecting by where it starts, laptop first", () => {
    renderWithProviders(<ConnectSection database={internal} />);

    const choices = within(screen.getByRole("group", { name: "Connect from" }))
      .getAllByRole("button")
      .map((choice) => choice.textContent);
    expect(choices).toEqual(["From your laptop", "From your app", "From an AI agent"]);
    expect(screen.getByRole("button", { name: "From your laptop" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("leaves the agent out when the AI agents surface is off", () => {
    renderWithProviders(<ConnectSection database={internal} />, { flags: { mcp: false } });

    expect(screen.queryByRole("button", { name: "From an AI agent" })).not.toBeInTheDocument();
  });

  it("says why there is nothing to connect to yet instead of showing the choices", () => {
    renderWithProviders(
      <ConnectSection
        database={createMockDatabase({ name: "orders-dev", status: "provisioning" })}
      />,
    );

    expect(
      screen.getByText(
        "orders-dev is still coming up. Connection details appear once it is ready.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Connect from" })).not.toBeInTheDocument();
  });

  describe("from your laptop", () => {
    it("names the environment and gives every command to a client prompt", () => {
      renderWithProviders(<ConnectSection database={internal} />);

      expect(
        screen.getByText(
          "This database runs on spoke-a. No password is held here. These four commands are the way in. They run against that Kubernetes cluster and end at a prompt you can type in.",
        ),
      ).toBeInTheDocument();
      expect(screen.getByText("kubectl config use-context 'spoke-a'")).toBeInTheDocument();
      expect(screen.getByText(/kubectl -n 'pds' get secret 'orders-dev-conn'/)).toBeInTheDocument();
      expect(
        screen.getByText("kubectl -n 'pds' port-forward 'svc/orders-dev-haproxy' '3306:3306'"),
      ).toBeInTheDocument();
      expect(screen.getByText("4. Connect")).toBeInTheDocument();
      expect(screen.getByText("mysql -h 127.0.0.1 -P '3306' -u 'app' -p")).toBeInTheDocument();
    });

    it("still gives all four steps when no Secret or Service was reported", () => {
      const sparse = createMockDatabase({
        ...internal,
        connection: { ...internal.connection!, secretName: undefined, serviceName: undefined },
      });

      renderWithProviders(<ConnectSection database={sparse} />);

      expect(screen.getByText(/get instances 'orders-dev'/)).toBeInTheDocument();
      expect(
        screen.getByText("kubectl -n 'pds' port-forward 'svc/orders-dev-haproxy' '3306:3306'"),
      ).toBeInTheDocument();
      expect(screen.getByText("4. Connect")).toBeInTheDocument();
    });

    it("copies a command without asking for a credential", async () => {
      const writeText = stubClipboard();
      const fetchMock = stubCredential(null);

      renderWithProviders(<ConnectSection database={internal} />);
      await userEvent.click(screen.getByRole("button", { name: "Copy step 2" }));

      await waitFor(() =>
        expect(writeText).toHaveBeenCalledWith(
          expect.stringContaining("get secret 'orders-dev-conn'"),
        ),
      );
      expect(toast.success).toHaveBeenCalledWith("Command copied");
      expect(fetchMock).not.toHaveBeenCalled();
    });

    it("copies a public connection string and password, which stay off the page", async () => {
      const writeText = stubClipboard();
      const fetchMock = stubCredential("mysql://root:s3cr3t@198.51.100.20:3306");

      const { container } = renderWithProviders(<ConnectSection database={publicDatabase} />);

      expect(screen.getByText("mysql://root:••••••••@198.51.100.20:3306")).toBeInTheDocument();
      expect(screen.getByText("3. Connect")).toBeInTheDocument();
      expect(container.textContent).not.toContain("s3cr3t");

      await userEvent.click(screen.getByRole("button", { name: "Copy connection string" }));
      await waitFor(() =>
        expect(writeText).toHaveBeenCalledWith("mysql://root:s3cr3t@198.51.100.20:3306"),
      );
      expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/credential");

      writeText.mockClear();
      await userEvent.click(screen.getByRole("button", { name: "Copy password" }));
      await waitFor(() => expect(writeText).toHaveBeenCalledWith("s3cr3t"));
      expect(container.textContent).not.toContain("s3cr3t");
    });

    it("copies the decoded password", async () => {
      const writeText = stubClipboard();
      stubCredential("mysql://root:a%3Eb%23c%7Dd%2Ae%3Ff%21@198.51.100.20:3306/?tls=false");

      const { container } = renderWithProviders(<ConnectSection database={publicDatabase} />);
      await userEvent.click(screen.getByRole("button", { name: "Copy password" }));

      await waitFor(() => expect(writeText).toHaveBeenCalledWith("a>b#c}d*e?f!"));
      expect(container.textContent).not.toContain("a>b#c}d*e?f!");
      expect(toast.success).toHaveBeenCalledWith("Password copied");
    });

    it("copies a public PostgreSQL string instead of offering a port-forward", async () => {
      const writeText = stubClipboard();
      stubCredential("postgres://app:s3cr3t@198.51.100.30:5432");
      const postgres = createMockDatabase({
        ...internal,
        engine: "postgresql",
        access: { external: true, visibility: "public", address: "198.51.100.30" },
        connection: { host: "198.51.100.30", port: 5432, username: "app" },
      });

      const { container } = renderWithProviders(<ConnectSection database={postgres} />);

      expect(
        screen.getByText("postgres://app:••••••••@198.51.100.30:5432/postgres?sslmode=require"),
      ).toBeInTheDocument();
      expect(container.textContent).not.toContain("port-forward");
      await userEvent.click(screen.getByRole("button", { name: "Copy connection string" }));
      await waitFor(() =>
        expect(writeText).toHaveBeenCalledWith("postgres://app:s3cr3t@198.51.100.30:5432"),
      );
    });

    it("offers no copy of a private database's password and connects to its address", () => {
      const { container } = renderWithProviders(<ConnectSection database={privateDatabase} />);

      expect(
        screen.queryByRole("button", { name: "Copy connection string" }),
      ).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Copy password" })).not.toBeInTheDocument();
      expect(screen.getByText("mysql -h '10.0.4.12' -P '3306' -u 'root' -p")).toBeInTheDocument();
      expect(
        screen.getByText(
          "Run this from your VPC or a network connected to it. It asks for the password from step 2.",
        ),
      ).toBeInTheDocument();
      expect(container.textContent).not.toContain("127.0.0.1");
    });

    it("says when the browser blocks the copy", async () => {
      const blocked = Object.assign(new Error("blocked"), { name: "NotAllowedError" });
      vi.stubGlobal("navigator", {
        ...navigator,
        clipboard: { writeText: vi.fn().mockRejectedValue(blocked) },
      });

      renderWithProviders(<ConnectSection database={internal} />);
      await userEvent.click(screen.getByRole("button", { name: "Copy step 1" }));

      await waitFor(() =>
        expect(toast.error).toHaveBeenCalledWith(
          "The browser blocked the copy. Select the text and copy it.",
        ),
      );
    });
  });

  describe("from your app", () => {
    it("gives the in-cluster address and where the password lives", async () => {
      renderWithProviders(<ConnectSection database={internal} />);
      await userEvent.click(screen.getByRole("button", { name: "From your app" }));

      expect(screen.getByText("orders-dev-haproxy.pds")).toBeInTheDocument();
      expect(screen.getByText("3306")).toBeInTheDocument();
      expect(screen.getByText("app")).toBeInTheDocument();
      expect(
        screen.getByText(
          "An app on spoke-a connects at this address inside the Kubernetes cluster.",
        ),
      ).toBeInTheDocument();
      expect(screen.getByText("orders-dev-conn")).toHaveClass("font-mono");
      expect(screen.queryByRole("button", { name: /Copy/ })).not.toBeInTheDocument();
    });

    it("points to the laptop steps when no Secret name was reported", async () => {
      const sparse = createMockDatabase({
        ...internal,
        connection: { ...internal.connection!, secretName: undefined },
      });

      renderWithProviders(<ConnectSection database={sparse} />);
      await userEvent.click(screen.getByRole("button", { name: "From your app" }));

      expect(screen.getByText(/Step 2 under From your laptop names it/)).toBeInTheDocument();
    });

    it("keeps the in-cluster host off screen while a public address is pending", async () => {
      const pending = createMockDatabase({
        ...internal,
        access: { external: true, sourceRanges: ["10.0.0.0/8"] },
        connection: { host: "orders-dev-pxc.pds.svc", port: 3306, username: "app" },
      });

      renderWithProviders(<ConnectSection database={pending} />);
      await userEvent.click(screen.getByRole("button", { name: "From your app" }));

      expect(screen.getByText("Pending")).toBeInTheDocument();
      expect(screen.queryByText("orders-dev-pxc.pds.svc")).not.toBeInTheDocument();
    });

    it("tells a private database's app to connect from the VPC", async () => {
      renderWithProviders(<ConnectSection database={privateDatabase} />);
      await userEvent.click(screen.getByRole("button", { name: "From your app" }));

      expect(
        screen.getByText(
          "An app in your VPC, or a network connected to it, connects at this address.",
        ),
      ).toBeInTheDocument();
    });
  });

  describe("from an AI agent", () => {
    it("gives the plugin prompt for a database whose password the control plane holds", async () => {
      renderWithProviders(<ConnectSection database={publicDatabase} />);
      await userEvent.click(screen.getByRole("button", { name: "From an AI agent" }));

      expect(screen.getByText("Connect to orders-dev on spoke-a.")).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "Set up an agent" })).toHaveAttribute(
        "href",
        "/agents",
      );
    });

    it("says an agent cannot fetch a password that stays on the Kubernetes cluster", async () => {
      renderWithProviders(<ConnectSection database={internal} />);
      await userEvent.click(screen.getByRole("button", { name: "From an AI agent" }));

      expect(
        screen.getByText(/so connect with the steps under From your laptop/),
      ).toBeInTheDocument();
      expect(screen.queryByText("Connect to orders-dev on spoke-a.")).not.toBeInTheDocument();
    });
  });

  describe("reachability", () => {
    it("states an internal database is reachable inside the Kubernetes cluster only", () => {
      renderWithProviders(<ConnectSection database={internal} />);

      expect(screen.getByText("Reachable inside the Kubernetes cluster only")).toBeInTheDocument();
      expect(screen.getByTestId("reachability-icon-internal")).toBeInTheDocument();
    });

    it("shows a public database's allow list in mono", () => {
      const listed = createMockDatabase({
        ...publicDatabase,
        access: { ...publicDatabase.access!, sourceRanges: ["10.0.0.0/8", "203.0.113.7/32"] },
      });

      renderWithProviders(<ConnectSection database={listed} />);

      expect(screen.getByText("Public, reachable from the internet")).toBeInTheDocument();
      expect(screen.getByText("10.0.0.0/8, 203.0.113.7/32")).toHaveClass("font-mono");
      expect(
        screen.queryByText("Any address on the internet can connect."),
      ).not.toBeInTheDocument();
    });

    it("warns when a public database accepts any address", () => {
      const open = createMockDatabase({
        ...publicDatabase,
        access: { ...publicDatabase.access!, sourceRanges: ["0.0.0.0/0"] },
      });

      renderWithProviders(<ConnectSection database={open} />);

      expect(screen.getByText("Any address on the internet can connect.")).toBeInTheDocument();
    });

    it("says the address is pending before any connection is published", () => {
      renderWithProviders(
        <ConnectSection
          database={createMockDatabase({
            status: "provisioning",
            access: { external: true, visibility: "private" },
          })}
        />,
      );

      expect(screen.getByText("Private address pending")).toBeInTheDocument();
      expect(screen.getByTestId("reachability-icon-pending")).toBeInTheDocument();
    });

    it("states a private database is reachable from the VPC, with no allow list", () => {
      renderWithProviders(<ConnectSection database={privateDatabase} />);

      expect(screen.getByText("Private, reachable from your VPC")).toBeInTheDocument();
      expect(screen.queryByText(/Allowed from/)).not.toBeInTheDocument();
    });
  });
});
