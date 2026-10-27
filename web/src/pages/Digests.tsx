import { useEffect, useState } from "react";
import type { Digest, DigestSection, DigestSettings } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, Empty, ErrorText, field, Input, PageHeader, Status, toast } from "../ui";
import { appPath, type Tab } from "@/lib/paths";

type Provider = { id: string; name: string; connector: string };

const weekdays = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
const hours = Array.from({ length: 24 }, (_, h) => h);
const zones = (() => {
  try {
    return Intl.supportedValuesOf("timeZone");
  } catch {
    return ["UTC"];
  }
})();

const when = (s: { period_start: string; period_end: string }) =>
  `${new Date(s.period_start).toLocaleDateString()} to ${new Date(s.period_end).toLocaleDateString()}`;

// Digests: a daily or weekly summary of the whole workspace, kept here,
// sent as the digest.ready webhook and, when the workspace picks its own
// email provider, emailed to its members.
export function Digests({ ws, canManage, go }: { ws: string; canManage: boolean; go: (t: Tab) => void }) {
  const path = { params: { path: { ws } } };
  const [set, setSet] = useState<DigestSettings | null>(null);
  const [form, setForm] = useState<Partial<DigestSettings>>({});
  const [providers, setProviders] = useState<Provider[]>([]);
  const [list, setList] = useState<Digest[] | null>(null);
  const [preview, setPreview] = useState<DigestSection[] | null>(null);
  const [open, setOpen] = useState<string>("");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  async function load() {
    const [s, l, p] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/digest-settings", path),
      api.GET("/v1/workspaces/{ws}/digests", path),
      api.GET("/v1/workspaces/{ws}/digests/preview", path),
    ]);
    if (s.data) {
      setSet(s.data);
      setForm(s.data);
    }
    if (l.data) setList(l.data.data);
    if (p.data) setPreview(p.data.sections);
    setError(s.error ? errorMessage(s.error) : "");
  }
  useEffect(() => {
    load();
    if (canManage)
      api.GET("/v1/workspaces/{ws}/email-providers", path).then(({ data }) => data && setProviders(data.data as Provider[]));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, canManage]);

  async function save() {
    setBusy("save");
    const { frequency, weekday, hour, timezone, recipients, provider_id, from_address } = form;
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/digest-settings", {
      ...path,
      body: { frequency, weekday, hour, timezone, recipients, provider_id, from_address },
    });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (data && "frequency" in data) {
      setSet(data);
      setForm(data);
      toast("Digest settings saved.");
    }
  }

  async function sendNow() {
    setBusy("send");
    const { error } = await api.POST("/v1/workspaces/{ws}/digests", { ...path, body: { frequency: set?.frequency === "daily" ? "daily" : "weekly" } });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (!error) {
      toast("Making the digest. It shows here in a moment.");
      setTimeout(load, 2000);
    }
  }

  async function mine(email: boolean) {
    setBusy("me");
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/digest-settings/me", { ...path, body: { email } });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (data && "frequency" in data) setSet(data);
  }

  if (!set) return <div className="max-w-3xl"><PageHeader title="Digests" /><ErrorText>{error}</ErrorText></div>;
  const emails = form.recipients !== "none";
  const changed = JSON.stringify(form) !== JSON.stringify(set);

  return (
    <div className="max-w-3xl space-y-6">
      <PageHeader
        title="Digests"
        description="A daily or weekly summary of the whole workspace: fixes, deploys, site health, outreach, lifecycle email, missions and what needs you."
        action={canManage && <Button variant="ghost" disabled={busy !== "" || set.frequency === "off"} onClick={sendNow}>{busy === "send" ? "Making…" : "Make one now"}</Button>}
      />

      <Card title="Schedule" description={canManage ? "Each digest is kept here and sent as the digest.ready webhook." : "Only owners and admins can change this."}>
        <div className="grid gap-3 sm:grid-cols-4">
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">How often</span>
            <select className={field + " w-full"} disabled={!canManage} value={form.frequency} onChange={(e) => setForm({ ...form, frequency: e.target.value as DigestSettings["frequency"] })}>
              <option value="weekly">Weekly</option>
              <option value="daily">Daily</option>
              <option value="off">Off</option>
            </select>
          </label>
          {form.frequency === "weekly" && (
            <label className="block text-sm">
              <span className="mb-1.5 block text-[13px] font-medium">On</span>
              <select className={field + " w-full"} disabled={!canManage} value={form.weekday} onChange={(e) => setForm({ ...form, weekday: Number(e.target.value) })}>
                {weekdays.map((d, i) => <option key={d} value={i}>{d}</option>)}
              </select>
            </label>
          )}
          {form.frequency !== "off" && (
            <>
              <label className="block text-sm">
                <span className="mb-1.5 block text-[13px] font-medium">At</span>
                <select className={field + " w-full"} disabled={!canManage} value={form.hour} onChange={(e) => setForm({ ...form, hour: Number(e.target.value) })}>
                  {hours.map((h) => <option key={h} value={h}>{String(h).padStart(2, "0")}:00</option>)}
                </select>
              </label>
              <label className="block text-sm">
                <span className="mb-1.5 block text-[13px] font-medium">Time zone</span>
                <select className={field + " w-full"} disabled={!canManage} value={form.timezone} onChange={(e) => setForm({ ...form, timezone: e.target.value })}>
                  {(zones.includes(form.timezone ?? "") ? zones : [form.timezone ?? "UTC", ...zones]).map((z) => <option key={z}>{z}</option>)}
                </select>
              </label>
            </>
          )}
        </div>
      </Card>

      <Card title="Email" description="Sent through your own email provider, from an address on one of its verified sending domains. Outflare never sends it from an account of its own.">
        <div className="grid gap-3 sm:grid-cols-3">
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">Who gets it</span>
            <select className={field + " w-full"} disabled={!canManage} value={form.recipients} onChange={(e) => setForm({ ...form, recipients: e.target.value as DigestSettings["recipients"] })}>
              <option value="none">Nobody, keep it here</option>
              <option value="admins">Owners and admins</option>
              <option value="members">Every member</option>
            </select>
          </label>
          {emails && (
            <>
              <label className="block text-sm">
                <span className="mb-1.5 block text-[13px] font-medium">Email provider</span>
                <select className={field + " w-full"} disabled={!canManage} value={form.provider_id ?? ""} onChange={(e) => setForm({ ...form, provider_id: e.target.value || null })}>
                  <option value="">Pick one</option>
                  {providers.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
                </select>
              </label>
              <Input label="From" placeholder="digest@acme.example" value={form.from_address ?? ""} disabled={!canManage} maxLength={320} onChange={(e) => setForm({ ...form, from_address: e.target.value })} />
            </>
          )}
        </div>
        {emails && canManage && providers.length === 0 && (
          <p className="mt-3 text-sm text-muted-foreground">
            Connect an email provider first, such as Amazon SES or Resend, under <button className="underline" onClick={() => go("providers")}>Email providers</button>.
          </p>
        )}
        {set.recipients !== "none" && (
          <p className="mt-3 text-sm">
            {set.email_problem ? <span className="text-amber-700 dark:text-amber-400">{set.email_problem}</span> : <span className="text-muted-foreground">Goes to {set.recipient_count} {set.recipient_count === 1 ? "person" : "people"} who see every brand and confirmed their address.</span>}
          </p>
        )}
        {set.me && set.recipients !== "none" && (
          <div className="mt-3 flex items-center gap-3 text-sm">
            <span className="text-muted-foreground">{set.me.opted_out ? "You turned the email off for yourself." : set.me.email ? "You get it by email." : "You don't get it by email."}</span>
            {(set.me.opted_out || set.me.email) && (
              <Button variant="ghost" disabled={busy !== ""} onClick={() => mine(set.me!.opted_out)}>{set.me.opted_out ? "Turn it back on" : "Turn it off for me"}</Button>
            )}
          </div>
        )}
        {canManage && (
          <div className="mt-4">
            <Button disabled={busy !== "" || !changed} onClick={save}>{busy === "save" ? "Saving…" : "Save"}</Button>
          </div>
        )}
        <ErrorText>{error}</ErrorText>
      </Card>

      <Card title="This week so far" description="What the next digest says if it were made now.">
        {preview && preview.length === 0 && <p className="text-sm text-muted-foreground">Nothing to report yet.</p>}
        {preview && <Sections ws={ws} sections={preview} go={go} />}
      </Card>

      <Card title="Past digests">
        {list && list.length === 0 && <Empty title="No digests yet">The first one is made at the time above, once something happened.</Empty>}
        <ul className="divide-y">
          {list?.map((d) => (
            <li key={d.id} className="py-2">
              <button className="flex w-full items-center justify-between gap-3 text-left text-sm" onClick={() => setOpen(open === d.id ? "" : d.id)}>
                <span>
                  <span className="font-medium capitalize">{d.frequency}</span> · {when(d)}
                  {d.trigger === "manual" && <span className="text-muted-foreground"> · asked for</span>}
                </span>
                <span className="text-muted-foreground">
                  {d.email_error ? <Status tone="amber">Not emailed</Status> : d.emailed > 0 ? `Emailed to ${d.emailed}` : ""}
                </span>
              </button>
              {open === d.id && (
                <div className="mt-2">
                  {d.email_error && <p className="mb-2 text-sm text-muted-foreground">{d.email_error}</p>}
                  <Sections ws={ws} sections={d.sections} go={go} />
                </div>
              )}
            </li>
          ))}
        </ul>
      </Card>
    </div>
  );
}

