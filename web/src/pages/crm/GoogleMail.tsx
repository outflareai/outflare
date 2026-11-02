import { useCallback, useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { Mail, ShieldCheck, RefreshCw, Unplug } from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, ErrorText, Loading, field, toast } from "@/ui";
import { useBrand } from "@/lib/brand";
import { stamp } from "@/format";
import { MemberPicker } from "../RecordEdit";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from "@/components/ui/dialog";

type Rollout = Schemas["CRMMailRollout"];
type Connection = Schemas["CRMGoogleConnection"];
type Capture = Schemas["CRMMailCapture"];
export function GoogleMailSetup({
  ws,
  canManage,
  canManagePolicy,
}: {
  ws: string;
  canManage: boolean;
  canManagePolicy: boolean;
}) {
  const { brand } = useBrand(ws);
  const [status, setStatus] = useState<Connection | null>(null);
  const [rollout, setRollout] = useState<Rollout | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);
  const [purge, setPurge] = useState(false);
  const [notice] = useState(() => {
    const code = new URLSearchParams(window.location.search).get("crm_google");
    return (
      (
        {
          connected:
            "Your Google account is connected. Capture stays limited to the records you select.",
          denied:
            "Google access was not approved. Your account has not been connected.",
          wrong_account:
            "Choose the Google account whose address matches your Outflare sign-in.",
          not_eligible:
            "Your administrator changed your eligibility. Check access before connecting again.",
          consent_failed:
            "Google consent could not be completed. Use your matching account and approve read-only Gmail access, then try again.",
        } as Record<string, string>
      )[code ?? ""] ?? ""
    );
  });
  const load = useCallback(
    async (signal?: AbortSignal) => {
      if (!brand) return;
      try {
        const r = await api.GET("/v1/brands/{brand}/crm/mail/connection", {
          params: { path: { brand: brand.id } },
          signal,
        });
        if (signal?.aborted) return;
        if (r.data) setStatus(r.data);
        else setError(errorMessage(r.error));
      } catch (e) {
        if (!signal?.aborted) setError(errorMessage(e));
      }
    },
    [brand?.id],
  );
  useEffect(() => {
    const c = new AbortController();
    setStatus(null);
    setError("");
    void load(c.signal);
    const timer = setInterval(() => void load(c.signal), 30000);
    return () => {
      c.abort();
      clearInterval(timer);
    };
  }, [load]);
  useEffect(() => {
    if (!brand || !canManagePolicy) return;
    const c = new AbortController();
    setRollout(null);
    void api
      .GET("/v1/brands/{brand}/crm/mail/rollout", {
        params: { path: { brand: brand.id } },
        signal: c.signal,
      })
      .then((r) => {
        if (c.signal.aborted) return;
        if (r.data) setRollout(r.data);
        else setError(errorMessage(r.error));
      })
      .catch((e) => {
        if (!c.signal.aborted) setError(errorMessage(e));
      });
    return () => c.abort();
  }, [brand?.id, canManagePolicy]);
  async function saveRollout() {
    if (!brand || !rollout || busy) return;
    setBusy(true);
    setError("");
    try {
      const r = await api.PUT("/v1/brands/{brand}/crm/mail/rollout", {
        params: { path: { brand: brand.id } },
        body: rollout,
      });
      if (r.data) {
        setRollout(r.data);
        await load();
        toast("Sales eligibility saved. Exclusions override membership.");
      } else setError(errorMessage(r.error));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function connect() {
    if (!brand || busy) return;
    setBusy(true);
    setError("");
    try {
      const r = await api.POST("/v1/brands/{brand}/crm/mail/google/authorize", {
        params: { path: { brand: brand.id } },
      });
      if (r.data) window.location.assign(r.data.url);
      else {
        setError(errorMessage(r.error));
        setBusy(false);
      }
    } catch (e) {
      setError(errorMessage(e));
      setBusy(false);
    }
  }
  async function sync() {
    if (!brand || busy) return;
    setBusy(true);
    setError("");
    try {
      const r = await api.POST("/v1/brands/{brand}/crm/mail/sync", {
        params: { path: { brand: brand.id } },
      });
      if (r.data) {
        toast("Google capture queued for your selected records.");
        await load();
      } else setError(errorMessage(r.error));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  async function disconnect() {
    if (!brand || busy) return;
    setBusy(true);
    setError("");
    try {
      const r = await api.DELETE("/v1/brands/{brand}/crm/mail/connection", {
        params: { path: { brand: brand.id }, query: { purge } },
      });
      if (r.data) {
        setDisconnecting(false);
        toast(
          purge
            ? "Google disconnected and your imported copies deleted."
            : "Google disconnected. Retained copies keep their current sharing.",
        );
        await load();
      } else setError(errorMessage(r.error));
    } catch (e) {
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="space-y-4 rounded-xl border bg-card p-5">
      <header className="flex items-center gap-2 font-semibold">
        <Mail className="size-4" />
        Personal Google capture
      </header>
      <p className="text-sm text-muted-foreground">
        IT controls who may connect. Each eligible salesperson approves their
        own Google account. No company-wide mailbox access, sending permission
        or administrator impersonation is used.
      </p>
      {notice && (
        <p role="status" className="rounded-lg border bg-muted/30 p-3 text-sm">
          {notice}
        </p>
      )}
      <ErrorText>{error}</ErrorText>
      {!status && !error && <Loading />}
      {status && (
        <div className="space-y-3 rounded-lg border p-4">
          <h3 className="text-sm font-medium">My Google account</h3>
          <p className="text-sm">
            {status.status === "not_configured"
              ? "Google app setup is required."
              : status.status === "not_eligible"
                ? "Your administrator needs to enable you and configure internal domains."
                : status.status === "reconnect_required"
                  ? "Reconnect Google to restore read-only capture."
                  : status.connected
                    ? "Connected with individual consent."
                    : "Ready to connect your own Google account."}
          </p>
          <p className="text-xs text-muted-foreground">
            {status.capture_count} selected records ·{" "}
            {status.last_synced_at
              ? `Last captured ${stamp(status.last_synced_at)}`
              : "No completed capture yet"}
          </p>
          {status.error_code && (
            <p role="alert" className="text-xs text-destructive">
              Capture could not finish. Check Google access and your selected
              records, then retry. Reconnect if Google access was revoked.
            </p>
          )}
          {(canManage || status.connected) && (
            <div className="flex flex-wrap gap-2">
              {canManage &&
                status.configured &&
                status.eligible &&
                (!status.connected ||
                  status.status === "reconnect_required") && (
                  <Button disabled={busy} onClick={() => void connect()}>
                    {busy
                      ? "Opening Google…"
                      : status.connected
                        ? "Reconnect my Google account"
                        : "Connect my Google account"}
                  </Button>
                )}
              {canManage &&
                status.connected &&
                status.eligible &&
                status.status === "connected" && (
                  <Button
                    disabled={busy || status.capture_count === 0}
                    onClick={() => void sync()}
                  >
                    <RefreshCw className="size-3.5" />
                    Capture now
                  </Button>
                )}
              {status.connected && (
                <Button
                  variant="ghost"
                  disabled={busy}
                  onClick={() => {
                    setPurge(false);
                    setDisconnecting(true);
                  }}
                >
                  <Unplug className="size-3.5" />
                  Disconnect Google
                </Button>
              )}
            </div>
          )}
          <p className="text-xs text-muted-foreground">
            Google grants read-only mailbox access; Outflare searches only your
            selected contacts/accounts, checks all known participants and
            retains eligible message text. Capture starts with the last seven
            days, includes received and sent mail, and checks about every five
            minutes. Drafts, scheduled mail, spam and trash are excluded.
            Attachments stay in Google. No record is selected automatically.
          </p>
          <p className="text-xs text-muted-foreground">
            Select a contact or company in its Email tab. Capture does not
            change its sharing rule. These are CRM copies: deleting the Google
            original does not remove a retained copy here.
          </p>
        </div>
      )}
      {rollout && canManagePolicy && (
        <fieldset disabled={busy} className="min-w-0 space-y-4 border-t pt-4">
          <h3 className="flex items-center gap-2 text-sm font-semibold">
            <ShieldCheck className="size-4" />
            Sales eligibility
          </h3>
          <p className="text-xs text-muted-foreground">
            Selected members also need CRM edit access to start capture. Start
            with the sales team. Leave executives, Finance, HR, accountants and
            legal users out unless there is an explicit business need. An
            eligible person must still approve their own matching Google
            account.
          </p>
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={rollout.enabled}
              disabled={busy}
              onChange={(e) =>
                setRollout({ ...rollout, enabled: e.target.checked })
              }
            />
            Enable individual Google connections for selected members
          </label>
          <div>
            <label htmlFor="crm-mail-allowed" className="mb-1 block text-xs">
              Eligible sales members
            </label>
            <MemberPicker
              ws={ws}
              id="crm-mail-allowed"
              value={rollout.allowed_user_ids}
              multiple
              onChange={(v) =>
                setRollout({
                  ...rollout,
                  allowed_user_ids: Array.isArray(v) ? (v as string[]) : [],
                })
              }
            />
          </div>
          <div>
            <label htmlFor="crm-mail-excluded" className="mb-1 block text-xs">
              Always excluded members
            </label>
            <MemberPicker
              ws={ws}
              id="crm-mail-excluded"
              value={rollout.excluded_user_ids}
              multiple
              onChange={(v) =>
                setRollout({
                  ...rollout,
                  excluded_user_ids: Array.isArray(v) ? (v as string[]) : [],
                })
              }
            />
          </div>
          <p className="text-xs text-muted-foreground">
            Exclusions always win. Removing eligibility or disabling this
            rollout disconnects affected Google grants. It does not delete
            existing copies or give managers/HR permission to read them. Capture
            and sharing remain separate.
          </p>
          <Button disabled={busy} onClick={() => void saveRollout()}>
            Save sales eligibility
          </Button>
          <details className="rounded-lg border p-3 text-xs">
            <summary className="cursor-pointer font-medium">
              Google Workspace administrator setup
            </summary>
            <ol className="mt-3 list-decimal space-y-2 pl-5">
              {status?.client_id && (
                <li>
                  Find the approved Google app by its public client ID:
                  <input
                    aria-label="Google OAuth client ID"
                    readOnly
                    value={status.client_id}
                    className={`${field} mt-1 block w-full font-mono text-xs`}
                  />
                </li>
              )}
              <li>
                Configure the server’s Google OAuth web app and register this
                redirect URI:
                <input
                  aria-label="Personal Google redirect URI"
                  readOnly
                  value={status?.redirect_uri ?? ""}
                  className={`${field} mt-1 block w-full font-mono text-xs`}
                />
              </li>
              <li>
                In Google Workspace app access controls, approve that app for
                the intended sales users. Outflare does not read your Google
                Directory or import Google Group membership.
              </li>
              <li>
                Approve OpenID/email and read-only Gmail access. Do not enable
                domain-wide delegation or sending scopes for personal CRM
                capture.
              </li>
              <li>
                Configure internal domains below, enable eligible members here,
                then ask those members to connect individually.
              </li>
            </ol>
          </details>
        </fieldset>
      )}
      {disconnecting && (
        <Dialog open onOpenChange={(v) => !busy && setDisconnecting(v)}>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Disconnect my Google account?</DialogTitle>
              <DialogDescription>
                New capture stops and the stored Google grant is deleted.
                Original Google messages are unaffected. Retained CRM copies
                keep their existing sharing unless you delete them.
              </DialogDescription>
            </DialogHeader>
            <label className="flex items-start gap-2 text-sm">
              <input
                type="checkbox"
                checked={purge}
                onChange={(e) => setPurge(e.target.checked)}
                disabled={busy}
              />
              Also delete all my imported email copies in this brand, including
              hidden copies.
            </label>
            <ErrorText>{error}</ErrorText>
            <DialogFooter>
              <Button
                variant="ghost"
                disabled={busy}
                onClick={() => setDisconnecting(false)}
              >
                Cancel
              </Button>
              <Button disabled={busy} onClick={() => void disconnect()}>
                Disconnect my Google account
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </section>
  );
}
export function GoogleCaptureControl({
  ws,
  record,
  canManage,
}: {
  ws: string;
  record: string;
  canManage: boolean;
}) {
  const { brand } = useBrand(ws);
  const [value, setValue] = useState<Capture | null>(null);
  const [status, setStatus] = useState<Connection | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!brand) return;
    const c = new AbortController();
    setValue(null);
    setStatus(null);
    setError("");
    void (async () => {
      try {
        const [v, s] = await Promise.all([
          api.GET("/v1/brands/{brand}/crm/mail/records/{record}/capture", {
            params: { path: { brand: brand.id, record } },
            signal: c.signal,
          }),
          api.GET("/v1/brands/{brand}/crm/mail/connection", {
            params: { path: { brand: brand.id } },
            signal: c.signal,
          }),
        ]);
        if (c.signal.aborted) return;
        if (v.data) setValue(v.data);
        else setError(errorMessage(v.error));
        if (s.data) setStatus(s.data);
        else setError(errorMessage(s.error));
      } catch (e) {
        if (!c.signal.aborted) setError(errorMessage(e));
      }
    })();
    return () => c.abort();
  }, [brand?.id, record]);
  async function save(enabled: boolean) {
    if (!brand || !value || busy) return;
    const previous = value;
    setValue({ ...value, enabled });
    setBusy(true);
    setError("");
    try {
      const r = await api.PUT(
        "/v1/brands/{brand}/crm/mail/records/{record}/capture",
        {
          params: { path: { brand: brand.id, record } },
          body: { enabled, version: value.version },
        },
      );
      if (r.data) {
        setValue(r.data);
        toast(
          enabled
            ? "Google capture selected. Your sharing rule is unchanged."
            : "Automatic capture disabled for this record.",
        );
      } else {
        setValue(previous);
        setError(errorMessage(r.error));
      }
    } catch (e) {
      setValue(previous);
      setError(errorMessage(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="space-y-2 rounded-lg border p-3">
      <h3 className="text-sm font-medium">My automatic Google capture</h3>
      <ErrorText>{error}</ErrorText>
      {value && status ? (
        <>
          <label className="flex items-start gap-2 text-xs">
            <input
              type="checkbox"
              checked={value.enabled}
              disabled={
                !canManage || busy || (!status.eligible && !value.enabled)
              }
              onChange={(e) => void save(e.target.checked)}
            />
            Include this contact/account in my Google capture
          </label>
          <p className="text-xs text-muted-foreground">
            {!status.eligible
              ? "Your administrator must enable your access and internal-domain safeguards in CRM settings."
              : !status.connected
                ? "Connect your Google account in CRM settings to start capture."
                : "Only eligible received/sent mail matching this record is captured. Internal conversations, exclusions and ambiguous matches are skipped."}{" "}
            Your sharing rule below still controls who can read copies.
            Selecting a company does not change the sharing rules on linked
            contacts.
          </p>
        </>
      ) : !error ? (
        <Loading />
      ) : null}
    </section>
  );
}
