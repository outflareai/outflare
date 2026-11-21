// The send log: every event a conversion rule matched, newest first, with
// what was sent, where, on what it was matched, and why not when it was
// held or failed. Each row links to the person, so a send can be checked
// against who it was for.

import { useCallback, useEffect, useState, type MouseEvent } from "react";
import type { AdAccount, AdConversionCheck, AdConversionRule, AdConversionSend, AdConversionSendStatus, AdMatchQuality } from "@outflare/sdk";
import type { Tab } from "../../App";
import { api, errorMessage } from "../../api";
import { DataTable } from "@/components/data-table";
import { Badge, Button, Card, ErrorText, Loading, field } from "../../ui";
import { money, platformName } from "../Ads";

const tone = { sent: "green", skipped: "grey", failed: "red", queued: "amber" } as const;
const label = { sent: "Sent", skipped: "Held back", failed: "Failed", queued: "Waiting" } as const;
const matchName: Record<string, string> = { gclid: "Google click id", gbraid: "Google click id (iOS app)", wbraid: "Google click id (iOS web)", fbclid: "Meta click id", msclkid: "Microsoft click id", email: "Email (hashed)", phone: "Phone (hashed)" };
const countryFrom: Record<string, string> = { visit: "their latest website visit", event: "this event", earlier_event: "an earlier event of theirs", record: "their record" };
const tries = 5;
const verdictTone = { processed: "green", partly_processed: "amber", failed: "red", processing: "blue", unknown: "grey" } as const;
const verdictLabel = { processed: "Processed", partly_processed: "Partly refused", failed: "Refused", processing: "Still processing", unknown: "Not reported" } as const;

const checkTone = { ok: "green", low: "amber", none: "red", too_few: "grey", not_reported: "grey" } as const;
const checkLabel = { ok: "Counted", low: "Under half counted", none: "None counted", too_few: "Too few to judge", not_reported: "Not reported" } as const;
const checkWhy: Record<AdConversionCheck["status"], (c: AdConversionCheck) => string> = {
  ok: () => "A network only counts a conversion it can tie to one of its ad clicks, so a little under what was sent is normal.",
  low: (c) => `${platformName(c.connector)} counts under half of the conversions that carried its click id. Check the conversion action is a primary conversion and its click window covers how long people take to convert.`,
  none: (c) => `${platformName(c.connector)} counts none of them. Check the conversion action still exists, is a primary conversion, and belongs to this account.`,
  too_few: () => "Under 10 sends carried the network's click id over these days, too few to compare.",
  not_reported: (c) => `${platformName(c.connector)} doesn't report conversions by conversion action, so there is nothing to compare with.`,
};
const whole = (n: number) => n.toLocaleString(undefined, { maximumFractionDigits: 1 });

