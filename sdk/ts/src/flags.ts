// Deciding feature flags in your own server's memory.
//
// POST .../flags/evaluate asks Outflare for every visitor. LocalFlags
// instead reads the brand's flag definitions (GET .../flags/definitions)
// every half minute and decides each visitor here, with no request per
// page: the same rules and the same hash as the server and the website
// script, so all three give a visitor the same version. The decisions
// are checked against the engine's own (engine/internal/flags/testdata).

/** A version of a flag, with its share of visitors and any content. */
export interface FlagDefinitionVariant {
  key: string;
  weight: number;
  payload?: unknown;
}

/** One condition on the visitor or the page; every rule of a flag must hold. */
export interface FlagDefinitionRule {
  attribute: string;
  op: string;
  values?: string[] | null;
}

/** A flag that is on, as GET .../flags/definitions gives it. */
export interface FlagDefinition {
  key: string;
  variants: FlagDefinitionVariant[];
  rollout: number;
  rules: FlagDefinitionRule[];
  salt: string;
  group: string;
  group_from: number;
  group_to: number;
}

/** The answer of GET .../flags/definitions. */
export interface FlagDefinitions {
  flags: FlagDefinition[];
  refresh_seconds: number;
}

/** Who is asking and from where: what POST .../flags/evaluate takes. */
export interface FlagVisitor {
  /** The website's id for the visitor: the of_aid cookie, or outflare.anonymousId() in the page. */
  anonymousId?: string;
  /** Your own id for the user. Used when there is no anonymousId, and makes signed_in true. */
  userId?: string;
  /** The page's address, for host, path, url and query.<name> rules. */
  url?: string;
  referrer?: string;
  /** For device rules. */
  userAgent?: string;
  /** For language rules, such as en-GB. */
  language?: string;
  /** For property.<name> rules; values are compared as text. */
  properties?: Record<string, unknown>;
  /**
   * Ids of the People lists the visitor is in, for list rules. Deciding
   * in memory can't look a person up: pass what your server knows.
   */
  lists?: string[];
  /** Fields of the visitor's record, for person.<field> rules; values are compared as text. */
  person?: Record<string, unknown>;
  /** Only these flags; all when absent. */
  keys?: string[];
}

/** Why a flag gave what it gave. */
export type FlagReason = "match" | "rules" | "group" | "rollout" | "off" | "no_subject";

/** What deciding returns: the shape of POST .../flags/evaluate. */
export interface LocalFlagDecisions {
  flags: Record<string, string | false>;
  payloads: Record<string, unknown>;
  reasons: Record<string, FlagReason>;
}

const BUCKETS = 10_000;

// ---- sha256 (so deciding is synchronous and runs anywhere, the edge included) ----

const K = new Uint32Array([
  0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
  0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
  0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
  0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
  0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
  0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
]);

