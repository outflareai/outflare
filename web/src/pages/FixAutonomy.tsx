import { useEffect, useState } from "react";
import type { FixAutonomy as Setting, SEOSite } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, ErrorText, field, Loading, toast } from "../ui";

// Fix autonomy: how far agents in a mission may go with the fixes list on
// their own. The workspace's setting applies to every site without one of
// its own. Fixes that change redirects, canonicals, indexing or URLs
// always wait for a person, whatever the setting.

type Mode = Setting["mode"];

const modes: { id: Mode; title: string; body: string }[] = [
  { id: "copilot", title: "Copilot", body: "Agents suggest. Every fix they take waits for a person to approve it." },
  { id: "rules", title: "Rules", body: "The kinds of fix you pick run on their own. The rest wait for a person." },
  { id: "autonomous", title: "Autonomous", body: "Agents take any fix on their own, except the ones that always need a person." },
];

const categoryLabel: Record<string, string> = {
  metadata: "Titles, descriptions and headings",
  content: "Content",
  links: "Links",
  indexability: "Indexing (only the fixes that don't change it)",
  performance: "Speed",
  security: "Security",
  social: "Social previews",
};

const label = (c: string) => categoryLabel[c] ?? c.replace(/_/g, " ").replace(/^./, (x) => x.toUpperCase());

export function FixAutonomy({ ws, sites }: { ws: string; sites: SEOSite[] }) {
  const [site, setSite] = useState("");
  const [set, setSet] = useState<Setting | null>(null);
  const [draft, setDraft] = useState<{ mode: Mode; allowed: string[]; verify_on_deploy: boolean } | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  function show(s: Setting) {
    setSet(s);
    setDraft({ mode: s.mode, allowed: s.allowed, verify_on_deploy: s.verify_on_deploy });
  }

  useEffect(() => {
    setSet(null);
    const req = site
      ? api.GET("/v1/workspaces/{ws}/seo-sites/{site}/fix-autonomy", { params: { path: { ws, site } } })
      : api.GET("/v1/workspaces/{ws}/fix-autonomy", { params: { path: { ws } } });
    req.then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setError("");
      show(data);
    });
  }, [ws, site]);

  async function save() {
    if (!draft) return;
    setBusy(true);
    const { data, error } = site
      ? await api.PUT("/v1/workspaces/{ws}/seo-sites/{site}/fix-autonomy", { params: { path: { ws, site } }, body: draft })
      : await api.PUT("/v1/workspaces/{ws}/fix-autonomy", { params: { path: { ws } }, body: draft });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    if ("queued" in data || "mission_action" in data) return toast("Held for approval by the mission's owner.");
    show(data);
    toast("Saved.");
  }

  async function reset() {
    setBusy(true);
    const { data, error } = await api.DELETE("/v1/workspaces/{ws}/seo-sites/{site}/fix-autonomy", { params: { path: { ws, site } } });
    setBusy(false);
    if (error || !data) return toast(errorMessage(error), "error");
    if ("queued" in data || "mission_action" in data) return toast("Held for approval by the mission's owner.");
    show(data);
    toast("This site uses the workspace's setting again.");
  }

  const changed = !!set && !!draft && (draft.mode !== set.mode || draft.verify_on_deploy !== set.verify_on_deploy || draft.allowed.join() !== set.allowed.join());

  return (
    <div className="space-y-5 text-[13px]">
      {sites.length > 0 && (
        <select className={field} aria-label="Applies to" value={site} onChange={(e) => setSite(e.target.value)}>
          <option value="">Whole workspace</option>
          {sites.map((s) => <option key={s.id} value={s.id}>{s.host}</option>)}
        </select>
      )}
      {error && <ErrorText>{error}</ErrorText>}
      {!set || !draft ? (
        !error && <Loading />
      ) : (
        <>
          {site && set.inherited && <p className="text-muted-foreground">This site uses the workspace's setting. Saving here gives it its own.</p>}
          <fieldset className="space-y-2">
            <legend className="mb-2 font-medium">What agents may do in a mission</legend>
            {modes.map((m) => (
              <label key={m.id} className="flex cursor-pointer gap-3 rounded-md border p-3 has-[:checked]:border-foreground">
                <input type="radio" name="mode" className="mt-0.5" checked={draft.mode === m.id} onChange={() => setDraft({ ...draft, mode: m.id })} />
                <span>
                  <span className="block font-medium">{m.title}</span>
                  <span className="text-muted-foreground">{m.body}</span>
                </span>
              </label>
            ))}
          </fieldset>
          {draft.mode === "rules" && (
            <fieldset className="space-y-2">
              <legend className="mb-2 font-medium">Fixes that run on their own</legend>
              {set.categories.map((c) => (
                <label key={c} className="flex items-center gap-2">
                  <input
                    type="checkbox"
                    checked={draft.allowed.includes(c)}
                    onChange={(e) => setDraft({ ...draft, allowed: e.target.checked ? [...draft.allowed, c] : draft.allowed.filter((a) => a !== c) })}
                  />
                  {label(c)}
                </label>
              ))}
              {draft.allowed.filter((a) => !set.categories.includes(a)).length > 0 && (
                <p className="text-muted-foreground">Also these rules, set through the API: {draft.allowed.filter((a) => !set.categories.includes(a)).join(", ")}.</p>
              )}
            </fieldset>
          )}
          <p className="rounded-md bg-muted p-3 text-muted-foreground">
            Fixes that change redirects, canonicals, indexing, robots.txt or URLs can move or hide a whole site in search, so they always wait for a person.
          </p>
          <label className="flex items-start gap-2">
            <input type="checkbox" className="mt-0.5" checked={draft.verify_on_deploy} onChange={(e) => setDraft({ ...draft, verify_on_deploy: e.target.checked })} />
            <span>
              <span className="block font-medium">Check fixes after each deploy</span>
              <span className="text-muted-foreground">When a watched GitHub or Vercel project goes live, crawl the site again to confirm or reopen its fixes.</span>
            </span>
          </label>
          <div className="flex flex-wrap gap-2">
            <Button disabled={busy || !changed} onClick={save}>Save</Button>
            {site && !set.inherited && <Button variant="ghost" disabled={busy} onClick={reset}>Use the workspace's setting</Button>}
          </div>
        </>
      )}
    </div>
  );
}
