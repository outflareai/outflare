import { useCallback, useEffect, useState } from "react";
import type { AdAccount, AdConnection, AdConversionRule, AdConversionSend, AdConversionTarget, AdSettings, ConversionDefinition } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Input, confirmAction, field, toast } from "../ui";
import { ago } from "../format";

// Sending conversions back: each rule tells one ad platform about one
// customer event (a sign-up, a payment), server to server, so its bidding
// learns from real outcomes. Sends run in the background every few minutes;
// the log shows what went out and why anything was skipped.

type Field = AdConnection["setup_needed"][number];

// The platforms that can take conversions today.
const senders: Record<string, string> = { google_ads: "Google Ads", meta_ads: "Meta Ads", microsoft_ads: "Microsoft Advertising", reddit_ads: "Reddit Ads", tiktok_ads: "TikTok Ads", snapchat_ads: "Snapchat Ads", taboola_ads: "Taboola Ads", pinterest_ads: "Pinterest Ads" };

const statusTone = { sent: "green", skipped: "grey", failed: "red", queued: "amber" } as const;

export function AdConversions({ ws, canManage, accounts }: { ws: string; canManage: boolean; accounts: AdAccount[] }) {
  const [rules, setRules] = useState<AdConversionRule[] | null>(null);
  const [sends, setSends] = useState<AdConversionSend[]>([]);
  const [settings, setSettings] = useState<AdSettings | null>(null);
  const [adding, setAdding] = useState(false);
  const usable = accounts.filter((a) => a.status === "active" && senders[a.connector]);
  const accountName = (id: string) => {
    const a = accounts.find((x) => x.id === id);
    return a ? `${senders[a.connector] ?? a.connector} · ${a.name || a.external_id}` : "";
  };

  const load = useCallback(async () => {
    const path = { ws };
    const [r, s, st] = await Promise.all([
      api.GET("/v1/workspaces/{ws}/ads/conversion-rules", { params: { path } }),
      api.GET("/v1/workspaces/{ws}/ads/conversion-sends", { params: { path, query: { limit: 25 } } }),
      api.GET("/v1/workspaces/{ws}/ads/settings", { params: { path } }),
    ]);
    if (r.error) toast(errorMessage(r.error), "error");
    setRules(r.data?.data ?? []);
    setSends(s.data?.data ?? []);
    setSettings(st.data ?? null);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);

  async function setMode(consent_mode: "opt_out" | "opt_in") {
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/ads/settings", { params: { path: { ws } }, body: { consent_mode } });
    if (error || !data) return toast(errorMessage(error), "error");
    setSettings(data as AdSettings);
  }

  async function toggle(r: AdConversionRule) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/ads/conversion-rules/{id}", { params: { path: { ws, id: r.id } }, body: { enabled: !r.enabled } });
    if (error) return toast(errorMessage(error), "error");
    load();
  }

  async function remove(r: AdConversionRule) {
    if (!(await confirmAction("Delete this rule?", `${r.event_name} stops going to ${r.target_name || r.target_id}. Conversions the platform already has stay there.`, "Delete rule"))) return;
    const { error } = await api.DELETE("/v1/workspaces/{ws}/ads/conversion-rules/{id}", { params: { path: { ws, id: r.id } } });
    if (error) return toast(errorMessage(error), "error");
    load();
  }

  if (usable.length === 0 && (rules?.length ?? 0) === 0) return null;

  return (
    <Card
      title="Send conversions back"
      description="Tell each ad platform when someone it brought signs up or pays, server to server, so its reports and bidding learn from real results. Outflare sends the ad click and the person's email and phone hashed, never in the clear, and the platform counts each conversion once even when your pixel sends it too."
      action={canManage && usable.length > 0 && !adding ? <Button variant="ghost" onClick={() => setAdding(true)}>Add a rule</Button> : undefined}
    >
      <div className="space-y-4">
        {adding && <NewRule ws={ws} accounts={usable} cancel={() => setAdding(false)} done={() => { setAdding(false); load(); }} />}

        {rules && rules.length > 0 ? (
          <DataTable
            data={rules}
            rowId={(r) => r.id}
            searchPlaceholder="Search rules"
            columns={[
              {
                id: "event",
                header: "When",
                accessorFn: (r) => r.event_name,
                cell: ({ row: { original: r } }) => (
                  <div className="space-y-0.5">
                    <div className="text-[13px]">{r.conversion_name ? <span className="font-medium">{r.conversion_name}</span> : <span className="font-mono">{r.event_name}</span>}</div>
                    <div className="text-[12px] text-muted-foreground">
                      {r.conversion_name && <span className="font-mono">{r.event_name} · </span>}
                      {r.value_property ? `Value from ${r.value_property}` : r.fixed_value_micros !== null ? "Fixed value" : r.conversion ? "Value from the conversion" : "No value"}
                    </div>
                  </div>
                ),
              },
              {
                id: "target",
                header: "Sends to",
                accessorFn: (r) => `${r.target_name} ${accountName(r.ad_account_id)}`,
                cell: ({ row: { original: r } }) => (
                  <div className="space-y-0.5">
                    <div className="font-medium">{r.target_name || r.target_id}{r.event_type && <span className="text-muted-foreground"> · {r.event_type}</span>}</div>
                    <div className="text-[12px] text-muted-foreground">{accountName(r.ad_account_id)}</div>
                  </div>
                ),
              },
              {
                id: "results",
                header: "Last 30 days",
                accessorFn: (r) => r.sent_30d,
                cell: ({ row: { original: r } }) => (
                  <div className="text-[13px]">
                    <span className="tabular-nums">{r.sent_30d.toLocaleString()} sent</span>
                    <span className="text-muted-foreground">{r.skipped_30d > 0 && ` · ${r.skipped_30d.toLocaleString()} skipped`}</span>
                    {r.failed_30d > 0 && <span className="text-red-600 dark:text-red-400"> · {r.failed_30d.toLocaleString()} failed</span>}
                    <div className="text-[12px] text-muted-foreground">{!r.enabled ? "Off" : r.last_sent_at ? `Last sent ${ago(r.last_sent_at)}` : "Waiting for the first event"}</div>
                  </div>
                ),
              },
              {
                id: "actions",
                header: "",
                enableSorting: false,
                cell: ({ row: { original: r } }) =>
                  canManage && (
                    <div className="flex justify-end gap-1">
                      <Button variant="ghost" onClick={() => toggle(r)}>{r.enabled ? "Switch off" : "Switch on"}</Button>
                      <Button variant="ghost" onClick={() => remove(r)}>Delete</Button>
                    </div>
                  ),
              },
            ]}
          />
        ) : (
          !adding && <p className="text-[13px] text-muted-foreground">No rules yet. Add one to send an event, such as a sign-up or a payment, to an ad platform as a conversion.</p>
        )}

        {settings && (
          <div className="space-y-1 text-[13px]">
            <label className="flex flex-wrap items-center gap-2">
              <span className="font-medium">Who to send for</span>
              <select className={field} value={settings.consent_mode} disabled={!canManage} onChange={(e) => setMode(e.target.value as "opt_out" | "opt_in")} aria-label="Who to send conversions for">
                <option value="opt_out">Everyone, unless they opted out</option>
                <option value="opt_in">Only people who said yes</option>
              </select>
            </label>
            <p className="text-muted-foreground">In the EEA, the UK and Switzerland, and when someone's country is unknown, Outflare always waits for a yes from your consent banner, as the law and the platforms require. Anyone who opted out is never sent.</p>
          </div>
        )}

        {sends.length > 0 && (
          <div className="space-y-2">
            <div className="text-[13px] font-medium">Recent sends</div>
            <div className="divide-y rounded-md border text-[13px]">
              {sends.map((s) => (
                <div key={s.id} className="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
                  <div className="min-w-0">
                    <span className="font-mono">{s.event_name}</span> <span className="text-muted-foreground">→ {senders[s.connector] ?? s.connector} · {ago(s.occurred_at)}</span>
                    {s.reason && <div className="text-[12px] text-muted-foreground">{s.reason}</div>}
                  </div>
                  <div className="flex items-center gap-2">
                    {s.match_keys.length > 0 && <span className="text-[12px] text-muted-foreground">{s.match_keys.join(", ")}</span>}
                    <Badge tone={statusTone[s.status]}>{s.status[0].toUpperCase() + s.status.slice(1)}</Badge>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </Card>
  );
}

// NewRule picks an account, then one of the conversions the platform
// offers for it, then the event and its value.
function NewRule({ ws, accounts, cancel, done }: { ws: string; accounts: AdAccount[]; cancel: () => void; done: () => void }) {
  const [account, setAccount] = useState(accounts[0]?.id ?? "");
  const [targets, setTargets] = useState<AdConversionTarget[] | null>(null);
  const [setup, setSetup] = useState<{ integration: string; fields: Field[] } | null>(null);
  const [values, setValues] = useState<Record<string, string>>({});
  const [target, setTarget] = useState("");
  const [eventType, setEventType] = useState("");
  const [eventName, setEventName] = useState("");
  // The conversion definitions the rule can send instead of a bare event.
  const [definitions, setDefinitions] = useState<ConversionDefinition[]>([]);
  const [conversion, setConversion] = useState("");
  const [valueProperty, setValueProperty] = useState("");
  const [unit, setUnit] = useState<"major" | "minor">("major");
  const [busy, setBusy] = useState(false);
  const [loadError, setLoadError] = useState("");

  const loadTargets = useCallback(async () => {
    if (!account) return;
    setTargets(null);
    setLoadError("");
    const { data, error } = await api.GET("/v1/workspaces/{ws}/ads/conversion-targets", { params: { path: { ws }, query: { account_id: account } } });
    if (error || !data) {
      setLoadError(errorMessage(error));
      return setTargets([]);
    }
    setTargets(data.data);
    setSetup(data.setup_needed.length > 0 ? { integration: data.integration_id, fields: data.setup_needed } : null);
    setTarget(data.data[0]?.id ?? "");
    setEventType(data.data[0]?.event_types[0] ?? "");
  }, [ws, account]);
  useEffect(() => {
    loadTargets();
  }, [loadTargets]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/conversion-definitions", { params: { path: { ws } } }).then(({ data }) => {
      const list = data?.data ?? [];
      setDefinitions(list);
      if (list.length > 0) setConversion(list[0].key ?? "");
    });
  }, [ws]);

  const chosen = targets?.find((t) => t.id === target);
  // A conversion's own name for this network (meta for meta_ads) is the
  // event type to send, when the platform offers it.
  const network = accounts.find((a) => a.id === account)?.connector.replace(/_ads$/, "") ?? "";
  const named = definitions.find((d) => d.key === conversion)?.network_events?.[network];
  useEffect(() => {
    if (named && chosen?.event_types.includes(named)) setEventType(named);
  }, [named, chosen]);

  async function saveSetup() {
    if (!setup) return;
    setBusy(true);
    const { error } = await api.PUT("/v1/workspaces/{ws}/ads/connections/{id}/setup", { params: { path: { ws, id: setup.integration } }, body: { values } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setValues({});
    loadTargets();
  }

  async function save() {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/ads/conversion-rules", {
      params: { path: { ws } },
      body: conversion
        ? { account_id: account, conversion, target_id: target, event_type: eventType || undefined }
        : { account_id: account, event_name: eventName.trim(), target_id: target, event_type: eventType || undefined, value_property: valueProperty.trim() || undefined, value_unit: unit },
    });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Rule added. New events go to the platform within a few minutes.");
    done();
  }

  return (
    <div className="space-y-3 rounded-md border p-3">
      <label className="flex flex-col gap-1 text-[13px]">
        <span className="font-medium">Ad account</span>
        <select className={field} value={account} onChange={(e) => setAccount(e.target.value)}>
          {accounts.map((a) => <option key={a.id} value={a.id}>{senders[a.connector]} · {a.name || a.external_id}</option>)}
        </select>
      </label>

      {setup ? (
        <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); saveSetup(); }}>
          <p className="text-[13px] text-muted-foreground">This platform needs one more thing from your own account before it can take conversions. Outflare checks it and stores it encrypted.</p>
          {setup.fields.map((f) => (
            <Input key={f.name} label={f.label} hint={f.help} type={f.secret ? "password" : "text"} autoComplete="off" required value={values[f.name] ?? ""} onChange={(e) => setValues((v) => ({ ...v, [f.name]: e.target.value }))} />
          ))}
          <div className="flex gap-2">
            <Button type="submit" disabled={busy}>Save</Button>
            <Button type="button" variant="ghost" onClick={cancel}>Cancel</Button>
          </div>
        </form>
      ) : targets === null ? (
        <p className="text-[13px] text-muted-foreground">Asking the platform…</p>
      ) : targets.length === 0 ? (
        <div className="space-y-2 text-[13px]">
          <p className="text-muted-foreground">{loadError || "This account has nowhere to send conversions yet. Create a conversion action (Google), a pixel (Meta) or an offline conversion goal (Microsoft) in the ad platform, then try again."}</p>
          <Button variant="ghost" onClick={cancel}>Cancel</Button>
        </div>
      ) : (
        <form className="space-y-3" onSubmit={(e) => { e.preventDefault(); save(); }}>
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="flex flex-col gap-1 text-[13px]">
              <span className="font-medium">Conversion</span>
              <select className={field} value={target} onChange={(e) => { setTarget(e.target.value); setEventType(targets.find((t) => t.id === e.target.value)?.event_types[0] ?? ""); }}>
                {targets.map((t) => <option key={t.id} value={t.id}>{t.name || t.id}</option>)}
              </select>
            </label>
            {chosen && chosen.event_types.length > 0 && (
              <label className="flex flex-col gap-1 text-[13px]">
                <span className="font-medium">Send as</span>
                <select className={field} value={eventType} onChange={(e) => setEventType(e.target.value)}>
                  {chosen.event_types.map((t) => <option key={t} value={t}>{t}</option>)}
                </select>
              </label>
            )}
          </div>
          {definitions.length > 0 && (
            <label className="flex flex-col gap-1 text-[13px]">
              <span className="font-medium">What to send</span>
              <select className={field} value={conversion} onChange={(e) => setConversion(e.target.value)}>
                {definitions.map((d) => <option key={d.key} value={d.key}>{d.name}</option>)}
                <option value="">A customer event…</option>
              </select>
              <span className="text-[12px] text-muted-foreground">A conversion sends only the events that meet its conditions, worth what it says. Define them under Analytics → Channels.</span>
            </label>
          )}
          {!conversion && <Input label="When this event happens" hint="The customer event's name, as your website or app sends it, such as signed_up or order_completed." placeholder="order_completed" required value={eventName} onChange={(e) => setEventName(e.target.value)} />}
          {!conversion && <div className="grid gap-3 sm:grid-cols-2">
            <Input label="Value property (optional)" hint="The event property with the amount, such as total." value={valueProperty} onChange={(e) => setValueProperty(e.target.value)} />
            <label className="flex flex-col gap-1 text-[13px]">
              <span className="font-medium">The amount is in</span>
              <select className={field} value={unit} onChange={(e) => setUnit(e.target.value as "major" | "minor")}>
                <option value="major">Dollars, euros… (12.50)</option>
                <option value="minor">Cents (1250)</option>
              </select>
            </label>
          </div>}
          <div className="flex gap-2">
            <Button type="submit" disabled={busy || (!conversion && !eventName.trim())}>Add rule</Button>
            <Button type="button" variant="ghost" onClick={cancel}>Cancel</Button>
          </div>
        </form>
      )}
    </div>
  );
}