// Sent against counted: per conversion action, what Outflare sent over the
// checked days and what the network counts for it.
function SentAgainstCounted({ brand }: { brand: string }) {
  const [rows, setRows] = useState<AdConversionCheck[] | null>(null);
  const [range, setRange] = useState<{ from: string; to: string } | null>(null);
  const [days, setDays] = useState(14);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    setRows(null);
    api.GET("/v1/brands/{brand}/ads/conversion-check", { params: { path: { brand }, query: { days } } }).then(({ data, error }) => {
      if (!live) return;
      setError(error ? errorMessage(error) : "");
      setRows(data?.data ?? []);
      setRange(data ? { from: data.from, to: data.to } : null);
    });
    return () => {
      live = false;
    };
  }, [brand, days]);
  const day = (iso: string) => new Date(`${iso}T00:00:00Z`).toLocaleDateString(undefined, { day: "numeric", month: "short", timeZone: "UTC" });
  return (
    <Card title="Sent against counted" description={`For each conversion action, how many conversions Outflare sent and how many the ad network counts${range ? `, ${day(range.from)} to ${day(range.to)}` : ""}. It ends two days ago because networks report a conversion a day or two late. Source: Outflare's record of each send and each network's own conversion report.`}>
      {error && <ErrorText>{error}</ErrorText>}
      {!rows ? <Loading /> : (
        <DataTable
          data={rows}
          rowId={(c) => `${c.ad_account_id} ${c.action}`}
          toolbar={(
            <select className={field} aria-label="Days checked" value={days} onChange={(e) => setDays(Number(e.target.value))}>
              <option value={7}>Last 7 days</option>
              <option value={14}>Last 14 days</option>
              <option value={30}>Last 30 days</option>
              <option value={90}>Last 90 days</option>
            </select>
          )}
          download={{ file: "conversions-sent-against-counted.csv" }}
          empty="No conversion was sent over these days. A row appears here once a rule has sent one."
          mobile={(c) => (
            <div className="space-y-1">
              <div className="flex items-center justify-between gap-2"><span>{c.action}</span><Badge tone={checkTone[c.status]}>{checkLabel[c.status]}</Badge></div>
              <div className="text-[12px] text-muted-foreground">{platformName(c.connector)} · {c.sent} sent · {c.counted == null ? "not reported" : `${whole(c.counted)} counted`}</div>
            </div>
          )}
          columns={[
            { id: "action", header: "Conversion action", accessorFn: (c) => c.action, cell: ({ row: { original: c } }) => (
              <div>
                <div>{c.action}</div>
                <div className="text-[12px] text-muted-foreground">{platformName(c.connector)}{c.account_name ? ` · ${c.account_name}` : ""}</div>
              </div>
            ) },
            { id: "sent", header: "Sent", meta: { align: "right" }, accessorFn: (c) => c.sent, cell: ({ row: { original: c } }) => <span className="font-mono tabular-nums">{c.sent.toLocaleString()}</span> },
            { id: "clicked", header: "With the network's click id", meta: { align: "right" }, accessorFn: (c) => c.sent_with_click_id, cell: ({ row: { original: c } }) => <span className="font-mono tabular-nums">{c.sent_with_click_id.toLocaleString()}</span> },
            { id: "counted", header: "Counted by the network", meta: { align: "right" }, accessorFn: (c) => c.counted ?? -1, cell: ({ row: { original: c } }) => c.counted == null ? <span className="text-muted-foreground">—</span> : <span className="font-mono tabular-nums">{whole(c.counted)}</span> },
            { id: "share", header: "Share counted", meta: { align: "right" }, accessorFn: (c) => c.share ?? -1, cell: ({ row: { original: c } }) => c.share == null ? <span className="text-muted-foreground">—</span> : <span className="font-mono tabular-nums">{Math.round(c.share * 100)}%</span> },
            { id: "status", header: "Status", accessorFn: (c) => checkLabel[c.status], cell: ({ row: { original: c } }) => <Badge tone={checkTone[c.status]}>{checkLabel[c.status]}</Badge> },
            { id: "why", header: "What it means", enableSorting: false, accessorFn: (c) => checkWhy[c.status](c), cell: ({ row: { original: c } }) => <div className="max-w-[26rem] whitespace-normal text-[12px] text-muted-foreground">{checkWhy[c.status](c)}</div> },
          ]}
        />
      )}
    </Card>
  );
}

const qualityTone = { ok: "green", low: "red", fell: "amber", none: "grey" } as const;
const qualityLabel = { ok: "Good", low: "Low", fell: "Fell", none: "No score yet" } as const;
const identifierName: Record<string, string> = {
  email: "Email", phone: "Phone", ip_address: "IP address", user_agent: "Browser", external_id: "Your own person id", fbc: "Meta click id", fbp: "Meta browser id",
  first_name: "First name", last_name: "Last name", city: "City", state: "State", zip: "Postcode", country: "Country", date_of_birth: "Date of birth", gender: "Gender",
};
const identifier = (key: string) => identifierName[key] ?? key.replace(/_/g, " ");

