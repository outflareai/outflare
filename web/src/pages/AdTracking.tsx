import { useEffect, useState } from "react";
import type { AdTrackingTemplate } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Card, CopyButton, ErrorText, field, Loading } from "../ui";

// The tracking template for each ad network: pasted once into the network,
// it makes every paid click land with UTM tags and the network's own
// campaign, ad set and ad ids, which website tracking stores on the visit.

export function AdTrackingTemplates({ ws }: { ws: string }) {
  const [all, setAll] = useState<AdTrackingTemplate[] | null>(null);
  const [network, setNetwork] = useState("google");
  const [error, setError] = useState("");

  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/ads/tracking-templates", { params: { path: { ws } } })
      .then(({ data, error }) => (error ? setError(errorMessage(error)) : setAll(data.data)));
  }, [ws]);

  const t = all?.find((n) => n.network === network);
  return (
    <Card
      title="Tracking templates"
      description="Paste one into each ad network so every paid click arrives with its campaign, ad set and ad ids. Website tracking keeps them on the visit, so sales are credited to the ad even after it is renamed."
    >
      {error ? <ErrorText>{error}</ErrorText> : !all ? <Loading /> : (
        <div className="space-y-3 text-[13px]">
          <select aria-label="Ad network" className={field} value={network} onChange={(e) => setNetwork(e.target.value)}>
            {all.map((n) => <option key={n.network} value={n.network}>{n.label}</option>)}
          </select>
          {t && (
            <>
              {t.template ? (
                <>
                  <div className="text-muted-foreground">Paste into <span className="font-medium text-foreground">{t.field}</span>:</div>
                  <div className="flex items-start gap-1 rounded-md border bg-muted/40 p-2">
                    <code className="min-w-0 flex-1 break-all font-mono text-[12px]">{t.template}</code>
                    <CopyButton text={t.template} label="Copy template" />
                  </div>
                </>
              ) : null}
              {t.click_ids.length > 0 && (
                <div className="text-muted-foreground">
                  Click id: {t.click_ids.map((c) => <code key={c} className="font-mono text-[12px]">{c}</code>)}
                  {t.cookies.length > 0 && <> · Pixel cookies, read with consent: {t.cookies.map((c, i) => <code key={c} className="font-mono text-[12px]">{i ? ", " : ""}{c}</code>)}</>}
                </div>
              )}
              {t.note && <p className="text-muted-foreground">{t.note}</p>}
            </>
          )}
        </div>
      )}
    </Card>
  );
}
