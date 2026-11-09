import { createRoot } from "react-dom/client";
import { PartnerPortal } from "./PartnerPortal";
import { trackStaleBuildReload } from "@/lib/analytics";
import "../index.css";
trackStaleBuildReload();
createRoot(document.getElementById("root")!).render(<PartnerPortal />);
