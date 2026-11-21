import { PickBrand } from "@/components/pick-brand";
import { pickedBrandId, useBrand } from "@/lib/brand";
// Named clicks: turn a click the website script captured into an event
// with a name of its own, such as Signup Clicked, without touching the
// site. The page lists the brand's named clicks and, below, the clicks the
// site captured lately, each with a button to name it. Naming and editing
// open a page of their own.

import { useCallback, useEffect, useState } from "react";
import type { ClickAction, paths } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { Switch } from "@/components/ui/switch";
import { api, errorMessage } from "../api";
import { Badge, Button, confirmAction, CopyButton, Empty, ErrorText, field, Input, Loading, PageHeader, Stat, Stats, toast } from "../ui";
import type { Tab } from "../App";

type Props = { ws: string; canManage: boolean; go: (tab: Tab, item?: string) => void; item?: string };
type List = paths["/v1/brands/{brand}/click-actions"]["get"]["responses"]["200"]["content"]["application/json"];
type Seen = paths["/v1/brands/{brand}/click-actions/seen"]["get"]["responses"]["200"]["content"]["application/json"]["data"][number];
type Draft = { name: string; label: string; label_match: "exact" | "contains"; href_contains: string; element_id: string; class: string; path: string };

const blank: Draft = { name: "", label: "", label_match: "exact", href_contains: "", element_id: "", class: "", path: "" };
// A click picked from the list waits here while the form opens.
let picked: Draft | null = null;

// rule says in words which click a named click is.
function rule(a: Pick<ClickAction, "label" | "label_match" | "href_contains" | "element_id" | "class" | "path">): string {
  const parts: string[] = [];
  if (a.label) parts.push(a.label_match === "contains" ? `label contains "${a.label}"` : `label is "${a.label}"`);
  if (a.href_contains) parts.push(`goes to an address with "${a.href_contains}"`);
  if (a.element_id) parts.push(`id is ${a.element_id}`);
  if (a.class) parts.push(`has the class ${a.class}`);
  if (a.path) parts.push(`on ${a.path}`);
  return parts.join(", ");
}

export function NamedClicks(props: Props) {
  const { brand, active } = useBrand(props.ws);
  if (!brand) return <PickBrand what="Named clicks" brands={active.length} />;
  if (props.item) return <Form key={`${brand.id}${props.item}`} id={props.item} go={props.go} canManage={props.canManage} />;
  return <Overview key={brand.id} {...props} />;
}

