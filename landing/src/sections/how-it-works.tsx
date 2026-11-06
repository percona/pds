import * as Tabs from "@radix-ui/react-tabs";
import {
  ArchiveRestore,
  CircleCheck,
  CircleDashed,
  Copy,
  Gauge,
  HardDriveUpload,
  KeyRound,
  LifeBuoy,
  Loader2,
} from "lucide-react";
import type { ReactNode } from "react";
import { SectionHeading } from "@/components/section-heading";
import { StatusPill } from "@/components/status-pill";
import { buttonVariants } from "@/components/ui/button";
import { sectionIds } from "@/lib/site";
import { cn } from "@/lib/utils";

type Step = {
  id: string;
  title: string;
  summary: string;
  panel: ReactNode;
};

const steps: Step[] = [
  {
    id: "eks",
    title: "Connect an environment",
    summary:
      "One command creates an EKS cluster in your AWS account, or joins a Kubernetes cluster you already have.",
    panel: <EksPanel />,
  },
  {
    id: "profile",
    title: "Pick a profile",
    summary: "Dev, Staging or Prod, sized for the job.",
    panel: <ProfilePanel />,
  },
  {
    id: "create",
    title: "Create a database",
    summary: "A name is the only thing you have to type.",
    panel: <CreatePanel />,
  },
  {
    id: "operate",
    title: "Operate it",
    summary: "Connect, scale, back up, restore and get help in place.",
    panel: <OperatePanel />,
  },
];

export function HowItWorks() {
  return (
    <section
      id={sectionIds.howItWorks}
      aria-labelledby="how-it-works-title"
      className="border-y border-border bg-muted/40 px-6 py-24"
    >
      <div className="mx-auto max-w-6xl space-y-12">
        <SectionHeading
          id="how-it-works-title"
          eyebrow="From EKS cluster to database"
          title="How it works"
        >
          Bring an AWS account. The command creates an Amazon EKS cluster, or joins a Kubernetes
          cluster you already have, installs what MySQL and PostgreSQL need, then gives your team
          one place to create and run databases there.
        </SectionHeading>

        <Tabs.Root
          defaultValue={steps[0]!.id}
          orientation="vertical"
          className="grid grid-cols-[minmax(0,1fr)] gap-6 lg:grid-cols-[20rem_minmax(0,1fr)]"
        >
          <Tabs.List aria-label="Steps" className="flex flex-col gap-2">
            {steps.map((step, index) => (
              <Tabs.Trigger
                key={step.id}
                value={step.id}
                className={cn(
                  "group flex gap-4 rounded-lg border border-transparent p-4 text-left transition-colors",
                  "hover:bg-card focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary",
                  "data-[state=active]:border-border data-[state=active]:bg-card",
                )}
              >
                <span className="font-mono text-xs leading-5 text-muted-foreground group-data-[state=active]:text-primary">
                  {String(index + 1).padStart(2, "0")}
                </span>
                <span className="space-y-1">
                  <span className="block text-sm font-semibold">{step.title}</span>
                  <span className="block text-sm text-muted-foreground">{step.summary}</span>
                </span>
              </Tabs.Trigger>
            ))}
          </Tabs.List>

          {steps.map((step) => (
            <Tabs.Content
              key={step.id}
              value={step.id}
              className="animate-fade-in rounded-lg border border-border bg-card p-6 sm:p-8 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
            >
              {step.panel}
            </Tabs.Content>
          ))}
        </Tabs.Root>
      </div>
    </section>
  );
}

function PanelHeading({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="space-y-1.5">
      <h3 className="text-base font-semibold">{title}</h3>
      <p className="text-sm text-muted-foreground">{children}</p>
    </div>
  );
}

function EksPanel() {
  const creates = [
    "An EKS cluster with four t3.2xlarge nodes, unless you join a Kubernetes cluster you already have",
    "An S3 bucket for backups when the command creates the EKS cluster",
    "Percona Operators for MySQL and PostgreSQL, and monitoring, on that Kubernetes cluster",
    "An agent on that Kubernetes cluster that reports to PDS and runs work such as a backup",
  ];

  return (
    <div className="space-y-6">
      <PanelHeading title="One command creates an EKS cluster, or joins a Kubernetes cluster you already have">
        Name the environment in the console and copy the command. On a machine with aws, eksctl,
        kubectl, helm and python3 it creates an Amazon EKS cluster and asks which region to use
        before it creates anything. Add --local to join a Kubernetes cluster kubectl already points
        at. That needs kubectl and helm, and it creates nothing in AWS.
      </PanelHeading>

      <pre className="overflow-x-auto rounded-md border border-border bg-background p-4 font-mono text-xs leading-6">
        <code>
          <span className="text-muted-foreground">$ </span>curl -fsSL
          https://pds.percona.com/api/internal/connect/bootstrap.sh \{"\n"}
          {"    "}| bash -s -- --token <span className="text-muted-foreground">••••••••••••</span>
        </code>
      </pre>

      <ul className="space-y-2.5">
        {creates.map((item) => (
          <li key={item} className="flex items-start gap-2 text-sm">
            <CircleCheck
              className="mt-0.5 size-4 shrink-0 text-success"
              strokeWidth={1.5}
              aria-hidden="true"
            />
            {item}
          </li>
        ))}
      </ul>

      <p className="text-xs text-muted-foreground">
        AWS bills the EKS cluster, its nodes, a NAT gateway, the backup bucket and one load balancer
        per database to your account. Joining a Kubernetes cluster you already have creates none of
        that. The token connects an environment to your control plane, so treat it like a password.
      </p>

      <div className="flex flex-wrap items-center gap-3 border-t border-border pt-5 text-sm">
        <StatusPill kind="ready" label="Connected" />
        <span className="font-mono text-xs text-muted-foreground">prod-us-west-2</span>
      </div>
    </div>
  );
}

