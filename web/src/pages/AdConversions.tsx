import { useCallback, useEffect, useMemo, useState } from "react";
import type { AdAccount, AdConnection, AdConversionRule, AdConversionSend, AdConversionTarget, AdSettings, ConversionDefinition, Schemas } from "@outflare/sdk";
import { DataTable } from "@/components/data-table";
import { pickedBrandId } from "@/lib/brand";
import { appPath } from "@/lib/paths";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, CopyButton, Input, confirmAction, field, toast } from "../ui";
import { ago } from "../format";

// Sending conversions back: each rule tells one ad platform about one
// customer event (a sign-up, a payment), server to server, so its bidding
// learns from real outcomes. Sends run in the background every few minutes;
// the log shows what went out and why anything was skipped.

type Field = AdConnection["setup_needed"][number];
type EventDefinition = Schemas["EventDefinition"];

// The platforms that can take conversions today.
const senders: Record<string, string> = { google_ads: "Google Ads", meta_ads: "Meta Ads", microsoft_ads: "Microsoft Advertising", reddit_ads: "Reddit Ads", x_ads: "X Ads", tiktok_ads: "TikTok Ads", snapchat_ads: "Snapchat Ads", taboola_ads: "Taboola Ads", pinterest_ads: "Pinterest Ads", chatgpt_ads: "ChatGPT Ads" };

const statusTone = { sent: "green", skipped: "grey", failed: "red", queued: "amber" } as const;

