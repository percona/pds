import type { ReactElement } from "react";
import { Navigate, Outlet, Route, Routes, useLocation, useParams } from "react-router";
import { AppShell } from "@/components/layout/app-shell";
import { useIsAdmin } from "@/features/auth";
import { useFeature, type FeatureFlagName } from "@/features/flags";
import { AgentsPage } from "@/pages/agents";
import { ActivityPage } from "@/pages/activity";
import { ActivityDetailPage } from "@/pages/activity-detail";
import { AlertsPage } from "@/pages/alerts";
import { AwsCleanupPage } from "@/pages/aws-cleanup";
import { ChatPageRoute } from "@/pages/chat";
import { EnvironmentsPage } from "@/pages/environments";
import { ConnectEnvironmentPage } from "@/pages/connect-environment";
import { CreateDatabasePage } from "@/pages/create-database";
import { DatabaseDetailPage } from "@/pages/database-detail";
import { DatabasesPage } from "@/pages/databases";
import { HomePage } from "@/pages/home";
import { NotFoundPage } from "@/pages/not-found";
import { OpenEverestPage } from "@/pages/openeverest";
import { UpdateEnvironmentPage } from "@/pages/update-environment";
import { SupportPage } from "@/pages/support";
import { SupportTicketPage } from "@/pages/support-ticket";
import { SettingsPage } from "@/pages/settings";
import { TeamsPage } from "@/pages/teams";
import { UsersPage } from "@/pages/users";

/**
 * A screen that only exists while its surface is on. A turned off surface has
 * to answer the same way a path that was never routed does, or its own link
 * would still open it.
 */
function Surface({ name, children }: { name: FeatureFlagName; children: ReactElement }) {
  return useFeature(name) ? children : <NotFoundPage />;
}

/**
 * A screen only an Organization admin uses. Anybody else gets the page a path
 * that was never routed gets: the control plane refuses them anyway, and a
 * screen of refusals would only point at something they cannot have.
 */
function AdminOnly({ children }: { children: ReactElement }) {
  return useIsAdmin() ? children : <NotFoundPage />;
}

function ShellLayout() {
  return (
    <AppShell>
      <Outlet />
    </AppShell>
  );
}

export function AppRoutes() {
  return (
    <Routes>
      <Route element={<ShellLayout />}>
        <Route index element={<HomePage />} />
        <Route
          path="databases/:environment/:id"
          element={
            <Surface name="databases">
              <DatabaseDetailPage />
            </Surface>
          }
        />
        <Route
          path="environments"
          element={
            <Surface name="clusters">
              <EnvironmentsPage />
            </Surface>
          }
        />
        <Route
          path="environments/new"
          element={
            <Surface name="clusters">
              <ConnectEnvironmentPage />
            </Surface>
          }
        />
        <Route
          path="environments/:name"
          element={
            <Surface name="databases">
              <DatabasesPage />
            </Surface>
          }
        />
        <Route
          path="environments/:name/aws-cleanup"
          element={
            <Surface name="clusters">
              <AwsCleanupPage />
            </Surface>
          }
        />
        <Route
          path="environments/:name/update"
          element={
            <Surface name="clusters">
              <UpdateEnvironmentPage />
            </Surface>
          }
        />
        <Route
          path="environments/:name/openeverest"
          element={
            <Surface name="clusters">
              <OpenEverestPage />
            </Surface>
          }
        />
        <Route
          path="environments/:name/new"
          element={
            <Surface name="databases">
              <CreateDatabasePage />
            </Surface>
          }
        />
        <Route
          path="clusters"
          element={
            <Surface name="clusters">
              <LegacyClustersRedirect />
            </Surface>
          }
        />
        <Route
          path="clusters/*"
          element={
            <Surface name="clusters">
              <LegacyClustersRedirect />
            </Surface>
          }
        />
        <Route
          path="activity"
          element={
            <Surface name="activity">
              <ActivityPage />
            </Surface>
          }
        />
        <Route
          path="activity/:id"
          element={
            <Surface name="activity">
              <ActivityDetailPage />
            </Surface>
          }
        />
        <Route path="alerts" element={<AlertsPage />} />
        <Route
          path="support"
          element={
            <Surface name="support">
              <SupportPage />
            </Surface>
          }
        />
        <Route
          path="support/:number"
          element={
            <Surface name="support">
              <SupportTicketPage />
            </Surface>
          }
        />
        <Route
          path="teams"
          element={
            <AdminOnly>
              <TeamsPage />
            </AdminOnly>
          }
        />
        <Route
          path="users"
          element={
            <AdminOnly>
              <UsersPage />
            </AdminOnly>
          }
        />
        <Route
          path="settings"
          element={
            <AdminOnly>
              <SettingsPage />
            </AdminOnly>
          }
        />
        <Route
          path="chat"
          element={
            <Surface name="chat">
              <ChatPageRoute />
            </Surface>
          }
        />
        <Route
          path="agents"
          element={
            <Surface name="mcp">
              <AgentsPage />
            </Surface>
          }
        />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  );
}

/**
 * Bookmarks under /clusters. connect is the create page, so it does not become
 * an environment named connect. The rest of the path and the query stay.
 */
function environmentPathFromLegacy(splat: string): string {
  if (splat === "connect" || splat.startsWith("connect/")) {
    return `/environments/new${splat.slice("connect".length)}`;
  }
  return splat === "" ? "/environments" : `/environments/${splat}`;
}

function LegacyClustersRedirect() {
  const splat = useParams()["*"] ?? "";
  const location = useLocation();

  return (
    <Navigate
      to={{
        pathname: environmentPathFromLegacy(splat),
        search: location.search,
        hash: location.hash,
      }}
      replace
    />
  );
}
