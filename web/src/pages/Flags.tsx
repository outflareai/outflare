// Feature flags: switch something on for some visitors, or split them
// between versions to see which works better. The website script and the
// workspace's servers read them; each visitor keeps their variant.

import { useCallback, useEffect, useState, type FormEvent } from "react";
import type { Flag, FlagRule, FlagVariant } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, confirmAction, CopyButton, Empty, ErrorText, field, Loading, Menu, PageHeader, toast } from "../ui";
import { ago } from "../format";
import { useBrand } from "@/lib/brand";
import { BrandBadge } from "@/components/brand-badge";
import { DataTable } from "@/components/data-table";
import { Switch } from "@/components/ui/switch";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { Tab } from "@/lib/paths";
import { testFlag } from "./ABTests";

type Props = { ws: string; canManage: boolean; go: (t: Tab) => void };

// What a rule can look at, in words.
const attrs: { id: string; label: string; hint?: string }[] = [
  { id: "path", label: "Page path", hint: "/pricing" },
  { id: "host", label: "Site", hint: "northpeak.example" },
  { id: "query.", label: "Address parameter", hint: "utm_source" },
  { id: "referrer", label: "Came from", hint: "google." },
  { id: "device", label: "Device" },
  { id: "language", label: "Browser language", hint: "en" },
  { id: "url", label: "Full address", hint: "https://" },
  { id: "signed_in", label: "Signed in (servers)" },
  { id: "property.", label: "Property (servers)", hint: "plan" },
];
const ops: Record<FlagRule["op"], string> = {
  is: "is", is_not: "is not", contains: "contains", not_contains: "doesn't contain", starts_with: "starts with", ends_with: "ends with", exists: "is there", not_exists: "isn't there",
};
const attrOf = (a: string) => (a.startsWith("query.") ? "query." : a.startsWith("property.") ? "property." : a);
const attrLabel = (a: string) => {
  const base = attrs.find((x) => x.id === attrOf(a))?.label ?? a;
  return a.endsWith(".") || !a.includes(".") ? base : `${base} ${a.slice(a.indexOf(".") + 1)}`;
};

export function ruleText(r: FlagRule) {
  const vals = r.op === "exists" || r.op === "not_exists" ? "" : ` ${(r.values ?? []).join(" or ")}`;
  return `${attrLabel(r.attribute)} ${ops[r.op]}${vals}`;
}

const isSplit = (f: Pick<Flag, "variants">) => f.variants.length > 1;
const splitText = (vs: FlagVariant[]) => {
  const total = vs.reduce((n, v) => n + v.weight, 0) || 1;
  return vs.map((v) => `${v.key} ${Math.round((v.weight / total) * 100)}%`).join(" · ");
};

type Group = { name: string; holdout: number; free: number; flags: { key: string; name: string; status: string; share: number; start: number }[] };