// Match quality: what the ad network says about how well the conversions
// sent to it match its own accounts, per event, with what it says to fix.
// Shown once a network has reported a score.
function MatchQuality({ brand }: { brand: string }) {
  const [rows, setRows] = useState<AdMatchQuality[]>([]);
  const [low, setLow] = useState(6);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    api.GET("/v1/brands/{brand}/ads/match-quality", { params: { path: { brand } } }).then(({ data, error }) => {
      if (!live) return;
      setError(error ? errorMessage(error) : "");
      setRows(data?.data ?? []);
      if (data) setLow(data.low_under);
    });
    return () => {
      live = false;
    };
  }, [brand]);
  if (!rows.length && !error) return null;
  const why = (q: AdMatchQuality) =>
    q.status === "low" ? `Under ${low} out of 10: the network ties too few of these conversions to its ads, so its bidding learns from less.`
      : q.status === "fell" ? `Down from ${q.score_before?.toFixed(1)} over the week before. Something that was being sent has stopped.`
        : q.status === "none" ? "The network has not scored this event yet. It needs a few days of conversions."
          : "The network can tie most of these conversions to its ads.";
  return (
    <Card title="Match quality" description="How well each ad network can tie the conversions Outflare sends to one of its own accounts, out of 10, read from the network once a day. A higher score means more conversions counted against your ads. Source: the network's own quality report (Meta's event match quality).">
      {error && <ErrorText>{error}</ErrorText>}
      <DataTable
        data={rows}
        rowId={(q) => `${q.ad_account_id} ${q.target_id} ${q.event}`}
        download={{ file: "match-quality.csv" }}
        empty="No network has reported a score yet."
        mobile={(q) => (
          <div className="space-y-1">
            <div className="flex items-center justify-between gap-2"><span>{q.event}</span><Badge tone={qualityTone[q.status]}>{qualityLabel[q.status]}</Badge></div>
            <div className="text-[12px] text-muted-foreground">{platformName(q.connector)} · {q.score == null ? "no score" : `${q.score.toFixed(1)} out of 10`}</div>
          </div>
        )}
        columns={[
          { id: "event", header: "Event", accessorFn: (q) => q.event, cell: ({ row: { original: q } }) => (
            <div>
              <div>{q.event}{!q.sent && <span className="ml-2 text-[12px] text-muted-foreground">not sent by a rule</span>}</div>
              <div className="text-[12px] text-muted-foreground">{platformName(q.connector)}{q.target_name ? ` · ${q.target_name}` : ""}{q.account_name ? ` · ${q.account_name}` : ""}</div>
            </div>
          ) },
          { id: "score", header: "Score", meta: { align: "right" }, accessorFn: (q) => q.score ?? -1, cell: ({ row: { original: q } }) => q.score == null ? <span className="text-muted-foreground">—</span> : <span className="font-mono tabular-nums">{q.score.toFixed(1)} / 10</span> },
          { id: "before", header: "Week before", meta: { align: "right" }, accessorFn: (q) => q.score_before ?? -1, cell: ({ row: { original: q } }) => q.score_before == null ? <span className="text-muted-foreground">—</span> : <span className="font-mono tabular-nums">{q.score_before.toFixed(1)}</span> },
          { id: "status", header: "Status", accessorFn: (q) => qualityLabel[q.status], cell: ({ row: { original: q } }) => <Badge tone={qualityTone[q.status]}>{qualityLabel[q.status]}</Badge> },
          { id: "keys", header: "Sent with", enableSorting: false, accessorFn: (q) => q.keys.map((k) => `${identifier(k.key)} ${Math.round(k.percent)}%`).join(", "), cell: ({ row: { original: q } }) => (
            <div className="max-w-[20rem] whitespace-normal text-[12px] text-muted-foreground">
              {q.keys.length ? q.keys.map((k) => `${identifier(k.key)} ${Math.round(k.percent)}%`).join(" · ") : "—"}
            </div>
          ) },
          { id: "fix", header: "What to fix", enableSorting: false, accessorFn: (q) => q.issues.map((i) => `${i.name}: ${i.solution}`).join(" ") || why(q), cell: ({ row: { original: q } }) => (
            <div className="max-w-[30rem] space-y-1 whitespace-normal text-[12px] text-muted-foreground">
              <div>{why(q)}</div>
              {q.issues.map((i) => (
                <div key={i.name}><span className="text-foreground">{i.name}</span> ({Math.round(i.percent)}% of events). {i.solution}</div>
              ))}
            </div>
          ) },
        ]}
      />
    </Card>
  );
}

