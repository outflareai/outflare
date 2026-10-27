import { useCallback, useEffect, useState } from "react";
import type { components } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Badge, Card, CopyButton, ErrorText, Loading, PageHeader, toast } from "../ui";
import { Switch } from "@/components/ui/switch";
import { ago } from "../format";

type Status = components["schemas"]["TelemetryStatus"];
type Report = components["schemas"]["TelemetryReport"];

const docs = "https://github.com/outflareai/outflare/blob/main/docs/telemetry.md";

// Why the switch is not in the owner's hands, in plain words.
const offReasons: Record<string, string> = {
  env: "Turned off on the server with OUTFLARE_TELEMETRY=off or DO_NOT_TRACK=1. Change it there.",
  build: "This build doesn't send usage stats.",
  no_endpoint: "This build has no stats server set, so nothing is sent. Official releases have one.",
};

// The whole server, not one workspace: its version, update notices, and
// the anonymous usage stats it sends.
export function Instance() {
  const [status, setStatus] = useState<Status>();
  const [preview, setPreview] = useState<Report>();
  const [showPreview, setShowPreview] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const { data, error } = await api.GET("/v1/instance/telemetry");
    if (error) return setError(errorMessage(error));
    setStatus(data);
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  async function togglePreview() {
    if (!showPreview && !preview) {
      const { data, error } = await api.GET("/v1/instance/telemetry/preview");
      if (error) return setError(errorMessage(error));
      setPreview(data);
    }
    setShowPreview((s) => !s);
  }

  async function setEnabled(enabled: boolean) {
    setBusy(true);
    const { data, error } = await api.PATCH("/v1/instance/telemetry", { body: { enabled } });
    setBusy(false);
    if (error) return toast(errorMessage(error), "error");
    setStatus(data);
    toast(enabled ? "Usage stats turned on. Thank you." : "Usage stats turned off. Nothing more will be sent.");
  }

  if (!status) return error ? <ErrorText>{error}</ErrorText> : <Loading />;
  const settingOn = status.disabled_by !== "setting" && status.disabled_by !== "env" && status.disabled_by !== "build";
  const reason = status.disabled_by ? offReasons[status.disabled_by] : undefined;

  return (
    <div className="space-y-6">
      <PageHeader title="This server" description="Settings for the whole Outflare server, across every workspace on it." />

      {status.notices.map((n) => (
        <div key={n.id} role="status" className={`rounded-lg border px-4 py-3 text-[13px] ${n.severity === "critical" ? "border-red-600/30 bg-red-500/10" : n.severity === "warning" ? "border-amber-600/30 bg-amber-500/10" : "bg-muted"}`}>
          <span className="font-medium">{n.title}</span>
          {n.url && (
            <a href={n.url} target="_blank" rel="noreferrer noopener" className="ml-2 underline underline-offset-2">
              Details
            </a>
          )}
        </div>
      ))}

      <Card title="Version">
        <div className="flex flex-wrap items-center gap-2 text-[13px]">
          <span className="font-mono">{status.version}</span>
          {status.update_available ? (
            <Badge tone="amber">{status.latest_version} available</Badge>
          ) : status.latest_version ? (
            <Badge tone="green">Up to date</Badge>
          ) : (
            <span className="text-muted-foreground">Update checks come with usage stats; turn them on to hear about new versions and security fixes.</span>
          )}
          {status.update_available && (
            <a href="https://github.com/outflareai/outflare/releases" target="_blank" rel="noreferrer noopener" className="underline underline-offset-2">
              Release notes and upgrade steps
            </a>
          )}
        </div>
      </Card>

      <Card
        title="Anonymous usage stats"
        description="Once a day this server says it exists and roughly how much it is used, and checks for updates. It never sends names, email addresses, domains, email content or credentials."
        action={
          <label className="flex items-center gap-2 text-[13px]">
            <span className="text-muted-foreground">{settingOn ? "On" : "Off"}</span>
            <Switch aria-label="Send anonymous usage stats" checked={settingOn} disabled={!status.can_change || busy} onCheckedChange={setEnabled} />
          </label>
        }
      >
        <div className="space-y-3 text-[13px]">
          {reason && <p className="text-muted-foreground">{reason}</p>}
          {!status.can_change && !reason && <p className="text-muted-foreground">Only the person who set up this server can change this.</p>}
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5">
            <dt className="text-muted-foreground">Last sent</dt>
            <dd>{status.last_sent_at ? ago(status.last_sent_at) : "Never"}{status.last_error && <span className="ml-2 text-muted-foreground">(last try failed: {status.last_error})</span>}</dd>
            <dt className="text-muted-foreground">Server id</dt>
            <dd className="flex items-center gap-2 font-mono text-[12px]">
              {status.instance_id} <CopyButton text={status.instance_id} />
            </dd>
            {status.endpoint && (
              <>
                <dt className="text-muted-foreground">Sent to</dt>
                <dd className="font-mono text-[12px]">{status.endpoint}</dd>
              </>
            )}
          </dl>
          <p className="text-muted-foreground">
            The server id is random and names this install only. Every count is a range like "6-10", never an exact number.{" "}
            <a href={docs} target="_blank" rel="noreferrer noopener" className="underline underline-offset-2">
              Every field, and why we ask
            </a>
          </p>
          <button onClick={togglePreview} className="font-mono text-[12px] text-muted-foreground hover:text-foreground">
            {showPreview ? "▾ Hide" : "▸ See"} exactly what is sent
          </button>
          {showPreview && preview && <pre className="max-h-96 overflow-auto rounded-md border bg-muted p-3 font-mono text-[12px]">{JSON.stringify(preview, null, 2)}</pre>}
        </div>
      </Card>
    </div>
  );
}
