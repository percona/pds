import { FlaskConical } from "lucide-react";
import { badgeVariants } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

/** The label under the product name, and the popup it opens. */
export function ExperimentalNotice({ compact = false }: { compact?: boolean }) {
  return (
    <Dialog>
      <DialogTrigger
        aria-label="Experimental, open the disclaimer"
        className={cn(
          badgeVariants({ variant: "outline" }),
          "w-fit gap-1 px-2 py-0 text-muted-foreground hover:bg-accent hover:text-foreground",
          compact && "size-7 justify-center px-0",
        )}
      >
        <FlaskConical className="size-3" strokeWidth={1.5} aria-hidden="true" />
        {compact ? <span className="sr-only">Experimental</span> : "Experimental"}
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Experimental</DialogTitle>
          <DialogDescription>
            Percona Database Service is an experimental project.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-3 text-sm text-muted-foreground">
          <p>PDS is not covered by Percona support commitments,</p>
          <p>For now, it's not recommended for production, use with caution.</p>
        </div>
      </DialogContent>
    </Dialog>
  );
}
