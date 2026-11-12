// Tracker → Flows (plans/ad-tracker-ui.md B3): paths, landers and offers
// that several campaigns share by name. The list shows how many links route
// by each; a flow opens as a full page with the campaign editor's routing,
// and a save warns first how many links it changes.

import { useCallback, useEffect, useMemo, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { PickBrand } from "@/components/pick-brand";
import { pickedBrandId, useBrand } from "@/lib/brand";
import { api, errorMessage } from "../api";
import { ask, Button, Card, confirmAction, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { Routing, sentence, type RoutingBackend } from "./TrackerRouting";
import type { Tab } from "../App";

type Flow = Schemas["TrackerFlow"];
type Props = { ws: string; canManage: boolean; item?: string; go: (tab: Tab, item?: string) => void };

export function TrackerFlows(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="flows" brands={active.length} />;
  return props.item ? <FlowPage {...props} id={props.item} /> : <List {...props} />;
}

function List({ canManage, go }: Props) {
  const [flows, setFlows] = useState<Flow[] | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api.GET("/v1/brands/{brand}/flows", { params: { path: { brand: pickedBrandId() } } }).then(({ data, error }) => {
      if (error) return setError(errorMessage(error));
      setFlows((data?.data ?? []) as Flow[]);
    });
  }, []);
  const create = async () => {
    const r = await ask({ title: "New flow", input: { label: "Name", placeholder: "e.g. Sweepstakes - mobile" }, choices: [{ label: "Cancel", value: "cancel" }, { label: "Create", value: "ok", variant: "primary" }] });
    if (!r?.value.trim()) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/flows", { params: { path: { brand: pickedBrandId() } }, body: { name: r.value.trim() } });
    if (error) return toast(errorMessage(error), "error");
    go("tracker-flows", (data as Flow).id);
  };
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!flows) return <Loading />;
  return (
    <div className="space-y-4">
      <PageHeader
        title="Flows"
        description="Paths, landers and offers several campaigns share: change a flow once and every campaign on it follows."
        action={canManage && <Button variant="primary" onClick={() => void create()}>New flow</Button>}
      />
      {flows.length === 0 ? (
        <Empty title="No flows yet">Make a flow when several campaigns should send their clicks the same way, then pick it on each campaign's Destination tab.</Empty>
      ) : (
        <Card title={`${flows.length} flow${flows.length === 1 ? "" : "s"}`}>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  <th className="py-2 pr-3 font-medium">Flow</th>
                  <th className="px-3 py-2 font-medium">Paths</th>
                  <th className="px-3 py-2 text-right font-medium">Destinations</th>
                  <th className="px-3 py-2 text-right font-medium">Used by</th>
                </tr>
              </thead>
              <tbody>
                {flows.map((f) => (
                  <tr key={f.id} className="cursor-pointer border-t hover:bg-muted/40" onClick={() => go("tracker-flows", f.id)}>
                    <td className="py-2 pr-3 font-medium">{f.name}</td>
                    <td className="px-3 py-2 text-muted-foreground">{f.paths.length === 0 ? "Everyone" : f.paths.map((p) => p.name || sentence(p.conditions)).join(" · ")}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{f.destinations.length + f.paths.reduce((n, p) => n + p.destinations.length, 0)}</td>
                    <td className="px-3 py-2 text-right tabular-nums">{f.links === 0 ? "—" : `${f.links} link${f.links === 1 ? "" : "s"}`}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </div>
  );
}

function FlowPage({ id, canManage, go }: Props & { id: string }) {
  const brand = pickedBrandId();
  const [flow, setFlow] = useState<Flow | null>(null);
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/flows/{id}", { params: { path: { brand, id } } });
    if (error) return setError(errorMessage(error));
    setFlow(data as Flow);
    setName((data as Flow).name);
  }, [brand, id]);
  useEffect(() => void load(), [load]);
  const backend = useMemo(() => flowRouting(brand, id), [brand, id]);
  if (error) return <ErrorText>{error}</ErrorText>;
  if (!flow) return <Loading />;
  const links = flow.links;
  const rename = async () => {
    const { data, error } = await api.PATCH("/v1/brands/{brand}/flows/{id}", { params: { path: { brand, id } }, body: { name: name.trim() } });
    if (error) return toast(errorMessage(error), "error");
    setFlow(data as Flow);
    toast("Flow renamed", "ok");
  };
  const archive = async () => {
    if (!(await confirmAction(`Archive ${flow.name}?`, "It leaves the list. A flow campaigns still route by can't be archived: move them to their own paths or another flow first.", "Archive"))) return;
    const { error } = await api.PATCH("/v1/brands/{brand}/flows/{id}", { params: { path: { brand, id } }, body: { status: "archived" } });
    if (error) return toast(errorMessage(error), "error");
    toast("Flow archived", "ok");
    go("tracker-flows");
  };
  return (
    <div className="space-y-4">
      <PageHeader
        title={flow.name}
        description={links === 0 ? "No campaign uses it yet: pick it on a campaign's Destination tab." : `Used by ${links} link${links === 1 ? "" : "s"}: a change here changes them all.`}
        back={{ label: "Flows", onClick: () => go("tracker-flows") }}
        action={canManage && <Button variant="ghost" onClick={() => void archive()}>Archive</Button>}
      />
      <Card title="Name">
        <form className="flex flex-wrap gap-2" onSubmit={(e) => { e.preventDefault(); void rename(); }}>
          <input aria-label="Flow name" className={`${field} min-w-48 flex-1`} value={name} disabled={!canManage} onChange={(e) => setName(e.target.value)} />
          {canManage && <Button type="submit" variant="ghost" disabled={!name.trim() || name.trim() === flow.name}>Rename</Button>}
        </form>
      </Card>
      <Routing
        backend={backend}
        canManage={canManage}
        onSaved={() => void load()}
        confirm={links > 1 ? () => confirmAction(`Change ${links} links?`, `${links} links route by "${flow.name}": saving changes where all of their clicks go.`, "Save for all", false) : undefined}
      />
    </div>
  );
}

// flowRouting loads and saves a flow's paths and destinations.
function flowRouting(brand: string, id: string): RoutingBackend {
  const path = { brand, id };
  return {
    load: async () => {
      const [f, sg] = await Promise.all([
        api.GET("/v1/brands/{brand}/flows/{id}", { params: { path } }),
        api.GET("/v1/brands/{brand}/flows/{id}/weight-suggestions", { params: { path } }),
      ]);
      const flow = f.data as Flow | undefined;
      return { error: f.error, destinations: flow?.destinations ?? [], paths: flow?.paths ?? [], needsDomain: !!flow?.needs_domain, suggestions: (sg.data as Schemas["TrackerWeightSuggestions"] | undefined) ?? null };
    },
    save: async (destinations, paths) => {
      const r1 = await api.PUT("/v1/brands/{brand}/flows/{id}/destinations", { params: { path }, body: { destinations } as never });
      if (r1.error) return r1.error;
      return (await api.PUT("/v1/brands/{brand}/flows/{id}/paths", { params: { path }, body: { paths } as never })).error;
    },
    apply: async (weights) => (await api.PATCH("/v1/brands/{brand}/flows/{id}/weights", { params: { path }, body: { source: "suggestion", weights } })).error,
  };
}
