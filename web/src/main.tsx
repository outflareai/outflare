import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { ErrorBoundary } from "./ui";
import { restoreDNSReturn } from "./components/dns-one-click";
import { installExternalLinks } from "./lib/desktop";
import { track, trackStaleBuildReload } from "./lib/analytics";
import "./index.css";

restoreDNSReturn();
trackStaleBuildReload();
// In the Outflare desktop app, links that leave Outflare open in the browser.
installExternalLinks(() => track("ui.app_external_link_opened"));

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
);