const countryName = (code: string) => {
  try {
    return new Intl.DisplayNames(["en"], { type: "region" }).of(code) ?? code;
  } catch {
    return code;
  }
};
const time = (iso: string) => new Date(iso).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" });

// What happened, in a sentence, and what to check when it didn't go.
function outcome(s: AdConversionSend): { what: string; check?: string } {
  const to = platformName(s.connector);
  if (s.status === "sent") return { what: `${to} accepted it${s.sent_at ? ` at ${time(s.sent_at)}` : ""}.` };
  if (s.status === "queued") return { what: s.attempts > 0 ? `Try ${s.attempts} of ${tries} did not go through. Outflare tries again by itself.` : "Waiting for the next send, within a few minutes.", check: s.reason || undefined };
  if (s.status === "failed") {
    const gone = /NOT_FOUND/i.test(s.reason);
    return { what: `${to} refused it after ${s.attempts} ${s.attempts === 1 ? "try" : "tries"}. Nothing was counted.`, check: gone ? `${to} could not find the conversion. Check it still exists there and the rule points at it. ${to} said: ${s.reason}` : s.reason };
  }
  return { what: "Not sent, and not counted as an error.", check: /country is unknown/i.test(s.reason) ? `${s.reason[0].toUpperCase()}${s.reason.slice(1)}. Events that carry the person's IP address or country fix this.` : s.reason };
}