export function AdConversions({ ws, canManage, accounts, openLog }: { ws: string; canManage: boolean; accounts: AdAccount[]; openLog?: () => void }) {
  const [rules, setRules] = useState<AdConversionRule[] | null>(null);
  const [sends, setSends] = useState<AdConversionSend[]>([]);
  const [settings, setSettings] = useState<AdSettings | null>(null);
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<AdConversionRule | null>(null);
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
      api.GET("/v1/brands/{brand}/ads/settings", { params: { path: { brand: pickedBrandId() } } }),
    ]);
    if (r.error) toast(errorMessage(r.error), "error");
    setRules(r.data?.data ?? []);
    setSends(s.data?.data ?? []);
    setSettings(st.data ?? null);
  }, [ws]);
  useEffect(() => {
    load();
  }, [load]);

  async function setMode(consent_mode: AdSettings["consent_mode"]) {
    // Saying everyone has consented is the brand's own statement: ask once.
    if (consent_mode === "collected" && !(await confirmAction("Send for everyone?",
      "You are confirming that your site already asks every visitor for consent to use their data for ads and to show them personalised ads, including in the EEA, the UK and Switzerland. Conversions will be sent, and people added to your ad audiences, for everyone as consented. People who opted out are still never sent or added.",
      "Yes, everyone has consented", false))) return;
    const { data, error } = await api.PUT("/v1/brands/{brand}/ads/settings", { params: { path: { brand: pickedBrandId() } }, body: { consent_mode } });
    if (error || !data) return toast(errorMessage(error), "error");
    setSettings(data as AdSettings);
  }

  async function toggle(r: AdConversionRule) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/ads/conversion-rules/{id}", { params: { path: { ws, id: r.id } }, body: { enabled: !r.enabled } });
    if (error) return toast(errorMessage(error), "error");
    load();
  }

  // A payment rule with no value sends every purchase worth nothing: one
  // click gives it the payment's own amount.
  async function usePaymentAmount(r: AdConversionRule) {
    const { error } = await api.PATCH("/v1/workspaces/{ws}/ads/conversion-rules/{id}", { params: { path: { ws, id: r.id } }, body: { value_property: "amount_minor", value_unit: "minor" } });
    if (error) return toast(errorMessage(error), "error");
    toast("Payments now go with their own amount.");
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
        {editing && <EditRule key={editing.id} ws={ws} rule={editing} cancel={() => setEditing(null)} done={() => { setEditing(null); load(); }} />}

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
                      {paymentsLabel[r.feed_role] && <span> · {paymentsLabel[r.feed_role]}</span>}
                      {r.predicted_value && " · Predicted value"}
                      {r.qualified_only && " · Qualified only"}
                    </div>
                    {canManage && noAmount(r) && (
                      <div className="text-[12px] text-amber-700 dark:text-amber-400">
                        Every payment would be sent worth 0. <button type="button" className="underline" onClick={() => usePaymentAmount(r)}>Send the payment's amount</button>
                      </div>
                    )}
                  </div>
                ),
              },
              {
                id: "target",
                header: "Sends to",
                accessorFn: (r) => `${r.target_name} ${r.target_id} ${accountName(r.ad_account_id)}`,
                cell: ({ row: { original: r } }) => (
                  <div className="space-y-0.5">
                    <div className="font-medium">{r.target_name || r.target_id}{r.event_type && <span className="text-muted-foreground"> · {r.event_type}</span>}</div>
                    <div className="text-[12px] text-muted-foreground">{accountName(r.ad_account_id)}</div>
                    {r.target_name && r.target_id && (
                      <div className="flex items-center gap-1 text-[12px] text-muted-foreground">
                        <span>ID <span className="font-mono">{r.target_id}</span></span>
                        <CopyButton text={r.target_id} label={`Copy ID ${r.target_id}`} />
                      </div>
                    )}
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
                      {!r.conversion && <Button variant="ghost" onClick={() => { setAdding(false); setEditing(r); }}>Edit</Button>}
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
              <select className={field} value={settings.consent_mode} disabled={!canManage} onChange={(e) => setMode(e.target.value as AdSettings["consent_mode"])} aria-label="Who to send conversions for">
                <option value="opt_out">Everyone, unless they opted out</option>
                <option value="opt_in">Only people who said yes</option>
                <option value="collected">Everyone: our site already collects consent</option>
              </select>
            </label>
            <p className="text-muted-foreground">{settings.consent_mode === "collected"
              ? "You confirmed your site collects consent for ad data and personalised ads from every visitor, so conversions are sent, and people added to your ad audiences, for everyone, in every country. Anyone who opted out is never sent or added."
              : "In the EEA, the UK and Switzerland, and when someone's country is unknown, Outflare waits for a yes from your consent banner, as the law and the platforms require. If your site already collects that consent from everyone, choose the third option. Anyone who opted out is never sent."}</p>
          </div>
        )}

        {sends.length > 0 && (
          <div className="space-y-2">
            <div className="flex items-center justify-between gap-2">
              <div className="text-[13px] font-medium">Recent sends</div>
              {openLog && (
                <a className="text-[12px] text-blue-700 underline-offset-2 hover:underline dark:text-blue-400" href="sends"
                  onClick={(e) => { if (e.metaKey || e.ctrlKey || e.shiftKey) return; e.preventDefault(); openLog(); }}>Open the full log</a>
              )}
            </div>
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
  const [payments, setPayments] = useState<Payments>("custom");
  // The brand's own events, so nobody has to remember a name, and the
  // number properties seen on the chosen one, to pick the amount from.
  const [events, setEvents] = useState<EventDefinition[]>([]);
  const [amounts, setAmounts] = useState<string[] | null>(null);
  const [otherValue, setOtherValue] = useState(false);
  const [predicted, setPredicted] = useState(false);
  const [qualifiedOnly, setQualifiedOnly] = useState(false);
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
    api.GET("/v1/brands/{brand}/conversion-definitions", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => {
      const list = data?.data ?? [];
      setDefinitions(list);
      if (list.length > 0) setConversion(list[0].key ?? "");
    });
  }, [ws]);

  useEffect(() => {
    api.GET("/v1/brands/{brand}/catalog/events", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => setEvents(data?.data ?? []));
  }, [ws]);
  // Picking an event reads which number properties it carries and guesses
  // the amount: a payment's own amount, else the likeliest name.
  const known = events.some((e) => e.name === eventName);
  useEffect(() => {
    setAmounts(null);
    setOtherValue(false);
    if (!known) return;
    if (eventName === paymentEvent) {
      setValueProperty("amount_minor");
      setUnit("minor");
      return;
    }
    let live = true;
    api.GET("/v1/brands/{brand}/catalog/events/{event}/usage", { params: { path: { brand: pickedBrandId(), event: eventName } } }).then(({ data }) => {
      if (!live) return;
      const keys = (data?.properties ?? []).filter((p) => p.types[0] === "number").map((p) => p.key);
      setAmounts(keys);
      const guess = guessAmount(keys);
      setValueProperty(guess);
      setUnit(inCents(guess) ? "minor" : "major");
    });
    return () => {
      live = false;
    };
  }, [eventName, known]);

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

  // A rule on payments can send the first payment only, or the later ones.
  const sendsPayments = conversion ? definitions.find((d) => d.key === conversion)?.event === paymentEvent : eventName.trim() === paymentEvent;

  async function save() {
    setBusy(true);
    const { error } = await api.POST("/v1/workspaces/{ws}/ads/conversion-rules", {
      params: { path: { ws } },
      body: conversion
        ? { account_id: account, conversion, target_id: target, event_type: eventType || undefined, feed_role: sendsPayments ? payments : undefined }
        : { account_id: account, event_name: eventName.trim(), target_id: target, event_type: eventType || undefined, value_property: valueProperty.trim() || undefined, value_unit: unit, feed_role: sendsPayments ? payments : undefined, predicted_value: predicted, qualified_only: qualifiedOnly },
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
          {!conversion && <EventPicker ws={ws} events={events} value={eventName} onChange={setEventName} />}
          {!conversion && eventName === paymentEvent && <p className="text-[13px] text-muted-foreground">Each payment is sent with its own amount and currency.</p>}
          {sendsPayments && <PaymentsPicker value={payments} onChange={setPayments} />}
          {!conversion && eventName !== paymentEvent && eventName.trim() !== "" && !(known && amounts === null) && (
            <div className="grid gap-3 sm:grid-cols-2">
              {amounts && amounts.length > 0 && !otherValue ? (
                <label className="flex flex-col gap-1 text-[13px]">
                  <span className="font-medium">Worth</span>
                  <select className={field} value={valueProperty} onChange={(e) => { if (e.target.value === "\u0000") { setOtherValue(true); setValueProperty(""); } else { setValueProperty(e.target.value); setUnit(inCents(e.target.value) ? "minor" : "major"); } }}>
                    <option value="">No amount, count it only</option>
                    {amounts.map((k) => <option key={k} value={k}>The event's {k}</option>)}
                    <option value={"\u0000"}>Another property…</option>
                  </select>
                  <span className="text-[12px] text-muted-foreground">The number properties this event carried in the last 30 days.</span>
                </label>
              ) : (
                <Input label="Worth (optional)" hint={known && amounts ? "This event has carried no number yet. Leave empty to count it only, or name the property that will hold the amount." : "The event property with the amount, such as total. Leave empty to count it only."} value={valueProperty} onChange={(e) => { setValueProperty(e.target.value); setUnit(inCents(e.target.value) ? "minor" : "major"); }} />
              )}
              {valueProperty.trim() !== "" && (
                <label className="flex flex-col gap-1 text-[13px]">
                  <span className="font-medium">Read as</span>
                  <select className={field} value={unit} onChange={(e) => setUnit(e.target.value as "major" | "minor")}>
                    <option value="major">12.50 means 12.50</option>
                    <option value="minor">1250 means 12.50 (cents)</option>
                  </select>
                  <span className="text-[12px] text-muted-foreground">Picked from the property's name. Change it only if the amounts in the send log look 100 times off.</span>
                </label>
              )}
            </div>
          )}
          {!conversion && eventName !== paymentEvent && eventName.trim() !== "" && (
            <WhoPays event={eventName.trim()} predicted={predicted} qualifiedOnly={qualifiedOnly} setPredicted={setPredicted} setQualifiedOnly={setQualifiedOnly} />
          )}
          <div className="flex gap-2">
            <Button type="submit" disabled={busy || (!conversion && !eventName.trim())}>Add rule</Button>
            <Button type="button" variant="ghost" onClick={cancel}>Cancel</Button>
          </div>
        </form>
      )}
    </div>
  );
}

// noAmount: a rule on the payment event that carries no value at all.
function noAmount(r: AdConversionRule) {
  return r.event_name === paymentEvent && !r.conversion && !r.value_property && r.fixed_value_micros === null;
}

// EditRule changes a rule's event and the amount it carries, in place.
// Where it sends stays as it is: a different conversion is a new rule.
function EditRule({ ws, rule, cancel, done }: { ws: string; rule: AdConversionRule; cancel: () => void; done: () => void }) {
  const [events, setEvents] = useState<EventDefinition[]>([]);
  const [eventName, setEventName] = useState(rule.event_name);
  const [valueProperty, setValueProperty] = useState(rule.value_property ?? "");
  const [unit, setUnit] = useState<"major" | "minor">(rule.value_unit === "minor" ? "minor" : "major");
  const [payments, setPayments] = useState<Payments>(isPayments(rule.feed_role) ? rule.feed_role : "custom");
  const [busy, setBusy] = useState(false);
  const [predicted, setPredicted] = useState(rule.predicted_value);
  const [qualifiedOnly, setQualifiedOnly] = useState(rule.qualified_only);
  useEffect(() => {
    api.GET("/v1/brands/{brand}/catalog/events", { params: { path: { brand: pickedBrandId() } } }).then(({ data }) => setEvents(data?.data ?? []));
  }, [ws]);
  const payment = eventName === paymentEvent;

  async function save() {
    setBusy(true);
    const { error } = await api.PATCH("/v1/workspaces/{ws}/ads/conversion-rules/{id}", {
      params: { path: { ws, id: rule.id } },
      body: payment
        ? { event_name: eventName, value_property: "amount_minor", value_unit: "minor", feed_role: payments, predicted_value: false, qualified_only: false }
        : { event_name: eventName.trim(), value_property: valueProperty.trim(), value_unit: unit, feed_role: rule.feed_role === "signup" ? undefined : "custom", predicted_value: predicted, qualified_only: qualifiedOnly },
    });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    toast("Rule changed. It applies to events from now on.");
    done();
  }

  return (
    <form className="space-y-3 rounded-md border p-3" onSubmit={(e) => { e.preventDefault(); save(); }}>
      <p className="text-[13px]"><span className="font-medium">Edit rule</span> <span className="text-muted-foreground">· sends to {rule.target_name || rule.target_id}</span></p>
      <EventPicker ws={ws} events={events} value={eventName} onChange={setEventName} />
      {payment ? (
        <>
          <p className="text-[13px] text-muted-foreground">Each payment is sent with its own amount and currency.</p>
          <PaymentsPicker value={payments} onChange={setPayments} />
        </>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2">
          <Input label="Worth (optional)" hint="The event property with the amount, such as total. Leave empty to count it only." value={valueProperty} onChange={(e) => { setValueProperty(e.target.value); setUnit(inCents(e.target.value) ? "minor" : "major"); }} />
          {valueProperty.trim() !== "" && (
            <label className="flex flex-col gap-1 text-[13px]">
              <span className="font-medium">Read as</span>
              <select className={field} value={unit} onChange={(e) => setUnit(e.target.value as "major" | "minor")}>
                <option value="major">12.50 means 12.50</option>
                <option value="minor">1250 means 12.50 (cents)</option>
              </select>
            </label>
          )}
        </div>
      )}
      {!payment && !rule.conversion && eventName.trim() !== "" && (
        <WhoPays event={eventName.trim()} predicted={predicted} qualifiedOnly={qualifiedOnly} setPredicted={setPredicted} setQualifiedOnly={setQualifiedOnly} />
      )}
      <div className="flex gap-2">
        <Button type="submit" disabled={busy || !eventName.trim()}>Save</Button>
        <Button type="button" variant="ghost" onClick={cancel}>Cancel</Button>
      </div>
    </form>
  );
}

type SignupQuality = Schemas["SignupQuality"];

const emailKinds: Record<string, string> = { work: "Work email", free: "Free email", throwaway: "Throwaway email", unknown: "Unknown email" };

function share(n: number) {
  return `${(n * 100).toFixed(n < 0.1 ? 1 : 0)}%`;
}

function micros(n: number, currency: string) {
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency, maximumFractionDigits: 2 }).format(n / 1_000_000);
  } catch {
    return `${(n / 1_000_000).toFixed(2)} ${currency}`;
  }
}

