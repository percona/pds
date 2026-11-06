import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

// ComponentProps rather than InputHTMLAttributes so a caller that has to reach
// the element, such as a dialog focusing its confirmation field, can pass a ref.
export type InputProps = ComponentProps<"input">;

export function Input({ className, type = "text", ...props }: InputProps) {
  return (
    <input
      type={type}
      className={cn(
        "h-9 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground",
        "placeholder:text-muted-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary",
        "disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive",
        className,
      )}
      {...props}
    />
  );
}