function Overview({ go, canManage }: Props) {
  const [data, setData] = useState<List | null>(null);
  const [seen, setSeen] = useState<Seen[] | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    const brand = pickedBrandId();
    api.GET("/v1/brands/{brand}/click-actions", { params: { path: { brand } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      setData(data);
    });
    api.GET("/v1/brands/{brand}/click-actions/seen", { params: { path: { brand } } }).then(({ data }) => setSeen(data?.data ?? []));
  }, []);
  useEffect(load, [load]);

  const toggle = async (a: ClickAction, enabled: boolean) => {
    const { error } = await api.PATCH("/v1/brands/{brand}/click-actions/{id}", { params: { path: { brand: pickedBrandId(), id: a.id } }, body: { enabled } });
    if (error) return toast(errorMessage(error), "error");
    toast(enabled ? `${a.name} is on.` : `${a.name} is off. Clicks are no longer stored under this name.`);
    load();
  };
  const remove = async (a: ClickAction) => {
    if (!(await confirmAction(`Delete ${a.name}?`, <p>New clicks stop being stored as {a.name}. The events it already stored stay in your reports.</p>, "Delete"))) return;
    const { error } = await api.DELETE("/v1/brands/{brand}/click-actions/{id}", { params: { path: { brand: pickedBrandId(), id: a.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`${a.name} deleted.`);
    load();
  };
  const name = (c: Seen) => {
    picked = { ...blank, label: c.text, href_contains: c.text ? "" : c.href, element_id: c.text ? "" : c.id };
    go("named-clicks", "new");
  };

  const header = (
    <PageHeader
      title="Named clicks"
      description="Give a button or link on your site an event name, such as Signup Clicked, with no change to the site. Use the name in funnels, trends and A/B tests."
      action={canManage ? <Button onClick={() => { picked = null; go("named-clicks", "new"); }}>Name a click</Button> : undefined}
    />
  );
  if (error) return <div>{header}<ErrorText>{error}</ErrorText></div>;
  if (!data) return <div>{header}<Loading /></div>;
  return (
    <div className="space-y-6">
      {header}
      {!data.capturing && (
        <Empty title="Click capture is off" action={<Button onClick={() => go("tracking")}>Open Website tracking</Button>}>
          Named clicks work from the clicks your site captures. Open Website tracking, press Edit on your site and switch on "Capture clicks". Your script stays the same.
        </Empty>
      )}
      <Stats cols={3}>
        <Stat label="Named clicks" value={data.data.length.toLocaleString()} sub={`${data.data.filter((a) => a.enabled).length.toLocaleString()} on`} />
        <Stat label="Clicks captured" value={data.clicks_7d.toLocaleString()} sub="Last 7 days" />
        <Stat label="Click capture" value={data.capturing ? "On" : "Off"} tone={data.capturing ? "green" : "amber"} />
      </Stats>

      <section aria-label="Named clicks">
        <h2 className="mb-2 text-[13px] font-medium">Your named clicks</h2>
        <DataTable
          data={data.data}
          rowId={(a: ClickAction) => a.id}
          onRowClick={canManage ? (a: ClickAction) => go("named-clicks", a.id) : undefined}
          columnsMenu={false}
          searchPlaceholder="Search named clicks"
          empty="No named clicks yet. Pick one from the clicks below, or press Name a click."
          columns={[
            { id: "name", header: "Event name", accessorFn: (a: ClickAction) => a.name, cell: ({ row: { original: a } }: { row: { original: ClickAction } }) => (
              <span className="block max-w-[22rem]">
                <span className="block truncate font-medium">{a.name}</span>
                <span className="block truncate font-mono text-[12px] text-muted-foreground">{a.event}</span>
              </span>
            ) },
            { id: "rule", header: "Which click", accessorFn: (a: ClickAction) => rule(a), cell: ({ row: { original: a } }: { row: { original: ClickAction } }) => <span className="block max-w-[28rem] truncate" title={rule(a)}>{rule(a)}</span> },
            { id: "status", header: "Status", accessorFn: (a: ClickAction) => (a.enabled ? "On" : "Off"), cell: ({ row: { original: a } }: { row: { original: ClickAction } }) => <Badge tone={a.enabled ? "green" : "grey"}>{a.enabled ? "On" : "Off"}</Badge> },
            { id: "matches", header: "Clicks, 7 days", accessorFn: (a: ClickAction) => a.matches_7d, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: a } }: { row: { original: ClickAction } }) => a.matches_7d.toLocaleString() },
            ...(canManage ? [{ id: "act", header: "", enableSorting: false, cell: ({ row: { original: a } }: { row: { original: ClickAction } }) => (
              <span className="flex items-center justify-end gap-2" onClick={(e) => e.stopPropagation()}>
                <Switch checked={a.enabled} onCheckedChange={(on: boolean) => toggle(a, on)} aria-label={`${a.name} on or off`} />
                <CopyButton text={a.event} label="Copy name" />
                <Button variant="ghost" onClick={() => go("named-clicks", a.id)}>Edit</Button>
                <Button variant="ghost" onClick={() => remove(a)}>Delete</Button>
              </span>
            ) }] : []),
          ]}
        />
      </section>

      <section aria-label="Clicks your site captured">
        <h2 className="mb-1 text-[13px] font-medium">Clicks your site captured in the last 7 days</h2>
        <p className="mb-2 text-[13px] text-muted-foreground">Every button and link people clicked, most clicked first. Name the ones you care about.</p>
        {!seen ? <Loading /> : (
          <DataTable
            data={seen}
            rowId={(c: Seen) => `${c.tag}|${c.text}|${c.href}|${c.id}`}
            columnsMenu={false}
            searchPlaceholder="Search clicks"
            empty={data.capturing ? "No clicks captured yet. They show here a minute or so after someone clicks." : "Nothing captured: click capture is off."}
            initialSort={[{ id: "count", desc: true }]}
            columns={[
              { id: "text", header: "Label", accessorFn: (c: Seen) => c.text, cell: ({ row: { original: c } }: { row: { original: Seen } }) => <span className="block max-w-[20rem] truncate font-medium" title={c.text}>{c.text || "No label"}</span> },
              { id: "tag", header: "Kind", accessorFn: (c: Seen) => c.tag, cell: ({ row: { original: c } }: { row: { original: Seen } }) => (c.tag === "a" ? "Link" : "Button") },
              { id: "href", header: "Goes to", accessorFn: (c: Seen) => c.href, cell: ({ row: { original: c } }: { row: { original: Seen } }) => <span className="block max-w-[20rem] truncate font-mono text-[12px] text-muted-foreground" title={c.href}>{c.href || "—"}</span> },
              { id: "path", header: "Last clicked on", accessorFn: (c: Seen) => c.path, cell: ({ row: { original: c } }: { row: { original: Seen } }) => <span className="font-mono text-[12px]">{c.path || "—"}</span> },
              { id: "count", header: "Clicks", accessorFn: (c: Seen) => c.count, meta: { align: "right" as const, mono: true }, cell: ({ row: { original: c } }: { row: { original: Seen } }) => c.count.toLocaleString() },
              { id: "action", header: "Named", accessorFn: (c: Seen) => c.action, cell: ({ row: { original: c } }: { row: { original: Seen } }) => (
                c.action ? <Badge tone="green">{c.action}</Badge> : canManage ? <Button variant="ghost" onClick={() => name(c)}>Name this click</Button> : <span className="text-muted-foreground">Not named</span>
              ) },
            ]}
          />
        )}
      </section>
    </div>
  );
}