// GroupsSheet lists the exclusion groups: which flags share each one's
// visitors, its holdout, and the holdout against everyone else on a goal.
function GroupsSheet({ ws, canManage, onClose }: { ws: string; canManage: boolean; onClose: () => void }) {
  const [groups, setGroups] = useState<Group[] | null>(null);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    api.GET("/v1/workspaces/{ws}/flag-groups", { params: { path: { ws } } }).then(({ data, error }) => {
      setError(error ? errorMessage(error) : "");
      setGroups((data?.data as Group[] | undefined) ?? []);
    });
  }, [ws]);
  useEffect(() => load(), [load]);
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>Exclusion groups</SheetTitle>
          <SheetDescription>Flags in one group split its visitors, so no visitor is in two of their A/B tests. A holdout keeps some visitors out of every flag in the group, to measure what its changes did together.</SheetDescription>
        </SheetHeader>
        <div className="space-y-4 px-4 pb-6 text-[13px]">
          <ErrorText>{error}</ErrorText>
          {groups === null ? <Loading /> : groups.map((g) => <GroupCard key={g.name} ws={ws} group={g} canManage={canManage} onChanged={load} />)}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function GroupCard({ ws, group, canManage, onChanged }: { ws: string; group: Group; canManage: boolean; onChanged: () => void }) {
  const [holdout, setHoldout] = useState(group.holdout);
  const [event, setEvent] = useState("");
  const [report, setReport] = useState<{ lift: number; chance: number; holdout: number; rest: number } | null>(null);
  const [error, setError] = useState("");
  const save = async () => {
    const { error } = await api.PUT("/v1/workspaces/{ws}/flag-groups/{group}", { params: { path: { ws, group: group.name } }, body: { holdout } });
    if (error) return setError(errorMessage(error));
    setError("");
    toast(holdout ? `${holdout}% of ${group.name}'s visitors are now held out.` : `${group.name} has no holdout.`);
    onChanged();
  };
  const compare = async (e: FormEvent) => {
    e.preventDefault();
    const { data, error } = await api.GET("/v1/workspaces/{ws}/flag-groups/{group}/holdout", { params: { path: { ws, group: group.name }, query: { event: event.trim() } } });
    if (error || !data) return setError(error ? errorMessage(error) : "");
    setError("");
    setReport({ lift: data.comparison?.lift ?? 0, chance: data.comparison?.chance_to_beat ?? 0.5, holdout: data.arms[0]?.visitors ?? 0, rest: data.arms[1]?.visitors ?? 0 });
  };
  return (
    <Card title={group.name}>
      <div className="space-y-2 text-[12px]">
        <p>
          {group.flags.map((f) => `${f.name} ${f.share}%`).join(", ") || "No flags"}
          {group.holdout > 0 && `, holdout ${group.holdout}%`}, free {group.free}%.
        </p>
        {canManage && (
          <div className="flex items-center gap-2">
            <span>Holdout</span>
            <input aria-label="Holdout" type="number" min={0} max={50} className={`${field} w-20`} value={holdout} onChange={(e) => setHoldout(Number(e.target.value))} />
            <span>%</span>
            <Button variant="ghost" disabled={holdout === group.holdout} onClick={save}>Save</Button>
          </div>
        )}
        {group.holdout > 0 && (
          <form className="flex items-center gap-2" onSubmit={compare}>
            <input aria-label="Goal event" className={`${field} w-48 font-mono`} required placeholder="signup_started" value={event} onChange={(e) => setEvent(e.target.value)} />
            <Button variant="ghost">Compare with the holdout</Button>
          </form>
        )}
        <ErrorText>{error}</ErrorText>
        {report && (
          <p>
            Over the last 28 days, everyone else did {report.lift >= 0 ? "+" : ""}
            {Math.round(report.lift * 100)}% against the holdout ({Math.round(report.chance * 100)}% likely better), from {report.rest.toLocaleString()} and {report.holdout.toLocaleString()} visitors.
          </p>
        )}
      </div>
    </Card>
  );
}

