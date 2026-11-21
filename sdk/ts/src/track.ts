// Sending product events from a server: identify, track, page, screen,
// group and alias calls are queued in memory and sent in batches to
// POST /v1/brands/{brand}/events, in the background, with retries. It
// uses nothing but fetch, so it runs on Node 18+, Bun, Deno and workers.

export type Traits = Record<string, unknown>;
export type Properties = Record<string, unknown>;

/** Who an event is about: a userId, an anonymousId, or both. */
export type Who = { userId?: string; anonymousId?: string };

type Common = Who & {
  /** When it happened; now when left out. */
  timestamp?: Date | string;
  /** Your own id for the message, so a retry is stored once. One is made when left out. */
  messageId?: string;
  context?: Record<string, unknown>;
};

export type TrackMessage = Common & { event: string; properties?: Properties };
export type IdentifyMessage = Common & { traits?: Traits };
export type GroupMessage = Common & { groupId: string; traits?: Traits };
export type PageMessage = Common & { name?: string; properties?: Properties };
export type AliasMessage = Common & { previousId: string };

/** What captureException takes besides the error: who it happened to, if anyone, and anything else worth keeping. */
export type ExceptionMessage = Common & {
  /** Extra properties, such as the route or the job's name. Never what a person typed. */
  properties?: Properties;
  /** False for an error nothing caught (a crash handler). Default true. */
  handled?: boolean;
};

const scrub = (t: unknown, max: number): string =>
  String(t ?? "")
    .replace(/[^\s@"'<>()]+@[^\s@"'<>()]+\.[A-Za-z]{2,}/g, "[email]")
    .replace(/[A-Za-z0-9_\-]{32,}/g, "[key]")
    .slice(0, max);

/** The website script's hash, so the same bug groups alike wherever it was caught. */
export function errorHash(t: string): string {
  let h1 = 5381;
  let h2 = 52711;
  for (let i = 0; i < t.length; i++) {
    const c = t.charCodeAt(i);
    h1 = (h1 * 33) ^ c;
    h2 = (h2 * 33) ^ c;
  }
  return ("0000000" + (h1 >>> 0).toString(16)).slice(-8) + ("0000000" + (h2 >>> 0).toString(16)).slice(-8);
}

/**
 * What an error is sent as: its type, message and stack with email addresses
 * and key-shaped strings taken out, and a fingerprint that stays the same when
 * only numbers, ids or line numbers change.
 */
export function describeError(error: unknown, handled = true): Properties {
  const e = error as { name?: unknown; message?: unknown; stack?: unknown } | null;
  const isErr = !!e && typeof e === "object" && ("message" in e || "stack" in e);
  const type = scrub(isErr ? e!.name || "Error" : "Error", 100);
  let text: unknown = error;
  if (isErr) text = e!.message;
  else if (typeof error !== "string") {
    try {
      text = JSON.stringify(error);
    } catch {
      text = String(error);
    }
  }
  const message = scrub(text, 500);
  const stack = isErr && e!.stack ? scrub(String(e!.stack).replace(/\?[^\s):]*/g, ""), 4000) : "";
  let top = "";
  for (const line of stack.split("\n")) {
    if (/(https?:|file:|\/|\\)[^\s]*\.\w+/.test(line) && !line.includes(message || "\u0000")) {
      top = line.replace(/:\d+(:\d+)?\)?\s*$/, "").trim();
      break;
    }
  }
  const p: Properties = { type, message, handled };
  if (stack) p.stack = stack;
  p.fingerprint = errorHash(`${type}|${message.replace(/\d+/g, "0").replace(/[0-9a-f]{8}-[0-9a-f-]{27}/gi, "id")}|${top}`);
  return p;
}

/**
 * Whether a user agent may be a crawler: a cheap first look, so a busy
 * server keeps no count for ordinary browsers. Outflare decides which of
 * them are AI crawlers.
 */
export function looksLikeCrawler(userAgent: string | null | undefined): boolean {
  return !!userAgent && /bot|crawl|spider|gpt|claude|anthropic|openai|perplexity|cohere|bytespider|duckassist|mistral|meta-external|webindexer|amzn|google-extended|ai2|diffbot|scrapy|fetcher|agent\b/i.test(userAgent);
}

export type TrackerOptions = {
  /** An API token with customers.manage (oft_...). Keep it on your servers. */
  token: string;
  /** The brand's id or slug. */
  brand: string;
  /** Your Outflare server. Default https://app.outflare.ai. */
  baseUrl?: string;
  /** Send once this many events wait. Default 20, at most 500. */
  flushAt?: number;
  /** Send at least this often, in milliseconds. Default 10000. */
  flushInterval?: number;
  /** The most events kept waiting; the oldest are dropped past it. Default 10000. */
  maxQueue?: number;
  /** How often a batch the server could not take is tried again. Default 3. */
  maxRetries?: number;
  /** Called when a batch is given up on, with the events that were lost. */
  onError?: (error: Error, lost: number) => void;
  /** Replace fetch, for tests. */
  fetch?: typeof fetch;
};

