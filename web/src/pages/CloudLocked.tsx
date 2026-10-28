// A Cloud feature's page (ADR 11): the real one where the server runs it,
// otherwise what it does and where to get it.

import { useEffect, type ReactNode } from "react";
import { Empty, Loading, PageHeader } from "../ui";
import { Icon } from "../icons";
import { track } from "../lib/analytics";
import { useCapabilities, type CloudFeature } from "../lib/cloud";

export function CloudPage({ feature, children }: { feature: CloudFeature; children: ReactNode }) {
  const caps = useCapabilities();
  if (!caps) return <Loading />;
  return caps.includes(feature.capability) ? children : <CloudLocked feature={feature} />;
}

export function CloudLocked({ feature }: { feature: CloudFeature }) {
  useEffect(() => track("ui.cloud_feature_shown", { capability: feature.capability }), [feature.capability]);
  return (
    <div className="space-y-6">
      <PageHeader title={feature.name} description={feature.line} />
      <Empty
        title="Part of Outflare Cloud"
        action={
          <a href="https://outflare.ai/" target="_blank" rel="noreferrer noopener" onClick={() => track("ui.cloud_feature_clicked", { capability: feature.capability })}
            className="inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-[13px] shadow-xs hover:bg-accent">
            See Outflare Cloud <Icon name="external" className="size-3" />
          </a>
        }
      >
        This server doesn't run it. It works the same way as the rest of Outflare, on your own connected accounts and AI key.
      </Empty>
    </div>
  );
}