export function Flags({ ws, canManage, go }: Props) {
  const { query } = useBrand(ws);
  const [flags, setFlags] = useState<Flag[] | null>(null);
  const [editing, setEditing] = useState<Flag | "new" | null>(null);
  const [groupsOpen, setGroupsOpen] = useState(false);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/flags", { params: { path: { ws }, query } });
    if (error) setError(errorMessage(error));
    setFlags(data?.data ?? []);
  }, [ws, query.brand]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    load();
  }, [load]);

  async function toggle(f: Flag, on: boolean) {
    setFlags((list) => list?.map((x) => (x.id === f.id ? { ...x, status: on ? "on" : "off" } : x)) ?? null);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/flags/{flag}", { params: { path: { ws, flag: f.id } }, body: { status: on ? "on" : "off" } });
    if (error) {
      toast(errorMessage(error), "error");
      return load();
    }
    toast(on ? `${f.name} is on. Visitors get it within 10 seconds.` : `${f.name} is off.`);
  }

  async function archive(f: Flag) {
    if (!(await confirmAction(`Archive ${f.name}?`, "It turns off for good, and its key is free to use again. Code that still asks for it gets false.", "Archive flag"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/flags/{flag}", { params: { path: { ws, flag: f.id } } });
    if (error) return toast(errorMessage(error), "error");
    toast(`Archived ${f.name}.`);
    load();
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Feature flags"
        description="Turn something on for some visitors, or split them between versions to see which works better. Your website and your servers read them, and each visitor keeps the version they first got."
        action={
          <div className="flex gap-2">
            {flags?.some((f) => f.exclusion_group) && <Button variant="ghost" onClick={() => setGroupsOpen(true)}>Exclusion groups</Button>}
            {canManage && <Button onClick={() => setEditing("new")}>New flag</Button>}
          </div>
        }
      />
      {groupsOpen && <GroupsSheet ws={ws} canManage={canManage} onClose={() => setGroupsOpen(false)} />}
      <ErrorText>{error}</ErrorText>
      {flags === null ? (
        <Loading />
      ) : flags.length === 0 ? (
        <Empty title="No flags yet" action={canManage && <Button onClick={() => setEditing("new")}>New flag</Button>}>
          Try a new headline on half your visitors, or show a banner only to people from one campaign. Flags need Outflare's script on your site (<button className="underline underline-offset-2" onClick={() => go("tracking")}>Website tracking</button>).
        </Empty>
      ) : (
        <DataTable
          data={flags}
          rowId={(f) => f.id}
          searchPlaceholder="Search flags"
          onRowClick={canManage ? (f) => setEditing(f) : undefined}
          mobile={(f) => (
            <div className="space-y-0.5">
              <div className="font-medium">{f.name} {f.status === "on" ? <Badge tone="green">On</Badge> : <Badge>Off</Badge>}</div>
              <div className="text-[12px] text-muted-foreground">{isSplit(f) ? splitText(f.variants) : "Switch"} · {f.rollout}% of visitors</div>
            </div>
          )}
          columns={[
            {
              id: "on", header: "On", accessorFn: (f) => f.status,
              cell: ({ row: { original: f } }) => (
                <span onClick={(e) => e.stopPropagation()}>
                  <Switch aria-label={`Turn ${f.name} ${f.status === "on" ? "off" : "on"}`} checked={f.status === "on"} disabled={!canManage} onCheckedChange={(on) => toggle(f, on)} />
                </span>
              ),
            },
            {
              id: "name", header: "Flag", accessorFn: (f) => `${f.name} ${f.key}`,
              cell: ({ row: { original: f } }) => (
                <div>
                  <div className="font-medium">{f.name} <BrandBadge id={f.brand_id} /> {!f.on_site && <Badge tone="blue" title="Not sent to the website script">Servers only</Badge>} {f.exclusion_group && <Badge title="Exclusion group: no visitor is in two of its flags' tests">{f.exclusion_group} · {f.group_share}%</Badge>}</div>
                  <div className="font-mono text-[12px] text-muted-foreground">{f.key}</div>
                </div>
              ),
            },
            { id: "split", header: "Versions", accessorFn: (f) => (isSplit(f) ? splitText(f.variants) : "Switch"), cell: ({ row: { original: f } }) => (isSplit(f) ? <span className="font-mono text-[12px]">{splitText(f.variants)}</span> : <span className="text-muted-foreground">Switch</span>) },
            { id: "who", header: "Who", accessorFn: (f) => f.rules.map(ruleText).join(", "), cell: ({ row: { original: f } }) => <span className="text-muted-foreground">{f.rollout < 100 ? `${f.rollout}% of ` : ""}{f.rules.length ? f.rules.map(ruleText).join(", and ") : "everyone"}</span> },
            { id: "updated", header: "Changed", accessorFn: (f) => f.updated_at, cell: ({ row: { original: f } }) => <span className="text-muted-foreground">{ago(f.updated_at)}</span> },
            {
              id: "menu", header: "", enableSorting: false,
              cell: ({ row: { original: f } }) => canManage && (
                <span onClick={(e) => e.stopPropagation()}>
                  <Menu items={[{ label: "Edit", onClick: () => setEditing(f) }, ...(isSplit(f) ? [{ label: "A/B test it", onClick: () => { testFlag(f.key); go("ab-tests"); } }] : []), { label: "Archive", danger: true, onClick: () => archive(f) }]} />
                </span>
              ),
            },
          ]}
        />
      )}
      <UseIt />
      {editing && (
        <FlagSheet ws={ws} flag={editing === "new" ? null : editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); load(); }} />
      )}
    </div>
  );
}

