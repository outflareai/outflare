// The part of the library that knows nothing about React Native: it keeps
// a visitor id and a queue of events in the storage it is given, and sends
// them to POST /v1/site/events with the app's public site key. index.ts
// fills in the phone's details and sends when the app goes to the
// background.

export type Properties = Record<string, unknown>;

/** Where the visitor id and waiting events are kept: AsyncStorage fits. */
export type Storage = {
  getItem(key: string): Promise<string | null>;
  setItem(key: string, value: string): Promise<unknown>;
  removeItem(key: string): Promise<unknown>;
};

export type Options = {
  /** The app's public site key (ofk_...), from a site key made with no origins. */
  key: string;
  /** Your Outflare server. Default https://app.outflare.ai. */
  host?: string;
  /** Where the visitor id and waiting events are kept between launches. Without it they last until the app closes. */
  storage?: Storage;
  /** The app, for reports by version. */
  app?: { name?: string; version?: string; build?: string };
  /** The phone: index.ts fills these in. */
  device?: { manufacturer?: string; model?: string; type?: string };
  os?: { name?: string; version?: string };
  locale?: string;
  timezone?: string;
  /** Send once this many events wait. Default 20 (the most one request takes). */
  flushAt?: number;
  /** Send at least this often, in milliseconds. Default 30000. */
  flushInterval?: number;
  /** The most events kept waiting; the oldest are dropped past it. Default 1000. */
  maxQueue?: number;
  /** Called when events are given up on. */
  onError?: (error: Error, lost: number) => void;
  /** The library's name in the events and the User-Agent. */
  library?: string;
  /** Replace fetch, for tests. */
  fetch?: typeof fetch;
};

type Wire = Record<string, unknown>;

export const version = "0.110.0";
const maxBatch = 20;
const idKey = "outflare.anonymousId";
const queueKey = "outflare.queue";

const uuid = (): string => {
  const c = (globalThis as { crypto?: { randomUUID?: () => string } }).crypto;
  if (c?.randomUUID) return c.randomUUID();
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (ch) => {
    const r = (Math.random() * 16) | 0;
    return (ch === "x" ? r : (r & 0x3) | 0x8).toString(16);
  });
};

/** userAgent names the library and the phone, the way Outflare reads them. */
export function userAgent(library: string, os?: { name?: string; version?: string }, model?: string): string {
  const v = os?.version ?? "";
  const name = (os?.name ?? "").toLowerCase();
  if (name === "ios" || name === "ipados") {
    return `${library}/${version} (${name === "ipados" ? "iPad" : "iPhone"}; CPU iPhone OS ${v.replace(/\./g, "_")} like Mac OS X)`;
  }
  if (name === "android") return `${library}/${version} (Linux; Android ${v}; ${model || "Phone"}) Mobile`;
  return `${library}/${version}`;
}

/** One app's connection to Outflare. Make one and share it. */
export class Outflare {
  private id = "";
  private queue: Wire[] = [];
  private timer: ReturnType<typeof setTimeout> | undefined;
  private sending: Promise<void> = Promise.resolve();
  private readonly ready: Promise<void>;
  private readonly url: string;
  private readonly library: string;
  private readonly flushAt: number;
  private readonly flushInterval: number;
  private readonly maxQueue: number;

  constructor(private readonly options: Options) {
    if (!options.key) throw new Error("Outflare: key is required");
    this.url = `${(options.host ?? "https://app.outflare.ai").replace(/\/+$/, "")}/v1/site/events`;
    this.library = options.library ?? "outflare-react-native";
    this.flushAt = Math.min(Math.max(options.flushAt ?? maxBatch, 1), maxBatch);
    this.flushInterval = options.flushInterval ?? 30_000;
    this.maxQueue = options.maxQueue ?? 1000;
    this.ready = this.load();
  }

  /** The visitor's id on this phone. Send it to your server with your user's id to join the two. */
  async anonymousId(): Promise<string> {
    await this.ready;
    return this.id;
  }

