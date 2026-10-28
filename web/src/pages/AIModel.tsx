import { useEffect, useState, type FormEvent } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, Card, ErrorText, field, Status, toast } from "../ui";
import { ConnectorLogo } from "@/components/connector-logo";
import { appPath } from "@/lib/paths";
import { AICostNotice } from "./AIUsage";

type Settings = components["schemas"]["AISettings"];
type Model = { id: string; name: string; price: components["schemas"]["AIPrice"] | null };
type Price = components["schemas"]["AIPrice"];
type Task = "plan" | "turn" | "check_in" | "reply_labelling" | "classify_domain" | "social_replies" | "social_posts" | "review_replies" | "email_verification";
type TaskChoice = { model: string; inPrice: string; outPrice: string };
const tasks: { id: Task; name: string; help: string }[] = [
  { id: "plan", name: "Planning", help: "Drafting a mission from your goal" },
  { id: "turn", name: "Agent turns", help: "Starting a mission, answering you, acting on your decisions" },
  { id: "check_in", name: "Routine check-ins", help: "Looking in on a running mission when nothing new came from you" },
  { id: "reply_labelling", name: "Reply labelling", help: "Labelling replies to your outreach: interested, not now, out of office" },
  { id: "classify_domain", name: "Domain research", help: "Reading an unknown email domain's homepage to tell what kind of company it is" },
  { id: "social_replies", name: "Social replies", help: "Drafting replies to posts your social watches found, for a person to approve" },
  { id: "social_posts", name: "Social posts", help: "Drafting the brand's own posts on X and LinkedIn, for a person to approve" },
  { id: "review_replies", name: "Review answers", help: "Drafting public answers to reviews of your brands, for a person to edit and post" },
  { id: "email_verification", name: "Email verification", help: "Checking email addresses the free checks could not settle" },
];
const noChoice: TaskChoice = { model: "", inPrice: "", outPrice: "" };
const noChoices = Object.fromEntries(tasks.map((t) => [t.id, noChoice])) as Record<Task, TaskChoice>;
// " · $2/$10": US$ per million input/output tokens, for a model whose price is known.
const priceLabel = (p: Price | null | undefined) => (p ? ` · $${+(p.input_cents_per_million / 100).toFixed(2)}/$${+(p.output_cents_per_million / 100).toFixed(2)}` : "");
const cost = (p: Price | null | undefined) => (p ? p.input_cents_per_million + p.output_cents_per_million : Infinity);

const dollars = (cents: number) => `$${(cents / 100).toFixed(2)}`;
// Prices are entered in dollars per million tokens, as providers list them.
const toCents = (s: string) => (s.trim() === "" ? null : Math.round(parseFloat(s) * 100 * 10000) / 10000);
const toDollars = (c: number | null | undefined) => (c == null ? "" : String(c / 100));

