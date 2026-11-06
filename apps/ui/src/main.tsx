import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "@/App";
import { installFeatureFlags } from "@/features/flags";
import "@/index.css";

const container = document.getElementById("root");
if (!container) {
  throw new Error("Root element not found");
}

// Every surface starts on so the first render has a flag set to read from. The
// real one is behind the session, so the gate around the app reads it once the
// adapter says who is signed in and installs it before any screen mounts.
installFeatureFlags();

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
