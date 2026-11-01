import { useState } from "react";
import type {
  SearchGroup,
  SearchMigration,
  SEOSite,
  Integration,
} from "@outflare/sdk";
import { api, errorMessage } from "../../api";
import { Button, field, toast } from "../../ui";
import type { Schedule, SavedReport, Filters } from "./model";
import { TagEditor } from "./Primitives";
type Path = { brand: string; site: string };
export function GroupEditor({
  path,
  current,
  onSaved,
  onCancel,
}: {
  path: Path;
  current?: SearchGroup;
  onSaved: () => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState(current?.name ?? "");
  const [kind, setKind] = useState<"content" | "topic">(
    current?.kind ?? "content",
  );
  const [rules, setRules] = useState<SearchGroup["rules"]>(
    current?.rules ?? [{ operator: "prefix", value: "/blog/" }],
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const { error } = await api.POST(
        "/v1/brands/{brand}/seo-sites/{site}/search-groups",
        { params: { path }, body: { id: current?.id, name, kind, rules } },
      );
      if (error) return setError(errorMessage(error));
      toast("Group saved. You can now use it as a report filter.");
      onSaved();
    } catch {
      setError(
        "Could not save this group. Check your connection and try again.",
      );
    } finally {
      setBusy(false);
    }
  }
  return (
    <form onSubmit={save} className="space-y-4">
      <label className="block text-sm">
        Group name
        <input
          required
          className={`${field} mt-1 block w-full`}
          maxLength={120}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </label>
      <label className="block text-sm">
        What belongs in this group?
        <select
          className={`${field} mt-1 block w-full`}
          value={kind}
          onChange={(e) => setKind(e.target.value as typeof kind)}
        >
          <option value="content">Pages matching URL rules</option>
          <option value="topic">Queries matching topic rules</option>
        </select>
      </label>
      <div className="space-y-2">
        {rules.map((r, i) => (
          <div className="flex items-center gap-2" key={i}>
            <select
              aria-label={`Rule ${i + 1} match`}
              className={`${field} mt-1 block w-full`}
              value={r.operator}
              onChange={(e) =>
                setRules(
                  rules.map((x, j) =>
                    i === j
                      ? { ...x, operator: e.target.value as typeof r.operator }
                      : x,
                  ),
                )
              }
            >
              {["prefix", "contains", "equals", "regex"].map((v) => (
                <option key={v}>{v}</option>
              ))}
            </select>
            <input
              aria-label={`Rule ${i + 1} value`}
              required
              className={`${field} mt-1 block w-full`}
              value={r.value}
              maxLength={500}
              placeholder={kind === "content" ? "/blog/" : "pricing"}
              onChange={(e) =>
                setRules(
                  rules.map((x, j) =>
                    i === j ? { ...x, value: e.target.value } : x,
                  ),
                )
              }
            />
            <Button
              type="button"
              variant="ghost"
              disabled={rules.length === 1}
              onClick={() => setRules(rules.filter((_, j) => j !== i))}
            >
              Remove
            </Button>
          </div>
        ))}
        <Button
          type="button"
          variant="ghost"
          disabled={rules.length >= 100}
          onClick={() =>
            setRules([...rules, { operator: "contains", value: "" }])
          }
        >
          Add another rule
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        Any matching rule includes a row. Path rules start with /. Topics ignore
        letter case. Groups can overlap; their totals should not be added
        together.
      </p>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <Button disabled={busy} type="submit">
          {busy ? "Saving…" : "Save group"}
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
export function MigrationEditor({
  path,
  sites,
  current,
  onSaved,
  onCancel,
}: {
  path: Path;
  sites: SEOSite[];
  current?: SearchMigration;
  onSaved: () => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState(current?.name ?? "");
  const [target, setTarget] = useState(
    current?.target_site_id ??
      sites.find((s) => s.id !== path.site)?.id ??
      path.site,
  );
  const [date, setDate] = useState(current?.go_live ?? "");
  const [mappings, setMappings] = useState(
    current?.mappings?.length ? current.mappings : [{ source: "", target: "" }],
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const { error } = await api.POST(
        "/v1/brands/{brand}/seo-sites/{site}/search-migrations",
        {
          params: { path },
          body: {
            id: current?.id,
            name,
            target_site_id: target,
            go_live: date,
            mappings: mappings.filter((x) => x.source || x.target),
          },
        },
      );
      if (error) return setError(errorMessage(error));
      onSaved();
    } catch {
      setError("Could not save the migration. Try again.");
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="space-y-4" onSubmit={save}>
      <label className="block text-sm">
        Migration name
        <input
          required
          className={`${field} mt-1 block w-full`}
          value={name}
          maxLength={120}
          onChange={(e) => setName(e.target.value)}
        />
      </label>
      <label className="block text-sm">
        Where did the pages move?
        <select
          className={`${field} mt-1 block w-full`}
          value={target}
          onChange={(e) => setTarget(e.target.value)}
        >
          {sites.map((s) => (
            <option key={s.id} value={s.id}>
              {s.host}
              {s.id === path.site ? " (URLs changed on this site)" : ""}
            </option>
          ))}
        </select>
      </label>
      <label className="block text-sm">
        Go-live date
        <input
          required
          type="date"
          className={`${field} mt-1 block w-full`}
          value={date}
          onChange={(e) => setDate(e.target.value)}
        />
      </label>
      <div className="space-y-2">
        <p className="text-sm font-medium">Old → new URL mappings</p>
        {mappings.map((m, i) => (
          <div className="space-y-2 rounded border p-3" key={i}>
            <label className="block text-xs">
              Old URL
              <input
                type="url"
                className={`${field} mt-1 block w-full`}
                value={m.source}
                onChange={(e) =>
                  setMappings(
                    mappings.map((x, j) =>
                      i === j ? { ...x, source: e.target.value } : x,
                    ),
                  )
                }
              />
            </label>
            <label className="block text-xs">
              New URL
              <input
                type="url"
                className={`${field} mt-1 block w-full`}
                value={m.target}
                onChange={(e) =>
                  setMappings(
                    mappings.map((x, j) =>
                      i === j ? { ...x, target: e.target.value } : x,
                    ),
                  )
                }
              />
            </label>
            <Button
              variant="ghost"
              type="button"
              disabled={mappings.length === 1}
              onClick={() => setMappings(mappings.filter((_, j) => j !== i))}
            >
              Remove mapping
            </Button>
          </div>
        ))}
        <Button
          variant="ghost"
          type="button"
          disabled={mappings.length >= 1000}
          onClick={() => setMappings([...mappings, { source: "", target: "" }])}
        >
          Add mapping
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        A restructure on the same site needs mappings. Redirect evidence comes
        from the latest crawls of both sites. Unchecked mappings stay unknown.
      </p>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <Button disabled={busy} type="submit">
          {busy ? "Saving…" : "Save migration"}
        </Button>
        <Button variant="ghost" type="button" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
export function ReportEditor({
  path,
  current,
  filters,
  onSaved,
  onCancel,
}: {
  path: Path;
  current?: SavedReport;
  filters: Filters;
  onSaved: (r: SavedReport) => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState(current?.name ?? "");
  const [tags, setTags] = useState<string[]>(current?.tags ?? []);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const { data, error } = await api.POST(
        "/v1/brands/{brand}/seo-sites/{site}/insights/reports",
        { params: { path }, body: { id: current?.id, name, tags, filters } },
      );
      if (error || !data) return setError(errorMessage(error));
      if ("id" in data) onSaved(data);
    } catch {
      setError("Could not save the report. Try again.");
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="space-y-4" onSubmit={save}>
      <label className="block text-sm">
        Report name
        <input
          required
          className={`${field} mt-1 block w-full`}
          value={name}
          onChange={(e) => setName(e.target.value)}
          maxLength={120}
          placeholder="Weekly content recovery"
        />
      </label>
      <TagEditor tags={tags} onChange={setTags} />
      <div className="rounded border bg-muted/30 p-3 text-sm">
        <p className="font-medium">Saved with this report</p>
        <p className="mt-1 text-muted-foreground">
          The report type, dates, group and analysis settings currently selected
          in Explore. Leaving dates automatic gives each new run the latest
          period.
        </p>
      </div>
      <p className="text-xs text-muted-foreground">
        Generate a snapshot after saving to preserve its results and the exact
        data period. Editing a definition does not change earlier snapshots.
      </p>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <Button disabled={busy} type="submit">
          {busy ? "Saving…" : "Save report"}
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
export function ScheduleEditor({
  path,
  host,
  current,
  connections,
  onSaved,
  onCancel,
}: {
  path: Path;
  host: string;
  current?: Schedule;
  connections: Integration[];
  onSaved: () => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState(current?.name ?? "");
  const [conn, setConn] = useState(current?.integration_id ?? "");
  const [page, setPage] = useState(current?.page ?? `https://${host}`);
  const [device, setDevice] = useState(current?.device ?? "PHONE");
  const [frequency, setFrequency] = useState<Schedule["frequency"]>(
    current?.frequency ?? "weekly",
  );
  const [weekday, setWeekday] = useState(current?.weekday ?? 1);
  const [hour, setHour] = useState(current?.hour ?? 8);
  const [zone, setZone] = useState(
    current?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone,
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const { error } = await api.POST(
        "/v1/brands/{brand}/seo-sites/{site}/insights/schedules",
        {
          params: { path },
          body: {
            id: current?.id,
            name,
            integration_id: conn,
            page,
            device,
            frequency,
            weekday,
            hour,
            timezone: zone,
          },
        },
      );
      if (error) return setError(errorMessage(error));
      toast("Schedule saved. Its next run is shown in Automations.");
      onSaved();
    } catch {
      setError("Could not save the schedule. Try again.");
    } finally {
      setBusy(false);
    }
  }
  return (
    <form className="space-y-4" onSubmit={save}>
      <label className="block text-sm">
        Schedule name
        <input
          required
          className={`${field} mt-1 block w-full`}
          value={name}
          maxLength={120}
          onChange={(e) => setName(e.target.value)}
          placeholder="Monday page experience check"
        />
      </label>
      <label className="block text-sm">
        What should be collected?
        <select
          className={`${field} mt-1 block w-full`}
          value={conn}
          onChange={(e) => setConn(e.target.value)}
        >
          <option value="">Official Google ranking updates</option>
          {connections.map((c) => (
            <option key={c.id} value={c.id}>
              Core Web Vitals · {c.name}
            </option>
          ))}
        </select>
      </label>
      {conn && (
        <>
          <label className="block text-sm">
            Page or origin
            <input
              required
              type="url"
              className={`${field} mt-1 block w-full`}
              value={page}
              onChange={(e) => setPage(e.target.value)}
            />
          </label>
          <label className="block text-sm">
            Device
            <select
              className={`${field} mt-1 block w-full`}
              value={device}
              onChange={(e) => setDevice(e.target.value)}
            >
              {["PHONE", "DESKTOP", "TABLET"].map((d) => (
                <option key={d}>{d}</option>
              ))}
            </select>
          </label>
        </>
      )}
      <label className="block text-sm">
        Frequency
        <select
          className={`${field} mt-1 block w-full`}
          value={frequency}
          onChange={(e) => setFrequency(e.target.value as typeof frequency)}
        >
          <option value="weekly">Weekly</option>
          <option value="daily">Daily</option>
          <option value="off">Paused</option>
        </select>
      </label>
      {frequency === "weekly" && (
        <label className="block text-sm">
          Day
          <select
            className={`${field} mt-1 block w-full`}
            value={weekday}
            onChange={(e) => setWeekday(Number(e.target.value))}
          >
            {[
              "Sunday",
              "Monday",
              "Tuesday",
              "Wednesday",
              "Thursday",
              "Friday",
              "Saturday",
            ].map((d, i) => (
              <option value={i} key={d}>
                {d}
              </option>
            ))}
          </select>
        </label>
      )}
      <div className="grid grid-cols-2 gap-3">
        <label className="block text-sm">
          Local time
          <select
            className={`${field} mt-1 block w-full`}
            value={hour}
            onChange={(e) => setHour(Number(e.target.value))}
          >
            {Array.from({ length: 24 }, (_, i) => (
              <option value={i} key={i}>
                {String(i).padStart(2, "0")}:00
              </option>
            ))}
          </select>
        </label>
        <label className="block text-sm">
          Timezone
          <input
            required
            className={`${field} mt-1 block w-full`}
            value={zone}
            onChange={(e) => setZone(e.target.value)}
            list="search-insight-timezones"
          />
          <datalist id="search-insight-timezones">
            {[
              ...new Set([
                Intl.DateTimeFormat().resolvedOptions().timeZone,
                "UTC",
                "Europe/Lisbon",
                "Europe/London",
                "America/New_York",
                "America/Los_Angeles",
                "Asia/Singapore",
                "Australia/Sydney",
              ]),
            ].map((z) => (
              <option key={z} value={z} />
            ))}
          </datalist>
        </label>
      </div>
      <p className="text-xs text-muted-foreground">
        This collects data; it does not change your website. Pausing or editing
        invalidates queued work from the old schedule. Provider failures remain
        visible in Activity and do not stop the next scheduled collection.
      </p>
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <div className="flex gap-2">
        <Button disabled={busy} type="submit">
          {busy ? "Saving…" : "Save schedule"}
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}
