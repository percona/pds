import { ChevronDown } from "lucide-react";
import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

/**
 * A select built on the platform control.
 *
 * The Radix select in `select.tsx` is for the create form, where the options
 * carry a line of consequence each and need custom markup. A filter is a list
 * of one word options, and the native control already gives it a keyboard path,
 * a touch friendly picker, and screen reader support that cost nothing to keep.
 *
 * The chevron is decorative: `appearance-none` removes the platform one so the
 * control matches the rest of the inputs.
 */
export function NativeSelect({ className, children, ...props }: ComponentProps<"select">) {
  return (
    <div className="relative inline-flex">
      <select
        className={cn(
          "h-9 w-full appearance-none rounded-md border border-input bg-background pr-9 pl-3 text-sm text-foreground",
          "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary",
          "disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
        {...props}
      >
        {children}
      </select>
      <ChevronDown
        className="pointer-events-none absolute top-1/2 right-3 size-4 -translate-y-1/2 text-muted-foreground"
        strokeWidth={1.5}
        aria-hidden="true"
      />
    </div>
  );
}