export function SendLog({ ws, brand, accounts, go }: { ws: string; brand: string; accounts: AdAccount[]; go: (tab: Tab, item?: string) => void }) {
  const [rows, setRows] = useState<AdConversionSend[] | null>(null);
  const [rules, setRules] = useState<AdConversionRule[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [status, setStatus] = useState("");
  const [rule, setRule] = useState("");
  const [error, setError] = useState("");
  const [more, setMore] = useState(false);
  // What the platform did after accepting a send, asked one row at a time.
  const [verdicts, setVerdicts] = useState<Record<string, AdConversionSendStatus | "asking" | { failed: string }>>({});

  async function ask(id: string) {
    setVerdicts((v) => ({ ...v, [id]: "asking" }));
    const { data, error } = await api.GET("/v1/brands/{brand}/ads/conversion-sends/{id}/status", { params: { path: { brand, id } } });
    setVerdicts((v) => ({ ...v, [id]: data ?? { failed: error ? errorMessage(error) : "No answer" } }));
  }

  const fetchPage = useCallback((before?: string) => api.GET("/v1/workspaces/{ws}/ads/conversion-sends", {
    params: { path: { ws }, query: { limit: 100, ...(status ? { status: status as AdConversionSend["status"] } : {}), ...(rule ? { rule_id: rule } : {}), ...(before ? { cursor: before } : {}) } },
  }), [ws, status, rule]);

  useEffect(() => {
    let live = true;
    setRows(null);
    fetchPage().then(({ data, error }) => {
      if (!live) return;
      setError(error ? errorMessage(error) : "");
      setRows(data?.data ?? []);
      setCursor(data?.next_cursor ?? null);
    });
    return () => {
      live = false;
    };
  }, [fetchPage]);
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/ads/conversion-rules", { params: { path: { ws } } }).then(({ data }) => setRules(data?.data ?? []));
  }, [ws]);

  async function loadMore() {
    if (!cursor) return;
    setMore(true);
    const { data, error } = await fetchPage(cursor);
    setMore(false);
    if (error) return setError(errorMessage(error));
    setRows((r) => [...(r ?? []), ...(data?.data ?? [])]);
    setCursor(data?.next_cursor ?? null);
  }

  const account = (id: string) => {
    const a = accounts.find((x) => x.id === id);
    return a ? a.name || a.external_id : "";
  };
  // A real link, so it opens in a new tab too; a plain click stays in the app.
  const openPerson = (e: MouseEvent, id: string) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    e.preventDefault();
    go("people", id);
  };

  const filters = (
    <>
      <select className={field} aria-label="Result" value={status} onChange={(e) => setStatus(e.target.value)}>
        <option value="">Every result</option>
        <option value="sent">Sent</option>
        <option value="skipped">Held back</option>
        <option value="failed">Failed</option>
        <option value="queued">Waiting</option>
      </select>
      <select className={field} aria-label="Rule" value={rule} onChange={(e) => setRule(e.target.value)}>
        <option value="">Every rule</option>
        {rules.map((r) => <option key={r.id} value={r.id}>{r.conversion_name ?? r.event_name} → {r.target_name}</option>)}
      </select>
    </>
  );

  return (
    <div className="space-y-4">
    <SentAgainstCounted brand={brand} />
    <MatchQuality brand={brand} />
    <Card title="Send log" description="Every event a conversion rule matched, newest first: who it was for, where it went, and what happened. Source: Outflare's own record of each send.">
      {error && <ErrorText>{error}</ErrorText>}
      {!rows ? <Loading /> : (
        <DataTable
          data={rows}
          rowId={(s) => s.id}
          toolbar={filters}
          hidden={["event_id", "request_id", "via"]}
          download={{ file: "conversion-send-log.csv" }}
          empty="Nothing matches yet. A row appears here within a few minutes of an event a rule sends."
          mobile={(s) => (
            <div className="space-y-1">
              <div className="flex items-center justify-between gap-2"><span className="font-mono">{s.event_name}</span><Badge tone={tone[s.status]}>{label[s.status]}</Badge></div>
              <div className="text-[12px] text-muted-foreground">{time(s.occurred_at)} · {s.target_name}</div>
              <div className="text-[12px] text-muted-foreground">{outcome(s).check ?? outcome(s).what}</div>
            </div>
          )}
          columns={[
            { id: "when", header: "Happened", accessorFn: (s) => s.occurred_at, cell: ({ row: { original: s } }) => <span className="whitespace-nowrap">{time(s.occurred_at)}</span> },
            { id: "event", header: "Event", accessorFn: (s) => s.event_name, cell: ({ row: { original: s } }) => <span className="font-mono">{s.event_name}</span> },
            { id: "person", header: "Person", accessorFn: (s) => s.record_name, cell: ({ row: { original: s } }) => s.record_id
              ? <a className="font-medium text-blue-700 underline-offset-2 hover:underline dark:text-blue-400" href={`../people/${s.record_id}`} onClick={(e) => openPerson(e, s.record_id!)}>{s.record_name || "Open person"}</a>
              : <span className="whitespace-nowrap text-muted-foreground">Nobody known</span> },
            { id: "to", header: "Sent to", accessorFn: (s) => `${s.target_name} ${platformName(s.connector)} ${account(s.ad_account_id)}`, cell: ({ row: { original: s } }) => (
              <div>
                <div>{s.target_name}</div>
                <div className="text-[12px] text-muted-foreground">{platformName(s.connector)}{account(s.ad_account_id) ? ` · ${account(s.ad_account_id)}` : ""}</div>
              </div>
            ) },
            { id: "result", header: "Result", accessorFn: (s) => label[s.status], cell: ({ row: { original: s } }) => <Badge tone={tone[s.status]}>{label[s.status]}</Badge> },
            { id: "what", header: "What happened", enableSorting: false, accessorFn: (s) => `${outcome(s).what} ${outcome(s).check ?? ""}`.trim(), cell: ({ row: { original: s } }) => {
              const o = outcome(s);
              return (
                <div className="max-w-[26rem] whitespace-normal">
                  <div>{o.what}</div>
                  {o.check && <div className="text-[12px] text-muted-foreground">{o.check}</div>}
                </div>
              );
            } },
            { id: "verdict", header: "After it was accepted", enableSorting: false, accessorFn: (s) => { const v = verdicts[s.id]; return v && typeof v === "object" && "state" in v ? verdictLabel[v.state] : ""; }, cell: ({ row: { original: s } }) => {
              if (s.status !== "sent" || !s.request_id) return <span className="text-muted-foreground">—</span>;
              const v = verdicts[s.id];
              if (!v) return <Button variant="ghost" aria-label={`Ask ${platformName(s.connector)} what it did with this ${s.event_name}`} onClick={() => ask(s.id)}>Ask {platformName(s.connector)}</Button>;
              if (v === "asking") return <span className="text-muted-foreground">Asking…</span>;
              if ("failed" in v) return <div className="max-w-[18rem] whitespace-normal text-[12px] text-muted-foreground">Could not ask: {v.failed} <Button variant="ghost" onClick={() => ask(s.id)}>Try again</Button></div>;
              const notes = [...v.errors.map((e) => `Refused ${e.count}: ${e.label}`), ...v.warnings.map((e) => `Remark on ${e.count}: ${e.label}`)];
              return (
                <div className="max-w-[20rem] whitespace-normal">
                  <Badge tone={verdictTone[v.state]}>{verdictLabel[v.state]}</Badge>
                  <div className="mt-1 text-[12px] text-muted-foreground">{notes.length ? notes.join(". ") + "." : v.detail}</div>
                </div>
              );
            } },
            { id: "matched", header: "Matched on", accessorFn: (s) => s.match_keys.map((k) => matchName[k] ?? k).join(", "), cell: ({ row: { original: s } }) => s.match_keys.length ? <span className="whitespace-normal">{s.match_keys.map((k) => matchName[k] ?? k).join(", ")}</span> : <span className="text-muted-foreground">—</span> },
            { id: "value", header: "Value", meta: { align: "right" }, accessorFn: (s) => s.value_micros ?? -1, cell: ({ row: { original: s } }) => s.value_micros == null ? <span className="whitespace-nowrap text-muted-foreground">No value</span> : <span className="font-mono tabular-nums">{money(s.value_micros, s.currency)}</span> },
            { id: "country", header: "Country", accessorFn: (s) => s.country ? `${countryName(s.country)} (from ${countryFrom[s.country_source] ?? "the event"})` : "", cell: ({ row: { original: s } }) => s.country
              ? <div><div>{countryName(s.country)}</div><div className="text-[12px] text-muted-foreground">from {countryFrom[s.country_source] ?? "the event"}</div></div>
              : <span className="text-muted-foreground">{s.status === "skipped" ? "Unknown" : "—"}</span> },
            { id: "via", header: "Sent through", accessorFn: (s) => s.transport === "data_manager" ? "Google Data Manager" : s.transport === "ads_api" ? "Ads API" : "" },
            { id: "event_id", header: "Event id", accessorFn: (s) => s.event_id, cell: ({ row: { original: s } }) => <span className="font-mono text-[12px]">{s.event_id}</span> },
            { id: "request_id", header: "Google request id", accessorFn: (s) => s.request_id, cell: ({ row: { original: s } }) => <span className="font-mono text-[12px]">{s.request_id}</span> },
          ]}
        />
      )}
      {cursor && <div className="mt-3 flex justify-center"><Button variant="ghost" disabled={more} onClick={loadMore}>Show more</Button></div>}
    </Card>
    </div>
  );
}
