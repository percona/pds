import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/utils";
import { MonitoringPanel } from "./monitoring-panel";

const baseAccess = {
  username: "admin",
  passwordCommand:
    "kubectl get secret pmm-secret -n everest-system -o jsonpath='{.data.PMM_ADMIN_PASSWORD}' | base64 --decode",
  portForwardCommand: "kubectl port-forward -n everest-system svc/monitoring-service 8443:443",
  address: "",
  url: "https://localhost:8443",
  tunnelUrl: "https://localhost:8443",
};

const monitoring = {
  settings: {
    access: "cluster",
    cpuMillicores: 1000,
    memoryMi: 1024,
    storageGi: 5,
  },
  access: baseAccess,
};

const exposed = (access: string, address = "", ranges = ["203.0.113.0/24"]) => ({
  settings: { ...monitoring.settings, access, sourceRanges: ranges },
  access: { ...baseAccess, address, url: address ? `https://${address}` : baseAccess.url },
});

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubApi(
  put: (body: unknown) => Response = () => jsonResponse(monitoring),
  password: () => Response = () => jsonResponse({ password: "s3cret-pw" }),
  current: unknown = monitoring,
) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input).endsWith("/monitoring/password")) {
      return Promise.resolve(password());
    }
    if (init?.method === "PUT") {
      return Promise.resolve(put(JSON.parse(String(init.body))));
    }

    return Promise.resolve(jsonResponse(current));
  });
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

async function chooseAccess(access: string, ranges?: string) {
  const user = userEvent.setup();
  await user.click(await screen.findByRole("radio", { name: access }));
  if (ranges) {
    const field = screen.getByLabelText("Allowed addresses");
    await user.clear(field);
    await user.type(field, ranges);
  }
  await user.click(screen.getByRole("button", { name: "Save settings" }));

  return user;
}

function monitoringReads(fetchMock: ReturnType<typeof stubApi>) {
  return fetchMock.mock.calls.filter(
    ([input, init]) => String(input).endsWith("/monitoring") && init?.method !== "PUT",
  );
}

function stubClipboard() {
  const writeText = vi.fn(() => Promise.resolve());
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });

  return writeText;
}

function passwordCalls(fetchMock: ReturnType<typeof stubApi>) {
  return fetchMock.mock.calls.filter(([input]) => String(input).endsWith("/monitoring/password"));
}

