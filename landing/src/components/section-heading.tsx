import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

type SectionHeadingProps = {
  id: string;
  eyebrow?: string;
  title: string;
  children?: ReactNode;
  align?: "left" | "center";
};

/** The heading block every section opens with. `id` names the h2 for the section. */
export function SectionHeading({
  id,
  eyebrow,
  title,
  children,
  align = "left",
}: SectionHeadingProps) {
  return (
    <div className={cn("max-w-2xl space-y-3", align === "center" && "mx-auto text-center")}>
      {eyebrow ? <p className="text-sm font-medium text-primary">{eyebrow}</p> : null}
      <h2 id={id} className="text-2xl font-semibold tracking-tight text-balance">
        {title}
      </h2>
      {children ? <p className="text-base text-muted-foreground text-pretty">{children}</p> : null}
    </div>
  );
}
