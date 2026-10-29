// Campaign links: the workspace's one tagging scheme. A campaign fixes the
// tags every link in it carries (channel, source, medium, utm_campaign and
// the short ofc code), and a link is one page in it. Links go straight to
// the page; the website tracking snippet reads the tags there. A link can
// also have a short redirect link that counts clicks, for places the
// snippet can't run (bio links, partner pages, QR codes).

import { useCallback, useEffect, useMemo, useState, type FormEvent } from "react";
import type { Brand, TrackingCampaign, TrackingChannel, TrackingChannelID, TrackingLink } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, CopyButton, Empty, ErrorText, field, Input, Loading, Menu, PageHeader, toast } from "../ui";
import { ago, plural } from "../format";
import { DataTable, type ColumnDef } from "@/components/data-table";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Checkbox } from "@/components/ui/checkbox";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";

type Props = { ws: string; canManage: boolean };

export function CampaignLinks({ ws, canManage }: Props) {
  const [campaigns, setCampaigns] = useState<TrackingCampaign[] | null>(null);
  const [channels, setChannels] = useState<TrackingChannel[]>([]);
  const [brands, setBrands] = useState<Brand[]>([]);
  const [status, setStatus] = useState<"active" | "archived">("active");
  const [channel, setChannel] = useState<TrackingChannelID | "">("");
  const [creating, setCreating] = useState(false);
  const [open, setOpen] = useState<TrackingCampaign | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/tracking-campaigns", { params: { path: { ws }, query: { status, ...(channel ? { channel } : {}) } } });
    if (error) setError(errorMessage(error));
    setCampaigns(data?.data ?? []);
  }, [ws, status, channel]);
  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/tracking-taxonomy", { params: { path: { ws } } }).then(({ data }) => setChannels(data?.channels ?? []));
    api.GET("/v1/workspaces/{ws}/brands", { params: { path: { ws } } }).then(({ data }) => setBrands(data?.data ?? []));
  }, [ws]);

  const label = useMemo(() => Object.fromEntries(channels.map((c) => [c.id, c.label])) as Record<string, string>, [channels]);
  const brandName = useMemo(() => Object.fromEntries(brands.map((b) => [b.id, b.name])) as Record<string, string>, [brands]);

  async function setArchived(c: TrackingCampaign, archived: boolean) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/tracking-campaigns/{id}", { params: { path: { ws, id: c.id } }, body: { status: archived ? "archived" : "active" } });
    if (error) return toast(errorMessage(error), "error");
    toast(archived ? `Archived ${c.name}. Links already shared keep working.` : `Restored ${c.name}.`);
    load();
  }

  const columns: ColumnDef<TrackingCampaign>[] = [
    {
      id: "name", header: "Campaign", accessorFn: (c) => c.name,
      cell: ({ row: { original: c } }) => (
        <span className="block min-w-0">
          <span className="block truncate font-medium">{c.name}</span>
          <span className="block truncate font-mono text-[12px] text-muted-foreground">{c.slug}</span>
        </span>
      ),
    },
    { id: "channel", header: "Channel", accessorFn: (c) => label[c.channel] ?? c.channel },
    { id: "source", header: "Source", accessorFn: (c) => c.source, cell: ({ row: { original: c } }) => <span className="font-mono text-[12px]">{c.source}</span> },
    { id: "brand", header: "Brand", accessorFn: (c) => (c.brand_id ? brandName[c.brand_id] ?? "" : ""), cell: ({ getValue }) => (getValue() as string) || <span className="text-muted-foreground">All</span> },
    {
      id: "made", header: "Made by", accessorFn: (c) => c.object_kind,
      cell: ({ row: { original: c } }) => (c.object_kind ? <Badge tone="grey">{c.object_kind.replace(/_/g, " ")}</Badge> : "You"),
    },
    { id: "links", header: "Links", accessorFn: (c) => c.links },
    { id: "created", header: "Made", accessorFn: (c) => c.created_at, cell: ({ row: { original: c } }) => ago(c.created_at) },
  ];

  if (!campaigns) return <Loading />;
  const none = campaigns.length === 0 && status === "active" && !channel;
  return (
    <div className="space-y-6">
      <PageHeader
        title="Campaign links"
        description="One way to tag every link you share, so visits, sign-ups and revenue are credited to the right channel and campaign. Each link goes straight to your page with UTM tags and a short ofc code, which names the campaign even if someone edits the tags."
        action={canManage && <Button onClick={() => setCreating(true)}>New campaign</Button>}
      />
      <ErrorText>{error}</ErrorText>
      {none ? (
        <Empty title="No campaigns yet" action={canManage && <Button onClick={() => setCreating(true)}>New campaign</Button>}>
          Make a campaign for each thing you promote, such as a launch, a newsletter sponsorship or an ad campaign, then make a link for each page and placement. Outflare also makes campaigns for its own sequences, broadcasts and posts.
        </Empty>
      ) : (
        <DataTable
          columns={canManage ? [...columns, {
            id: "actions", header: "", enableSorting: false,
            cell: ({ row: { original: c } }) => (
              <Menu aria={`Actions for ${c.name}`} items={[
                { label: "Links", onClick: () => setOpen(c) },
                c.status === "active" ? { label: "Archive", onClick: () => setArchived(c, true) } : { label: "Restore", onClick: () => setArchived(c, false) },
              ]} />
            ),
          }] : columns}
          data={campaigns}
          rowId={(c) => c.id}
          onRowClick={(c) => setOpen(c)}
          searchPlaceholder="Search campaigns"
          toolbar={
            <>
              <select aria-label="Channel" className={field} value={channel} onChange={(e) => setChannel(e.target.value as TrackingChannelID | "")}>
                <option value="">Every channel</option>
                {channels.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
              </select>
              <select aria-label="Show" className={field} value={status} onChange={(e) => setStatus(e.target.value as "active" | "archived")}>
                <option value="active">Active</option>
                <option value="archived">Archived</option>
              </select>
            </>
          }
          empty={status === "archived" ? "No archived campaigns." : "No campaigns match."}
          mobile={(c) => (
            <span className="block min-w-0">
              <span className="block truncate text-[13px] font-medium">{c.name}</span>
              <span className="block truncate text-[12px] text-muted-foreground">{label[c.channel] ?? c.channel} · {c.source} · {plural(c.links, "link")}</span>
            </span>
          )}
        />
      )}
      {creating && (
        <NewCampaign ws={ws} channels={channels} brands={brands} onClose={() => setCreating(false)}
          onSaved={(c) => { setCreating(false); load(); setOpen(c); }} />
      )}
      {open && <CampaignSheet ws={ws} campaign={open} channelLabel={label[open.channel] ?? open.channel} canManage={canManage} onClose={() => { setOpen(null); load(); }} />}
    </div>
  );
}