type Wire = Record<string, unknown>;

export const trackerVersion = "0.110.0";

/** A queue of product events for one brand. Make one and share it. */
export class Tracker {
  private queue: Wire[] = [];
  private timer: ReturnType<typeof setTimeout> | undefined;
  private sending: Promise<void> = Promise.resolve();
  private closed = false;
  private crawlers = new Map<string, number>();
  private readonly crawlerUrl: string;
  private readonly url: string;
  private readonly flushAt: number;
  private readonly flushInterval: number;
  private readonly maxQueue: number;
  private readonly maxRetries: number;

  constructor(private readonly options: TrackerOptions) {
    if (!options.token) throw new Error("Outflare Tracker: token is required");
    if (!options.brand) throw new Error("Outflare Tracker: brand is required");
    const base = (options.baseUrl ?? "https://app.outflare.ai").replace(/\/+$/, "");
    this.url = `${base}/v1/brands/${encodeURIComponent(options.brand)}/events`;
    this.crawlerUrl = `${base}/v1/brands/${encodeURIComponent(options.brand)}/ai-bot-hits`;
    this.flushAt = Math.min(Math.max(options.flushAt ?? 20, 1), 500);
    this.flushInterval = options.flushInterval ?? 10_000;
    this.maxQueue = options.maxQueue ?? 10_000;
    this.maxRetries = options.maxRetries ?? 3;
  }

  /** Something a person did. */
  track(m: TrackMessage): string {
    if (!m.event) throw new Error("Outflare Tracker: track needs an event name");
    return this.add("track", m, { event: m.event, properties: m.properties ?? {} });
  }

  /** Who a person is. Needs a userId. */
  identify(m: IdentifyMessage): string {
    if (!m.userId) throw new Error("Outflare Tracker: identify needs a userId");
    return this.add("identify", m, { traits: m.traits ?? {} });
  }

  /** The company a person belongs to. */
  group(m: GroupMessage): string {
    if (!m.groupId) throw new Error("Outflare Tracker: group needs a groupId");
    return this.add("group", m, { groupId: m.groupId, traits: m.traits ?? {} });
  }

  /** A page someone saw. */
  page(m: PageMessage): string {
    return this.add("page", m, { name: m.name, properties: m.properties ?? {} });
  }

  /** A screen someone saw in an app. */
  screen(m: PageMessage): string {
    return this.add("screen", m, { name: m.name, properties: m.properties ?? {} });
  }

  /**
   * A visitor was shown a flag's version: sends Experiment Viewed, which joins
   * them to the flag's A/B test. Call it when your server renders the version,
   * not when it decides it, with the id the flags were decided for. Sends
   * nothing for a flag that is off for them (false or "").
   */
  exposed(flag: string, variant: string | false | undefined, m: Common): string | undefined {
    if (!flag) throw new Error("Outflare Tracker: exposed needs the flag's key");
    if (!variant) return undefined;
    return this.add("track", m, { event: "Experiment Viewed", properties: { flag, variant, source: "server" } });
  }

  /**
   * An error your server caught: sends Error Caught, which the Errors page
   * groups into issues. Email addresses and key-shaped strings are taken out
   * of the message and stack first. Without a userId or anonymousId the error
   * is about no one and is filed under the anonymous id "server".
   */
  captureException(error: unknown, m: ExceptionMessage = {}): string {
    const who = m.userId || m.anonymousId ? m : { ...m, anonymousId: "server" };
    return this.add("track", who, { event: "Error Caught", properties: { ...m.properties, ...describeError(error, m.handled ?? true) } });
  }

  /**
   * Counts a request from an AI crawler (GPTBot, ClaudeBot, PerplexityBot and
   * others), for a site that isn't behind a CDN Outflare reads logs from.
   * Call it for every request from your server's middleware, with the
   * request's User-Agent and Host: it returns at once, keeps a count in
   * memory when the user agent looks like a crawler, and sends the counts
   * with the next flush. Outflare decides which are AI crawlers and keeps
   * only how often each came per host and day. Returns whether it counted.
   */
  crawlerVisit(request: { userAgent?: string | null; host?: string | null }): boolean {
    if (this.closed) return false;
    const ua = (request.userAgent ?? "").slice(0, 300);
    const host = (request.host ?? "").trim().toLowerCase();
    if (!host || !looksLikeCrawler(ua)) return false;
    const key = host + "\n" + ua;
    this.crawlers.set(key, (this.crawlers.get(key) ?? 0) + 1);
    if (this.crawlers.size >= 1000) void this.flush();
    else if (!this.timer) {
      this.timer = setTimeout(() => void this.flush(), this.flushInterval);
      (this.timer as { unref?: () => void }).unref?.();
    }
    return true;
  }

