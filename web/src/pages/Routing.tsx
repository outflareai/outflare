// Routing (docs/routing.md): which teammate gets a person or company that
// has no owner. A brand keeps rules in order; the first one that matches
// gives the record to the member of its pool who owns the fewest, so work
// is spread evenly. The whole list is edited here and saved at once.

import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { RoutingRule, RoutingRuleInput, RuleCondition } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Button, Card, Empty, ErrorText, field, Loading, PageHeader, toast } from "../ui";
import { useBrand } from "@/lib/brand";
import { nameItem, useCatalog } from "@/lib/records";
import { track } from "@/lib/analytics";
import { ops, readValue, useFieldNames } from "./Scores";
import type { Tab } from "../App";

type Kind = "account" | "person";
type Draft = RoutingRuleInput & { assigned?: number };
type Member = { user_id: string; name: string; email: string };
type Team = { id: string; name: string; member_ids: string[] };
type Country = { key: string; label: string };

const kinds: { key: Kind; title: string; what: string; many: string }[] = [
  { key: "account", title: "Companies", what: "company", many: "companies" },
  { key: "person", title: "People", what: "person", many: "people" },
];

const opText = Object.fromEntries(ops) as Record<string, string>;

// A field's key in words: company_size is "company size", scores.fit_band "fit band".
function fieldWords(key: string): string {
  return key.replace(/^(scores|metrics)\./, "").replace(/_/g, " ");
}

// A rule's conditions in words: "segment is enterprise and company_size is more than 200".
function whenText(r: Draft): string {
  const list = r.conditions ?? [];
  if (list.length === 0) return "";
  return list.map((c) => `${fieldWords(c.attribute ?? "")} ${opText[c.op] ?? c.op}${c.op === "exists" || c.op === "not_exists" ? "" : ` ${String(c.value ?? "")}`}`).join(r.match === "any" ? " or " : " and ");
}

export function Routing({ ws, canManage, tabs, go }: { ws: string; canManage: boolean; tabs: ReactNode; go: (t: Tab, item?: string) => void }) {
  const { brand, active } = useBrand(ws);
  useEffect(() => nameItem("routing", "Routing"), []);
  return (
    <div className="space-y-6">
      <PageHeader
        title="Routing"
        description="Who gets a new company or person. Rules are tried from the top; the first that matches gives it to the teammate in its pool who owns the fewest, so work is spread evenly. Anyone who already has an owner keeps them."
      />
      {tabs}
      {brand ? (
        <RoutingFor key={brand.id} ws={ws} brand={brand.id} canManage={canManage} go={go} />
      ) : (
        <Empty title="Pick a brand">
          Routing belongs to one brand. Pick {active.length > 1 ? `one of your ${active.length} brands` : "your brand"} in the brand switcher.
        </Empty>
      )}
    </div>
  );
}

