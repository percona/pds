import type { ReactNode } from "react";
import { BackLink } from "@/components/layout/back-link";
import { Sidebar } from "@/components/layout/sidebar";
import { ChatUiProvider } from "@/features/chat/chat-ui";
import { ChatWidget } from "@/features/chat/components/chat-widget";
import { EnvironmentProvider } from "@/features/environments";
import { Feature } from "@/features/flags";
import { ActingTeamProvider } from "@/features/teams";

export function AppShell({ children }: { children: ReactNode }) {
  return (
    <ActingTeamProvider>
      <ChatUiProvider>
        <EnvironmentProvider>
          <div className="min-h-screen bg-background text-foreground">
            <Sidebar />
            <main className="pl-64">
              <div
                data-testid="app-content"
                className="mx-auto max-w-6xl animate-fade-in px-6 py-8"
              >
                <BackLink />
                {children}
              </div>
            </main>
            <Feature name="chat">
              <ChatWidget />
            </Feature>
          </div>
        </EnvironmentProvider>
      </ChatUiProvider>
    </ActingTeamProvider>
  );
}
