import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
// First, so the layer order in index.css is set before the demo stylesheets declare theirs.
import "./index.css";
import { App } from "./App";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
