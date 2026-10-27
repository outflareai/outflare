import { useEffect, useState, type FormEvent } from "react";
import { api, errorMessage } from "../api";
import { Button, CenteredForm, ErrorText, Input } from "../ui";
import { Switch } from "@/components/ui/switch";

export function Setup({ onDone }: { onDone: () => void }) {
  const [form, setForm] = useState({ name: "", email: "", password: "", workspace_name: "" });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // Usage stats are on unless the server or build turned them off; the
  // owner sees that before they start and can switch it off here.
  const [telemetry, setTelemetry] = useState<{ shown: boolean; on: boolean }>({ shown: false, on: true });
  useEffect(() => {
    api.GET("/v1/setup").then(({ data }) => {
      if (data?.telemetry?.enabled) setTelemetry({ shown: true, on: true });
    });
  }, []);
  const set = (k: keyof typeof form) => (e: { target: { value: string } }) => setForm({ ...form, [k]: e.target.value });

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    const { error } = await api.POST("/v1/setup", { body: { ...form, ...(telemetry.shown && !telemetry.on ? { telemetry: false } : {}) } });
    setBusy(false);
    if (error) return setError(errorMessage(error));
    onDone();
  }

  return (
    <CenteredForm title="Set up Outflare" subtitle="Create the first account and workspace. You'll be the owner.">
      <form onSubmit={submit} className="space-y-4">
        <Input label="Your name" value={form.name} onChange={set("name")} required autoFocus />
        <Input label="Email" type="email" value={form.email} onChange={set("email")} required />
        <Input label="Password (10+ characters)" type="password" minLength={10} value={form.password} onChange={set("password")} required />
        <Input label="Workspace name" value={form.workspace_name} onChange={set("workspace_name")} required />
        {telemetry.shown && (
          <label className="flex items-start gap-3 rounded-md border p-3 text-[12px]">
            <Switch checked={telemetry.on} onCheckedChange={(on) => setTelemetry({ shown: true, on })} className="mt-0.5" aria-label="Send anonymous usage stats" />
            <span>
              <span className="block font-medium text-foreground">Send anonymous usage stats</span>
              <span className="text-muted-foreground">
                Once a day: version, platform and rough counts, plus a check for updates and security fixes. Never names, emails, domains or content.{" "}
                <a href="https://github.com/outflareai/outflare/blob/main/docs/telemetry.md" target="_blank" rel="noreferrer noopener" className="underline underline-offset-2">What's sent</a>
              </span>
            </span>
          </label>
        )}
        <ErrorText>{error}</ErrorText>
        <Button disabled={busy} className="w-full">Create workspace</Button>
      </form>
    </CenteredForm>
  );
}
