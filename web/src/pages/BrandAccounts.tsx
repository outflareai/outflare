import { useBrand } from "@/lib/brand";
import { PickBrand } from "@/components/pick-brand";
import { useEffect, useState } from "react";
import type { Account, AccountIncident, AccountTakeover, BrowserTask } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { ask, Badge, Button, Card, confirmAction, Empty, ErrorText, field, PageHeader, toast } from "../ui";
import { track } from "@/lib/analytics";

const signIns = [
  { value: "local_password", label: "Password on the site" },
  { value: "social_sign_in", label: "Sign in with another account" },
  { value: "api_oauth", label: "API (OAuth through a connection)" },
  { value: "browser_session", label: "Browser session" },
  { value: "mailbox_verification", label: "Mailbox verification" },
];
const views = [
  { value: "all", label: "All" },
  { value: "needs_attention", label: "Needs attention" },
  { value: "dependencies", label: "Identities others sign in through" },
  { value: "archived", label: "Archived" },
];
const statusTone: Record<string, "grey" | "green" | "amber" | "red"> = { unverified: "grey", healthy: "green", needs_attention: "amber", blocked: "red", archived: "grey" };
const severityTone: Record<string, "grey" | "green" | "amber" | "red"> = { info: "grey", warning: "amber", critical: "red" };
const when = (s?: string | null) => (s ? new Date(s).toLocaleString() : "never");