// Which AI model plans missions and runs Outflare's own agent, and the most
// AI may spend in a month. The key itself lives on the connection.
export function AIModelCard({ ws, canManage }: { ws: string; canManage: boolean }) {
  const [s, setS] = useState<Settings | null>(null);
  const [conn, setConn] = useState("");
  const [model, setModel] = useState("");
  const [models, setModels] = useState<Model[] | null>(null);
  const [modelsError, setModelsError] = useState("");
  const [inPrice, setInPrice] = useState("");
  const [outPrice, setOutPrice] = useState("");
  const [limit, setLimit] = useState("");
  const [noLimit, setNoLimit] = useState(false);
  const [taskChoice, setTaskChoice] = useState<Record<Task, TaskChoice>>(noChoices);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const path = { params: { path: { ws } } };

  async function load() {
    const { data, error } = await api.GET("/v1/workspaces/{ws}/ai-settings", path);
    if (error) return setError(errorMessage(error));
    setS(data);
    setConn(data.integration_id ?? data.active?.integration_id ?? "");
    setModel(data.model || data.active?.model || "");
    setInPrice(toDollars(data.input_cents_per_million));
    setOutPrice(toDollars(data.output_cents_per_million));
    setNoLimit(data.monthly_limit_cents == null);
    setLimit(data.monthly_limit_cents == null ? "50" : String(data.monthly_limit_cents / 100));
    const provider = data.active?.provider;
    const saved = (t: Task): TaskChoice => {
      const c = data.task_models[t];
      return c && c.provider === provider ? { model: c.model, inPrice: toDollars(c.input_cents_per_million), outPrice: toDollars(c.output_cents_per_million) } : noChoice;
    };
    setTaskChoice(Object.fromEntries(tasks.map((t) => [t.id, saved(t.id)])) as Record<Task, TaskChoice>);
  }
  useEffect(() => {
    load();
  }, [ws]);

  // The models come from the provider, with the connection's own key.
  useEffect(() => {
    setModels(null);
    setModelsError("");
    if (!conn || !canManage) return;
    api.GET("/v1/workspaces/{ws}/integrations/{id}/models", { params: { path: { ws, id: conn } } }).then(({ data, error }) => {
      if (error) return setModelsError(errorMessage(error));
      setModels(data.data);
      setModel((m) => (m && data.data.some((x) => x.id === m) ? m : data.default_model ?? ""));
    });
  }, [conn, ws, canManage]);

  if (!s) return <Card title="AI model"><ErrorText>{error}</ErrorText></Card>;

  const connections = s.providers.flatMap((p) => p.connections.map((c) => ({ ...c, provider: p })));
  const chosen = connections.find((c) => c.id === conn);
  const known = chosen && model ? chosen.provider.known_prices[model] ?? models?.find((m) => m.id === model)?.price ?? null : null;
  const needsPrice = !!chosen && !!model && !known;
  const priceOf = (id: string) => (chosen ? chosen.provider.known_prices[id] ?? models?.find((m) => m.id === id)?.price ?? null : null);
  // What a task uses when the workspace doesn't choose: the main model, but
  // the provider's lighter one for routine check-ins and its cheapest for
  // short jobs, when they cost less (as the engine decides).
  const taskDefault = (t: Task) => {
    const p = chosen?.provider;
    const tier = t === "check_in" || t === "social_replies" || t === "social_posts" || t === "review_replies" ? p?.light_model : t === "reply_labelling" || t === "classify_domain" || t === "email_verification" ? (p?.cheap_model ?? p?.light_model) : null;
    return tier && tier !== model && cost(priceOf(tier)) < cost(known) ? tier : model;
  };
  const taskNeedsPrice = (t: Task) => !!taskChoice[t].model && !priceOf(taskChoice[t].model);
  const taskPriceMissing = tasks.some((t) => taskNeedsPrice(t.id) && (!toCents(taskChoice[t.id].inPrice) || !toCents(taskChoice[t.id].outPrice)));
  const setTask = (t: Task, c: Partial<TaskChoice>) => setTaskChoice((all) => ({ ...all, [t]: { ...all[t], ...c } }));

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/ai-settings", {
      ...path,
      body: {
        integration_id: conn || null,
        model: conn ? model : "",
        input_cents_per_million: needsPrice ? toCents(inPrice) : null,
        output_cents_per_million: needsPrice ? toCents(outPrice) : null,
        monthly_limit_cents: noLimit ? null : Math.round(parseFloat(limit || "0") * 100),
        ...(conn && models
          ? {
              task_models: Object.fromEntries(
                tasks.map(({ id }) => {
                  const c = taskChoice[id];
                  if (!c.model || c.model === taskDefault(id)) return [id, null];
                  return [id, { model: c.model, input_cents_per_million: taskNeedsPrice(id) ? toCents(c.inPrice) : null, output_cents_per_million: taskNeedsPrice(id) ? toCents(c.outPrice) : null }];
                }),
              ),
            }
          : {}),
      },
    });
    setBusy(false);
    setError(error ? errorMessage(error) : "");
    if (data) {
      setS(data);
      toast(data.active ? `Missions now use ${data.active.provider_name}, ${data.active.model}.` : "Saved.");
    }
  }

  if (connections.length === 0) {
    return (
      <Card title="AI model" description="Outflare plans missions and runs its own agent with an AI model you connect, using your own API key.">
        <p className="mb-3 text-[13px] text-muted-foreground">No AI model is connected yet. Pick one:</p>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          {s.providers.map((p) => (
            <a key={p.connector} href={appPath(ws, "connectors", p.connector)} className="flex min-h-10 items-center gap-2 rounded-md border px-3 py-2 text-[13px] font-medium hover:bg-muted/50">
              <ConnectorLogo id={p.connector} size="sm" />
              {p.name}
            </a>
          ))}
        </div>
        <SecurityNote />
      </Card>
    );
  }

  const limitCents = noLimit ? null : Math.round(parseFloat(limit || "0") * 100);
  return (
    <Card title="AI model" description="Which model plans missions from a goal and runs Outflare's own agent. Usage is billed by the provider, to your account.">
      <div className="mb-4 flex flex-wrap items-center gap-2 text-[13px]">
        {s.active && !s.problem ? (
          <Status tone="green">In use: {s.active.provider_name}, {s.active.model}</Status>
        ) : (
          <Status tone="amber">{s.problem?.message ?? "Choose a model."}</Status>
        )}
        <span className="text-muted-foreground">
          {dollars(s.spent_this_month_cents)} spent this month{s.monthly_limit_cents != null && ` of ${dollars(s.monthly_limit_cents)}`}
        </span>
        <a className="text-[13px] underline underline-offset-2" href={appPath(ws, "ai-usage")}>See where it goes</a>
      </div>
      <form onSubmit={save} className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">Provider</span>
            <select className={field + " w-full"} value={conn} disabled={!canManage} onChange={(e) => { if (e.target.value === conn) return; setConn(e.target.value); setModel(""); setTaskChoice(noChoices); }}>
              <option value="">Automatic: first connected ({connections[0].provider.name})</option>
              {connections.map((c) => (
                <option key={c.id} value={c.id}>{c.provider.name}{connections.filter((x) => x.provider.connector === c.provider.connector).length > 1 ? ` · ${c.name}` : ""}</option>
              ))}
            </select>
          </label>
          <label className="block text-sm">
            <span className="mb-1.5 block text-[13px] font-medium">Model</span>
            <select className={field + " w-full"} value={model} disabled={!canManage || !conn || !models} onChange={(e) => setModel(e.target.value)}>
              {!conn && <option value="">{connections[0].provider.default_model ?? "Choose a provider first"}</option>}
              {conn && !models && <option value={model}>{modelsError ? model || "Couldn't load models" : "Loading models…"}</option>}
              {conn && models && !model && <option value="">Choose a model</option>}
              {models?.map((m) => (
                <option key={m.id} value={m.id}>{m.name === m.id ? m.id : `${m.name} (${m.id})`}</option>
              ))}
            </select>
            {modelsError && <span className="mt-1 block text-xs text-destructive">{modelsError}</span>}
          </label>
        </div>

        {needsPrice && chosen && (
          <div>
            <p className="mb-2 text-[13px] text-muted-foreground">
              Outflare doesn't know what {model} costs. Enter {chosen.provider.name}'s price so spend limits work (
              <a className="underline underline-offset-2" href={chosen.provider.pricing_url} target="_blank" rel="noreferrer">pricing page</a>).
            </p>
            <div className="grid gap-3 sm:grid-cols-2">
              <PriceInput label="Input, US$ per million tokens" value={inPrice} onChange={setInPrice} disabled={!canManage} />
              <PriceInput label="Output, US$ per million tokens" value={outPrice} onChange={setOutPrice} disabled={!canManage} />
            </div>
          </div>
        )}
        {known && (
          <p className="text-[13px] text-muted-foreground">
            ${(known.input_cents_per_million / 100).toFixed(2)} per million input tokens, ${(known.output_cents_per_million / 100).toFixed(2)} per million output tokens.
          </p>
        )}

        <div>
          <span className="mb-1 block text-[13px] font-medium">Model for each task</span>
          <p className="mb-2 text-xs text-muted-foreground">Use a cheaper model where the work is routine. Prices are US$ per million input/output tokens. All tasks use the same {chosen?.provider.name ?? "provider"} key.</p>
          {conn && models ? (
            <div className="divide-y rounded-md border">
              {tasks.map((t) => {
                const c = taskChoice[t.id];
                const def = taskDefault(t.id);
                return (
                  <div key={t.id} className="grid gap-2 px-3 py-2.5 sm:grid-cols-[1fr_minmax(0,20rem)] sm:items-center">
                    <div className="text-[13px]">
                      <div className="font-medium">{t.name}</div>
                      <div className="text-xs text-muted-foreground">{t.help}</div>
                    </div>
                    <div>
                      <select aria-label={`Model for ${t.name}`} className={field + " w-full"} value={c.model && c.model !== def ? c.model : ""} disabled={!canManage} onChange={(e) => setTask(t.id, { model: e.target.value })}>
                        <option value="">{def ? `${def} (default)${priceLabel(priceOf(def))}` : "Same as above"}</option>
                        {models.filter((m) => m.id !== def).map((m) => (
                          <option key={m.id} value={m.id}>{m.id}{priceLabel(priceOf(m.id))}</option>
                        ))}
                      </select>
                      {taskNeedsPrice(t.id) && (
                        <div className="mt-2 grid grid-cols-2 gap-2">
                          <PriceInput label="Input US$/M" value={c.inPrice} onChange={(v) => setTask(t.id, { inPrice: v })} disabled={!canManage} />
                          <PriceInput label="Output US$/M" value={c.outPrice} onChange={(v) => setTask(t.id, { outPrice: v })} disabled={!canManage} />
                        </div>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
          ) : (
            <p className="text-xs text-muted-foreground">
              {conn ? "Loading models…" : `Choose a provider above to pick a model per task. Until then planning and agent turns use ${s.active?.model ?? "the default"}${s.tasks.find((t) => t.task === "check_in")?.model && s.tasks.find((t) => t.task === "check_in")?.model !== s.active?.model ? `, and check-ins ${s.tasks.find((t) => t.task === "check_in")?.model}` : ""}.`}
            </p>
          )}
        </div>

        <div>
          <span className="mb-1.5 block text-[13px] font-medium">Monthly AI limit</span>
          <div className="flex flex-wrap items-center gap-3">
            <span className="flex h-8 items-center rounded-md border border-input pl-2.5 text-[13px]">
              <span className="text-muted-foreground">US$</span>
              <input aria-label="Monthly AI limit in US dollars" className="h-full w-24 bg-transparent px-1.5 outline-none disabled:opacity-50" inputMode="decimal" value={limit} disabled={!canManage || noLimit}
                onChange={(e) => setLimit(e.target.value.replace(/[^0-9.]/g, ""))} />
            </span>
            <label className="flex min-h-8 items-center gap-2 text-[13px]">
              <input type="checkbox" className="size-4" checked={noLimit} disabled={!canManage} onChange={(e) => setNoLimit(e.target.checked)} />
              No limit
            </label>
          </div>
          <span className="mt-1 block text-xs text-muted-foreground">
            {limitCents != null ? `Planning stops and Outflare's agent pauses once AI calls this month reach ${dollars(limitCents)}. Each mission's own spend limit still applies.` : "Only each mission's own spend limit applies."}
          </span>
        </div>

        <SecurityNote provider={chosen?.provider.name ?? connections[0].provider.name} />
        <ErrorText>{error}</ErrorText>
        {canManage ? (
          <Button type="submit" disabled={busy || (!!conn && !model) || (needsPrice && (!toCents(inPrice) || !toCents(outPrice))) || taskPriceMissing}>{busy ? "Saving…" : "Save"}</Button>
        ) : (
          <p className="text-[13px] text-muted-foreground">Only owners and admins can change these.</p>
        )}
      </form>
    </Card>
  );
}

function PriceInput({ label, value, onChange, disabled }: { label: string; value: string; onChange: (v: string) => void; disabled: boolean }) {
  return (
    <label className="block text-sm">
      <span className="mb-1.5 block text-[13px] font-medium">{label}</span>
      <input className={field + " w-full"} inputMode="decimal" placeholder="e.g. 2.50" value={value} disabled={disabled} onChange={(e) => onChange(e.target.value.replace(/[^0-9.]/g, ""))} required />
    </label>
  );
}

function SecurityNote({ provider }: { provider?: string }) {
  return (
    <>
      <p className="mt-3 rounded-md border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
        Your key is encrypted, sent only to {provider ?? "the provider"}'s own API, and never shown again, logged or given to the model. Text the agent reads from emails and the web can't instruct it, and its email replies always wait for you. For extra safety, use a key just for Outflare.
      </p>
      <AICostNotice className="mt-2" />
    </>
  );
}
