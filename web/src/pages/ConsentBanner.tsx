import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { track } from "@/lib/analytics";
import { Button, confirmAction, ErrorText, field, Input, toast } from "../ui";
import { ago } from "../format";

type Banner = components["schemas"]["ConsentBanner"];
type Config = components["schemas"]["ConsentBannerConfig"];
type Rule = NonNullable<components["schemas"]["ConsentBannerRegions"]["eea"]>;
type Texts = components["schemas"]["ConsentBannerTexts"];

const rules: { value: Rule; label: string }[] = [
  { value: "opt_in", label: "Ask first (opt-in)" },
  { value: "opt_out", label: "Opt-out (Do not sell or share)" },
  { value: "notice", label: "Inform only" },
  { value: "none", label: "No banner" },
];
const regions: { key: "eea" | "us" | "other"; label: string; hint: string }[] = [
  { key: "eea", label: "EEA, UK and Switzerland", hint: "GDPR asks first." },
  { key: "us", label: "United States", hint: "State laws such as the CCPA let people opt out." },
  { key: "other", label: "Everywhere else", hint: "" },
];
const frameworks: { key: "gpc" | "google_consent_mode" | "meta" | "tiktok" | "microsoft"; label: string }[] = [
  { key: "gpc", label: "Honour Global Privacy Control" },
  { key: "google_consent_mode", label: "Google Consent Mode v2" },
  { key: "meta", label: "Meta pixel consent" },
  { key: "tiktok", label: "TikTok pixel consent" },
  { key: "microsoft", label: "Microsoft UET consent" },
];
const categories: { key: "analytics" | "marketing" | "preferences"; label: string }[] = [
  { key: "analytics", label: "Analytics" },
  { key: "marketing", label: "Marketing" },
  { key: "preferences", label: "Preferences" },
];
// The English words a person most often changes; the rest are in the API.
const textFields: { key: keyof Texts; label: string }[] = [
  { key: "title", label: "Title" },
  { key: "body", label: "Text" },
  { key: "accept", label: "Accept button" },
  { key: "reject", label: "Reject button" },
];