// Accounts: the external accounts a brand's agent acts through (docs/accounts.md):
// how each signs in, whether access works, what depends on what, the vault
// (set by a person, never read back), the incidents that stop an account,
// the takeovers waiting for a person, and the browser tasks running on them.
export function BrandAccounts({ ws, canManage }: { ws: string; canManage: boolean }) {
  const { brand, active } = useBrand(ws);
  const brandId = brand?.id ?? "";
  const bp = { params: { path: { brand: brandId } } };
  const [view, setView] = useState("all");
  const [accounts, setAccounts] = useState<Account[] | null>(null);
  const [incidents, setIncidents] = useState<AccountIncident[]>([]);
  const [takeovers, setTakeovers] = useState<AccountTakeover[]>([]);
  const [tasks, setTasks] = useState<BrowserTask[]>([]);
  const [adding, setAdding] = useState(false);
  const [form, setForm] = useState({ platform: "", label: "", identity: "", sign_in: "local_password", url: "", depends_on: "" });
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");

  async function load() {
    if (!brandId) return;
    const [a, i, t, b] = await Promise.all([
      api.GET("/v1/brands/{brand}/accounts", { params: { path: { brand: brandId }, query: { view: view as "all" } } }),
      api.GET("/v1/brands/{brand}/account-incidents", bp),
      api.GET("/v1/brands/{brand}/account-takeovers", bp),
      api.GET("/v1/brands/{brand}/browser-tasks", { params: { path: { brand: brandId }, query: { status: "open" } } }),
    ]);
    if (a.data) setAccounts(a.data.data);
    if (i.data) setIncidents(i.data.data);
    if (t.data) setTakeovers(t.data.data);
    if (b.data) setTasks(b.data.data);
    const err = a.error ?? i.error ?? t.error ?? b.error;
    setError(err ? errorMessage(err) : "");
  }
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, brandId, view]);

  // Takeovers and tasks move while a person acts; keep them fresh.
  useEffect(() => {
    if (!brandId || !takeovers.some((t) => t.status === "active") && !tasks.length) return;
    const id = setInterval(load, 15000);
    return () => clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [brandId, takeovers.length, tasks.length]);

  async function run(key: string, call: () => Promise<{ error?: unknown }>, done?: string) {
    setBusy(key);
    const { error } = await call();
    setBusy("");
    setError(error ? errorMessage(error) : "");
    if (!error) {
      if (done) toast(done);
      load();
    }
    return !error;
  }

  async function add() {
    const body: Record<string, unknown> = { platform: form.platform.trim(), label: form.label.trim(), identity: form.identity.trim(), sign_in: form.sign_in, url: form.url.trim() };
    if (form.depends_on) body.depends_on = form.depends_on;
    if (await run("add", () => api.POST("/v1/brands/{brand}/accounts", { ...bp, body: body as never }), "Account recorded")) {
      track("ui.account_recorded", { sign_in: form.sign_in });
      setAdding(false);
      setForm({ platform: "", label: "", identity: "", sign_in: "local_password", url: "", depends_on: "" });
    }
  }

  async function setSecret(a: Account, kind: string) {
    const r = await ask({
      title: `${kind === "totp_seed" ? "Authenticator seed" : kind === "recovery" ? "Recovery codes" : "Password"} for ${a.label}`,
      body: "Sealed on the server and opened only for the account's own sign-ins. It is never shown again, not to you and not to the agent.",
      input: { label: "Value", type: "password", placeholder: kind === "totp_seed" ? "The base32 seed from the authenticator set-up" : "" },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Keep in the vault", value: "ok" }],
    });
    if (r?.choice !== "ok" || !r.value) return;
    await run("secret-" + a.id, () => api.PUT("/v1/brands/{brand}/accounts/{id}/secrets/{kind}", { params: { path: { brand: brandId, id: a.id, kind: kind as "password" } }, body: { value: r.value } }), "Kept in the vault");
  }

  async function removeSecret(a: Account, kind: string) {
    const signOut = kind === "session";
    if (!(await confirmAction(signOut ? "Sign the agent out?" : "Remove it from the vault?", signOut ? `The saved session for ${a.label} is removed; the next browser task signs in again from scratch.` : `The ${kind} for ${a.label} is removed; sign-ins that need it stop until a person adds it again.`, signOut ? "Sign out" : "Remove"))) return;
    await run("secret-" + a.id, () => api.DELETE("/v1/brands/{brand}/accounts/{id}/secrets/{kind}", { params: { path: { brand: brandId, id: a.id, kind: kind as "password" } } }), "Removed");
  }

  async function setStatus(a: Account, status: "unverified" | "blocked" | "archived") {
    if (status === "archived" && !(await confirmAction("Archive this account?", `${a.label} leaves the list and nothing uses it any more. Its secrets stay in the vault until you remove them.`, "Archive"))) return;
    await run("status-" + a.id, () => api.PATCH("/v1/brands/{brand}/accounts/{id}", { params: { path: { brand: brandId, id: a.id } }, body: { status } }), status === "blocked" ? "Blocked: nothing uses it until you unblock it" : status === "unverified" ? "Back in use" : "Archived");
  }

  async function resolveIncident(i: AccountIncident) {
    const r = await ask({ title: "Resolve this incident", body: "Say what was done; the account goes back to unverified until its next use.", input: { label: "Resolution", placeholder: "Password rotated", maxLength: 1000 }, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Resolve", value: "ok" }] });
    if (r?.choice !== "ok") return;
    await run("inc-" + i.id, () => api.PATCH("/v1/brands/{brand}/account-incidents/{id}", { params: { path: { brand: brandId, id: i.id } }, body: { status: "resolved", resolution: r.value } }), "Resolved");
  }

  async function start(t: AccountTakeover) {
    if (await run("tk-" + t.id, () => api.POST("/v1/brands/{brand}/account-takeovers/{id}/start", { params: { path: { brand: brandId, id: t.id } }, body: { minutes: 15 } }), "You hold the account for 15 minutes")) track("ui.account_takeover_started");
  }

  async function handBack(t: AccountTakeover, a?: Account) {
    const r = await ask({
      title: "Hand the account back",
      body: "Which identity is signed in now? The brand's own puts the account back in use; another blocks it. Never type a code here.",
      input: { label: "Signed in as", value: a?.identity ?? "", placeholder: "ops@acme.example", maxLength: 320 },
      choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: "Hand back", value: "ok" }],
    });
    if (r?.choice !== "ok") return;
    if (await run("tk-" + t.id, () => api.POST("/v1/brands/{brand}/account-takeovers/{id}/handback", { params: { path: { brand: brandId, id: t.id } }, body: { identity: r.value } }), "Handed back")) track("ui.account_takeover_handed_back");
  }

  async function cancelTakeover(t: AccountTakeover) {
    await run("tk-" + t.id, () => api.POST("/v1/brands/{brand}/account-takeovers/{id}/cancel", { params: { path: { brand: brandId, id: t.id } } }), "Cancelled");
  }

  async function cancelTask(t: BrowserTask) {
    await run("task-" + t.id, () => api.POST("/v1/brands/{brand}/browser-tasks/{id}/cancel", { params: { path: { brand: brandId, id: t.id } } }), "The task stops");
  }

  async function offboard() {
    if (!(await confirmAction("Take every account out of automated use?", "Every account is archived, every secret leaves the vault, takeovers are cancelled, incidents are resolved and the brand's agent grants are revoked. You get a list of what still needs doing at each platform.", "Offboard the brand's accounts"))) return;
    const { data, error } = await api.POST("/v1/brands/{brand}/accounts/offboard", { ...bp, body: {} });
    setError(error ? errorMessage(error) : "");
    if (data) {
      track("ui.accounts_offboarded", { archived: data.archived });
      const lines = data.needs_owner.map((n) => `${n.platform} (${n.label}): ${n.why}`);
      await ask({ title: `${data.archived} accounts archived, ${data.secrets_removed} secrets removed, ${data.grants_revoked} grants revoked`, body: lines.length ? <ul className="list-disc space-y-1 pl-4 text-sm">{lines.map((l) => <li key={l}>{l}</li>)}</ul> : "Nothing is left to do at the platforms.", choices: [{ label: "Done", value: "ok" }] });
      load();
    }
  }

  const byId = (id: string | null | undefined) => accounts?.find((a) => a.id === id);
  const incidentsOf = (id: string) => incidents.filter((i) => i.account_id === id);
  const waiting = takeovers.filter((t) => t.status === "requested" || t.status === "active");

  return (
    <div className="max-w-4xl space-y-6">
      <PageHeader
        title="Accounts"
        description="The external accounts your brand's agent acts through: social profiles, community and review sites, partner platforms, sites with no API. Every use is tracked; secrets live in a sealed vault only a person fills."
        action={canManage && brandId ? <div className="flex gap-2"><Button variant="ghost" onClick={offboard} disabled={!accounts?.length}>Offboard</Button><Button onClick={() => setAdding(true)}>Record an account</Button></div> : undefined}
      />
      {error && <ErrorText>{error}</ErrorText>}
      {!brandId && <PickBrand what="Accounts" brands={active.length} />}

      {brandId && waiting.length > 0 && (
        <Card title="Waiting for a person" description="A sign-in met a code, a CAPTCHA, a passkey or a consent screen. Take the account over, do that step in your own browser, then hand it back saying who is signed in.">
          <ul className="divide-y">
            {waiting.map((t) => {
              const a = byId(t.account_id);
              return (
                <li key={t.id} className="flex items-start justify-between gap-3 py-2 text-sm">
                  <div className="min-w-0">
                    <p className="font-medium">{a ? `${a.label} on ${a.platform}` : "An account"} <Badge tone={t.status === "active" ? "amber" : "grey"}>{t.status === "active" ? `held until ${when(t.expires_at)}` : `needs a person: ${t.reason}`}</Badge></p>
                    {t.note && <p className="text-muted-foreground">{t.note}</p>}
                    <p className="text-xs text-muted-foreground">Asked {when(t.requested_at)}</p>
                  </div>
                  {canManage && (
                    <div className="flex shrink-0 gap-2">
                      {t.status === "requested" && <Button disabled={busy !== ""} onClick={() => start(t)}>Take over</Button>}
                      {t.status === "active" && <Button disabled={busy !== ""} onClick={() => handBack(t, a)}>Hand back</Button>}
                      <Button variant="ghost" disabled={busy !== ""} onClick={() => cancelTakeover(t)}>Cancel</Button>
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        </Card>
      )}

      {brandId && adding && canManage && (
        <Card title="Record an account" description="Recorded is not verified: the account shows as unverified until a sign-in works. Add its password or authenticator seed to the vault afterwards.">
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="text-sm">Platform<input className={field + " mt-1 w-full"} value={form.platform} onChange={(e) => setForm({ ...form, platform: e.target.value })} placeholder="g2, producthunt, x" /></label>
            <label className="text-sm">Label<input className={field + " mt-1 w-full"} value={form.label} onChange={(e) => setForm({ ...form, label: e.target.value })} placeholder="The brand's G2 profile" /></label>
            <label className="text-sm">Signs in as<input className={field + " mt-1 w-full"} value={form.identity} onChange={(e) => setForm({ ...form, identity: e.target.value })} placeholder="ops@acme.example" /></label>
            <label className="text-sm">Page<input className={field + " mt-1 w-full"} value={form.url} onChange={(e) => setForm({ ...form, url: e.target.value })} placeholder="https://www.g2.com/products/acme" /></label>
            <label className="text-sm">How it signs in<select className={field + " mt-1 w-full"} value={form.sign_in} onChange={(e) => setForm({ ...form, sign_in: e.target.value })}>{signIns.map((s) => <option key={s.value} value={s.value}>{s.label}</option>)}</select></label>
            <label className="text-sm">Through which identity<select className={field + " mt-1 w-full"} value={form.depends_on} onChange={(e) => setForm({ ...form, depends_on: e.target.value })}><option value="">None</option>{accounts?.filter((a) => a.status !== "archived").map((a) => <option key={a.id} value={a.id}>{a.label} ({a.identity || a.platform})</option>)}</select></label>
          </div>
          <div className="mt-3 flex gap-2">
            <Button disabled={busy !== "" || !form.platform.trim() || !form.label.trim()} onClick={add}>{busy === "add" ? "Recording…" : "Record"}</Button>
            <Button variant="ghost" onClick={() => setAdding(false)}>Cancel</Button>
          </div>
        </Card>
      )}

      {brandId && (
        <Card title="Accounts" action={<select className={field} value={view} onChange={(e) => setView(e.target.value)} aria-label="View">{views.map((v) => <option key={v.value} value={v.value}>{v.label}</option>)}</select>}>
          {accounts && accounts.length === 0 && <Empty title="No accounts here">{view === "all" ? "Record the accounts the agent may act through; it asks for the ones it needs." : "Nothing in this view."}</Empty>}
          <ul className="divide-y">
            {accounts?.map((a) => {
              const open = incidentsOf(a.id);
              const through = byId(a.depends_on);
              return (
                <li key={a.id} className="space-y-1 py-3 text-sm">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0">
                      <p className="font-medium">{a.label} <span className="text-muted-foreground">on {a.platform}</span> <Badge tone={statusTone[a.status] ?? "grey"}>{a.status.replace("_", " ")}</Badge></p>
                      <p className="text-muted-foreground">
                        {a.identity || "no identity"} · {signIns.find((s) => s.value === a.sign_in)?.label ?? a.sign_in}
                        {through && <> · through {through.label}</>}
                        {a.dependents > 0 && <> · {a.dependents} sign in through it</>}
                      </p>
                      <p className="text-xs text-muted-foreground">Last worked {when(a.last_verified_at)} · last tried {when(a.last_attempt_at)} · vault: {a.secrets.length ? a.secrets.join(", ") : "empty"}{a.url && <> · <a className="hover:underline" href={a.url} target="_blank" rel="noreferrer">page</a></>}</p>
                      {open.map((i) => (
                        <p key={i.id} className="text-xs">
                          <Badge tone={severityTone[i.severity] ?? "grey"}>{i.kind.replace("_", " ")}</Badge> seen {i.repeats} {i.repeats === 1 ? "time" : "times"}, last {when(i.last_seen_at)}
                          {canManage && <> · <button className="underline" disabled={busy !== ""} onClick={() => resolveIncident(i)}>resolve</button></>}
                        </p>
                      ))}
                    </div>
                    {canManage && a.status !== "archived" && (
                      <div className="flex shrink-0 flex-wrap justify-end gap-1">
                        {(["password", "totp_seed", "recovery"] as const).map((k) => (
                          (a.secrets as string[]).includes(k) ? (
                            <Button key={k} variant="ghost" disabled={busy !== ""} onClick={() => removeSecret(a, k)}>Remove {k.replace("_", " ")}</Button>
                          ) : (
                            <Button key={k} variant="ghost" disabled={busy !== ""} onClick={() => setSecret(a, k)}>Add {k.replace("_", " ")}</Button>
                          )
                        ))}
                        {(a.secrets as string[]).includes("session") && (
                          <Button variant="ghost" disabled={busy !== ""} onClick={() => removeSecret(a, "session")}>Sign out</Button>
                        )}
                        {a.status === "blocked" ? (
                          <Button variant="ghost" disabled={busy !== ""} onClick={() => setStatus(a, "unverified")}>Unblock</Button>
                        ) : (
                          <Button variant="ghost" disabled={busy !== ""} onClick={() => setStatus(a, "blocked")}>Block</Button>
                        )}
                        <Button variant="ghost" disabled={busy !== ""} onClick={() => setStatus(a, "archived")}>Archive</Button>
                      </div>
                    )}
                  </div>
                </li>
              );
            })}
          </ul>
        </Card>
      )}

      {brandId && tasks.length > 0 && (
        <Card title="Browser tasks under way" description="Bounded work in a real browser on sites with no API, within the hosts and account each task was given.">
          <ul className="divide-y">
            {tasks.map((t) => (
              <li key={t.id} className="flex items-start justify-between gap-3 py-2 text-sm">
                <div className="min-w-0">
                  <p className="font-medium">{t.goal} <Badge tone={t.status === "waiting_person" ? "amber" : t.status === "running" ? "green" : "grey"}>{t.status.replace("_", " ")}</Badge></p>
                  <p className="text-xs text-muted-foreground">{t.hosts.join(", ")} · {t.steps} steps · {t.runs} {t.runs === 1 ? "run" : "runs"}{t.summary && <> · {t.summary}</>}</p>
                </div>
                {canManage && <Button variant="ghost" disabled={busy !== ""} onClick={() => cancelTask(t)}>Cancel</Button>}
              </li>
            ))}
          </ul>
        </Card>
      )}
    </div>
  );
}
