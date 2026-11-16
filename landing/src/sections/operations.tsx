import * as Tabs from "@radix-ui/react-tabs";
import { SectionHeading } from "@/components/section-heading";
import { sectionIds } from "@/lib/site";
import { cn } from "@/lib/utils";
import { OperationStill, type OperationId } from "./operation-stills";

const operations: { id: OperationId; title: string; line: string }[] = [
  {
    id: "scale",
    title: "Scale",
    line: "Change CPU, memory, storage and members from the database page.",
  },
  {
    id: "backups",
    title: "Backups",
    line: "Back up on demand or on a schedule. A restore makes a new database.",
  },
  {
    id: "connect",
    title: "Connect",
    line: "Copy the connection string. Credentials stay off screen.",
  },
  {
    id: "alerts",
    title: "Alerts",
    line: "Firing alerts show on their database and on one Alerts page.",
  },
  {
    id: "agents",
    title: "AI agents",
    line: "An AI agent in your editor can list databases and read metrics.",
  },
  {
    id: "support",
    title: "Support",
    line: "Contact support from the database, with the context attached and secrets removed.",
  },
];

export function Operations() {
  return (
    <section
      id={sectionIds.operations}
      aria-labelledby="operations-title"
      className="px-6 py-16 sm:py-20"
    >
      <div className="mx-auto max-w-6xl space-y-8">
        <SectionHeading id="operations-title" eyebrow="After create" title="Day two, handled" />

        <Tabs.Root defaultValue={operations[0]!.id} className="space-y-6">
          <Tabs.List aria-label="Operations" className="flex flex-wrap gap-2">
            {operations.map((operation) => (
              <Tabs.Trigger
                key={operation.id}
                value={operation.id}
                className={cn(
                  "min-h-10 rounded-full border border-border bg-card px-4 text-sm transition-colors",
                  "hover:bg-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary",
                  "data-[state=active]:border-primary data-[state=active]:font-medium",
                )}
              >
                {operation.title}
              </Tabs.Trigger>
            ))}
          </Tabs.List>

          {operations.map((operation) => (
            <Tabs.Content
              key={operation.id}
              value={operation.id}
              className="grid animate-fade-in items-start gap-6 rounded-md md:grid-cols-[minmax(0,1fr)_minmax(0,520px)] md:gap-10 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary"
            >
              <p className="text-xl text-pretty">{operation.line}</p>
              <OperationStill id={operation.id} />
            </Tabs.Content>
          ))}
        </Tabs.Root>
      </div>
    </section>
  );
}
