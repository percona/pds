import { Check, Clock3, Minus } from "lucide-react";
import { Fragment, type ReactNode } from "react";
import { SectionHeading } from "@/components/section-heading";
import { Badge } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button";
import { engines } from "@/lib/configurator";
import { consoleUrl, sectionIds } from "@/lib/site";
import { cn } from "@/lib/utils";

/** A plan's value in one row of the comparison. */
type Value =
  | { kind: "none" }
  | {
      kind: "text" | "included";
      lines: { text: string; soon?: boolean }[];
      note?: string;
    };

type Row = { label: string; detail?: string; yourself: Value; us: Value };

const none: Value = { kind: "none" };
const text = (value: string, soon = false): Value => ({
  kind: "text",
  lines: [{ text: value, soon }],
});
const included = (value: string, options: { soon?: boolean; note?: string } = {}): Value => ({
  kind: "included",
  lines: [{ text: value, soon: options.soon }],
  note: options.note,
});

const engineValue: Value = {
  kind: "text",
  lines: engines.map(({ label }) => ({ text: label })),
};

const groups: { title: string; rows: Row[] }[] = [
  {
    title: "Platform",
    rows: [
      { label: "Database engines", yourself: engineValue, us: engineValue },
      {
        label: "Provisioning templates",
        yourself: text("Dev / Staging / Prod"),
        us: text("Dev / Staging / Prod"),
      },
    ],
  },
  {
    title: "Automation",
    rows: [
      {
        label: "Fleet policies",
        detail: "HA, version currency, backup health",
        yourself: included("Enforceable", { soon: true }),
        us: included("Enforceable", { soon: true, note: "+ custom policies on Enterprise" }),
      },
      {
        label: "Monitoring",
        detail: "Open source monitoring",
        yourself: included("Monitoring"),
        us: included("Proactive monitoring and alerting"),
      },
    ],
  },
  {
    title: "Support",
    rows: [
      {
        label: "AI DBA",
        detail: "Tuning, indexing and diagnostics",
        yourself: none,
        us: text("Limited → Full access", true),
      },
      { label: "Channel", yourself: text("Community"), us: text("Email → Chat, Zoom") },
      { label: "Coverage", yourself: none, us: text("Business hours → 24×7×365") },
      {
        label: "Response SLA",
        detail: "Highest severity issue",
        yourself: none,
        us: text("4 hours → 30 minutes"),
      },
      {
        label: "Live sessions with Percona experts",
        yourself: none,
        us: text("Enterprise only (P1/P2)"),
      },
      {
        label: "Dedicated Service Delivery Manager",
        yourself: none,
        us: included("Enterprise only"),
      },
    ],
  },
];