  /** A screen the person saw. */
  screen(name: string, properties: Properties = {}): void {
    if (!name) throw new Error("Outflare: screen needs a name");
    this.add({ type: "screen", name, properties });
  }

  /** Something the person did, such as Order Started. */
  track(event: string, properties: Properties = {}): void {
    if (!event) throw new Error("Outflare: track needs an event name");
    this.add({ type: "track", event, properties });
  }

  /** Forget the visitor: call it when someone signs out, so the next person starts fresh. */
  async reset(): Promise<void> {
    await this.ready;
    await this.flush();
    this.id = uuid();
    await this.options.storage?.setItem(idKey, this.id).catch(() => undefined);
  }

  /** Send everything waiting now. Resolves once it has been sent or kept for later. */
  flush(): Promise<void> {
    if (this.timer) clearTimeout(this.timer);
    this.timer = undefined;
    this.sending = this.sending.then(() => this.drain());
    return this.sending;
  }

  private async load(): Promise<void> {
    const s = this.options.storage;
    try {
      this.id = (await s?.getItem(idKey)) ?? "";
      const kept = JSON.parse((await s?.getItem(queueKey)) ?? "[]");
      // Events from the last launch go first.
      if (Array.isArray(kept)) this.queue = [...kept, ...this.queue];
    } catch {
      // Storage that can't be read is the same as none.
    }
    if (!/^[A-Za-z0-9-]{8,64}$/.test(this.id)) {
      this.id = uuid();
      await s?.setItem(idKey, this.id).catch(() => undefined);
    }
  }

  private add(event: Wire): void {
    const o = this.options;
    const context: Wire = { library: { name: this.library, version } };
    if (o.app) context.app = o.app;
    if (o.device) context.device = o.device;
    if (o.os) context.os = o.os;
    if (o.locale) context.locale = o.locale;
    if (o.timezone) context.timezone = o.timezone;
    this.queue.push({ ...event, messageId: uuid(), context });
    if (this.queue.length > this.maxQueue) {
      const lost = this.queue.length - this.maxQueue;
      this.queue.splice(0, lost);
      o.onError?.(new Error("Outflare: too many events are waiting, so the oldest were dropped"), lost);
    }
    void this.save();
    if (this.queue.length >= this.flushAt) void this.flush();
    else if (!this.timer) this.timer = setTimeout(() => void this.flush(), this.flushInterval);
  }

  private async save(): Promise<void> {
    await this.ready;
    const s = this.options.storage;
    if (!s) return;
    try {
      if (this.queue.length === 0) await s.removeItem(queueKey);
      else await s.setItem(queueKey, JSON.stringify(this.queue));
    } catch {
      // The events are still in memory.
    }
  }

  private async drain(): Promise<void> {
    await this.ready;
    while (this.queue.length > 0) {
      const batch = this.queue.slice(0, maxBatch);
      const outcome = await this.send(batch);
      // No answer, or a busy server: keep the events for the next try.
      if (outcome === "later") break;
      this.queue.splice(0, batch.length);
      if (outcome === "refused") this.options.onError?.(new Error("Outflare: the server refused the events; check the site key"), batch.length);
    }
    await this.save();
  }

  private async send(batch: Wire[]): Promise<"sent" | "later" | "refused"> {
    const o = this.options;
    const body = JSON.stringify({ key: o.key, batch: batch.map((e) => ({ ...e, anonymousId: this.id })) });
    try {
      const res = await (o.fetch ?? fetch)(this.url, {
        method: "POST",
        headers: { "Content-Type": "text/plain;charset=UTF-8", "User-Agent": userAgent(this.library, o.os, o.device?.model) },
        body,
      });
      if (res.ok) return "sent";
      if (res.status === 408 || res.status === 429 || res.status >= 500) return "later";
      return "refused";
    } catch {
      return "later";
    }
  }
}
