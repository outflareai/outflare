import { useEffect, useState } from "react";
import type { Alert, AlertKind, AlertSetting, Integration } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, Empty, ErrorText, field, PageHeader } from "../ui";
import { appPath, type Tab } from "@/lib/paths";

type Channel = { id: string; name: string; private: boolean };
type Settings = { kinds: AlertKind[]; default_email: string; daily_email_limit: number; settings: AlertSetting[]; muted: string[] };

const choices = [
  { value: "owner", label: "The person it's for" },
  { value: "admins", label: "Owners and admins" },
  { value: "off", label: "Nobody, keep it here" },
];

// The page an alert opens: its path is "{tab}/{item}" under the workspace.
function link(ws: string, path: string) {
  const [tab, ...item] = path.split("/");
  return appPath(ws, tab as Tab, item.length ? item.join("/") : undefined);
}

// Alerts: what needs someone now, from every part of Outflare, and who
// hears about it by email.
export function Alerts({ ws, canManage }: { ws: string; canManage: boolean }) {
  const path = { params: { path: { ws } } };
  const [list, setList] = useState<Alert[] | null>(null);
  const [next, setNext] = useState<string | null>(null);
  const [set, setSet] = useState<Settings | null>(null);
  const [slack, setSlack] = useState<Integration | null>(null);
  const [channels, setChannels] = useState<Channel[]>([]);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  // The workspace's own Slack, and the channels it can post to.
  useEffect(() => {
    if (!canManage) return;
    api.GET("/v1/workspaces/{ws}/integrations", path).then(({ data }) => {
      const found = data?.data.find((i) => i.connector === "slack" && i.status === "connected") ?? null;
      setSlack(found);
      if (found)
        api.GET("/v1/workspaces/{ws}/alerts/channels", { params: { path: { ws }, query: { integration_id: found.id } } }).then(({ data, error }) => {
          if (data) setChannels(data.data);
          if (error) setError(errorMessage(error));
        });
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, canManage]);

  async function load() {
    const [l, s] = await Promise.all([api.GET("/v1/workspaces/{ws}/alerts", path), api.GET("/v1/workspaces/{ws}/alerts/settings", path)]);
    if (l.data) {
      setList(l.data.data);
      setNext(l.data.next_before ?? null);
    }
    if (s.data) setSet(s.data as Settings);
    setError(l.error ? errorMessage(l.error) : s.error ? errorMessage(s.error) : "");
  }
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws]);

  async function more() {
    if (!next) return;
    setBusy("more");
    const { data, error } = await api.GET("/v1/workspaces/{ws}/alerts", { params: { path: { ws }, query: { before: next } } });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (data) {
      setList([...(list ?? []), ...data.data]);
      setNext(data.next_before ?? null);
    }
  }

  async function choose(kind: string, email: string) {
    setBusy(kind);
    const { error } = await api.PUT("/v1/workspaces/{ws}/alerts/settings", { ...path, body: { kind, email: email as "owner" | "admins" | "off" } });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (!error) load();
  }

  async function post(kind: string, channel: string) {
    setBusy(kind);
    const body = channel && slack ? { kind, slack_integration_id: slack.id, slack_channel: channel } : { kind, slack_integration_id: null, slack_channel: null };
    const { error } = await api.PUT("/v1/workspaces/{ws}/alerts/settings", { ...path, body });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (!error) load();
  }

  async function mute(kind: string, muted: boolean) {
    setBusy("mute-" + kind);
    const { error } = await api.PUT("/v1/workspaces/{ws}/alerts/mutes", { ...path, body: { kind, muted } });
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (!error) load();
  }

  const mine = (kind: string) => set?.settings.find((s) => s.kind === kind && !s.brand_id);
  const current = (kind: string) => mine(kind)?.email ?? set?.default_email ?? "owner";
  const brandOverrides = set?.settings.filter((s) => s.brand_id).length ?? 0;

  return (
    <div className="max-w-3xl space-y-6">
      <PageHeader title="Alerts" description="What needs someone now, such as a hot reply or a hot website visitor. Each alert is kept here, sent as the alert.raised webhook and counted in the digest." />

      <Card title="Recent alerts">
        {list && list.length === 0 && <Empty title="No alerts yet">Hot replies and hot website visitors show here as they come in.</Empty>}
        <ul className="divide-y">
          {list?.map((a) => (
            <li key={a.id} className="flex items-start justify-between gap-3 py-2 text-sm">
              <div className="min-w-0">
                <a className="font-medium hover:underline" href={link(ws, a.path)}>{a.title}</a>
                <p className="text-muted-foreground">{a.text}</p>
              </div>
              <span className="shrink-0 text-xs text-muted-foreground">{new Date(a.created_at).toLocaleString()}</span>
            </li>
          ))}
        </ul>
        {next && <Button variant="ghost" disabled={busy !== ""} onClick={more}>{busy === "more" ? "Loading…" : "Show older"}</Button>}
      </Card>

      {set && (
        <Card
          title="Where they go"
          description={`Emails come from Outflare's own account email, at most ${set.daily_email_limit} a day for each person; the rest wait here. Emails name what happened and link to it, and never quote a message.`}
        >
          <ul className="divide-y">
            {set.kinds.map((k) => {
              const muted = set.muted.includes(k.id);
              return (
                <li key={k.id} className="grid gap-2 py-3 sm:grid-cols-[1fr_auto] sm:items-center">
                  <div className="text-sm">
                    <p className="font-medium">{k.title}</p>
                    <p className="text-muted-foreground">{k.description}</p>
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    {slack && canManage && (
                      <select className={field} aria-label={`Slack channel for ${k.title}`} disabled={busy !== ""} value={mine(k.id)?.slack_channel ?? ""} onChange={(e) => post(k.id, e.target.value)}>
                        <option value="">Not in Slack</option>
                        {mine(k.id)?.slack_channel && !channels.some((c) => c.id === mine(k.id)?.slack_channel) && <option value={mine(k.id)!.slack_channel!}>#{mine(k.id)!.slack_channel_name}</option>}
                        {channels.map((c) => <option key={c.id} value={c.id}>#{c.name}</option>)}
                      </select>
                    )}
                    <select className={field} aria-label={`Who gets ${k.title} by email`} disabled={!canManage || busy !== ""} value={current(k.id)} onChange={(e) => choose(k.id, e.target.value)}>
                      {choices.map((c) => <option key={c.value} value={c.value}>{c.label}</option>)}
                    </select>
                    <Button variant="ghost" disabled={busy !== ""} onClick={() => mute(k.id, !muted)}>{muted ? "Unmute for me" : "Mute for me"}</Button>
                  </div>
                </li>
              );
            })}
          </ul>
          {canManage && !slack && (
            <p className="mt-3 text-sm text-muted-foreground">
              Want them in Slack too? <a className="underline" href={appPath(ws, "connectors", "slack")}>Connect your Slack</a>, then pick a channel for each kind here.
            </p>
          )}
          {!canManage && <p className="mt-3 text-sm text-muted-foreground">Only owners and admins can change who gets them. Anyone can mute a kind for themselves.</p>}
          {brandOverrides > 0 && <p className="mt-3 text-sm text-muted-foreground">{brandOverrides} brand {brandOverrides === 1 ? "setting chooses" : "settings choose"} differently from these, set through the API or an agent.</p>}
          <ErrorText>{error}</ErrorText>
        </Card>
      )}
      {!set && <ErrorText>{error}</ErrorText>}
    </div>
  );
}
