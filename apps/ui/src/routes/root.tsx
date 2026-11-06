import { Route, Routes } from "react-router";
import { SignedIn } from "@/features/auth";
import { AuthorizeAgentPage } from "@/pages/authorize-agent";
import { ClaimInvitePage } from "@/pages/claim-invite";
import { LoginPage } from "@/pages/login";
import { AppRoutes } from "@/routes";

/**
 * The outermost routes. Two of them answer without a session, and everything
 * else is behind the gate.
 *
 * The claim page has to be reachable signed out by definition: it is the link
 * Percona sends a customer who has no password yet. The sign in page has to be
 * reachable so the gate has somewhere to send people.
 */
export function RootRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route path="/claim" element={<ClaimInvitePage />} />
      <Route
        path="/authorize-agent"
        element={
          <SignedIn>
            <AuthorizeAgentPage />
          </SignedIn>
        }
      />
      <Route
        path="*"
        element={
          <SignedIn>
            <AppRoutes />
          </SignedIn>
        }
      />
    </Routes>
  );
}