// WhoPays shows the brand's own history of who pays after this event, and
// the two switches that use it: send each event with what people like the
// person went on to pay, and hold the ones less likely to pay than average.
function WhoPays({ event, predicted, qualifiedOnly, setPredicted, setQualifiedOnly }: { event: string; predicted: boolean; qualifiedOnly: boolean; setPredicted: (v: boolean) => void; setQualifiedOnly: (v: boolean) => void }) {
  const [q, setQ] = useState<SignupQuality | null>(null);
  const [open, setOpen] = useState(false);
  useEffect(() => {
    setQ(null);
    let live = true;
    const t = setTimeout(() => {
      api.GET("/v1/brands/{brand}/ads/signup-quality", { params: { path: { brand: pickedBrandId() }, query: { event } } }).then(({ data }) => {
        if (live && data) setQ(data);
      });
    }, 400);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [event]);
  return (
    <div className="space-y-2 rounded-md border p-3 text-[13px]" data-testid="who-pays">
      <p className="font-medium">Who pays after this event</p>
      {q === null ? (
        <p className="text-muted-foreground">Reading your history…</p>
      ) : q.enough ? (
        <p className="text-muted-foreground">
          Of {q.signups.toLocaleString()} people who did this in the last 90 days, {share(q.paid_share)} paid within {q.pay_days} days, {micros(q.value_per_payer_micros, q.currency)} each on average.{" "}
          <button type="button" className="underline" onClick={() => setOpen(!open)}>{open ? "Hide the breakdown" : "See it by country and email"}</button>
        </p>
      ) : (
        <p className="text-muted-foreground">
          Not enough history yet: {q.signups.toLocaleString()} people did this in the last 90 days and {q.paid.toLocaleString()} of them paid. Predictions start at {q.min_signups} people and {q.min_payers} payers. Until then every event is sent, with the value set above.
        </p>
      )}
      {q?.enough && open && (
        <table className="w-full text-[12px]">
          <thead>
            <tr className="text-left text-muted-foreground">
              <th className="py-1 font-medium">Country</th>
              <th className="py-1 font-medium">Email</th>
              <th className="py-1 text-right font-medium">People</th>
              <th className="py-1 text-right font-medium">Paid</th>
              <th className="py-1 text-right font-medium">Predicted value</th>
              <th className="py-1 text-right font-medium">Qualified</th>
            </tr>
          </thead>
          <tbody>
            {q.segments.slice(0, 12).map((s) => (
              <tr key={`${s.country}|${s.email_kind}`} className="border-t">
                <td className="py-1">{s.country || "Unknown"}</td>
                <td className="py-1">{emailKinds[s.email_kind] ?? s.email_kind}</td>
                <td className="py-1 text-right tabular-nums">{s.signups.toLocaleString()}</td>
                <td className="py-1 text-right tabular-nums">{share(s.paid_share)}</td>
                <td className="py-1 text-right tabular-nums">{micros(s.predicted_value_micros, q.currency)}</td>
                <td className="py-1 text-right">{s.qualified ? "Yes" : "No"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <label className="flex items-start gap-2">
        <input type="checkbox" className="mt-0.5" checked={predicted} onChange={(e) => setPredicted(e.target.checked)} />
        <span>
          Send a predicted value
          <span className="block text-[12px] text-muted-foreground">Each event is sent worth what people from the same country with the same kind of email went on to pay, so the platform bids more for the ones who pay.</span>
        </span>
      </label>
      <label className="flex items-start gap-2">
        <input type="checkbox" className="mt-0.5" checked={qualifiedOnly} onChange={(e) => setQualifiedOnly(e.target.checked)} />
        <span>
          Send qualified ones only
          <span className="block text-[12px] text-muted-foreground">Events from people less likely to pay than your average, and from throwaway email addresses, are held back. The send log says why.</span>
        </span>
      </label>
    </div>
  );
}

// Every payment, however it arrives, becomes this event with amount_minor.
const paymentEvent = "payment.succeeded";

// inCents: a property whose name says it holds minor units.
function inCents(key: string) {
  return /(_minor|_cents|cents|_micros)$/i.test(key.trim());
}

// guessAmount picks the property most likely to hold the event's value.
function guessAmount(keys: string[]) {
  const order = ["amount_minor", "amount", "total", "revenue", "value", "price"];
  return order.find((k) => keys.includes(k)) ?? keys.find((k) => /amount|total|revenue|value|price/i.test(k)) ?? "";
}

// EventPicker: type to search the brand's events by name or label, newest
// and busiest first. A name not listed yet can still be typed.
function EventPicker({ ws, events, value, onChange }: { ws: string; events: EventDefinition[]; value: string; onChange: (name: string) => void }) {
  const [open, setOpen] = useState(false);
  const [at, setAt] = useState(0);
  const q = value.trim().toLowerCase();
  const shown = useMemo(() => {
    const live = events.filter((e) => e.status !== "deprecated" && !e.same_as);
    const hit = q ? live.filter((e) => e.name.toLowerCase().includes(q) || e.label.toLowerCase().includes(q)) : live;
    return hit.sort((a, b) => Number(b.name === paymentEvent) - Number(a.name === paymentEvent) || b.seen_count - a.seen_count || a.name.localeCompare(b.name)).slice(0, 50);
  }, [events, q]);
  const pick = (name: string) => {
    onChange(name);
    setOpen(false);
  };
  const exact = events.some((e) => e.name === value.trim());
  return (
    <div className="relative text-[13px]">
      <label className="flex flex-col gap-1">
        <span className="font-medium">When this event happens</span>
        <input
          className={`${field} w-full`}
          role="combobox"
          aria-expanded={open}
          aria-controls="conversion-events"
          aria-autocomplete="list"
          autoComplete="off"
          required
          placeholder="Search your events, such as payment or sign-up"
          value={value}
          onFocus={() => setOpen(true)}
          onBlur={() => setTimeout(() => setOpen(false), 120)}
          onChange={(e) => { onChange(e.target.value); setOpen(true); setAt(0); }}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") { e.preventDefault(); setOpen(true); setAt((i) => Math.min(i + 1, shown.length - 1)); }
            else if (e.key === "ArrowUp") { e.preventDefault(); setAt((i) => Math.max(i - 1, 0)); }
            else if (e.key === "Enter" && open && shown[at]) { e.preventDefault(); pick(shown[at].name); }
            else if (e.key === "Escape") setOpen(false);
          }}
        />
      </label>
      {open && shown.length > 0 && (
        <ul id="conversion-events" role="listbox" className="absolute z-20 mt-1 max-h-64 w-full overflow-auto rounded-md border bg-popover py-1 shadow-md">
          {shown.map((e, i) => (
            <li key={e.name} role="option" aria-selected={i === at} className={`flex cursor-pointer items-baseline justify-between gap-3 px-2.5 py-1.5 ${i === at ? "bg-muted" : ""}`} onMouseEnter={() => setAt(i)} onMouseDown={(ev) => { ev.preventDefault(); pick(e.name); }}>
              <span className="min-w-0 truncate">
                <span className="font-medium">{e.label || e.name}</span>
                {e.label && <span className="ml-2 font-mono text-[11px] text-muted-foreground">{e.name}</span>}
                {e.name === paymentEvent && <span className="ml-2 text-[11px] text-muted-foreground">every payment, counted once</span>}
              </span>
              <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">{e.seen_count.toLocaleString()} seen{e.last_seen_at ? ` · last ${ago(e.last_seen_at)}` : ""}</span>
            </li>
          ))}
        </ul>
      )}
      <span className="mt-1 block text-xs text-muted-foreground">
        {value.trim() === "" ? "Pick from the events your website, app and connected tools already send." : exact ? "" : events.length > 0 ? "Not an event this brand has sent yet. The rule waits for the first one with exactly this name." : ""}
      </span>
      <a className="mt-1 block text-xs text-muted-foreground underline hover:text-foreground" href={appPath(ws, "people", "events")} target="_blank" rel="noreferrer">
        See every event with what it means and how many arrived
      </a>
    </div>
  );
}

// Which payments a rule on payment.succeeded sends: every one, a person's
// first only (a new paying customer, counted once), or every later one.
type Payments = "custom" | "first_payment" | "repeat_payment";

function isPayments(v: string): v is Payments {
  return v === "custom" || v === "first_payment" || v === "repeat_payment";
}

const paymentsLabel: Record<string, string> = { first_payment: "First payments only", repeat_payment: "Repeat payments only" };

function PaymentsPicker({ value, onChange }: { value: Payments; onChange: (v: Payments) => void }) {
  return (
    <label className="flex flex-col gap-1 text-[13px]">
      <span className="font-medium">Payments to send</span>
      <select className={field} value={value} onChange={(e) => onChange(e.target.value as Payments)}>
        <option value="custom">Every payment</option>
        <option value="first_payment">First payments only</option>
        <option value="repeat_payment">Repeat payments only (renewals and top-ups)</option>
      </select>
      <span className="text-[12px] text-muted-foreground">
        {value === "first_payment"
          ? "Sends a customer's first ever payment for this brand and nothing after it, so each new paying customer counts once. Set the conversion in the ad platform to count one per click."
          : value === "repeat_payment"
            ? "Sends every payment after a customer's first."
            : "Sends every payment, first ones and renewals alike."}
      </span>
    </label>
  );
}
