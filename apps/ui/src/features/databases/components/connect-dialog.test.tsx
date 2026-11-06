import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConnectDialog } from "@/features/databases";
import { createMockDatabase, renderWithProviders } from "@/test/utils";

/** The real string. It exists only behind the credential route. */
const realUri = "mysql://app:s3cr3t@orders-dev-haproxy.pds:3306";

function stubCredential(uri: string | null = realUri) {
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

const spoke = createMockDatabase({
  name: "orders-dev",
  namespace: "pds",
  connection: {
    host: "orders-dev-haproxy.pds",
    port: 3306,
    username: "app",
    maskedUri: "mysql://app:••••••••@orders-dev-haproxy.pds:3306",
    secretName: "orders-dev-conn",
    serviceName: "orders-dev-haproxy",
  },
});

/** The same database as the hub sees it: every field but the password. */
const hub = createMockDatabase({
  ...spoke,
  id: "spoke-a/pds.orders-dev",
  environment: "spoke-a",
  connection: { ...spoke.connection!, maskedUri: undefined },
});

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

afterEach(() => {
  vi.unstubAllGlobals();
  vi.mocked(toast.error).mockClear();
  vi.mocked(toast.success).mockClear();
});

describe("ConnectDialog", () => {
  it("names the environment the database runs on", () => {
    renderWithProviders(<ConnectDialog database={hub} open onOpenChange={() => {}} />);

    expect(
      screen.getByText(
        "This database runs on spoke-a. No password is held here. These four commands are the way in. They run against that Kubernetes cluster and end at a prompt you can type in.",
      ),
    ).toBeInTheDocument();
  });

  // A hub holds no credential and never reaches the spoke's API, so the
  // whole path to a prompt has to be on screen as commands.
  it("gives every command from kubectl context to a client prompt", () => {
    renderWithProviders(<ConnectDialog database={hub} open onOpenChange={() => {}} />);

    expect(screen.getByText("1. Point kubectl at the Kubernetes cluster")).toBeInTheDocument();
    expect(screen.getByText("kubectl config use-context 'spoke-a'")).toBeInTheDocument();

    expect(screen.getByText("2. Read the password")).toBeInTheDocument();
    expect(screen.getByText(/kubectl -n 'pds' get secret 'orders-dev-conn'/)).toBeInTheDocument();

    expect(screen.getByText("3. Forward the port")).toBeInTheDocument();
    expect(
      screen.getByText("kubectl -n 'pds' port-forward 'svc/orders-dev-haproxy' '3306:3306'"),
    ).toBeInTheDocument();

    expect(screen.getByText("4. Connect")).toBeInTheDocument();
    expect(screen.getByText("mysql -h 127.0.0.1 -P '3306' -u 'app' -p")).toBeInTheDocument();
  });

  // A spoke reports host, port and user, and nothing else about how the
  // password is stored or which Service fronts the database. Both are
  // derivable, so neither step is dropped.
  it("still gives all four steps when the spoke reported no Secret or Service", () => {
    const sparse = createMockDatabase({
      ...hub,
      connection: { ...hub.connection!, secretName: undefined, serviceName: undefined },
    });

    renderWithProviders(<ConnectDialog database={sparse} open onOpenChange={() => {}} />);

    expect(screen.getByText(/get instances 'orders-dev'/)).toBeInTheDocument();
    expect(
      screen.getByText("kubectl -n 'pds' port-forward 'svc/orders-dev-haproxy' '3306:3306'"),
    ).toBeInTheDocument();
    expect(screen.getByText("4. Connect")).toBeInTheDocument();
  });

  it("names the in-cluster address for a workload on that environment", () => {
    renderWithProviders(<ConnectDialog database={hub} open onOpenChange={() => {}} />);

    expect(screen.getByText("orders-dev-haproxy.pds:3306")).toBeInTheDocument();
  });

  // The credential route answers 501 on a hub by design. Asking anyway
  // would turn a stated limit into an error toast on every copy.
  it("copies the public connection string with the password, which stays off the page", async () => {
    const writeText = stubClipboard();
    const fetchMock = stubCredential("mysql://root:s3cr3t@198.51.100.20:3306");
    const external = createMockDatabase({
      ...hub,
      access: { external: true, address: "198.51.100.20" },
      connection: { host: "198.51.100.20", port: 3306, username: "root" },
    });

    const { container } = renderWithProviders(
      <ConnectDialog database={external} open onOpenChange={() => {}} />,
    );

    expect(
      screen.getByText(
        "This database runs on spoke-a. The password stays off this page. Copy it, or use these three commands. They run against that Kubernetes cluster and end at a prompt you can type in.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/No password is held here/)).not.toBeInTheDocument();
    expect(screen.getByText("3. Connect")).toBeInTheDocument();
    expect(screen.getByText("mysql://root:••••••••@198.51.100.20:3306")).toBeInTheDocument();
    expect(container.textContent).not.toContain("s3cr3t");
    await userEvent.click(screen.getByRole("button", { name: "Copy connection string" }));

    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith("mysql://root:s3cr3t@198.51.100.20:3306"),
    );
    expect(String(fetchMock.mock.calls[0]?.[0])).toContain("/credential");
    expect(container.textContent).not.toContain("s3cr3t");

    writeText.mockClear();
    await userEvent.click(screen.getByRole("button", { name: "Copy password" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("s3cr3t"));
    expect(container.textContent).not.toContain("s3cr3t");
  });

  it("copies a public PostgreSQL connection string instead of offering a port-forward", async () => {
    const writeText = stubClipboard();
    stubCredential("postgres://app:s3cr3t@198.51.100.30:5432");
    const external = createMockDatabase({
      ...hub,
      engine: "postgresql",
      access: { external: true, visibility: "public", address: "198.51.100.30" },
      connection: { host: "198.51.100.30", port: 5432, username: "app" },
    });

    const { container } = renderWithProviders(
      <ConnectDialog database={external} open onOpenChange={() => {}} />,
    );

    expect(
      screen.getByText("postgres://app:••••••••@198.51.100.30:5432/postgres?sslmode=require"),
    ).toBeInTheDocument();
    expect(container.textContent).not.toContain("port-forward");
    await userEvent.click(screen.getByRole("button", { name: "Copy connection string" }));

    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith("postgres://app:s3cr3t@198.51.100.30:5432"),
    );
  });

  it("offers no copy of a private database's password, which stays on its environment", () => {
    stubCredential();
    const privateLb = createMockDatabase({
      ...hub,
      access: { external: true, visibility: "private", address: "10.0.4.12" },
      connection: { host: "10.0.4.12", port: 3306, username: "root" },
    });

    renderWithProviders(<ConnectDialog database={privateLb} open onOpenChange={() => {}} />);

    expect(
      screen.queryByRole("button", { name: "Copy connection string" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Copy password" })).not.toBeInTheDocument();
    expect(
      screen.getByText(
        "This database runs on spoke-a. No password is held here. These three commands are the way in. They run against that Kubernetes cluster and end at a prompt you can type in.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("3. Connect")).toBeInTheDocument();
  });

  // A load balancer has no in-cluster Service to forward, so the client goes
  // straight to its address rather than to a port-forward that never started.
  it("connects to a private load balancer's address, not a port-forward", () => {
    const privateLb = createMockDatabase({
      ...hub,
      access: { external: true, visibility: "private", address: "10.0.4.12" },
      connection: {
        host: "10.0.4.12",
        port: 3306,
        username: "root",
        secretName: "orders-dev-secrets",
      },
    });

    const { container } = renderWithProviders(
      <ConnectDialog database={privateLb} open onOpenChange={() => {}} />,
    );

    expect(screen.getByText("mysql -h '10.0.4.12' -P '3306' -u 'root' -p")).toBeInTheDocument();
    expect(
      screen.getByText(
        "Run this from your VPC or a network connected to it. It asks for the password from step 2.",
      ),
    ).toBeInTheDocument();
    expect(container.textContent).not.toContain("127.0.0.1");
    expect(screen.queryByText("3. Forward the port")).not.toBeInTheDocument();
    expect(screen.queryByText(/In environment address/)).not.toBeInTheDocument();
  });

  it("never asks the hub for a credential it does not hold", async () => {
    const writeText = stubClipboard();
    const fetchMock = stubCredential();

    renderWithProviders(<ConnectDialog database={hub} open onOpenChange={() => {}} />);
    await userEvent.click(screen.getByRole("button", { name: "Copy step 2" }));

    await waitFor(() =>
      expect(writeText).toHaveBeenCalledWith(
        expect.stringContaining("get secret 'orders-dev-conn'"),
      ),
    );
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
