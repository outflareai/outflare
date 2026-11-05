import { useCallback, useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "../api";
import { Button, ErrorText, field, Loading, Logo } from "../ui";
import { Grid, RangePicker, rangeLabel } from "./Dashboards";
import { track } from "@/lib/analytics";

// The page behind a dashboard's share link (/share/<token>): the dashboard
// read-only, every tile run live through the link, for anyone who has it.
// A link with a password asks for it first and keeps the key it gets for
// the tiles, in this tab only.

type Shared = Schemas["SharedDashboard"];
type Tile = Schemas["DashboardTile"];
type DateRange = Schemas["InsightDateRange"];

export function SharedDashboard({ token }: { token: string }) {
  const [d, setD] = useState<Shared | null>(null);
  const [needsPassword, setNeedsPassword] = useState(false);
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [range, setRange] = useState<DateRange | undefined>();

  const open = useCallback(
    async (body: Schemas["SharedDashboardOpen"]) => {
      setBusy(true);
      const { data, error, response } = await api.POST("/v1/shared/dashboards/{token}", { params: { path: { token } }, body });
      setBusy(false);
      if (data) {
        setD(data);
        setRange(data.settings.date_range);
        setNeedsPassword(false);
        setError("");
        document.title = `${data.name} · ${data.brand}`;
        return;
      }
      const code = (error as { error?: { code?: string } } | undefined)?.error?.code;
      if (code === "password_required") {
        setNeedsPassword(true);
        return;
      }
      if (code === "wrong_password") {
        track("ui.shared_dashboard_password_wrong");
        setError("That isn't the password.");
        return;
      }
      setError(response?.status === 404 ? "This link is invalid, expired or was taken back." : errorMessage(error));
    },
    [token],
  );
  useEffect(() => {
    open({});
  }, [open]);

  const run = useCallback(
    async (_kind: Schemas["Insight"]["kind"], tile: string, rng: DateRange | undefined) => {
      const { data, error } = await api.POST("/v1/shared/dashboards/{token}/tiles/{tile}", { params: { path: { token, tile } }, body: { key: d?.key || undefined, date_range: rng } });
      return { data, error };
    },
    [token, d?.key],
  );

  if (!d && needsPassword) {
    return (
      <Frame>
        <form
          className="mx-auto mt-16 max-w-sm space-y-3 rounded-lg border bg-card p-6"
          onSubmit={(e) => {
            e.preventDefault();
            open({ password });
          }}
        >
          <h1 className="text-[15px] font-medium">This dashboard asks for its password</h1>
          <p className="text-[13px] text-muted-foreground">Whoever sent you the link has it.</p>
          <input className={`${field} w-full`} type="password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} aria-label="Password" />
          {error && <ErrorText>{error}</ErrorText>}
          <Button type="submit" className="w-full" disabled={!password || busy}>
            Open
          </Button>
        </form>
      </Frame>
    );
  }
  if (!d && error) {
    return (
      <Frame>
        <div className="mx-auto mt-16 max-w-sm rounded-lg border bg-card p-6 text-[13px]">
          <ErrorText>{error}</ErrorText>
        </div>
      </Frame>
    );
  }
  if (!d) {
    return (
      <Frame>
        <Loading />
      </Frame>
    );
  }
  const tiles: Tile[] = d.tile_list.map((t) => ({
    id: t.id,
    insight_id: t.kind ? t.id : null,
    insight: t.kind
      ? { id: t.id, brand_id: "", kind: t.kind, name: t.name ?? "", description: "", spec: { display: t.display ?? undefined } as Schemas["InsightSpec"], created_by: null, created_at: "", updated_at: "" }
      : null,
    note: t.note,
    x: t.x,
    y: t.y,
    w: t.w,
    h: t.h,
  }));
  return (
    <Frame brand={d.brand}>
      <div className="mb-4 flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-[18px] font-semibold">{d.name}</h1>
          <p className="text-[13px] text-muted-foreground">{d.description || (range ? `Every tile shows ${rangeLabel(range).toLowerCase()}.` : "Each tile shows its own dates.")}</p>
        </div>
        <RangePicker value={range} onChange={setRange} disabled={false} />
      </div>
      {tiles.length === 0 ? <p className="text-[13px] text-muted-foreground">This dashboard has no tiles yet.</p> : <Grid tiles={tiles} brand="" range={range} compare={!!d.settings.compare} editing={false} onPlace={() => {}} onRemove={() => {}} run={run} />}
    </Frame>
  );
}

function Frame({ brand, children }: { brand?: string; children: React.ReactNode }) {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <header className="flex items-center justify-between border-b px-6 py-3 text-[13px]">
        <span className="flex items-center gap-3">
          <Logo />
          {brand && <span className="text-muted-foreground">{brand}</span>}
        </span>
        <span className="text-muted-foreground">Shared read-only</span>
      </header>
      <main className="mx-auto max-w-7xl px-6 py-6">{children}</main>
    </div>
  );
}
