import type { ReactNode } from "react";
import { Card } from "@/components/ui/card";

type NoticeProps = { icon: ReactNode; title: string; detail: string; action?: ReactNode };

/** A composed empty or error state for a list: an icon, what happened, and at most one action. */
export function Notice({ icon, title, detail, action }: NoticeProps) {
  return (
    <Card className="flex flex-col items-center gap-3 px-6 py-12 text-center">
      {icon}
      <div className="space-y-1">
        <p className="text-sm font-medium">{title}</p>
        <p role={action ? "alert" : undefined} className="max-w-md text-sm text-muted-foreground">
          {detail}
        </p>
      </div>
      {action}
    </Card>
  );
}