function Select<T extends string>({ label, value, options, onChange }: { label: string; value: T; options: { value: T; label: string }[]; onChange: (v: T) => void }) {
  return (
    <label className="grid gap-1 text-[13px]">
      <span className="font-medium">{label}</span>
      <select className={field} value={value} onChange={(e) => onChange(e.target.value as T)}>
        {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
      </select>
    </label>
  );
}

function Check({ label, checked, onChange, hint, disabled }: { label: ReactNode; checked: boolean; onChange: (v: boolean) => void; hint?: string; disabled?: boolean }) {
  return (
    <label className="flex items-start gap-2 text-[13px]">
      <input type="checkbox" className="mt-0.5" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span>
        {label}
        {hint && <span className="block text-[12px] text-muted-foreground">{hint}</span>}
      </span>
    </label>
  );
}

/**
 * A site's cookie consent banner: shown by the site's own script, with a
 * rule per region, the frameworks it follows, the categories it asks
 * about, and how it looks. Every law and framework starts on.
 */
export function ConsentBannerEditor({ ws, keyID, name, canManage, onDone }: { ws: string; keyID: string; name: string; canManage: boolean; onDone: () => void }) {
  const [banner, setBanner] = useState<Banner | null>(null);
  const [cfg, setCfg] = useState<Config | null>(null);
  const [en, setEn] = useState<Texts>({});
  const [advanced, setAdvanced] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    track("ui.consent_banner_opened", { site_key_id: keyID });
    api.GET("/v1/workspaces/{ws}/site-keys/{id}/consent-banner", { params: { path: { ws, id: keyID } } }).then(({ data, error }) => {
      if (error || !data) return setError(errorMessage(error));
      setBanner(data);
      setCfg(data.config);
      setEn(data.config.texts.en ?? {});
    });
  }, [ws, keyID]);

  if (!cfg || !banner) return <div className="rounded-lg border p-3 text-[13px] text-muted-foreground">{error ? <ErrorText>{error}</ErrorText> : "Loading…"}</div>;
  const set = (patch: Partial<Config>) => setCfg({ ...cfg, ...patch });

  async function save(e: FormEvent) {
    e.preventDefault();
    if (!cfg || !banner) return;
    setBusy(true);
    const body: components["schemas"]["ConsentBannerInput"] = { ...cfg, texts: { ...cfg.texts, en } };
    // Custom markup needs a higher scope; send it only when it changed.
    if (cfg.custom_html === banner.config.custom_html) delete body.custom_html;
    if (cfg.custom_css === banner.config.custom_css) delete body.custom_css;
    const { data, error } = await api.PUT("/v1/workspaces/{ws}/site-keys/{id}/consent-banner", { params: { path: { ws, id: keyID } }, body });
    setBusy(false);
    if (error || !data) return setError(errorMessage(error));
    setBanner(data);
    setCfg(data.config);
    toast(data.config.enabled ? "Saved. Visitors see the banner within a few minutes." : "Saved. The banner is off.");
  }

  async function reask() {
    if (!(await confirmAction("Ask every visitor again?", <p>Visitors who already chose see the banner again on their next page, where their region asks first. Do this when the site starts using a new kind of cookie.</p>, "Ask again", false))) return;
    const { data, error } = await api.POST("/v1/workspaces/{ws}/site-keys/{id}/consent-banner/reask", { params: { path: { ws, id: keyID } } });
    if (error || !data) return setError(errorMessage(error));
    setBanner(data);
    toast("Visitors will be asked again.");
  }

  const fw = cfg.frameworks;
  const disabled = !canManage;
  return (
    <form onSubmit={save} className="space-y-4 rounded-lg border p-3">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <div className="font-medium">Consent banner for {name}</div>
          <p className="text-[12px] text-muted-foreground">
            The script shows it on your site and keeps each visitor's choice as their consent. Version {banner.version || "not saved"}
            {banner.updated_at ? `, saved ${ago(banner.updated_at)}` : ""}. Asks again from consent version {banner.consent_version}.
          </p>
        </div>
        <Check label={<span className="font-medium">Show the banner</span>} checked={cfg.enabled} onChange={(v) => set({ enabled: v })} />
      </div>

      <fieldset disabled={disabled} className="space-y-4">
        <div className="grid gap-3 sm:grid-cols-3">
          {regions.map((r) => (
            <div key={r.key} className="space-y-1">
              <Select label={r.label} value={cfg.regions[r.key] ?? "opt_in"} options={rules} onChange={(v) => set({ regions: { ...cfg.regions, [r.key]: v } })} />
              {r.hint && <p className="text-[12px] text-muted-foreground">{r.hint}</p>}
            </div>
          ))}
        </div>

        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <div className="text-[13px] font-medium">It asks about</div>
            <Check label="Necessary (always on)" checked disabled onChange={() => {}} />
            {categories.map((c) => (
              <Check key={c.key} label={c.label} checked={!!cfg.categories[c.key]} onChange={(v) => set({ categories: { ...cfg.categories, [c.key]: v } })} />
            ))}
            <p className="text-[12px] text-muted-foreground">
              Keep a tag back until its category is allowed with <code>type="text/plain" data-category="marketing"</code>.
            </p>
          </div>
          <div className="space-y-2">
            <div className="text-[13px] font-medium">Signals it follows and sets</div>
            {frameworks.map((f) => (
              <Check key={f.key} label={f.label} checked={!!fw[f.key]} onChange={(v) => set({ frameworks: { ...fw, [f.key]: v } })} />
            ))}
          </div>
        </div>

        <div className="grid gap-3 sm:grid-cols-3">
          <Select label="Layout" value={cfg.layout} options={[{ value: "bar", label: "Bar" }, { value: "box", label: "Box" }, { value: "modal", label: "Modal" }]} onChange={(v) => set({ layout: v })} />
          <Select
            label="Position"
            value={cfg.position}
            options={[
              { value: "bottom", label: "Bottom" },
              { value: "top", label: "Top" },
              { value: "bottom_right", label: "Bottom right" },
              { value: "bottom_left", label: "Bottom left" },
              { value: "center", label: "Centre" },
            ]}
            onChange={(v) => set({ position: v })}
          />
          <Select label="Look" value={cfg.theme} options={[{ value: "light", label: "Light" }, { value: "dark", label: "Dark" }, { value: "brand", label: "Your brand's colours" }]} onChange={(v) => set({ theme: v })} />
        </div>
        {cfg.theme === "brand" && (
          <p className="text-[12px] text-muted-foreground">
            Uses the colours and logo in the Company hub
            {banner.look.colors.primary ? ` (${banner.look.colors.primary})` : ": none set yet, so it looks like Light until you add them"}.
          </p>
        )}

        <div className="grid gap-3 sm:grid-cols-2">
          <Input label="Cookie policy" value={cfg.policy_url} onChange={(e) => set({ policy_url: e.target.value })} placeholder="/cookies" hint="An https address or a path on your site." />
          <Input
            label="Ask again after (days)"
            type="number"
            min={0}
            max={730}
            value={cfg.reask_after_days}
            onChange={(e) => set({ reask_after_days: Number(e.target.value) || 0 })}
            hint="Where the region asks first. 0 never asks again."
          />
        </div>
        <div className="flex flex-wrap gap-4">
          <Check label="Show the logo" checked={cfg.show_logo} onChange={(v) => set({ show_logo: v })} />
          <Check label="Button to change the choice later" checked={cfg.reopen_button} onChange={(v) => set({ reopen_button: v })} />
        </div>

        <details open={advanced} onToggle={(e) => setAdvanced((e.target as HTMLDetailsElement).open)}>
          <summary className="cursor-pointer text-[13px] font-medium">Words, colours and your own design</summary>
          <div className="mt-3 space-y-3">
            <div className="grid gap-3 sm:grid-cols-2">
              {textFields.map((t) => (
                <Input key={t.key} label={`${t.label} (English)`} value={en[t.key] ?? ""} onChange={(e) => setEn({ ...en, [t.key]: e.target.value })} placeholder="Outflare's own" />
              ))}
            </div>
            <p className="text-[12px] text-muted-foreground">Left empty, the banner uses Outflare's words in the visitor's language. Other languages and words are in the API.</p>
            <div className="grid gap-3 sm:grid-cols-3">
              {(["primary", "background", "text"] as const).map((k) => (
                <Input key={k} label={`${k[0].toUpperCase()}${k.slice(1)} colour`} value={cfg.colors[k]} onChange={(e) => set({ colors: { ...cfg.colors, [k]: e.target.value } })} placeholder="From the look" />
              ))}
            </div>
            <label className="grid gap-1 text-[13px]">
              <span className="font-medium">Your own CSS</span>
              <textarea className={field + " min-h-20 font-mono"} value={cfg.custom_css} onChange={(e) => set({ custom_css: e.target.value })} placeholder=".of-cb { border-radius: 0 }" />
            </label>
            <label className="grid gap-1 text-[13px]">
              <span className="font-medium">Your own HTML</span>
              <textarea
                className={field + " min-h-24 font-mono"}
                value={cfg.custom_html}
                onChange={(e) => set({ custom_html: e.target.value })}
                placeholder={'<div class="of-cb of-cb--bar"><p>…</p><button data-of-consent="reject">Reject</button><button data-of-consent="accept">Accept</button></div>'}
              />
              <span className="text-[12px] text-muted-foreground">Replaces the banner. Runs on your site, so only owners and admins can change it.</span>
            </label>
          </div>
        </details>
      </fieldset>

      <ErrorText>{error}</ErrorText>
      <div className="flex flex-wrap gap-2">
        {canManage && <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Save"}</Button>}
        {canManage && banner.saved && <Button type="button" variant="ghost" onClick={reask}>Ask everyone again</Button>}
        <Button type="button" variant="ghost" onClick={onDone}>Close</Button>
      </div>
    </form>
  );
}