function RoutingFor({ ws, brand, canManage, go }: { ws: string; brand: string; canManage: boolean; go: (t: Tab, item?: string) => void }) {
  const [rules, setRules] = useState<Draft[] | null>(null);
  const [unowned, setUnowned] = useState<Record<Kind, number>>({ account: 0, person: 0 });
  const [members, setMembers] = useState<Member[]>([]);
  const [teams, setTeams] = useState<Team[]>([]);
  const countries: Country[] = useCatalog(ws, brand).catalog?.option_sets.find((x) => x.key === "country")?.options ?? [];
  const [dirty, setDirty] = useState(false);
  const [editing, setEditing] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const fields = useFieldNames(brand);

  const take = (list: RoutingRule[]) => setRules(list.map((r) => ({ ...r })));
  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/brands/{brand}/routing", { params: { path: { brand } } });
    if (!data) return setError(errorMessage(error));
    setError("");
    take(data.data);
    setUnowned(data.unowned);
    setDirty(false);
    setEditing(null);
  }, [brand]);
  useEffect(() => {
    void load();
  }, [load]);
  useEffect(() => {
    void api.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } }).then(({ data }) => setMembers((data?.data ?? []) as Member[]));
    void api.GET("/v1/workspaces/{ws}/teams", { params: { path: { ws } } }).then(({ data }) => setTeams(((data as { data?: Team[] } | undefined)?.data ?? []) as Team[]));
  }, [ws]);

  if (!rules) return error ? <ErrorText>{error}</ErrorText> : <Loading />;

  const nameOf = (id: string) => members.find((m) => m.user_id === id)?.name || members.find((m) => m.user_id === id)?.email || "Someone who left";
  const countryName = (code: string) => countries.find((c) => c.key === code)?.label ?? code;
  // Who a rule gives records to, in words.
  const poolText = (r: Draft): ReactNode => {
    if (r.team_id) {
      const team = teams.find((t) => t.id === r.team_id);
      if (!team) return <span className="text-red-600">a team that is gone</span>;
      return team.member_ids.length === 0 ? <span className="text-red-600">the {team.name} team, which has nobody in it</span> : `the ${team.name} team (${team.member_ids.length})`;
    }
    const list = r.members ?? [];
    return list.length === 0 ? <span className="text-red-600">nobody yet</span> : list.map(nameOf).join(", ");
  };
  // Which records a rule takes, in words.
  const takesText = (r: Draft) => {
    const where = r.countries ?? [];
    const place = where.length === 0 ? "" : where.length <= 3 ? `in ${where.map(countryName).join(", ")}` : `in ${where.length} countries`;
    const when = whenText(r);
    if (!place && !when) return "Everything else";
    return [place && place[0].toUpperCase() + place.slice(1), when && (place ? `when ${when}` : `When ${when}`)].filter(Boolean).join(", ");
  };
  const change = (next: Draft[]) => {
    setRules(next);
    setDirty(true);
  };
  const valid = rules.every((r) => r.name.trim() && (r.team_id ? true : (r.members ?? []).length > 0) && (r.conditions ?? []).every((c) => c.attribute && (c.op === "exists" || c.op === "not_exists" || (c.value !== undefined && c.value !== ""))));

  async function save() {
    setBusy(true);
    setError("");
    const body = { rules: rules!.map(({ assigned: _, ...r }) => ({ ...r, name: r.name.trim(), members: r.team_id ? [] : r.members })) };
    const { data, error } = await api.PUT("/v1/brands/{brand}/routing", { params: { path: { brand } }, body });
    setBusy(false);
    if (error || !data || !("data" in data)) return setError(errorMessage(error));
    track("ui.routing_saved", { rules: body.rules.length });
    toast("Saved. Everyone with no owner is being routed now.");
    take(data.data);
    setUnowned(data.unowned);
    setDirty(false);
    setEditing(null);
  }

  function add(kind: Kind) {
    // A new rule goes above the catch-all of its kind, which stays last.
    const next = [...rules!];
    const lastOfKind = next.map((r, i) => ({ r, i })).filter((x) => x.r.record_type === kind).pop();
    const catchAll = lastOfKind && (lastOfKind.r.conditions ?? []).length === 0 && (lastOfKind.r.countries ?? []).length === 0;
    const at = catchAll ? lastOfKind.i : next.length;
    const first = !next.some((r) => r.record_type === kind);
    next.splice(at, 0, { name: "", record_type: kind, conditions: first ? [] : [{ attribute: "", op: "eq", value: "" }], match: "all", members: [], team_id: null, countries: [], status: "active" });
    change(next);
    setEditing(at);
  }

  // Move a rule up or down among the rules of its kind.
  function move(i: number, by: -1 | 1) {
    const kind = rules![i].record_type;
    let j = i + by;
    while (j >= 0 && j < rules!.length && rules![j].record_type !== kind) j += by;
    if (j < 0 || j >= rules!.length) return;
    const next = [...rules!];
    [next[i], next[j]] = [next[j], next[i]];
    change(next);
    if (editing === i) setEditing(j);
    else if (editing === j) setEditing(i);
  }

  return (
    <div className="space-y-6">
      {error && <ErrorText>{error}</ErrorText>}
      {kinds.map((k) => {
        const mine = rules.map((r, i) => ({ r, i })).filter((x) => x.r.record_type === k.key);
        const waiting = unowned[k.key];
        const count = waiting === 1 ? `1 ${k.what}` : `${waiting} ${k.many}`;
        return (
          <Card
            key={k.key}
            title={k.title}
            description={
              mine.length === 0
                ? `No rules yet. ${waiting > 0 ? `${count} ${waiting === 1 ? "has" : "have"} no owner.` : ""}`
                : waiting > 0
                  ? `${count} with no owner ${waiting === 1 ? "matches" : "match"} no rule yet, or ${waiting === 1 ? "is" : "are"} waiting for the next round (about a minute).`
                  : `Every ${k.what} has an owner.`
            }
            action={canManage && <Button variant="ghost" onClick={() => add(k.key)}>Add a rule</Button>}
          >
            {mine.length === 0 ? (
              <p className="py-1 text-sm text-muted-foreground">
                Start with one rule that has no conditions and your whole team in its pool: every new {k.what} is then shared out evenly. Add rules above it for the ones that should go to someone in particular, such as big companies or a region.
              </p>
            ) : (
              <ol className="space-y-2">
                {mine.map(({ r, i }, n) =>
                  editing === i ? (
                    <li key={i}>
                      <RuleEditor
                        rule={r}
                        members={members}
                        teams={teams}
                        countries={countries}
                        fields={fields[k.key]}
                        onChange={(x) => change(rules.map((y, j) => (j === i ? x : y)))}
                        onDone={() => setEditing(null)}
                        onRemove={() => {
                          change(rules.filter((_, j) => j !== i));
                          setEditing(null);
                        }}
                      />
                    </li>
                  ) : (
                    <li key={i} className={`flex flex-wrap items-center gap-x-4 gap-y-2 rounded-md border p-3 text-sm ${r.status === "paused" ? "opacity-60" : ""}`}>
                      <span className="flex size-6 shrink-0 items-center justify-center rounded-full bg-muted text-[12px] tabular-nums text-muted-foreground">{n + 1}</span>
                      <span className="min-w-[12rem] flex-1">
                        <span className="block font-medium">{r.name || "New rule"} {r.status === "paused" && <Badge>Paused</Badge>}</span>
                        <span className="block text-[13px] text-muted-foreground">
                          {takesText(r)}
                          {" · goes to "}
                          {poolText(r)}
                        </span>
                      </span>
                      {r.assigned !== undefined && <span className="shrink-0 text-[12px] tabular-nums text-muted-foreground">{r.assigned} routed</span>}
                      {canManage && (
                        <span className="flex shrink-0 items-center gap-1">
                          <Button variant="ghost" aria-label="Move up" disabled={n === 0} onClick={() => move(i, -1)}>↑</Button>
                          <Button variant="ghost" aria-label="Move down" disabled={n === mine.length - 1} onClick={() => move(i, 1)}>↓</Button>
                          <Button variant="ghost" onClick={() => setEditing(i)}>Change</Button>
                        </span>
                      )}
                    </li>
                  ),
                )}
              </ol>
            )}
          </Card>
        );
      })}

      {canManage && dirty && (
        <div className="sticky bottom-4 flex flex-wrap items-center gap-3 rounded-lg border bg-card p-3 shadow-md">
          <Button disabled={busy || !valid} onClick={save}>{busy ? "Saving…" : "Save routing"}</Button>
          <Button variant="ghost" disabled={busy} onClick={() => void load()}>Discard changes</Button>
          <span className="text-[12px] text-muted-foreground">
            {valid ? "Saving routes everyone who has no owner under the new rules. Owners already set stay." : "Give every rule a name and a teammate or a team, and finish its tests."}
          </span>
        </div>
      )}

      <p className="text-[12px] text-muted-foreground">
        A journey can route too: its "Assign an owner" step has "Use the routing rules" (<button className="underline" onClick={() => go("journeys")}>Journeys</button>). To move someone who already has an owner, open their page and change the owner there.
      </p>
    </div>
  );
}