export function Pricing() {
  return (
    <section
      id={sectionIds.pricing}
      aria-labelledby="pricing-title"
      className="border-y border-border bg-muted/40 px-6 py-24"
    >
      <div className="mx-auto max-w-6xl space-y-12">
        <SectionHeading
          id="pricing-title"
          eyebrow="Pricing"
          title="Plans built around your own infrastructure"
          align="center"
        >
          PDS runs in your own AWS account, so every plan includes the full platform. What changes
          between plans is how far automation goes and how fast Percona answers when something
          breaks.
        </SectionHeading>

        <div className="mx-auto grid max-w-3xl gap-4 md:grid-cols-2">
          <Plan
            title="Yourself"
            description="Run PDS on your own Amazon EKS cluster. The same open source software; you handle upgrades and operations."
            cta="Get started"
            price={
              <p className="flex items-baseline gap-1.5">
                <span className="font-mono text-2xl font-semibold">$0</span>
                <span className="text-xs text-muted-foreground">/ month</span>
              </p>
            }
            support={
              <>
                <span className="font-medium text-foreground">Community</span> support: docs and
                forum
              </>
            }
          />
          <Plan
            title="Us"
            description="The same software, operated for you by Percona. Business hours or 24×7 support, depending on the plan."
            cta="Contact Percona"
            price={<p className="text-2xl font-semibold tracking-tight">Coming soon</p>}
            support={
              <>
                <span className="font-medium text-foreground">Business hours → 24×7×365</span>{" "}
                support, depending on the plan
              </>
            }
          />
        </div>

        <div className="overflow-x-auto rounded-lg border border-border bg-card">
          <table className="w-full min-w-[40rem] border-collapse text-sm">
            <caption className="sr-only">Compare plans</caption>
            <thead>
              <tr className="border-b border-border text-xs text-muted-foreground">
                <th scope="col" className="w-2/5 px-5 py-3 text-left font-medium">
                  Compare plans
                </th>
                <th scope="col" className="px-5 py-3 text-center font-medium">
                  Yourself
                </th>
                <th scope="col" className="px-5 py-3 text-center font-medium">
                  Us
                </th>
              </tr>
            </thead>
            <tbody>
              {groups.map((group) => (
                <Fragment key={group.title}>
                  <tr className="border-b border-border bg-muted/60">
                    <th
                      scope="colgroup"
                      colSpan={3}
                      className="px-5 py-2.5 text-left text-xs font-semibold text-primary"
                    >
                      {group.title}
                    </th>
                  </tr>
                  {group.rows.map((row) => (
                    <tr key={row.label} className="border-b border-border last:border-b-0">
                      <th scope="row" className="px-5 py-3.5 text-left align-top font-normal">
                        <span className="block">{row.label}</span>
                        {row.detail ? (
                          <span className="block text-xs text-muted-foreground">{row.detail}</span>
                        ) : null}
                      </th>
                      <td className="px-5 py-3.5 text-center align-top">
                        <Cell value={row.yourself} />
                      </td>
                      <td className="px-5 py-3.5 text-center align-top">
                        <Cell value={row.us} />
                      </td>
                    </tr>
                  ))}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </section>
  );
}

function Plan({
  title,
  description,
  cta,
  price,
  support,
}: {
  title: string;
  description: string;
  cta: string;
  price: ReactNode;
  support: ReactNode;
}) {
  return (
    <div className="flex flex-col rounded-lg border border-border bg-card p-6">
      <h3 className="text-base font-semibold">{title}</h3>
      <p className="mt-2 flex-1 text-sm text-muted-foreground">{description}</p>
      <a
        href={consoleUrl}
        className={buttonVariants({ variant: "outline", className: "mt-5 w-full" })}
      >
        {cta}
      </a>
      <div className="mt-6">{price}</div>
      <p className="mt-4 border-t border-border pt-4 text-xs text-muted-foreground">{support}</p>
    </div>
  );
}

function Soon() {
  return (
    <Badge variant="muted" className="px-2 py-0 font-sans text-[11px]">
      <Clock3 className="size-3" strokeWidth={1.5} aria-hidden="true" />
      Soon
    </Badge>
  );
}

function Cell({ value }: { value: Value }) {
  if (value.kind === "none") {
    return (
      <span className="inline-flex text-muted-foreground">
        <Minus className="size-4" strokeWidth={1.5} aria-hidden="true" />
        <span className="sr-only">Not included</span>
      </span>
    );
  }

  return (
    <div className="space-y-1">
      {value.lines.map((line) => (
        <p
          key={line.text}
          className={cn(
            "flex items-center justify-center gap-1.5 font-mono text-[13px]",
            value.kind === "included" && "text-success",
          )}
        >
          {value.kind === "included" ? (
            <Check className="size-3.5 shrink-0" strokeWidth={1.5} aria-hidden="true" />
          ) : null}
          {line.text}
          {line.soon ? <Soon /> : null}
        </p>
      ))}
      {value.note ? <p className="text-xs text-muted-foreground">{value.note}</p> : null}
    </div>
  );
}