  private async drainCrawlers(): Promise<void> {
    if (this.crawlers.size === 0) return;
    const hits = [...this.crawlers].map(([key, requests]) => {
      const i = key.indexOf("\n");
      return { host: key.slice(0, i), user_agent: key.slice(i + 1), requests };
    });
    this.crawlers = new Map();
    try {
      // The key makes a retried report count once.
      await this.post(this.crawlerUrl, JSON.stringify({ hits }), { "Idempotency-Key": crypto.randomUUID() });
    } catch (err) {
      this.options.onError?.(err instanceof Error ? err : new Error(String(err)), 0);
    }
  }

  /** An id a person had before: joins previousId to userId. */
  alias(m: AliasMessage): string {
    if (!m.userId || !m.previousId) throw new Error("Outflare Tracker: alias needs a userId and a previousId");
    return this.add("alias", m, { previousId: m.previousId });
  }

  /** Send everything waiting now. Resolves once it has been sent or given up on. */
  flush(): Promise<void> {
    if (this.timer) clearTimeout(this.timer);
    this.timer = undefined;
    this.sending = this.sending.then(() => this.drain());
    return this.sending;
  }

  /** Send what waits and take no more. Call it before your process exits. */
  async close(): Promise<void> {
    this.closed = true;
    await this.flush();
  }

  private add(type: string, m: Common, rest: Wire): string {
    if (this.closed) throw new Error("Outflare Tracker: closed");
    if (!m.userId && !m.anonymousId) throw new Error(`Outflare Tracker: ${type} needs a userId or an anonymousId`);
    const messageId = m.messageId ?? crypto.randomUUID();
    const ts = m.timestamp instanceof Date ? m.timestamp.toISOString() : (m.timestamp ?? new Date().toISOString());
    const wire: Wire = { type, messageId, timestamp: ts, ...rest };
    if (m.userId) wire.userId = m.userId;
    if (m.anonymousId) wire.anonymousId = m.anonymousId;
    wire.context = { ...m.context, library: { name: "outflare-node", version: trackerVersion } };
    this.queue.push(wire);
    if (this.queue.length > this.maxQueue) {
      const lost = this.queue.length - this.maxQueue;
      this.queue.splice(0, lost);
      this.options.onError?.(new Error("Outflare Tracker: the queue is full, so the oldest events were dropped"), lost);
    }
    if (this.queue.length >= this.flushAt) void this.flush();
    else if (!this.timer) {
      this.timer = setTimeout(() => void this.flush(), this.flushInterval);
      // Never keep a process alive only to send events: close() does that.
      (this.timer as { unref?: () => void }).unref?.();
    }
    return messageId;
  }

  private async drain(): Promise<void> {
    await this.drainCrawlers();
    while (this.queue.length > 0) {
      const batch = this.queue.splice(0, 500);
      try {
        await this.send(batch);
      } catch (err) {
        this.options.onError?.(err instanceof Error ? err : new Error(String(err)), batch.length);
      }
    }
  }

  private send(batch: Wire[]): Promise<void> {
    return this.post(this.url, JSON.stringify({ source: "node", batch }), {});
  }

  private async post(url: string, body: string, extra: Record<string, string>): Promise<void> {
    const doFetch = this.options.fetch ?? fetch;
    for (let attempt = 0; ; attempt++) {
      let status = 0;
      let retryAfter = 0;
      try {
        const res = await doFetch(url, {
          method: "POST",
          headers: { "Content-Type": "application/json", Authorization: `Bearer ${this.options.token}`, "User-Agent": `outflare-node/${trackerVersion}`, ...extra },
          body,
        });
        status = res.status;
        retryAfter = Number(res.headers.get("retry-after")) || 0;
        await res.arrayBuffer().catch(() => undefined);
        if (res.ok) return;
      } catch (err) {
        if (attempt >= this.maxRetries) throw err;
      }
      // A refusal (bad token, no such brand, a batch too large) won't change on a retry.
      if (status >= 400 && status < 500 && status !== 429 && status !== 408) throw new Error(`Outflare Tracker: the server refused the batch (${status})`);
      if (attempt >= this.maxRetries) throw new Error(`Outflare Tracker: the server could not take the batch (${status || "no answer"})`);
      await new Promise((r) => setTimeout(r, Math.min(retryAfter * 1000 || 500 * 2 ** attempt, 30_000)));
    }
  }
}
