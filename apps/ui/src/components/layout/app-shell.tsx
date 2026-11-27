import { Menu } from "lucide-react";
import { useEffect, useRef } from "react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { BackLink } from "@/components/layout/back-link";
import { Sidebar } from "@/components/layout/sidebar";
import {
  SidebarPreferenceProvider,
  useSidebarPreference,
} from "@/components/layout/sidebar-preference";
import { buttonVariants } from "@/components/ui/button";
import { ChatUiProvider } from "@/features/chat/chat-ui";
import { ChatWidget } from "@/features/chat/components/chat-widget";
import { EnvironmentProvider } from "@/features/environments";
import { Feature } from "@/features/flags";
import { ActingTeamProvider } from "@/features/teams";
import { shortenedProductName } from "@/lib/product";
import { assetUrl } from "@/lib/base-path";
import { cn } from "@/lib/utils";

export function AppShell({ children }: { children: ReactNode }) {
  return (
    <ActingTeamProvider>
      <ChatUiProvider>
        <EnvironmentProvider>
          <SidebarPreferenceProvider>
            <ShellFrame>{children}</ShellFrame>
          </SidebarPreferenceProvider>
        </EnvironmentProvider>
      </ChatUiProvider>
    </ActingTeamProvider>
  );
}

function ShellFrame({ children }: { children: ReactNode }) {
  const { rail, phone, drawerOpen, openDrawer, closeDrawer } = useSidebarPreference();
  const menuRef = useRef<HTMLButtonElement>(null);
  const wasOpen = useRef(false);

  useEffect(() => {
    if (wasOpen.current && !drawerOpen) {
      menuRef.current?.focus();
    }
    wasOpen.current = drawerOpen;
  }, [drawerOpen]);

  return (
    <div className="min-h-screen bg-background text-foreground">
      <div inert={drawerOpen ? true : undefined}>
        {phone ? (
          <header className="sticky top-0 z-30 flex h-14 items-center gap-2 border-b border-border bg-card px-4">
            <button
              ref={menuRef}
              type="button"
              className={buttonVariants({ variant: "ghost", size: "icon" })}
              aria-label="Open menu"
              aria-expanded={drawerOpen}
              aria-controls="app-sidebar"
              onClick={openDrawer}
            >
              <Menu strokeWidth={1.5} aria-hidden="true" />
            </button>
            <Link to="/" className="flex min-w-0 items-center gap-2 rounded-md">
              <img src={assetUrl("percona-mark.svg")} alt="" className="size-5 shrink-0" />
              <span className="truncate text-sm font-semibold tracking-tight">
                {shortenedProductName}
              </span>
            </Link>
          </header>
        ) : null}
        <main className={cn(!phone && (rail ? "pl-16" : "pl-64"))}>
          <div data-testid="app-content" className="mx-auto max-w-6xl animate-fade-in px-6 py-8">
            <BackLink />
            {children}
          </div>
        </main>
        <Feature name="chat">
          <ChatWidget />
        </Feature>
      </div>
      {drawerOpen ? (
        <button
          type="button"
          tabIndex={-1}
          aria-hidden="true"
          data-testid="sidebar-scrim"
          className="fixed inset-0 z-40 bg-foreground/40"
          onClick={closeDrawer}
        />
      ) : null}
      {!phone || drawerOpen ? <Sidebar restoreFocusRef={menuRef} /> : null}
    </div>
  );
}