describe("MonitoringPanel", () => {
  it("shows the current settings and where the login lives", async () => {
    stubApi();

    renderWithProviders(<MonitoringPanel environment="spoke-a" />);

    expect(await screen.findByLabelText("Storage (GiB)")).toHaveValue(5);
    expect(screen.getByLabelText("Memory (MiB)")).toHaveValue(1024);
    expect(screen.getByText(/get secret pmm-secret -n everest-system/)).toBeInTheDocument();
    expect(screen.getByText(/port-forward -n everest-system/)).toBeInTheDocument();
  });

  it("saves changed settings", async () => {
    const bodies: unknown[] = [];
    stubApi((body) => {
      bodies.push(body);

      return jsonResponse(monitoring);
    });
    const user = userEvent.setup();

    renderWithProviders(<MonitoringPanel environment="spoke-a" />);

    const storage = await screen.findByLabelText("Storage (GiB)");
    await user.clear(storage);
    await user.type(storage, "20");
    await user.click(screen.getByRole("button", { name: "Save settings" }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({
      access: "cluster",
      cpuMillicores: 1000,
      memoryMi: 1024,
      storageGi: 20,
    });
  });

  describe("access", () => {
    it("starts environment only, with no allowed addresses to fill in", async () => {
      stubApi();

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      expect(
        await screen.findByRole("radio", { name: "Inside the Kubernetes cluster" }),
      ).toBeChecked();
      expect(screen.queryByLabelText("Allowed addresses")).not.toBeInTheDocument();
      expect(screen.queryByLabelText("Service type")).not.toBeInTheDocument();
      expect(screen.queryByLabelText("Service annotations")).not.toBeInTheDocument();
      expect(
        screen.getByText(/Reachable only from inside the Kubernetes cluster/),
      ).toBeInTheDocument();
    });

    it("asks for allowed addresses once monitoring is exposed", async () => {
      stubApi();
      const user = userEvent.setup();

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      await user.click(await screen.findByRole("radio", { name: "Public" }));

      expect(screen.getByText("Reachable from the internet.")).toBeInTheDocument();
      expect(screen.getByText(/billed by your cloud provider/)).toBeInTheDocument();
      // Public starts empty. Opening monitoring to the world is typed, not preset.
      expect(screen.getByLabelText("Allowed addresses")).toHaveValue("");
    });

    it("explains the private default and shows what a saved private service is limited to", async () => {
      stubApi(undefined, undefined, exposed("private", "", ["10.0.0.0/8", "172.16.0.0/12"]));

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      expect(await screen.findByRole("radio", { name: "Private" })).toBeChecked();
      expect(screen.getByLabelText("Allowed addresses")).toHaveValue("10.0.0.0/8, 172.16.0.0/12");
      expect(screen.getByText(/Reachable from your VPC/)).toBeInTheDocument();
    });

    it("refuses a public service with no allowed address, without calling the control plane", async () => {
      const fetchMock = stubApi();
      const user = userEvent.setup();

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      await user.click(await screen.findByRole("radio", { name: "Public" }));
      await user.click(screen.getByRole("button", { name: "Save settings" }));

      expect(await screen.findByText("Enter at least one address or range.")).toBeInTheDocument();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "PUT")).toHaveLength(0);
    });

    it("names an address it cannot read", async () => {
      stubApi();
      const user = userEvent.setup();

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      await user.click(await screen.findByRole("radio", { name: "Public" }));
      await user.type(screen.getByLabelText("Allowed addresses"), "office");
      await user.click(screen.getByRole("button", { name: "Save settings" }));

      expect(
        await screen.findByText("`office` is not an IP address or CIDR range."),
      ).toBeInTheDocument();
    });

    it("warns when any address on the internet is allowed", async () => {
      stubApi();
      const user = userEvent.setup();

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      await user.click(await screen.findByRole("radio", { name: "Public" }));
      expect(screen.queryByText(/Any address on the internet can connect/)).not.toBeInTheDocument();
      await user.type(screen.getByLabelText("Allowed addresses"), "0.0.0.0/0");

      expect(screen.getByText(/Any address on the internet can connect/)).toBeInTheDocument();
    });
  });

  describe("changing access", () => {
    it("asks first, says what changes, and saves nothing until it is confirmed", async () => {
      const bodies: unknown[] = [];
      stubApi((body) => {
        bodies.push(body);

        return jsonResponse(exposed("public"));
      });

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);
      const user = await chooseAccess("Public", "203.0.113.0/24");

      const dialog = await screen.findByRole("dialog");
      expect(dialog).toHaveTextContent(/adds a load balancer/i);
      expect(dialog).toHaveTextContent(/billed by your cloud provider/i);
      expect(bodies).toHaveLength(0);

      await user.click(screen.getByRole("button", { name: "Change access" }));

      await waitFor(() => expect(bodies).toHaveLength(1));
      expect(bodies[0]).toEqual({
        access: "public",
        sourceRanges: ["203.0.113.0/24"],
        cpuMillicores: 1000,
        memoryMi: 1024,
        storageGi: 5,
      });
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    });

    it("saves nothing when the change is cancelled", async () => {
      const bodies: unknown[] = [];
      stubApi((body) => {
        bodies.push(body);

        return jsonResponse(monitoring);
      });

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);
      const user = await chooseAccess("Private");

      await screen.findByRole("dialog");
      await user.click(screen.getByRole("button", { name: "Cancel" }));

      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      expect(bodies).toHaveLength(0);
      // The form keeps what was chosen, so a second look costs nothing.
      expect(screen.getByRole("radio", { name: "Private" })).toBeChecked();
    });

    it("says the load balancer goes away when monitoring is closed again", async () => {
      stubApi(undefined, undefined, exposed("public", "pmm.elb.example.com"));

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);
      await chooseAccess("Inside the Kubernetes cluster");

      expect(await screen.findByRole("dialog")).toHaveTextContent(/removes the load balancer/i);
    });

    it("says the address changes when private becomes public", async () => {
      stubApi(undefined, undefined, exposed("private", "pmm.elb.example.com", ["10.0.0.0/8"]));

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);
      await chooseAccess("Public", "203.0.113.0/24");

      expect(await screen.findByRole("dialog")).toHaveTextContent(/address changes/i);
    });

    it("does not ask when access is unchanged", async () => {
      const bodies: unknown[] = [];
      stubApi(
        (body) => {
          bodies.push(body);

          return jsonResponse(exposed("public"));
        },
        undefined,
        exposed("public"),
      );
      const user = userEvent.setup();

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      const field = await screen.findByLabelText("Allowed addresses");
      await user.clear(field);
      await user.type(field, "198.51.100.0/24");
      await user.click(screen.getByRole("button", { name: "Save settings" }));

      await waitFor(() => expect(bodies).toHaveLength(1));
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(bodies[0]).toMatchObject({ access: "public", sourceRanges: ["198.51.100.0/24"] });
    });

    it("sends no ranges while monitoring stays environment only", async () => {
      const bodies: unknown[] = [];
      stubApi((body) => {
        bodies.push(body);

        return jsonResponse(monitoring);
      });
      const user = userEvent.setup();

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);
      await user.click(await screen.findByRole("button", { name: "Save settings" }));

      await waitFor(() => expect(bodies).toHaveLength(1));
      expect(bodies[0]).not.toHaveProperty("sourceRanges");
      expect(bodies[0]).not.toHaveProperty("serviceType");
    });
  });

  describe("signing in", () => {
    it("links to the address the environment reported", async () => {
      stubApi(undefined, undefined, exposed("public", "pmm.elb.example.com"));

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      const link = await screen.findByRole("link", { name: "https://pmm.elb.example.com" });
      expect(link).toHaveAttribute("href", "https://pmm.elb.example.com");
      expect(link).toHaveAttribute("rel", expect.stringContaining("noreferrer"));
      expect(screen.getByText(/self-signed certificate/)).toBeInTheDocument();
    });

    it("says the address is pending while the cloud creates the load balancer", async () => {
      stubApi(undefined, undefined, exposed("private", ""));

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      expect(await screen.findByText(/Address pending/)).toBeInTheDocument();
      expect(screen.queryByRole("link", { name: /https:/ })).not.toBeInTheDocument();
    });

    describe("while the address is pending", () => {
      afterEach(() => {
        vi.useRealTimers();
      });

      it("looks again until the cloud has assigned one", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        const fetchMock = stubApi(undefined, undefined, exposed("public", ""));

        renderWithProviders(<MonitoringPanel environment="spoke-a" />);
        await screen.findByText(/Address pending/);
        const before = monitoringReads(fetchMock).length;

        await vi.advanceTimersByTimeAsync(16_000);

        expect(monitoringReads(fetchMock).length).toBeGreaterThan(before);
      });

      it("stops looking once there is an address", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        const fetchMock = stubApi(undefined, undefined, exposed("public", "pmm.elb.example.com"));

        renderWithProviders(<MonitoringPanel environment="spoke-a" />);
        await screen.findByRole("link", { name: "https://pmm.elb.example.com" });
        const before = monitoringReads(fetchMock).length;

        await vi.advanceTimersByTimeAsync(60_000);

        expect(monitoringReads(fetchMock)).toHaveLength(before);
      });

      it("does not poll an environment only service", async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        const fetchMock = stubApi();

        renderWithProviders(<MonitoringPanel environment="spoke-a" />);
        await screen.findByText(/Reachable only from inside the Kubernetes cluster/);
        const before = monitoringReads(fetchMock).length;

        await vi.advanceTimersByTimeAsync(60_000);

        expect(monitoringReads(fetchMock)).toHaveLength(before);
      });
    });

    it("keeps the tunnel under Advanced for an environment only service", async () => {
      stubApi();

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      expect(
        await screen.findByText(/Reachable only from inside the Kubernetes cluster/),
      ).toBeInTheDocument();
      expect(screen.queryByText(/Address pending/)).not.toBeInTheDocument();
      expect(screen.queryByRole("link", { name: /https:/ })).not.toBeInTheDocument();
    });
  });

  it("shows the reason when the control plane refuses the settings", async () => {
    stubApi(() =>
      jsonResponse(
        { code: "ErrValidation", message: "storage must be between 5Gi and 2000Gi" },
        400,
      ),
    );
    const user = userEvent.setup();

    renderWithProviders(<MonitoringPanel environment="spoke-a" />);

    await user.click(await screen.findByRole("button", { name: "Save settings" }));

    expect(await screen.findByText(/storage must be between 5Gi and 2000Gi/)).toBeInTheDocument();
  });

  describe("copying the password", () => {
    it("puts the password on the clipboard without showing it", async () => {
      const fetchMock = stubApi();
      // user-event installs its own clipboard on setup, so the stub goes after it.
      const user = userEvent.setup();
      const writeText = stubClipboard();

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      const button = await screen.findByRole("button", { name: "Copy password" });
      // Opening the panel must not move a credential.
      expect(passwordCalls(fetchMock)).toHaveLength(0);

      await user.click(button);

      await waitFor(() => expect(writeText).toHaveBeenCalledWith("s3cret-pw"));
      expect(passwordCalls(fetchMock)).toHaveLength(1);
      expect(fetchMock.mock.calls.some(([input]) => String(input).includes("spoke-a"))).toBe(true);
      expect(screen.queryByText(/s3cret-pw/)).not.toBeInTheDocument();
    });

    it("copies nothing when the environment does not answer", async () => {
      stubApi(undefined, () =>
        jsonResponse(
          {
            code: "ErrDownstream",
            message: "The environment did not answer. Try again in a moment.",
          },
          504,
        ),
      );
      // user-event installs its own clipboard on setup, so the stub goes after it.
      const user = userEvent.setup();
      const writeText = stubClipboard();

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      await user.click(await screen.findByRole("button", { name: "Copy password" }));

      await waitFor(() =>
        expect(screen.getByRole("button", { name: "Copy password" })).toBeEnabled(),
      );
      expect(writeText).not.toHaveBeenCalled();
    });

    it("keeps the kubectl commands as a fallback under Advanced", async () => {
      stubApi();

      renderWithProviders(<MonitoringPanel environment="spoke-a" />);

      const command = await screen.findByText(/get secret pmm-secret -n everest-system/);
      const advanced = command.closest("details");
      expect(advanced).not.toBeNull();
      expect(advanced).not.toHaveAttribute("open");
      expect(advanced).toHaveTextContent("Advanced");
      // The port-forward command sits with it, so the primary view needs no kubectl.
      expect(advanced).toContainElement(screen.getByText(/port-forward -n everest-system/));
    });
  });
});