/** The first 8 bytes of sha256(s), as two unsigned 32-bit numbers. */
function sha256Head(s: string): [number, number] {
  const msg = new TextEncoder().encode(s);
  const len = msg.length;
  const padded = new Uint8Array(((len + 9 + 63) >> 6) << 6);
  padded.set(msg);
  padded[len] = 0x80;
  const view = new DataView(padded.buffer);
  view.setUint32(padded.length - 8, Math.floor((len * 8) / 0x100000000));
  view.setUint32(padded.length - 4, (len * 8) >>> 0);
  const h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
  const w = new Uint32Array(64);
  const rotr = (x: number, n: number) => (x >>> n) | (x << (32 - n));
  for (let off = 0; off < padded.length; off += 64) {
    for (let i = 0; i < 16; i++) w[i] = view.getUint32(off + i * 4);
    for (let i = 16; i < 64; i++) {
      const s0 = rotr(w[i - 15]!, 7) ^ rotr(w[i - 15]!, 18) ^ (w[i - 15]! >>> 3);
      const s1 = rotr(w[i - 2]!, 17) ^ rotr(w[i - 2]!, 19) ^ (w[i - 2]! >>> 10);
      w[i] = (w[i - 16]! + s0 + w[i - 7]! + s1) >>> 0;
    }
    let [a, b, c, d, e, f, g, hh] = h as unknown as number[];
    for (let i = 0; i < 64; i++) {
      const s1 = rotr(e!, 6) ^ rotr(e!, 11) ^ rotr(e!, 25);
      const ch = (e! & f!) ^ (~e! & g!);
      const t1 = (hh! + s1 + ch + K[i]! + w[i]!) >>> 0;
      const s0 = rotr(a!, 2) ^ rotr(a!, 13) ^ rotr(a!, 22);
      const maj = (a! & b!) ^ (a! & c!) ^ (b! & c!);
      const t2 = (s0 + maj) >>> 0;
      hh = g;
      g = f;
      f = e;
      e = (d! + t1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (t1 + t2) >>> 0;
    }
    h[0] = (h[0]! + a!) >>> 0;
    h[1] = (h[1]! + b!) >>> 0;
    h[2] = (h[2]! + c!) >>> 0;
    h[3] = (h[3]! + d!) >>> 0;
    h[4] = (h[4]! + e!) >>> 0;
    h[5] = (h[5]! + f!) >>> 0;
    h[6] = (h[6]! + g!) >>> 0;
    h[7] = (h[7]! + hh!) >>> 0;
  }
  return [h[0]!, h[1]!];
}

/**
 * Where s falls in 0..9999: the first 8 bytes of its sha256 as one
 * unsigned number, mod 10,000. The same everywhere Outflare decides.
 */
export function flagBucket(s: string): number {
  const [hi, lo] = sha256Head(s);
  // (hi * 2^32 + lo) mod 10,000, and 2^32 mod 10,000 is 7296.
  return (((hi % BUCKETS) * 7296) % BUCKETS + (lo % BUCKETS)) % BUCKETS;
}

// ---- the page's address, read as the server reads it ----

const HEX = /^[0-9a-fA-F]{2}$/;

function validEscapes(s: string): boolean {
  for (let i = s.indexOf("%"); i >= 0; i = s.indexOf("%", i + 1)) {
    if (!HEX.test(s.slice(i + 1, i + 3))) return false;
  }
  return true;
}

function percentDecodeBytes(s: string, plusIsSpace: boolean): Uint8Array {
  const raw = new TextEncoder().encode(s);
  const out: number[] = [];
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i]!;
    if (c === 0x25) {
      out.push(parseInt(String.fromCharCode(raw[i + 1]!, raw[i + 2]!), 16));
      i += 2;
    } else if (c === 0x2b && plusIsSpace) {
      out.push(0x20);
    } else {
      out.push(c);
    }
  }
  return new Uint8Array(out);
}

