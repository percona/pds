import { LifeBuoy, ShieldCheck, Unlink } from "lucide-react";

const pillars = [
  {
    icon: ShieldCheck,
    title: "Stays in your AWS account",
    body: "An EKS cluster the command creates, including its nodes and the S3 bucket for backups, is in your AWS account. A Kubernetes cluster you already have stays yours too. PDS does not hold the data.",
  },
  {
    icon: Unlink,
    title: "No lock-in",
    body: "Built on open source: Percona Operators for MySQL and PostgreSQL, and monitoring. Disconnect an environment and its databases keep running on the Kubernetes cluster.",
  },
  {
    icon: LifeBuoy,
    title: "Percona's experts behind it",
    body: "When you need a human, open a ticket from the database that needs help. Percona's engineers get your database context with it, and never your secrets.",
  },
];

export function Pillars() {
  return (
    <section aria-label="Why PDS" className="px-6 pb-24">
      <ul className="mx-auto grid max-w-6xl gap-4 md:grid-cols-3">
        {pillars.map(({ icon: Icon, title, body }) => (
          <li key={title} className="rounded-lg border border-border bg-card p-6">
            <span className="flex size-9 items-center justify-center rounded-md bg-muted">
              <Icon className="size-4 text-primary" strokeWidth={1.5} aria-hidden="true" />
            </span>
            <h3 className="mt-5 text-base font-semibold">{title}</h3>
            <p className="mt-2 text-sm text-muted-foreground">{body}</p>
          </li>
        ))}
      </ul>
    </section>
  );
}
