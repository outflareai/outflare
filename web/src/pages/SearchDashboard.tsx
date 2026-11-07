import { useEffect, useState } from "react";
import type { SEOSite } from "@outflare/sdk";
import { api } from "../api";
import { Button, Empty, Loading, PageHeader } from "../ui";
import { useBrand } from "../lib/brand";
import type { Tab } from "../lib/paths";
import { SitePicker } from "./Keywords";
import { SearchInsights } from "./search-insights/Workspace";
import { ErrorRecovery } from "./search-insights/Primitives";
import { everyBrand, pickedBrandId } from "@/lib/brand";
export function SearchDashboard({
  ws,
  canManage,
  go,
  initialSite,
}: {
  ws: string;
  canManage: boolean;
  go: (t: Tab, item?: string) => void;
  initialSite?: string;
}) {
  const [sites, setSites] = useState<SEOSite[]>();
  const [site, setSite] = useState(initialSite ?? "");
  const [error, setError] = useState("");
  const [version, setVersion] = useState(0);
  const brand = useBrand(ws).query.brand ?? "";
  useEffect(() => {
    let live = true;
    setSites(undefined);
    // The picked brand's sites, or every brand's while all are picked.
    const list = (b: string) => api.GET("/v1/brands/{brand}/seo-sites", { params: { path: { brand: b } } });
    (pickedBrandId() ? list(pickedBrandId()).then((r) => (r.error || !r.data ? Promise.reject(r.error) : r.data.data)) : everyBrand(list))
      .then((rows) => {
        if (!live) return;
        setSites(rows);
        setError("");
        setSite((s) => (rows.some((x) => x.id === s) ? s : (rows[0]?.id ?? "")));
      })
      .catch(() => {
        if (live) setError("Could not load sites.");
      });
    return () => {
      live = false;
    };
  }, [ws, brand, version]);
  const current = sites?.find((s) => s.id === site);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Search insights"
        description="Turn search evidence into prioritised work, reusable reports and measurable changes."
        action={
          sites && (
            <SitePicker
              sites={sites}
              site={site}
              onChange={(s) => {
                setSite(s);
                go("search-insights", s);
              }}
            />
          )
        }
      />
      {error ? (
        <ErrorRecovery
          message={error}
          onRetry={() => setVersion((v) => v + 1)}
        />
      ) : !sites ? (
        <Loading />
      ) : !current ? (
        <Empty
          title="Add a website to begin"
          action={<Button onClick={() => go("audits")}>Add a site</Button>}
        >
          Connect Search Console after adding a website to build its search
          history.
        </Empty>
      ) : (
        <SearchInsights
          key={current.id}
          ws={ws}
          site={current}
          sites={sites}
          canManage={canManage}
          go={go}
        />
      )}
    </div>
  );
}
