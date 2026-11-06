import { Navigate } from "react-router";

/** The root path is the environments list. */
export function HomePage() {
  return <Navigate to="/environments" replace />;
}