function NewCampaign({ ws, channels, brands, onClose, onSaved }: { ws: string; channels: TrackingChannel[]; brands: Brand[]; onClose: () => void; onSaved: (c: TrackingCampaign) => void }) {
  const [name, setName] = useState("");
  const [channel, setChannel] = useState<TrackingChannelID>("paid_ads");
  const [source, setSource] = useState("");
  const [brand, setBrand] = useState("");
  const [landing, setLanding] = useState("");
  const [redirects, setRedirects] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const ch = channels.find((c) => c.id === channel);
  const slug = name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 80);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.POST("/v1/workspaces/{ws}/tracking-campaigns", {
      params: { path: { ws } },
      body: { name: name.trim(), channel, source: source.trim() || undefined, brand: brand || undefined, landing_url: landing.trim() || undefined, redirect_links: redirects || undefined },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    toast(`Made ${data.name}. Now make a link for each page and placement.`);
    onSaved(data);
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New campaign</DialogTitle>
          <DialogDescription>The channel, source and campaign name become the tags on every link in it, and can't change later.</DialogDescription>
        </DialogHeader>
        <form onSubmit={save} className="space-y-3">
          <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Spring launch" maxLength={120} required autoFocus />
          <label className="block space-y-1 text-[13px]">
            <span className="font-medium">Channel</span>
            <select className={`${field} w-full`} value={channel} onChange={(e) => { setChannel(e.target.value as TrackingChannelID); setSource(""); }}>
              {channels.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
            </select>
          </label>
          <Input label="Source" list="campaign-sources" value={source} onChange={(e) => setSource(e.target.value)}
            placeholder={ch?.sources[0] ?? "google"} maxLength={60} required={(ch?.sources.length ?? 0) !== 1}
            hint="The platform, site or partner, such as google, linkedin or a partner's code." />
          <datalist id="campaign-sources">{ch?.sources.map((s) => <option key={s} value={s} />)}</datalist>
          {brands.length > 0 && (
            <label className="block space-y-1 text-[13px]">
              <span className="font-medium">Brand</span>
              <select className={`${field} w-full`} value={brand} onChange={(e) => setBrand(e.target.value)}>
                <option value="">All brands</option>
                {brands.map((b) => <option key={b.id} value={b.id}>{b.name}</option>)}
              </select>
            </label>
          )}
          <Input label="Landing page" type="url" value={landing} onChange={(e) => setLanding(e.target.value)} placeholder="https://example.com/pricing" maxLength={2000}
            hint="Where links go unless you pick another page. Optional." />
          <label className="flex items-start gap-2 text-[13px]">
            <Checkbox checked={redirects} onCheckedChange={(v) => setRedirects(v === true)} className="mt-0.5" />
            <span>
              Give every link a short redirect link
              <span className="block text-[12px] text-muted-foreground">It counts clicks before sending people on. For bio links, partner pages and QR codes, where the tracking snippet can't run.</span>
            </span>
          </label>
          {name.trim() && ch && (
            <p className="rounded-md bg-muted px-2.5 py-2 font-mono text-[12px] break-all text-muted-foreground">
              utm_source={(source.trim() || ch.sources[0] || "").toLowerCase()}&utm_medium={ch.medium}&utm_campaign={slug}&ofc=…
            </p>
          )}
          <ErrorText>{error}</ErrorText>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
            <Button disabled={busy || !name.trim()}>Make campaign</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function CampaignSheet({ ws, campaign: c, channelLabel, canManage, onClose }: { ws: string; campaign: TrackingCampaign; channelLabel: string; canManage: boolean; onClose: () => void }) {
  const [links, setLinks] = useState<TrackingLink[] | null>(null);
  const [url, setUrl] = useState(c.landing_url);
  const [content, setContent] = useState("");
  const [term, setTerm] = useState("");
  const [redirect, setRedirect] = useState(c.redirect_links);
  const [made, setMade] = useState<TrackingLink | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = useCallback(async () => {
    const { data } = await api.GET("/v1/workspaces/{ws}/tracking-campaigns/{id}/links", { params: { path: { ws, id: c.id } } });
    setLinks(data?.data ?? []);
  }, [ws, c.id]);
  useEffect(() => {
    load();
  }, [load]);

  async function make(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    const { data, error } = await api.POST("/v1/workspaces/{ws}/tracking-campaigns/{id}/links", {
      params: { path: { ws, id: c.id } },
      body: { url: url.trim() || undefined, content: content.trim() || undefined, term: term.trim() || undefined, redirect },
    });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setMade(data);
    setContent("");
    setTerm("");
    load();
  }

  const tags: [string, string][] = [["Channel", channelLabel], ["utm_source", c.source], ["utm_medium", c.medium], ["utm_campaign", c.slug], ["ofc", c.code]];
  return (
    <Sheet open onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-xl">
        <SheetHeader>
          <SheetTitle>{c.name}</SheetTitle>
          <SheetDescription>Every link in this campaign carries these tags.</SheetDescription>
        </SheetHeader>
        <div className="space-y-6 px-4 pb-6">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[13px]">
            {tags.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-muted-foreground">{k}</dt>
                <dd className="min-w-0 truncate font-mono text-[12px]">{v}</dd>
              </div>
            ))}
          </dl>
          {c.status === "archived" && <p className="text-[13px] text-muted-foreground">This campaign is archived. Links already shared keep working; restore it to make new ones.</p>}
          {canManage && c.status === "active" && (
            <form onSubmit={make} className="space-y-3">
              <h3 className="text-[13px] font-medium">Make a link</h3>
              <Input label="Page" type="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://example.com/pricing" maxLength={2000} required={!c.landing_url} />
              <div className="grid gap-3 sm:grid-cols-2">
                <Input label="Content" value={content} onChange={(e) => setContent(e.target.value)} placeholder="hero-cta" maxLength={80} hint="Which ad, button or post. Optional." />
                <Input label="Term" value={term} onChange={(e) => setTerm(e.target.value)} placeholder="residential proxies" maxLength={80} hint="The keyword, for search ads. Optional." />
              </div>
              <label className="flex items-start gap-2 text-[13px]">
                <Checkbox checked={redirect} onCheckedChange={(v) => setRedirect(v === true)} className="mt-0.5" />
                <span>
                  Short redirect link
                  <span className="block text-[12px] text-muted-foreground">Counts clicks first. For places the tracking snippet can't run.</span>
                </span>
              </label>
              <ErrorText>{error}</ErrorText>
              <Button disabled={busy}>Make link</Button>
              {made && (
                <div className="space-y-2 rounded-md border p-3">
                  <p className="font-mono text-[12px] break-all">{made.tagged_url}</p>
                  <CopyButton text={made.tagged_url} label="Copy link" />
                  {made.redirect_url && (
                    <>
                      <p className="font-mono text-[12px] break-all">{made.redirect_url}</p>
                      <CopyButton text={made.redirect_url} label="Copy redirect link" />
                    </>
                  )}
                </div>
              )}
            </form>
          )}
          <div className="space-y-2">
            <h3 className="text-[13px] font-medium">{links ? plural(links.length, "link") : "Links"}</h3>
            {!links ? <Loading /> : links.length === 0 ? (
              <p className="text-[13px] text-muted-foreground">No links yet.</p>
            ) : (
              <ul className="divide-y rounded-md border">
                {links.map((l) => (
                  <li key={l.id} className="flex min-w-0 items-center gap-3 px-3 py-2">
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-[13px]">{l.url}</span>
                      <span className="block truncate text-[12px] text-muted-foreground">
                        {[l.content, l.term].filter(Boolean).join(" · ") || "No content or term"} · {l.redirect_url ? `${plural(l.clicks, "click")} · ` : ""}{ago(l.created_at)}
                      </span>
                    </span>
                    {l.redirect_url && <CopyButton text={l.redirect_url} label="Redirect" />}
                    <CopyButton text={l.tagged_url} />
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      </SheetContent>
    </Sheet>
  );
}