function Sections({ ws, sections, go }: { ws: string; sections: DigestSection[]; go: (t: Tab) => void }) {
  return (
    <div className="space-y-4">
      {sections.map((s) => (
        <div key={s.id}>
          <h3 className="mb-1 text-sm font-medium">{s.title}</h3>
          <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-sm sm:grid-cols-3">
            {s.metrics.map((m) => (
              <div key={m.key} className="flex justify-between gap-2">
                <dt className="text-muted-foreground">{m.label}</dt>
                <dd className="font-medium tabular-nums">
                  {m.value}
                  {m.previous !== null && m.previous !== m.value && (
                    <span className="ml-1 text-xs text-muted-foreground">{m.value > m.previous ? "+" : "−"}{Math.abs(m.value - m.previous)}</span>
                  )}
                </dd>
              </div>
            ))}
          </dl>
          {s.items.length > 0 && (
            <ul className="mt-1 list-disc pl-5 text-sm">
              {s.items.map((it, i) => (
                <li key={i} className={it.tone === "bad" ? "text-destructive" : undefined}>
                  {it.tab ? (
                    <a className="hover:underline" href={appPath(ws, it.tab as Tab)} onClick={(e) => { e.preventDefault(); go(it.tab as Tab); }}>{it.text}</a>
                  ) : it.text}
                </li>
              ))}
            </ul>
          )}
        </div>
      ))}
    </div>
  );
}
