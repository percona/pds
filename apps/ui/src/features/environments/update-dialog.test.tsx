import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders } from "@/test/utils";
import { UpdateDialog } from "./update-dialog";
import type { UpdatePreview } from "./types";

function stubApi(preview: UpdatePreview) {
  const fetchMock = vi.fn<(input: string, init?: RequestInit) => Promise<Response>>((input) => {
    const path = String(input);
    const body = path.endsWith("/updates/preview")
      ? preview
      : { run: { id: 3 }, provisioning: { phases: [] } };

    return Promise.resolve(
      new Response(JSON.stringify(body), {
        status: path.endsWith("/runs") ? 202 : 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
  });
  vi.stubGlobal("fetch", fetchMock);

  return fetchMock;
}

function called(fetchMock: ReturnType<typeof stubApi>, suffix: string) {
  return fetchMock.mock.calls.filter(([path]) => String(path).endsWith(suffix));
}

const safe: UpdatePreview = {
  replacesNetwork: false,
  replacesNodeGroup: false,
  changes: [
    { resource: "NodeGroup", type: "AWS::EKS::Nodegroup", action: "Modify", replacement: "False" },
  ],
};

afterEach(() => vi.unstubAllGlobals());

describe("UpdateDialog for the template", () => {
  it("previews what changes before anything starts, then updates", async () => {
    const fetchMock = stubApi(safe);
    const onOpenChange = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <UpdateDialog environment="orders" kind="template" open onOpenChange={onOpenChange} />,
    );

    const changes = await screen.findByRole("list", { name: "Changes to the stack" });
    expect(changes).toHaveTextContent("NodeGroup");
    expect(changes).toHaveTextContent("Modify");
    expect(called(fetchMock, "/runs")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
    expect(JSON.parse(String(called(fetchMock, "/runs")[0][1]?.body))).toEqual({
      kind: "upgrade_kubernetes",
    });
  });

  it("stops and explains when the network or the Kubernetes cluster would be replaced", async () => {
    const fetchMock = stubApi({
      replacesNetwork: true,
      replacesNodeGroup: false,
      changes: [{ resource: "Vpc", type: "AWS::EC2::VPC", action: "Modify", replacement: "True" }],
    });

    renderWithProviders(
      <UpdateDialog environment="orders" kind="template" open onOpenChange={vi.fn()} />,
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "would replace the network or the Kubernetes cluster",
    );
    expect(screen.getByRole("alert")).toHaveTextContent("Contact Percona support");
    expect(screen.queryByRole("button", { name: "Update" })).not.toBeInTheDocument();
    expect(called(fetchMock, "/runs")).toHaveLength(0);
  });

  it("needs an explicit confirmation to replace the worker nodes", async () => {
    const fetchMock = stubApi({
      replacesNetwork: false,
      replacesNodeGroup: true,
      changes: [
        {
          resource: "NodeGroup",
          type: "AWS::EKS::Nodegroup",
          action: "Modify",
          replacement: "True",
        },
      ],
    });
    const user = userEvent.setup();

    renderWithProviders(
      <UpdateDialog environment="orders" kind="template" open onOpenChange={vi.fn()} />,
    );

    const update = await screen.findByRole("button", { name: "Update" });
    expect(update).toBeDisabled();
    await user.click(screen.getByRole("checkbox", { name: /Replace the worker nodes/ }));
    expect(update).toBeEnabled();
    await user.click(update);

    await waitFor(() => expect(called(fetchMock, "/runs")).toHaveLength(1));
    expect(JSON.parse(String(called(fetchMock, "/runs")[0][1]?.body))).toEqual({
      kind: "upgrade_kubernetes",
      confirmNodeGroupReplace: true,
    });
  });
});

describe("UpdateDialog for Kubernetes", () => {
  it("asks for the version, previews it, and starts the upgrade with it", async () => {
    const fetchMock = stubApi(safe);
    const user = userEvent.setup();

    renderWithProviders(
      <UpdateDialog
        environment="orders"
        kind="kubernetes"
        currentVersion="1.31"
        open
        onOpenChange={vi.fn()}
      />,
    );

    expect(called(fetchMock, "/updates/preview")).toHaveLength(0);
    const preview = screen.getByRole("button", { name: "Preview changes" });
    expect(preview).toBeDisabled();
    await user.type(screen.getByLabelText("Kubernetes version"), "1.32");
    await user.click(preview);
    await screen.findByRole("list", { name: "Changes to the stack" });
    await user.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() => expect(called(fetchMock, "/runs")).toHaveLength(1));
    expect(JSON.parse(String(called(fetchMock, "/updates/preview")[0][1]?.body))).toEqual({
      kubernetesVersion: "1.32",
    });
    expect(JSON.parse(String(called(fetchMock, "/runs")[0][1]?.body))).toEqual({
      kind: "upgrade_kubernetes",
      kubernetesVersion: "1.32",
    });
  });
});