// How to read a flag, on the site and on a server.
function UseIt() {
  const site = `<script async src=".../site.js" data-key="ofk_..." data-flags></script>

<script>
  window.outflare = window.outflare || [];
  // Runs once the visitor's flags are known, whether or not the script has loaded yet.
  function onFlags(fn) { outflare.onFlags ? outflare.onFlags(fn) : outflare.push(["onFlags", fn]); }

  onFlags(function (flags) {
    if (flags["pricing-headline"] === "faster") {
      document.querySelector("h1").textContent = outflare.flagPayload("pricing-headline").headline;
    }
  });
</script>`;
  const css = `<script>document.documentElement.classList.add("of-wait"); setTimeout(function () { document.documentElement.classList.remove("of-wait"); }, 1500);</script>
<style>.of-wait body { opacity: 0 } .of-flag-pricing-headline-faster .old-headline { display: none }</style>`;
  return (
    <Card title="Use flags on your site" description="Add data-flags to your tracking script tag. The page then knows each visitor's versions; you can use them in a script, or in CSS through the class Outflare puts on the page (of-flag-<flag>-<version>).">
      <div className="space-y-4 text-[13px]">
        <div className="relative">
          <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 font-mono text-[12px] leading-relaxed">{site}</pre>
          <CopyButton text={site} className="absolute top-2 right-2" />
        </div>
        <p className="text-muted-foreground">So visitors don't see the original flash before their version, hide the page until the flags arrive (at most one and a half seconds), with this in your <code>&lt;head&gt;</code>:</p>
        <div className="relative">
          <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 font-mono text-[12px] leading-relaxed">{css}</pre>
          <CopyButton text={css} className="absolute top-2 right-2" />
        </div>
        <p className="text-muted-foreground">To see a version yourself, add <code>?of_flag=pricing-headline:faster</code> to any page's address; it sticks for the rest of your visit. Servers ask with an API token: <code>POST /v1/workspaces/&#123;ws&#125;/flags/evaluate</code> with the visitor's <code>anonymous_id</code>, so they agree with the page.</p>
      </div>
    </Card>
  );
}

const slug = (s: string) => s.toLowerCase().trim().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 64);