function RuleEditor({ rule: r, members, teams, countries, fields, onChange, onDone, onRemove }: { rule: Draft; members: Member[]; teams: Team[]; countries: Country[]; fields: string[]; onChange: (r: Draft) => void; onDone: () => void; onRemove: () => void }) {
  const label = "text-[12px] text-muted-foreground";
  const conditions = r.conditions ?? [];
  const listID = `routing-fields-${r.record_type}`;
  const setC = (k: number, n: RuleCondition) => onChange({ ...r, conditions: conditions.map((y, j) => (j === k ? n : y)) });
  const pool = r.members ?? [];
  const where = r.countries ?? [];
  const byTeam = r.team_id !== null && r.team_id !== undefined;
  const team = teams.find((t) => t.id === r.team_id);
  const toggle = (id: string) => onChange({ ...r, members: pool.includes(id) ? pool.filter((m) => m !== id) : [...pool, id] });
  const tab = (on: boolean) => `rounded-md px-3 py-1 text-[13px] ${on ? "bg-foreground text-background" : "text-muted-foreground hover:text-foreground"}`;
  return (
    <div className="space-y-4 rounded-md border border-ring/50 p-4">
      <datalist id={listID}>{fields.map((k) => <option key={k} value={k} />)}</datalist>
      <div className="flex flex-wrap items-end gap-3">
        <label className={label}>Name<input autoFocus className={`${field} mt-1 block w-64`} placeholder="Enterprise" value={r.name} onChange={(e) => onChange({ ...r, name: e.target.value })} /></label>
        <label className="flex items-center gap-2 pb-1.5 text-[13px]">
          <input type="checkbox" className="size-4" checked={r.status !== "paused"} onChange={(e) => onChange({ ...r, status: e.target.checked ? "active" : "paused" })} />
          On
        </label>
      </div>

      <div className="space-y-2">
        <p className="text-[13px] font-medium">Which {r.record_type === "account" ? "companies" : "people"}</p>
        <div className="flex flex-wrap items-center gap-2 text-[13px]">
          <span className="w-10 text-muted-foreground">In</span>
          {where.length === 0 && <span className="text-muted-foreground">any country</span>}
          {where.map((code) => (
            <span key={code} className="inline-flex items-center gap-1 rounded-full border px-2.5 py-0.5">
              {countries.find((c) => c.key === code)?.label ?? code}
              <button type="button" aria-label={`Remove ${code}`} className="text-muted-foreground hover:text-foreground" onClick={() => onChange({ ...r, countries: where.filter((c) => c !== code) })}>×</button>
            </span>
          ))}
          <select
            aria-label="Add a country"
            className={field}
            value=""
            onChange={(e) => e.target.value && onChange({ ...r, countries: [...where, e.target.value] })}
          >
            <option value="">{where.length === 0 ? "Only some countries…" : "Add a country…"}</option>
            {countries.filter((c) => !where.includes(c.key)).map((c) => <option key={c.key} value={c.key}>{c.label}</option>)}
          </select>
        </div>
        {where.length > 0 && <p className="pl-12 text-[12px] text-muted-foreground">A territory: only {r.record_type === "account" ? "companies" : "people"} whose Country field is one of these. One with no country set goes to a rule further down.</p>}
        {conditions.length === 0 && where.length === 0 && <p className="text-[13px] text-muted-foreground">Everyone the rules above didn't take. Keep a rule like this last, so nobody is left without an owner.</p>}
        {conditions.map((c, k) => {
          const bare = c.op === "exists" || c.op === "not_exists";
          return (
            <div key={k} className="flex flex-wrap items-center gap-2 text-[13px]">
              <span className="w-10 text-muted-foreground">{k === 0 ? "When" : r.match === "any" ? "or" : "and"}</span>
              <input aria-label="Field" className={`${field} w-64 font-mono`} placeholder="company_size" list={listID} value={c.attribute ?? ""} onChange={(e) => setC(k, { ...c, attribute: e.target.value.trim() })} />
              <select aria-label="Compare" className={field} value={c.op} onChange={(e) => setC(k, { ...c, op: e.target.value as RuleCondition["op"] })}>
                {ops.map(([v, t]) => <option key={v} value={v}>{t}</option>)}
              </select>
              {!bare && <input aria-label="Value" className={`${field} w-40`} placeholder="200" value={c.value === undefined ? "" : String(c.value)} onChange={(e) => setC(k, { ...c, value: readValue(e.target.value) })} />}
              <Button variant="ghost" onClick={() => onChange({ ...r, conditions: conditions.filter((_, j) => j !== k) })}>Remove</Button>
            </div>
          );
        })}
        <div className="flex flex-wrap items-center gap-3 text-[12px] text-muted-foreground">
          {conditions.length < 5 && <button type="button" className="underline" onClick={() => onChange({ ...r, conditions: [...conditions, { attribute: "", op: "eq", value: "" }] })}>{conditions.length === 0 ? "Only some: add a test" : "Add another test"}</button>}
          {conditions.length > 1 && (
            <label className="flex items-center gap-1.5">
              True when
              <select aria-label="Match" className={field} value={r.match ?? "all"} onChange={(e) => onChange({ ...r, match: e.target.value as "all" | "any" })}>
                <option value="all">all tests hold</option>
                <option value="any">any test holds</option>
              </select>
            </label>
          )}
        </div>
      </div>

      <div className="space-y-2">
        <div className="flex flex-wrap items-center gap-3">
          <p className="text-[13px] font-medium">Goes to</p>
          <div role="group" aria-label="Pool" className="inline-flex rounded-lg border p-0.5">
            <button type="button" aria-pressed={!byTeam} className={tab(!byTeam)} onClick={() => onChange({ ...r, team_id: null })}>People</button>
            <button type="button" aria-pressed={byTeam} className={tab(byTeam)} onClick={() => onChange({ ...r, team_id: r.team_id ?? teams[0]?.id ?? "" })}>A team</button>
          </div>
        </div>
        {byTeam ? (
          teams.length === 0 ? (
            <p className="text-[13px] text-muted-foreground">No teams yet. Make one on the Team page, then pick it here.</p>
          ) : (
            <>
              <select aria-label="Team" className={`${field} w-64`} value={r.team_id ?? ""} onChange={(e) => onChange({ ...r, team_id: e.target.value })}>
                {teams.map((t) => <option key={t.id} value={t.id}>{t.name} ({t.member_ids.length})</option>)}
              </select>
              <p className="text-[12px] text-muted-foreground">
                {team && team.member_ids.length === 0
                  ? "Nobody is in this team yet, so the rule gives nothing out until someone joins it."
                  : `Shared evenly among whoever is in the team at the time${team ? `: ${team.member_ids.map((id) => members.find((m) => m.user_id === id)?.name || "a teammate").join(", ")}` : ""}. Add someone to the team and they start getting records, with nothing to change here.`}
              </p>
            </>
          )
        ) : (
          <>
            <div className="flex flex-wrap gap-2">
              {members.map((m) => {
                const on = pool.includes(m.user_id);
                return (
                  <button
                    key={m.user_id}
                    type="button"
                    aria-pressed={on}
                    onClick={() => toggle(m.user_id)}
                    className={`rounded-full border px-3 py-1 text-[13px] transition-colors ${on ? "border-foreground bg-foreground text-background" : "text-muted-foreground hover:border-ring/60 hover:text-foreground"}`}
                  >
                    {m.name || m.email}
                  </button>
                );
              })}
            </div>
            <p className="text-[12px] text-muted-foreground">
              {pool.length > 1 ? "Shared evenly: each one goes to whoever owns the fewest right now." : pool.length === 1 ? "Everything this rule takes goes to them. Pick more teammates to share it out." : "Pick who gets them."}
            </p>
          </>
        )}
      </div>

      <div className="flex items-center gap-2">
        <Button variant="ghost" onClick={onDone}>Done</Button>
        <Button variant="danger" onClick={onRemove}>Remove this rule</Button>
      </div>
    </div>
  );
}
