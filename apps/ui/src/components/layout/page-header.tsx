import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

type PageHeaderProps = {
  title: string;
  description?: string;
  /** A row of facts under the title, such as a status pill and an owner. */
  meta?: ReactNode;
  /** For a title that is a name the person typed, such as an environment's. */
  monoTitle?: boolean;
  actions?: ReactNode;
};

export function PageHeader({ title, description, meta, monoTitle, actions }: PageHeaderProps) {
  return (
    <header className="flex flex-wrap items-start justify-between gap-4">
      <div className="min-w-0 space-y-1">
        <h1
          className={cn(
            "text-2xl font-semibold tracking-tight break-words",
            monoTitle && "font-mono",
          )}
        >
          {title}
        </h1>
        {description ? <p className="text-sm text-muted-foreground">{description}</p> : null}
        {meta ? (
          <div className="flex flex-wrap items-center gap-x-4 gap-y-2 pt-1 text-xs text-muted-foreground">
            {meta}
          </div>
        ) : null}
      </div>
      {actions ? <div className="flex items-center gap-2">{actions}</div> : null}
    </header>
  );
}