function FlagSheet({ ws, flag, onClose, onSaved }: { ws: string; flag: Flag | null; onClose: () => void; onSaved: () => void }) {
  const { brand, active } = useBrand(ws);
  const [name, setName] = useState(flag?.name ?? "");
  const [key, setKey] = useState(flag?.key ?? "");
  const [keyTouched, setKeyTouched] = useState(!!flag);
  const [description, setDescription] = useState(flag?.description ?? "");
  const [brandID, setBrandID] = useState(flag ? flag.brand_id ?? "" : brand?.id ?? "");
  const [split, setSplit] = useState(flag ? isSplit(flag) : false);
  const [variants, setVariants] = useState<FlagVariant[]>(flag && isSplit(flag) ? flag.variants : [{ key: "control", weight: 50 }, { key: "test", weight: 50 }]);
  const [payloads, setPayloads] = useState<string[]>(() => (flag && isSplit(flag) ? flag.variants : []).map((v) => (v.payload == null ? "" : JSON.stringify(v.payload, null, 2))));
  const [rollout, setRollout] = useState(flag?.rollout ?? 100);
  const [rules, setRules] = useState<FlagRule[]>(flag?.rules ?? []);
  const [onSite, setOnSite] = useState(flag?.on_site ?? true);
  const [group, setGroup] = useState(flag?.exclusion_group ?? "");
  const [groupShare, setGroupShare] = useState(flag?.group_share || 50);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [editPage, setEditPage] = useState("");

  // The visual editor opens on a page of the site in a new tab. The sheet
  // closes, so saving it later can't undo what was saved in the editor.
  async function editVisually(variant: string) {
    if (!flag) return;
    setError("");
    if (!/^https?:\/\/[^/\s]+/i.test(editPage.trim())) return setError("Enter the address of a page on your site to edit, starting with https://.");
    const win = window.open("about:blank", "_blank");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/flags/{flag}/edit-link", { params: { path: { ws, flag: flag.id } }, body: { variant, url: editPage.trim() } });
    if (error || !data) {
      win?.close();
      return setError(errorMessage(error));
    }
    if (win) {
      win.opener = null;
      win.location.href = data.url;
    } else window.location.href = data.url;
    toast(`The editor is open on your page for ${variant}. Save there; visitors in it see the changes.`);
    onClose();
  }

  const setVariant = (i: number, v: Partial<FlagVariant>) => setVariants((vs) => vs.map((x, k) => (k === i ? { ...x, ...v } : x)));
  const setRule = (i: number, r: Partial<FlagRule>) => setRules((rs) => rs.map((x, k) => (k === i ? { ...x, ...r } : x)));

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError("");
    let vs: FlagVariant[] = [{ key: "on", weight: 100 }];
    if (split) {
      try {
        vs = variants.map((v, i) => ({ key: v.key, weight: v.weight, payload: payloads[i]?.trim() ? JSON.parse(payloads[i]) : null }));
      } catch {
        return setError("A version's content must be JSON, such as {\"headline\": \"Ship faster\"} or \"Ship faster\" in quotes.");
      }
    }
    setBusy(true);
    const g = split ? group.trim() : "";
    const body = { name, description, brand_id: brandID || null, variants: vs, rollout, rules, on_site: onSite, exclusion_group: g, ...(g ? { group_share: groupShare } : {}) };
    const res = flag
      ? await api.PATCH("/v1/workspaces/{ws}/flags/{flag}", { params: { path: { ws, flag: flag.id } }, body })
      : await api.POST("/v1/workspaces/{ws}/flags", { params: { path: { ws } }, body: { ...body, key } });
    setBusy(false);
    if (res.error) return setError(errorMessage(res.error));
    toast(flag ? "Saved. Visitors get the change within 10 seconds." : `Added ${res.data?.name}. It's off until you turn it on.`);
    onSaved();
  }

  const total = variants.reduce((n, v) => n + v.weight, 0) || 1;
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{flag ? `Edit ${flag.name}` : "New flag"}</SheetTitle>
          <SheetDescription>A switch turns something on for the visitors you pick. A split gives each of them one version, so you can see which works better.</SheetDescription>
        </SheetHeader>
        <form onSubmit={submit} className="space-y-6 px-4 pb-6 text-[13px]">
          <div className="space-y-3">
            <label className="block">
              <span className="mb-1 block font-medium">Name</span>
              <input className={`${field} w-full`} required maxLength={100} placeholder="e.g. Pricing headline" value={name} onChange={(e) => { setName(e.target.value); if (!keyTouched) setKey(slug(e.target.value)); }} />
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Key</span>
              <input className={`${field} w-full font-mono`} required disabled={!!flag} pattern="[a-z0-9][a-z0-9_\-]{0,63}" placeholder="pricing-headline" value={key} onChange={(e) => { setKey(e.target.value); setKeyTouched(true); }} />
              <span className="mt-1 block text-[12px] text-muted-foreground">What your code asks for. It can't change later.</span>
            </label>
            <label className="block">
              <span className="mb-1 block font-medium">Description <span className="font-normal text-muted-foreground">(optional)</span></span>
              <input className={`${field} w-full`} maxLength={1000} value={description} onChange={(e) => setDescription(e.target.value)} />
            </label>
            {active.length > 0 && (
              <label className="block">
                <span className="mb-1 block font-medium">Brand</span>
                <select className={`${field} w-full`} value={brandID} onChange={(e) => setBrandID(e.target.value)}>
                  <option value="">Every brand</option>
                  {active.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
                </select>
                <span className="mt-1 block text-[12px] text-muted-foreground">{brandID ? "Only this brand's site gets it." : "Every brand's site gets it."}</span>
              </label>
            )}
          </div>

          <div className="space-y-2">
            <span className="block font-medium">Kind</span>
            <div className="flex gap-2">
              <Button type="button" variant={split ? "ghost" : "primary"} onClick={() => setSplit(false)}>Switch</Button>
              <Button type="button" variant={split ? "primary" : "ghost"} onClick={() => setSplit(true)}>Split between versions</Button>
            </div>
            {split && (
              <div className="space-y-2">
                {variants.map((v, i) => (
                  <div key={i} className="space-y-2 rounded-md border p-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <input aria-label="Version" className={`${field} w-40 font-mono`} required pattern="[a-z0-9][a-z0-9_\-]{0,63}" value={v.key} onChange={(e) => setVariant(i, { key: e.target.value })} />
                      <input aria-label="Weight" type="number" min={0} max={100} className={`${field} w-20`} value={v.weight} onChange={(e) => setVariant(i, { weight: Number(e.target.value) })} />
                      <span className="text-muted-foreground tabular-nums">{Math.round((v.weight / total) * 100)}% of visitors</span>
                      {variants.length > 2 && <Button type="button" variant="ghost" className="ml-auto" onClick={() => { setVariants((vs) => vs.filter((_, k) => k !== i)); setPayloads((ps) => ps.filter((_, k) => k !== i)); }}>Remove</Button>}
                    </div>
                    {flag?.variants.some((x) => x.key === v.key) && flag.status !== "archived" && (
                      <Button type="button" variant="ghost" disabled={busy} onClick={() => editVisually(v.key)}>Edit visually</Button>
                    )}
                    <textarea aria-label="Content" className={`${field} w-full font-mono text-[12px]`} rows={2} placeholder='Content for this version (optional JSON), e.g. {"headline": "Ship faster"}' value={payloads[i] ?? ""} onChange={(e) => setPayloads((ps) => { const n = [...ps]; n[i] = e.target.value; return n; })} />
                  </div>
                ))}
                {variants.length < 10 && <Button type="button" variant="ghost" onClick={() => setVariants((vs) => [...vs, { key: `test-${vs.length}`, weight: 50 }])}>Add a version</Button>}
                {flag && isSplit(flag) && (
                  <label className="block">
                    <span className="mb-1 block font-medium">Page to edit on</span>
                    <input className={`${field} w-full`} type="url" placeholder="https://northpeak.example/pricing" value={editPage} onChange={(e) => setEditPage(e.target.value)} />
                    <span className="mt-1 block text-[12px] text-muted-foreground">Edit visually opens this page with the version and an editor over it: click anything to change its text, hide it, or change a link or image. The page needs the website script with data-flags. The link works for 2 hours.</span>
                  </label>
                )}
                {flag && flag.status === "on" && <p className="text-[12px] text-muted-foreground">Changing the split while it's on moves some visitors to another version.</p>}
                <div className="space-y-1 rounded-md border p-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <input aria-label="Exclusion group" className={`${field} w-40 font-mono`} pattern="[a-z0-9][a-z0-9_\-]{0,63}" placeholder="exclusion group" value={group} onChange={(e) => setGroup(e.target.value)} />
                    {group.trim() && (
                      <>
                        <input aria-label="Share of the group" type="number" min={1} max={100} className={`${field} w-20`} value={groupShare} onChange={(e) => setGroupShare(Number(e.target.value))} />
                        <span className="text-muted-foreground">% of the group's visitors</span>
                      </>
                    )}
                  </div>
                  <p className="text-[12px] text-muted-foreground">Flags in one exclusion group never share a visitor, so their A/B tests can run at the same time without affecting each other. Fixed while this flag's test runs.</p>
                </div>
              </div>
            )}
          </div>

          <div className="space-y-2">
            <span className="block font-medium">Who gets it</span>
            {rules.map((r, i) => {
              const a = attrOf(r.attribute);
              const valueless = r.op === "exists" || r.op === "not_exists";
              return (
                <div key={i} className="flex flex-wrap items-center gap-2 rounded-md border p-2">
                  <select aria-label="Looks at" className={field} value={a} onChange={(e) => setRule(i, { attribute: e.target.value, op: e.target.value === "device" || e.target.value === "signed_in" ? "is" : r.op, values: e.target.value === "device" ? ["mobile"] : e.target.value === "signed_in" ? ["true"] : r.values })}>
                    {attrs.map((x) => <option key={x.id} value={x.id}>{x.label}</option>)}
                  </select>
                  {(a === "query." || a === "property.") && (
                    <input aria-label="Name" className={`${field} w-32 font-mono`} required placeholder={attrs.find((x) => x.id === a)?.hint} value={r.attribute.slice(a.length)} onChange={(e) => setRule(i, { attribute: a + e.target.value.trim() })} />
                  )}
                  {a === "device" ? (
                    <select aria-label="Device" className={field} value={r.values?.[0] ?? "mobile"} onChange={(e) => setRule(i, { op: "is", values: [e.target.value] })}>
                      <option value="mobile">is a phone</option>
                      <option value="tablet">is a tablet</option>
                      <option value="desktop">is a computer</option>
                    </select>
                  ) : a === "signed_in" ? (
                    <select aria-label="Signed in" className={field} value={r.values?.[0] ?? "true"} onChange={(e) => setRule(i, { op: "is", values: [e.target.value] })}>
                      <option value="true">yes</option>
                      <option value="false">no</option>
                    </select>
                  ) : (
                    <>
                      <select aria-label="Test" className={field} value={r.op} onChange={(e) => setRule(i, { op: e.target.value as FlagRule["op"], values: e.target.value === "exists" || e.target.value === "not_exists" ? [] : r.values })}>
                        {(Object.keys(ops) as FlagRule["op"][]).map((o) => <option key={o} value={o}>{ops[o]}</option>)}
                      </select>
                      {!valueless && (
                        <input aria-label="Values" className={`${field} min-w-40 flex-1`} required placeholder={`${attrs.find((x) => x.id === a)?.hint ?? ""} (comma between several)`} value={(r.values ?? []).join(", ")} onChange={(e) => setRule(i, { values: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) })} />
                      )}
                    </>
                  )}
                  <Button type="button" variant="ghost" className="ml-auto" onClick={() => setRules((rs) => rs.filter((_, k) => k !== i))}>Remove</Button>
                </div>
              );
            })}
            {rules.length < 20 && <Button type="button" variant="ghost" onClick={() => setRules((rs) => [...rs, { attribute: "path", op: "starts_with", values: [] }])}>{rules.length ? "Add another condition" : "Only some visitors…"}</Button>}
            <p className="text-[12px] text-muted-foreground">{rules.length ? "Visitors who match every condition." : "Every visitor."}</p>
            <label className="flex items-center gap-3">
              <span className="font-medium">Of those, give it to</span>
              <input aria-label="Rollout" type="range" min={0} max={100} step={5} value={rollout} onChange={(e) => setRollout(Number(e.target.value))} className="flex-1" />
              <span className="w-12 text-right font-mono tabular-nums">{rollout}%</span>
            </label>
          </div>

          <label className="flex items-start gap-2">
            <input type="checkbox" className="mt-0.5" checked={onSite} onChange={(e) => setOnSite(e.target.checked)} />
            <span>
              <span className="font-medium">Send to the website</span>
              <span className="block text-[12px] text-muted-foreground">Off keeps a flag only your servers read out of your pages, so visitors can't see it exists.</span>
            </span>
          </label>

          <ErrorText>{error}</ErrorText>
          <div className="flex flex-wrap gap-2 border-t pt-4">
            <Button disabled={busy || !name.trim() || !key}>{busy ? "Saving…" : flag ? "Save" : "Add flag"}</Button>
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            {flag && <span className="ml-auto self-center text-[12px] text-muted-foreground">Created {ago(flag.created_at)}</span>}
          </div>
        </form>
      </SheetContent>
    </Sheet>
  );
}
