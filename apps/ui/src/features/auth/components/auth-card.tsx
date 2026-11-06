import type { ReactNode } from "react";
import { ThemeToggle } from "@/components/layout/theme-toggle";
import { productName } from "@/lib/product";
import { assetUrl } from "@/lib/base-path";
import { cn } from "@/lib/utils";

type AuthCardProps = {
  title: string;
  description: string;
  children: ReactNode;
  /** A card with more to read than a sign in form. */
  wide?: boolean;
};

/**
 * The frame around signing in and setting a password. There is no sidebar to
 * sit in yet, so the card carries the product mark and the theme toggle
 * itself: a customer who prefers dark should not have to sign in on a white
 * screen first.
 */
export function AuthCard({ title, description, children, wide = false }: AuthCardProps) {
  return (
    <div className="flex min-h-screen flex-col bg-background text-foreground">
      <header className="flex h-14 items-center justify-between px-6">
        <span className="flex items-center gap-2">
          <img src={assetUrl("percona-mark.svg")} alt="" className="size-5" />
          <span className="text-sm font-semibold tracking-tight">{productName}</span>
        </span>
        <ThemeToggle />
      </header>

      <main className="flex flex-1 items-start justify-center px-6 pt-10 pb-16 sm:items-center sm:pt-0">
        <div
          className={cn(
            "w-full animate-fade-in space-y-6 rounded-lg border border-border bg-card p-6",
            wide ? "max-w-lg" : "max-w-sm",
          )}
        >
          <div className="space-y-1.5">
            <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
            <p className="text-sm text-muted-foreground">{description}</p>
          </div>
          {children}
        </div>
      </main>
    </div>
  );
}
