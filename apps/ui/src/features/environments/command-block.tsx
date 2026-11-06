import { Copy } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";

type CommandBlockProps = {
  command: string;
  /** What the button says it copies. */
  label?: string;
  /** The toast after a successful copy. */
  copied?: string;
};

export function CommandBlock({
  command,
  label = "Copy command",
  copied = "Command copied",
}: CommandBlockProps) {
  async function copy(): Promise<void> {
    try {
      await navigator.clipboard.writeText(command);
      toast.success(copied);
    } catch {
      toast.error("The browser blocked the copy. Select the command and copy it.");
    }
  }

  return (
    <div className="space-y-2">
      <pre className="overflow-x-auto rounded-md bg-muted px-3 py-2 font-mono text-xs whitespace-pre-wrap">
        {command}
      </pre>
      <Button type="button" variant="outline" onClick={() => void copy()}>
        <Copy strokeWidth={1.5} aria-hidden="true" />
        {label}
      </Button>
    </div>
  );
}