function Form({ id, go, canManage }: { id: string; go: (tab: Tab, item?: string) => void; canManage: boolean }) {
  const isNew = id === "new";
  const [draft, setDraft] = useState<Draft | null>(isNew ? (picked ?? blank) : null);
  const [enabled, setEnabled] = useState(true);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    if (isNew) return;
    api.GET("/v1/brands/{brand}/click-actions", { params: { path: { brand: pickedBrandId() } } }).then(({ data, error }) => {
      const a = data?.data.find((x) => x.id === id);
      if (error || !a) return setError(error ? errorMessage(error) : "This named click no longer exists.");
      setDraft({ name: a.name, label: a.label, label_match: a.label_match, href_contains: a.href_contains, element_id: a.element_id, class: a.class, path: a.path });
      setEnabled(a.enabled);
    });
  }, [id, isNew]);

  const back = { label: "Named clicks", onClick: () => go("named-clicks") };
  const title = isNew ? "Name a click" : "Edit named click";
  if (!draft) return <div><PageHeader title={title} back={back} />{error ? <ErrorText>{error}</ErrorText> : <Loading />}</div>;
  const set = (patch: Partial<Draft>) => setDraft({ ...draft, ...patch });
  const ready = draft.name.trim() !== "" && (draft.label + draft.href_contains + draft.element_id + draft.class).trim() !== "";
  const save = async () => {
    setSaving(true);
    const brand = pickedBrandId();
    const { error } = isNew
      ? await api.POST("/v1/brands/{brand}/click-actions", { params: { path: { brand } }, body: { ...draft, enabled } })
      : await api.PATCH("/v1/brands/{brand}/click-actions/{id}", { params: { path: { brand, id } }, body: { ...draft, enabled } });
    setSaving(false);
    if (error) return setError(errorMessage(error));
    picked = null;
    toast(isNew ? `${draft.name.trim()} saved. Matching clicks are stored under this name from now on.` : "Saved.");
    go("named-clicks");
  };
  return (
    <div className="max-w-2xl space-y-6">
      <PageHeader title={title} back={back} description="A click that matches everything you fill in below is stored as an event with this name, from now on. Earlier clicks are not renamed." />
      <Input label="Event name" hint="How it shows in funnels, trends and A/B tests. For example: Signup Clicked." value={draft.name} maxLength={100} onChange={(e) => set({ name: e.target.value })} disabled={!canManage} autoFocus />
      <div className="space-y-4 rounded-lg border p-4">
        <h2 className="text-[13px] font-medium">Which click is it? Fill in at least one.</h2>
        <div className="flex flex-wrap items-end gap-2">
          <div className="min-w-0 flex-1"><Input label="Label" hint="What the button or link says." value={draft.label} maxLength={200} onChange={(e) => set({ label: e.target.value })} disabled={!canManage} /></div>
          <select aria-label="How the label matches" className={`${field} mb-[22px]`} value={draft.label_match} onChange={(e) => set({ label_match: e.target.value as Draft["label_match"] })} disabled={!canManage}>
            <option value="exact">Is exactly</option>
            <option value="contains">Contains</option>
          </select>
        </div>
        <Input label="Goes to an address containing" hint="For links. A part of the address, such as /signup." value={draft.href_contains} maxLength={500} onChange={(e) => set({ href_contains: e.target.value })} disabled={!canManage} />
        <Input label="Element id" hint="The id attribute in your page's code, if the button has one." value={draft.element_id} maxLength={200} onChange={(e) => set({ element_id: e.target.value })} disabled={!canManage} />
        <Input label="Class" hint="One class name the element has, such as btn-primary." value={draft.class} maxLength={200} onChange={(e) => set({ class: e.target.value })} disabled={!canManage} />
      </div>
      <Input label="Only on this page (optional)" hint="A path such as /pricing. End with * for every page under it, such as /blog/*. Empty means every page." value={draft.path} maxLength={200} onChange={(e) => set({ path: e.target.value })} disabled={!canManage} />
      <label className="flex items-start gap-3 text-[13px]">
        <Switch checked={enabled} onCheckedChange={setEnabled} aria-label="Named click on or off" className="mt-0.5" disabled={!canManage} />
        <span><span className="block font-medium">{enabled ? "On" : "Off"}</span><span className="text-muted-foreground">While it is off, clicks are not stored under this name.</span></span>
      </label>
      {error && <ErrorText>{error}</ErrorText>}
      {canManage && (
        <div className="flex gap-2">
          <Button onClick={save} disabled={!ready || saving}>{saving ? "Saving…" : "Save"}</Button>
          <Button variant="ghost" onClick={() => go("named-clicks")}>Cancel</Button>
        </div>
      )}
    </div>
  );
}
