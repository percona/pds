import {
  Activity,
  ArchiveRestore,
  Bell,
  Bot,
  Gauge,
  KeyRound,
  LifeBuoy,
  MessageSquare,
} from "lucide-react";
import { SectionHeading } from "@/components/section-heading";
import { sectionIds } from "@/lib/site";

const operations = [
  {
    icon: Gauge,
    title: "Scale from the database page",
    body: "Change CPU, memory, storage and members. Members restart one at a time to apply it, and storage only grows.",
  },
  {
    icon: ArchiveRestore,
    title: "Backups you can restore",
    body: "Back up on demand, or hourly, daily or weekly, to the backup store on the Kubernetes cluster. When the command creates an EKS cluster, that store is an S3 bucket in your account. A restore creates a new database and leaves the original running.",
  },
  {
    icon: Activity,
    title: "Metrics and history",
    body: "CPU and other readings sit on the database they belong to, next to a history of every change made to it.",
  },
  {
    icon: Bell,
    title: "Alerts",
    body: "Firing alerts show on the database they belong to, and on one Alerts page for every environment.",
  },
  {
    icon: MessageSquare,
    title: "Chat with an assistant",
    body: "Ask questions about your databases in Chats, with your own Claude or Cursor key.",
  },
  {
    icon: Bot,
    title: "AI agents",
    body: "An AI agent in your editor can list databases, read metrics and alerts, and change them when you allow it. It runs on your machine and signs in as you.",
  },
  {
    icon: KeyRound,
    title: "Credentials stay off screen",
    body: "The password is never shown. Copy puts it on the clipboard. A private database keeps its password on the Kubernetes cluster, and Connect shows how to read it there.",
  },
  {
    icon: LifeBuoy,
    title: "Support with context attached",
    body: "Contact support from the database that needs help. The ticket reaches Percona in ServiceNow with the context attached and secrets removed.",
  },
];

export function Operations() {
  return (
    <section id={sectionIds.operations} aria-labelledby="operations-title" className="px-6 py-24">
      <div className="mx-auto max-w-6xl space-y-12">
        <SectionHeading id="operations-title" eyebrow="After create" title="Day two, handled">
          Creating a database is the easy part. PDS covers the work that comes after it, from the
          same place you created it.
        </SectionHeading>

        <ul className="grid gap-x-8 gap-y-10 sm:grid-cols-2 lg:grid-cols-3">
          {operations.map(({ icon: Icon, title, body }) => (
            <li key={title} className="space-y-3">
              <span className="flex size-9 items-center justify-center rounded-md bg-muted">
                <Icon className="size-4 text-primary" strokeWidth={1.5} aria-hidden="true" />
              </span>
              <h3 className="text-base font-semibold">{title}</h3>
              <p className="text-sm text-muted-foreground">{body}</p>
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
}