const PATH_OK = /^[A-Za-z0-9\-_.~$&+,/:;=@!'()*[\]%]*$/;

/** The path as the server compares it: as written when that is a valid encoding, else encoded again. */
function escapedPath(raw: string): string {
  if (PATH_OK.test(raw)) return raw;
  let out = "";
  for (const b of percentDecodeBytes(raw, false)) {
    const ch = String.fromCharCode(b);
    out += /[A-Za-z0-9\-_.~$&+,/:;=@]/.test(ch) ? ch : "%" + b.toString(16).toUpperCase().padStart(2, "0");
  }
  return out;
}

interface Address {
  host: string;
  path: string;
  query: Map<string, string>;
}

function parseAddress(url: string): Address | null {
  if (url === "") return null;
  // Control characters make the address invalid.
  for (let i = 0; i < url.length; i++) {
    const c = url.charCodeAt(i);
    if (c < 0x20 || c === 0x7f) return null;
  }
  let rest = url;
  const hash = rest.indexOf("#");
  if (hash >= 0) rest = rest.slice(0, hash);
  const scheme = /^[a-zA-Z][a-zA-Z0-9+.\-]*:/.exec(rest);
  if (scheme) {
    rest = rest.slice(scheme[0].length);
  } else if (/^[^/?]*:/.test(rest)) {
    return null;
  }
  let rawQuery = "";
  const q = rest.indexOf("?");
  if (q >= 0) {
    rawQuery = rest.slice(q + 1);
    rest = rest.slice(0, q);
  }
  let host = "";
  // An address with a scheme and no // (mailto:someone) has no host or path to compare.
  if (scheme && !rest.startsWith("/") && rest !== "") return { host: "", path: "", query: parseQuery(rawQuery) };
  if (rest.startsWith("//")) {
    let end = rest.indexOf("/", 2);
    if (end < 0) end = rest.length;
    let authority = rest.slice(2, end);
    rest = rest.slice(end);
    const at = authority.lastIndexOf("@");
    if (at >= 0) authority = authority.slice(at + 1);
    if (authority.startsWith("[")) {
      const close = authority.indexOf("]");
      if (close < 0) return null;
      const port = authority.slice(close + 1);
      if (port !== "" && !/^:\d*$/.test(port)) return null;
      host = authority.slice(1, close);
    } else {
      const colon = authority.lastIndexOf(":");
      if (colon >= 0) {
        if (!/^\d*$/.test(authority.slice(colon + 1))) return null;
        authority = authority.slice(0, colon);
      }
      host = authority;
    }
    if (!validEscapes(host)) return null;
  }
  if (!validEscapes(rest)) return null;
  return { host: host.toLowerCase().replace(/^www\./, ""), path: escapedPath(rest), query: parseQuery(rawQuery) };
}

function parseQuery(raw: string): Map<string, string> {
  const out = new Map<string, string>();
  const decoder = new TextDecoder();
  for (const part of raw.split("&")) {
    if (part === "" || part.includes(";")) continue;
    const eq = part.indexOf("=");
    const k = eq < 0 ? part : part.slice(0, eq);
    const v = eq < 0 ? "" : part.slice(eq + 1);
    if (!validEscapes(k) || !validEscapes(v)) continue;
    const key = decoder.decode(percentDecodeBytes(k, true));
    // The first value of a repeated parameter is the one compared.
    if (!out.has(key)) out.set(key, decoder.decode(percentDecodeBytes(v, true)));
  }
  return out;
}

// ---- deciding ----

/** A visitor as the rules read them. Exported for the library's own tests. */
export interface FlagSubject {
  subject: string;
  signed_in?: boolean;
  url?: string;
  referrer?: string;
  device?: string;
  language?: string;
  properties?: Record<string, string>;
  user_id?: string;
  /** Ids of the People lists the visitor is in. */
  lists?: string[];
  /** Fields of the visitor's record, as text. */
  person?: Record<string, string>;
}

function ruleHolds(r: FlagDefinitionRule, v: FlagSubject, address: Address | null): boolean {
  if (r.attribute === "list") {
    const mine = (v.lists ?? []).map((x) => x.toLowerCase());
    const inOne = (r.values ?? []).some((x) => mine.includes(x.toLowerCase()));
    return r.op === "is" ? inOne : r.op === "is_not" ? !inOne : false;
  }
  let val = "";
  let present = false;
  const plain = (s: string | undefined) => {
    val = s ?? "";
    present = val !== "";
  };
  if (r.attribute.startsWith("query.")) {
    const name = r.attribute.slice(6);
    present = address?.query.has(name) ?? false;
    val = address?.query.get(name) ?? "";
  } else if (r.attribute.startsWith("property.")) {
    const name = r.attribute.slice(9);
    present = v.properties !== undefined && Object.prototype.hasOwnProperty.call(v.properties, name);
    val = present ? v.properties![name]! : "";
  } else if (r.attribute.startsWith("person.")) {
    const name = r.attribute.slice(7);
    present = v.person !== undefined && Object.prototype.hasOwnProperty.call(v.person, name);
    val = present ? v.person![name]! : "";
  } else {
    switch (r.attribute) {
      case "host":
        plain(address?.host);
        break;
      case "path":
        plain(address?.path);
        break;
      case "url":
        plain(v.url);
        break;
      case "referrer":
        plain(v.referrer);
        break;
      case "device":
        plain((v.device ?? "").toLowerCase());
        break;
      case "language":
        plain((v.language ?? "").toLowerCase());
        break;
      case "signed_in":
        val = v.signed_in ? "true" : "false";
        present = true;
        break;
      case "user_id":
        plain(v.user_id);
        break;
    }
  }
  if (r.op === "exists") return present;
  if (r.op === "not_exists") return !present;
  val = val.toLowerCase();
  const values = (r.values ?? []).map((x) => x.toLowerCase());
  switch (r.op) {
    case "is":
      return present && values.includes(val);
    case "is_not":
      return !values.includes(val);
    case "contains":
      return present && values.some((x) => val.includes(x));
    case "not_contains":
      return !values.some((x) => val.includes(x));
    case "starts_with":
      return present && values.some((x) => val.startsWith(x));
    case "ends_with":
      return present && values.some((x) => val.endsWith(x));
  }
  return false;
}

/**
 * Decides one flag for a visitor, as Outflare's server does. Most code
 * wants {@link decideFlags} or {@link LocalFlags}; this is the core they
 * share, exported so its answers can be tested against the server's.
 */
export function decideFlag(f: FlagDefinition, v: FlagSubject): { variant: string; payload?: unknown; reason: FlagReason } {
  if (!f.variants || f.variants.length === 0) return { variant: "", reason: "off" };
  if (!v.subject) return { variant: "", reason: "no_subject" };
  const address = parseAddress(v.url ?? "");
  for (const r of f.rules ?? []) {
    if (!ruleHolds(r, v, address)) return { variant: "", reason: "rules" };
  }
  if (f.group) {
    const g = flagBucket("group:" + f.group + ":" + v.subject);
    if (g < f.group_from || g >= f.group_to) return { variant: "", reason: "group" };
  }
  if (flagBucket(f.salt + ":rollout:" + v.subject) >= f.rollout * 100) return { variant: "", reason: "rollout" };
  const b = flagBucket(f.salt + ":" + v.subject);
  let total = 0;
  for (const x of f.variants) total += x.weight;
  if (total === 0) return { variant: "", reason: "off" };
  let at = 0;
  for (let i = 0; i < f.variants.length; i++) {
    const x = f.variants[i]!;
    at += x.weight;
    if (b * total < at * BUCKETS || i === f.variants.length - 1) return { variant: x.key, payload: x.payload, reason: "match" };
  }
  return { variant: "", reason: "off" };
}

/** desktop, mobile or tablet from a User-Agent header, or "" when it names no system: what device rules compare. */
export function flagDevice(userAgent: string): string {
  const ua = userAgent ?? "";
  const has = (...s: string[]) => s.some((x) => ua.includes(x));
  const os = has("iPhone", "iPad", "iPod") || has("Android") || has("Windows") || has("CrOS") || has("Macintosh", "Mac OS X") || has("Linux");
  if (has("iPad", "Tablet") || (!has("iPhone", "iPad", "iPod") && has("Android") && !has("Mobile"))) return "tablet";
  if (has("Mobi", "iPhone", "iPod")) return "mobile";
  return os ? "desktop" : "";
}

/** Cuts s to at most n bytes of UTF-8, never inside a character. */
function clip(s: string, n: number): string {
  const bytes = new TextEncoder().encode(s);
  if (bytes.length <= n) return s;
  while (n > 0 && (bytes[n]! & 0xc0) === 0x80) n--;
  return new TextDecoder().decode(bytes.subarray(0, n));
}

function subjectOf(v: FlagVisitor): FlagSubject {
  const userId = (v.userId ?? "").trim();
  const subject = (v.anonymousId ?? "").trim() || userId;
  if (subject === "" || subject.length > 200) {
    throw new Error("outflare: give anonymousId (outflare.anonymousId() from the page, so the version matches the site's) or userId, up to 200 characters");
  }
  const texts = (from: Record<string, unknown> | undefined) => {
    const out: Record<string, string> = {};
    for (const [k, x] of Object.entries(from ?? {})) {
      if (x === null || x === undefined) continue;
      out[k] = clip(typeof x === "string" ? x : (JSON.stringify(x) ?? "").replace(/^"+|"+$/g, ""), 500);
    }
    return out;
  };
  const properties = texts(v.properties);
  return {
    subject,
    signed_in: userId !== "",
    url: clip(v.url ?? "", 2000),
    referrer: clip(v.referrer ?? "", 2000),
    device: flagDevice(v.userAgent ?? ""),
    language: clip(v.language ?? "", 35),
    properties,
    user_id: userId,
    lists: v.lists ?? [],
    person: texts(v.person),
  };
}

/**
 * Decides every flag in definitions for a visitor, in memory: the answer
 * POST .../flags/evaluate would give, with no request.
 */
export function decideFlags(definitions: FlagDefinitions | FlagDefinition[], visitor: FlagVisitor): LocalFlagDecisions {
  const list = Array.isArray(definitions) ? definitions : definitions.flags;
  const who = subjectOf(visitor);
  const out: LocalFlagDecisions = { flags: {}, payloads: {}, reasons: {} };
  for (const f of list) {
    if (visitor.keys && visitor.keys.length > 0 && !visitor.keys.includes(f.key)) continue;
    const res = decideFlag(f, who);
    out.flags[f.key] = res.variant === "" ? false : res.variant;
    if (res.variant !== "" && res.payload !== undefined && res.payload !== null) out.payloads[f.key] = res.payload;
    out.reasons[f.key] = res.reason;
  }
  return out;
}

/** What {@link LocalFlags} needs of the client: createClient()'s GET. */
export interface DefinitionsClient {
  GET(
    path: "/v1/brands/{brand}/flags/definitions",
    init: { params: { path: { brand: string } }; headers?: Record<string, string> },
  ): Promise<{ data?: unknown; error?: unknown; response: Response }>;
}

export interface LocalFlagsOptions {
  /** The brand's id. */
  brand: string;
  /** How often to ask for the definitions again, in seconds. Default: what the server says (30). At least 5. */
  refreshSeconds?: number;
  /** Called when a refresh fails. Deciding carries on with the last definitions. */
  onError?: (err: unknown) => void;
}

/**
 * Decides flags in your server's memory. It reads the brand's flag
 * definitions once, then again every half minute in the background, so a
 * page costs no request to Outflare and a slow or unreachable Outflare
 * never holds a page up: deciding carries on with the last definitions.
 * A change to a flag reaches your server within the refresh time.
 *
 * ```ts
 * const flags = new LocalFlags(createClient({ baseUrl, token }), { brand });
 * const decisions = await flags.decide({ anonymousId, url: request.url, userAgent });
 * html = html.replace("</head>", flagsBootstrapTag(decisions, anonymousId) + "</head>");
 * ```
 *
 * The token needs analytics.read and stays on your server.
 */
export class LocalFlags {
  private definitions: FlagDefinitions | null = null;
  private etag = "";
  private loadedAt = 0;
  private loading: Promise<void> | null = null;

  constructor(
    private readonly client: DefinitionsClient,
    private readonly opts: LocalFlagsOptions,
  ) {}

  /** The definitions in memory, or null before the first load. */
  current(): FlagDefinitions | null {
    return this.definitions;
  }

  /** Reads the definitions now. Rejects when the first read fails; a later failure keeps what is in memory. */
  refresh(): Promise<void> {
    this.loading ??= this.load().finally(() => {
      this.loading = null;
    });
    return this.loading;
  }

  private async load(): Promise<void> {
    const res = await this.client.GET("/v1/brands/{brand}/flags/definitions", {
      params: { path: { brand: this.opts.brand } },
      headers: this.etag ? { "If-None-Match": this.etag } : {},
    });
    if (res.response.status === 304 && this.definitions) {
      this.loadedAt = Date.now();
      return;
    }
    const data = res.data as FlagDefinitions | undefined;
    if (!res.response.ok || !data || !Array.isArray(data.flags)) {
      throw new Error(`outflare: reading flag definitions failed with status ${res.response.status}`);
    }
    this.definitions = data;
    this.etag = res.response.headers.get("ETag") ?? "";
    this.loadedAt = Date.now();
  }

  private stale(): boolean {
    const every = Math.max(5, this.opts.refreshSeconds ?? this.definitions?.refresh_seconds ?? 30);
    return Date.now() - this.loadedAt >= every * 1000;
  }

  /**
   * The visitor's flags. The first call waits for the definitions; later
   * calls answer from memory at once and refresh in the background when
   * the definitions are older than the refresh time.
   */
  async decide(visitor: FlagVisitor): Promise<LocalFlagDecisions> {
    if (!this.definitions) {
      await this.refresh();
    } else if (this.stale()) {
      this.refresh().catch((err) => {
        // Try again after the refresh time, not on every page.
        this.loadedAt = Date.now();
        this.opts.onError?.(err);
      });
    }
    return decideFlags(this.definitions!, visitor);
  }

  /** One flag's version for the visitor: its variant's key, or false when the flag is off for them or isn't a flag. */
  async variant(key: string, visitor: FlagVisitor): Promise<string | false> {
    const d = await this.decide({ ...visitor, keys: [key] });
    return d.flags[key] || false;
  }

  /** Whether a flag is on for the visitor, whichever version they get. */
  async isEnabled(key: string, visitor: FlagVisitor): Promise<boolean> {
    return (await this.variant(key, visitor)) !== false;
  }

  /** The content of the visitor's version of a flag, or undefined when it has none or the flag is off for them. */
  async payload(key: string, visitor: FlagVisitor): Promise<unknown> {
    const d = await this.decide({ ...visitor, keys: [key] });
    return d.flags[key] ? d.payloads[key] : undefined;
  }
}
