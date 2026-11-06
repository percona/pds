import { CircleAlert } from "lucide-react";

/** The sentence under a form when what it did was refused: an icon and what happened. */
export function InlineError({ message }: { message: string | undefined }) {
  if (message === undefined) {
    return null;
  }

  return (
    <p role="alert" className="flex items-start gap-2 text-sm text-destructive">
      <CircleAlert className="mt-0.5 size-4 shrink-0" strokeWidth={1.5} aria-hidden="true" />
      {message}
    </p>
  );
}
