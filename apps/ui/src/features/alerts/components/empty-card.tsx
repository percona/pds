import type { BellRing } from "lucide-react";

type EmptyCardProps = {
  icon: typeof BellRing;
  title: string;
  body?: string;
  children?: React.ReactNode;
};

/** A composed empty state for an alerts list. */
export function EmptyCard({ icon: Icon, title, body, children }: EmptyCardProps) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-lg border border-border bg-card px-6 py-12 text-center">
      <Icon className="size-5 text-muted-foreground" strokeWidth={1.5} aria-hidden="true" />
      <div className="space-y-1">
        <p className="text-sm font-medium">{title}</p>
        {body ? <p className="text-sm text-muted-foreground">{body}</p> : null}
      </div>
      {children}
    </div>
  );
}