const profiles = [
  { name: "Dev", line: "1 member, on-demand backups", size: "1 CPU · 2 GB · 20 GB" },
  { name: "Staging", line: "3 members, HA, daily backups", size: "2 CPU · 8 GB · 100 GB" },
  { name: "Prod", line: "3 members, HA, daily backups", size: "4 CPU · 16 GB · 250 GB" },
];

function ProfilePanel() {
  return (
    <div className="space-y-6">
      <PanelHeading title="Start from a profile">
        Each profile sets members, resources, high availability and a backup schedule. Under
        Advanced, CPU, memory and storage can be overridden within limits the control plane checks.
      </PanelHeading>

      <ul className="grid gap-3 sm:grid-cols-3">
        {profiles.map((profile) => {
          const selected = profile.name === "Prod";

          return (
            <li
              key={profile.name}
              className={cn(
                "space-y-3 rounded-lg border p-4",
                selected ? "border-primary" : "border-border",
              )}
            >
              <div className="flex items-center justify-between">
                <span className="text-sm font-semibold">{profile.name}</span>
                {selected ? (
                  <span className="flex items-center gap-1 text-xs font-medium text-primary">
                    <CircleCheck className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
                    Selected
                  </span>
                ) : null}
              </div>
              <p className="text-xs text-muted-foreground">{profile.line}</p>
              <p className="font-mono text-xs">{profile.size}</p>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

// The provisioning steps and their state labels, as the console shows them.
const provisioning = [
  { label: "Creating volumes", state: "Done", icon: CircleCheck, tone: "text-success" },
  { label: "Starting database", state: "In progress", icon: Loader2, tone: "text-primary" },
  {
    label: "Creating app user",
    state: "Waiting",
    icon: CircleDashed,
    tone: "text-muted-foreground",
  },
];

function CreatePanel() {
  return (
    <div className="space-y-6">
      <PanelHeading title="One required field">
        Name the database and create it on the environment. The profile and resources come with
        defaults you can change before you submit.
      </PanelHeading>

      <div className="grid gap-6 md:grid-cols-2">
        <div className="space-y-4" aria-hidden="true">
          <div className="space-y-1.5">
            <p className="text-sm font-medium">Name</p>
            <p className="flex h-9 items-center rounded-md border border-input bg-background px-3 font-mono text-sm">
              orders
            </p>
          </div>
          <div className="space-y-1.5">
            <p className="text-sm font-medium">Profile</p>
            <p className="flex h-9 items-center rounded-md border border-input bg-background px-3 text-sm">
              Prod
            </p>
          </div>
          <span className={buttonVariants({ className: "w-full" })}>Create database</span>
        </div>

        <div className="space-y-4 rounded-lg border border-border p-4">
          <p className="text-sm font-medium">
            Creating <span className="font-mono">orders</span>
          </p>
          <ul className="space-y-3">
            {provisioning.map(({ icon: Icon, tone, label, state }) => (
              <li key={label} className="flex items-center gap-2 text-sm">
                <Icon
                  className={cn("size-4 shrink-0", tone)}
                  strokeWidth={1.5}
                  aria-hidden="true"
                />
                <span className="flex-1">{label}</span>
                <span className="text-xs text-muted-foreground">{state}</span>
              </li>
            ))}
          </ul>
        </div>
      </div>
    </div>
  );
}

function OperatePanel() {
  const actions = [
    { icon: Gauge, label: "Scale" },
    { icon: HardDriveUpload, label: "Back up now" },
    { icon: ArchiveRestore, label: "Restore" },
    { icon: LifeBuoy, label: "Contact support" },
  ];

  return (
    <div className="space-y-6">
      <PanelHeading title="Everything after create, on the database page">
        Every action lives next to the database it changes. A restore creates a new database from
        the backup and leaves the original running. The password is never shown. Copy puts it on the
        clipboard. A private database keeps its password on the Kubernetes cluster.
      </PanelHeading>

      <div className="space-y-2">
        <p className="text-sm font-medium">Connect</p>
        <div className="flex items-center gap-2">
          <code className="flex h-9 min-w-0 flex-1 items-center overflow-hidden rounded-md border border-input bg-background px-3 font-mono text-xs whitespace-nowrap">
            mysql://app:••••••••@orders-haproxy.pds:3306
          </code>
          <span className={buttonVariants({ variant: "outline" })} aria-hidden="true">
            <Copy strokeWidth={1.5} />
            Copy
          </span>
        </div>
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <KeyRound className="size-3.5" strokeWidth={1.5} aria-hidden="true" />
          The password is never shown. Copy puts it on the clipboard.
        </p>
      </div>

      <ul className="grid grid-cols-2 gap-2 xl:grid-cols-4" aria-label="Database actions">
        {actions.map(({ icon: Icon, label }) => (
          <li
            key={label}
            className="flex items-center gap-2 rounded-md border border-border px-3 py-2.5 text-sm"
          >
            <Icon className="size-4 text-muted-foreground" strokeWidth={1.5} aria-hidden="true" />
            {label}
          </li>
        ))}
      </ul>
    </div>
  );
}
