// Outflare TypeScript SDK. Licensed Apache-2.0 so it can be embedded anywhere.
//
// Types come from src/schema.ts, generated from api/openapi.yaml with
// `pnpm generate`, so the client always matches the server's contract.
//
//   const gtm = createClient({ baseUrl: "https://outflare.example.com", token: process.env.OUTFLARE_TOKEN });
//   const { data, error } = await gtm.GET("/v1/workspaces/{ws}/members", { params: { path: { ws } } });

import createFetchClient, { type Client } from "openapi-fetch";
import type { components, paths } from "./schema.js";

export type { components, paths } from "./schema.js";
export type Schemas = components["schemas"];
export type Workspace = Schemas["Workspace"];
export type Member = Schemas["Member"];
export type Invite = Schemas["Invite"];
export type WorkspaceRole = Schemas["WorkspaceRole"];
export type APIToken = Schemas["APIToken"];
export type AgentApp = Schemas["AgentApp"];
export type AgentAppKind = Schemas["AgentAppKind"];
export type AuditEvent = Schemas["AuditEvent"];
export type LogEntry = Schemas["LogEntry"];
export type Job = Schemas["Job"];
export type Role = Schemas["Role"];
export type Scope = Schemas["Scope"];
export type ApiError = Schemas["Error"];
export type Mailbox = Schemas["Mailbox"];
export type MailboxStatus = Schemas["MailboxStatus"];
export type MailboxInput = Schemas["MailboxInput"];
export type MailboxPool = Schemas["MailboxPool"];
export type MailboxWarmup = Schemas["MailboxWarmup"];
export type WarmupSettings = Schemas["WarmupSettings"];
export type WarmupDay = Schemas["WarmupDay"];
export type SendingDomain = Schemas["SendingDomain"];
export type SendingDomainDetail = Schemas["SendingDomainDetail"];
export type DomainIssue = Schemas["DomainIssue"];
export type SendingServer = Schemas["SendingServer"];
export type ServerIssue = Schemas["ServerIssue"];
export type BlocklistResult = Schemas["BlocklistResult"];
export type ImportResult = Schemas["ImportResult"];
export type OAuthConnection = Schemas["OAuthConnection"];
export type SupplierMailbox = Schemas["SupplierMailbox"];
export type EmailBisonCompatCall = Schemas["EmailBisonCompatCall"];
export type SupplierMailboxList = Schemas["SupplierMailboxList"];
export type SupplierSync = Schemas["SupplierSync"];
export type SupplierPush = Schemas["SupplierPush"];
export type SupplierImportResult = Schemas["SupplierImportResult"];
export type Meta = Schemas["Meta"];
export type WorkspaceStats = Schemas["WorkspaceStats"];
export type Lead = Schemas["Lead"];
export type LeadInput = Schemas["LeadInput"];
export type LeadList = Schemas["LeadList"];
export type LeadListInput = Schemas["LeadListInput"];
export type LeadListMember = Schemas["LeadListMember"];
export type ListRun = Schemas["ListRun"];
export type PeopleFilter = Schemas["PeopleFilter"];
export type DataProvider = Schemas["DataProvider"];
export type DataPerson = Schemas["DataPerson"];
export type LeadImportResult = Schemas["LeadImportResult"];
export type Suppression = Schemas["Suppression"];
export type SuppressionReason = Schemas["SuppressionReason"];
export type RecipientDecision = Schemas["RecipientDecision"];
export type Sequence = Schemas["Sequence"];
export type SequenceSettings = Schemas["SequenceSettings"];
export type Step = Schemas["Step"];
export type StepInput = Schemas["StepInput"];
export type Enrollment = Schemas["Enrollment"];
export type EnrollmentStatus = Schemas["EnrollmentStatus"];
export type Message = Schemas["Message"];
export type InboundMessage = Schemas["InboundMessage"];
export type InboxTag = Schemas["InboxTag"];
export type InboxView = Schemas["InboxView"];
export type Team = Schemas["Team"];
export type Sender = Schemas["Sender"];
export type AllocationMode = Schemas["AllocationMode"];
export type AllocationChange = Schemas["AllocationChange"];
export type AllocationSuggestion = Schemas["AllocationSuggestion"];
export type AllocationBlocking = Schemas["AllocationBlocking"];
export type AssignedMailbox = Schemas["AssignedMailbox"];
export type PlatformAccount = Schemas["PlatformAccount"];
export type CampaignLink = Schemas["CampaignLink"];
export type MailboxAllocationResult = Schemas["MailboxAllocationResult"];
export type PlannedChange = Schemas["PlannedChange"];
export type ThreadEntry = Schemas["ThreadEntry"];
export type Mission = Schemas["Mission"];
export type MissionInput = Schemas["MissionInput"];
export type Playbook = Schemas["Playbook"];
export type WorkspacePlaybook = Schemas["WorkspacePlaybook"];
export type Kit = Schemas["Kit"];
export type KitPlan = Schemas["KitPlan"];
export type PlaybookConnectionNeed = Schemas["PlaybookConnectionNeed"];
export type MissionMode = Schemas["MissionMode"];
export type MissionStatus = Schemas["MissionStatus"];
export type MissionCall = Schemas["MissionCall"];
export type MissionAction = Schemas["MissionAction"];
export type MissionHeld = Schemas["MissionHeld"];
export type MissionUpdate = Schemas["MissionUpdate"];
export type ConnectorCategory = Schemas["ConnectorCategory"];
export type ConnectorField = Schemas["ConnectorField"];
export type Connector = Schemas["Connector"];
export type ConnectorAuthMethod = Schemas["ConnectorAuthMethod"];
export type ConnectorAuthOption = Schemas["ConnectorAuthOption"];
export type ConnectorAccount = Schemas["ConnectorAccount"];
export type Integration = Schemas["Integration"];
export type ConnectionHook = Schemas["ConnectionHook"];
export type HookEvent = Schemas["HookEvent"];
export type AnalyticsSource = Schemas["AnalyticsSource"];
export type AnalyticsSite = Schemas["AnalyticsSite"];
export type AnalyticsSitePlan = Schemas["AnalyticsSitePlan"];
export type AnalyticsReport = Schemas["AnalyticsReport"];
export type AnalyticsReportName = Schemas["AnalyticsReportName"];
export type AnalyticsMetrics = Schemas["AnalyticsMetrics"];
export type AnalyticsSummaryItem = Schemas["AnalyticsSummaryItem"];
export type AnalyticsPoint = Schemas["AnalyticsPoint"];
export type AnalyticsHistoryRow = Schemas["AnalyticsHistoryRow"];
export type AnalyticsSync = Schemas["AnalyticsSync"];
export type AnalyticsOverviewSite = Schemas["AnalyticsOverviewSite"];
export type SEOReportName = Schemas["SEOReportName"];
export type SEOMetrics = Schemas["SEOMetrics"];
export type SEORow = Schemas["SEORow"];
export type SEOAnswer = Schemas["SEOAnswer"];
export type SEOSource = Schemas["SEOSource"];
export type SEOBudget = Schemas["SEOBudget"];
export type SEOHistoryRow = Schemas["SEOHistoryRow"];
export type SEOLatest = Schemas["SEOLatest"];
export type SEOSite = Schemas["SEOSite"];
export type SEORule = Schemas["SEORule"];
export type Crawl = Schemas["Crawl"];
export type CrawlIssue = Schemas["CrawlIssue"];
export type Fix = Schemas["Fix"];
export type FixTicket = Schemas["FixTicket"];
export type FixTicketDetail = Schemas["FixTicketDetail"];
export type IndexabilityPage = Schemas["IndexabilityPage"];
export type IndexabilityIssue = Schemas["IndexabilityIssue"];
export type IndexabilitySummary = Schemas["IndexabilitySummary"];
export type FixGuideline = Schemas["FixGuideline"];
export type DeployOption = Schemas["DeployOption"];
export type DeployTarget = Schemas["DeployTarget"];
export type Deploy = Schemas["Deploy"];
export type SearchSource = Schemas["SearchSource"];
export type SearchRow = Schemas["SearchRow"];
export type SearchDay = Schemas["SearchDay"];
export type URLInspection = Schemas["URLInspection"];
export type SearchSitemap = Schemas["SearchSitemap"];
export type DigestSettings = Schemas["DigestSettings"];
export type DigestSection = Schemas["DigestSection"];
export type Digest = Schemas["Digest"];
export type Alert = Schemas["Alert"];
export type AlertKind = Schemas["AlertKind"];
export type AlertSetting = Schemas["AlertSetting"];
export type Experiment = Schemas["Experiment"];
export type ExperimentResult = Schemas["ExperimentResult"];
export type FixAutonomy = Schemas["FixAutonomy"];
export type FixAutonomyDecision = Schemas["FixAutonomyDecision"];
export type ExperimentGroup = Schemas["ExperimentGroup"];
export type PageTarget = Schemas["PageTarget"];
export type SearchCompetition = Schemas["SearchCompetition"];
export type InternalLinkTarget = Schemas["InternalLinkTarget"];
export type TrackedKeyword = Schemas["TrackedKeyword"];
export type PageConversion = Schemas["PageConversion"];
export type SEORegression = Schemas["SEORegression"];
export type SocialChannel = Schemas["SocialChannel"];
export type SocialOverview = Schemas["SocialOverview"];
export type SocialWatch = Schemas["SocialWatch"];
export type SocialWatchInput = Schemas["SocialWatchInput"];
export type SocialItem = Schemas["SocialItem"];
export type SocialAction = Schemas["SocialAction"];
export type SocialAccountChannel = Schemas["SocialAccountChannel"];
export type SocialAccount = Schemas["SocialAccount"];
export type SocialAccountDay = Schemas["SocialAccountDay"];
export type SocialPost = Schemas["SocialPost"];
export type PartnerProgram = Schemas["PartnerProgram"];
export type PartnerProgramInput = Schemas["PartnerProgramInput"];
export type PartnerRule = Schemas["PartnerRule"];
export type PartnerRuleInput = Schemas["PartnerRuleInput"];
export type PartnerGroup = Schemas["PartnerGroup"];
export type Partner = Schemas["Partner"];
export type PartnerInput = Schemas["PartnerInput"];
export type PartnerStats = Schemas["PartnerStats"];
export type PartnerConversion = Schemas["PartnerConversion"];
export type PartnerConversionInput = Schemas["PartnerConversionInput"];
export type PartnerConversionResult = Schemas["PartnerConversionResult"];
export type PartnerReferral = Schemas["PartnerReferral"];
export type PartnerReward = Schemas["PartnerReward"];
export type PartnerPayout = Schemas["PartnerPayout"];
export type PartnerAsset = Schemas["PartnerAsset"];
export type PartnerImport = Schemas["PartnerImport"];
export type PartnerPortalSettings = Schemas["PartnerPortalSettings"];
export type SocialGrowth = Schemas["SocialGrowth"];
export type SocialPublication = Schemas["SocialPublication"];
export type SocialSeries = Schemas["SocialSeries"];
export type ReviewPlatform = Schemas["ReviewPlatform"];
export type ReviewMomentum = Schemas["ReviewMomentum"];
export type ReviewPage = Schemas["ReviewPage"];
export type ReviewPageDay = Schemas["ReviewPageDay"];
export type ReviewDraft = Schemas["ReviewDraft"];
export type Review = Schemas["Review"];
export type ReviewsSummary = Schemas["ReviewsSummary"];
export type ReviewGuide = Schemas["ReviewGuide"];
export type AdStatus = Schemas["AdStatus"];
export type AdConnection = Schemas["AdConnection"];
export type AdTrackingTemplate = Schemas["AdTrackingTemplate"];
export type AdTrackingHealth = Schemas["AdTrackingHealth"];
export type ConversionDefinition = Schemas["ConversionDefinition"];
export type ConversionPreset = Schemas["ConversionPreset"];
export type AdAccount = Schemas["AdAccount"];
export type AIProvider = Schemas["AIProvider"];
export type AIAccount = Schemas["AIAccount"];
export type AIAccountMember = Schemas["AIAccountMember"];
export type AIAccountInput = Schemas["AIAccountInput"];
export type AdTotals = Schemas["AdTotals"];
export type AdCampaign = Schemas["AdCampaign"];
export type AdSummaryRow = Schemas["AdSummaryRow"];
export type AdConversionTarget = Schemas["AdConversionTarget"];
export type AdConversionRule = Schemas["AdConversionRule"];
export type AdConversionSend = Schemas["AdConversionSend"];
export type AdSettings = Schemas["AdSettings"];
export type SEOOverviewBrand = Schemas["SEOOverviewBrand"];
export type Backlink = Schemas["Backlink"];
export type BacklinkEvent = Schemas["BacklinkEvent"];
export type BacklinkSiteSummary = Schemas["BacklinkSiteSummary"];
export type SEOCompetitor = Schemas["SEOCompetitor"];
export type SEOCompetitorEvent = Schemas["SEOCompetitorEvent"];
export type SEOKeywordGap = Schemas["SEOKeywordGap"];
export type SEOLinkGap = Schemas["SEOLinkGap"];
export type LinkProspect = Schemas["LinkProspect"];
export type LinkRead = Schemas["LinkRead"];
export type LinkBuildingSettings = Schemas["LinkBuildingSettings"];
export type LinkPitch = Schemas["LinkPitch"];
export type LinkSequence = Schemas["LinkSequence"];
export type LinkResultRow = Schemas["LinkResultRow"];
export type NewsSearch = Schemas["NewsSearch"];
export type NewsArticle = Schemas["NewsArticle"];
export type Journalist = Schemas["Journalist"];
export type PRExpert = Schemas["PRExpert"];
export type Story = Schemas["Story"];
export type StoryCheck = Schemas["StoryCheck"];
export type Coverage = Schemas["Coverage"];
export type SEOTrend = Schemas["SEOTrend"];
export type VerificationStatus = Schemas["VerificationStatus"];
export type VerificationSettings = Schemas["VerificationSettings"];
export type Verification = Schemas["Verification"];
export type VerificationSummary = Schemas["VerificationSummary"];
export type ConnectRequest = Schemas["ConnectRequest"];
export type ExternalCampaign = Schemas["ExternalCampaign"];
export type ExternalMailbox = Schemas["ExternalMailbox"];
export type ImportPreview = Schemas["ImportPreview"];
export type ImportRun = Schemas["ImportRun"];
export type CRMImportPreview = Schemas["CRMImportPreview"];
export type CRMImport = Schemas["CRMImport"];
export type MailboxHealth = Schemas["MailboxHealth"];
export type HealthSignal = Schemas["HealthSignal"];
export type MailboxHealthSummary = Schemas["MailboxHealthSummary"];
export type HealthSettings = Schemas["HealthSettings"];
export type Report = Schemas["Report"];
export type TrackingDomain = Schemas["TrackingDomain"];
export type ReportMetrics = Schemas["ReportMetrics"];
export type Meeting = Schemas["Meeting"];
export type MeetingStatus = Schemas["MeetingStatus"];
export type Attribution = Schemas["Attribution"];
export type Deal = Schemas["Deal"];
export type SpendCategory = Schemas["SpendCategory"];
export type SpendEntry = Schemas["SpendEntry"];
export type RecurringSpend = Schemas["RecurringSpend"];
export type SpendRate = Schemas["SpendRate"];
export type OutcomesReport = Schemas["OutcomesReport"];
export type OutcomeRow = Schemas["OutcomeRow"];
export type OutcomeSync = Schemas["OutcomeSync"];
export type CompanyContext = Schemas["CompanyContext"];
export type WorkspaceContext = Schemas["WorkspaceContext"];
export type Memory = Schemas["Memory"];
export type KnowledgeItem = Schemas["KnowledgeItem"];
export type KnowledgeCollection = Schemas["KnowledgeCollection"];
export type KnowledgeVersion = Schemas["KnowledgeVersion"];
export type KnowledgePassage = Schemas["KnowledgePassage"];
export type KnowledgeAnswer = Schemas["KnowledgeAnswer"];
export type KnowledgeStats = Schemas["KnowledgeStats"];
export type KnowledgeGap = Schemas["KnowledgeGap"];
export type Brand = Schemas["Brand"];
export type BrandContext = Schemas["BrandContext"];
export type BrandMoved = Schemas["BrandMoved"];
export type BrandLinkSuggestion = Schemas["BrandLinkSuggestion"];
export type AnalyticsSiteBrand = Schemas["AnalyticsSiteBrand"];
export type TrackingChannel = Schemas["TrackingChannel"];
export type TrackingChannelID = Schemas["TrackingChannelID"];
export type TrackingCampaign = Schemas["TrackingCampaign"];
export type TrackingCampaignInput = Schemas["TrackingCampaignInput"];
export type AttributionSettings = Schemas["AttributionSettings"];
export type AttributionChannels = Schemas["AttributionChannels"];
export type AttributionCampaignRow = Schemas["AttributionCampaignRow"];
export type AttributionAdRow = Schemas["AttributionAdRow"];
export type AttributionConversion = Schemas["AttributionConversion"];
export type TrackingLink = Schemas["TrackingLink"];
export type WorkspaceStatus = Schemas["WorkspaceStatus"];
export type ICP = Schemas["ICP"];
export type CustomerProfile = Schemas["CustomerProfile"];
export type CustomerProfileInput = Schemas["CustomerProfileInput"];
export type MissionPlanInput = Schemas["MissionPlanInput"];
export type MissionPlanResult = Schemas["MissionPlanResult"];
export type MissionDraft = Schemas["MissionDraft"];
export type LifecycleStage = Schemas["LifecycleStage"];
export type InfraPlan = Schemas["InfraPlan"];
export type InfraPlanInput = Schemas["InfraPlanInput"];
export type InfraPlanItem = Schemas["InfraPlanItem"];
export type InfraPlanDraft = Schemas["InfraPlanDraft"];
export type InfraWarning = Schemas["InfraWarning"];
export type InfrastructureOverview = Schemas["InfrastructureOverview"];

export type WebhookEndpoint = Schemas["WebhookEndpoint"];
export type WebhookEndpointWithSecret = Schemas["WebhookEndpointWithSecret"];
export type EmailOverview = Schemas["EmailOverview"];
export type EmailProviderGuide = Schemas["EmailProviderGuide"];
export type EmailProvider = Schemas["EmailProvider"];
export type EmailDomain = Schemas["EmailDomain"];
export type EmailDomainRecord = Schemas["EmailDomainRecord"];
export type EmailTemplate = Schemas["EmailTemplate"];
export type EmailTemplateInput = Schemas["EmailTemplateInput"];
export type EmailTemplateVariable = Schemas["EmailTemplateVariable"];
export type EmailTemplateVersion = Schemas["EmailTemplateVersion"];
export type EmailRecipient = Schemas["EmailRecipient"];
export type EmailTrigger = Schemas["EmailTrigger"];
export type EmailTopic = Schemas["EmailTopic"];
export type EmailBroadcast = Schemas["EmailBroadcast"];
export type Journey = Schemas["Journey"];
export type JourneyRun = Schemas["JourneyRun"];
export type JourneyStep = Schemas["JourneyStep"];
export type JourneyCondition = Schemas["JourneyCondition"];
export type JourneyVersion = Schemas["JourneyVersion"];
export type JourneyTest = Schemas["JourneyTest"];
export type Rule = Schemas["Rule"];
export type RuleTrigger = Schemas["RuleTrigger"];
export type RuleCondition = Schemas["RuleCondition"];
export type RuleSimulation = Schemas["RuleSimulation"];
export type SmartList = Schemas["SmartList"];
export type SmartListRule = Schemas["SmartListRule"];
export type SmartListPerson = Schemas["SmartListPerson"];
export type Flag = Schemas["Flag"];
export type FlagVariant = Schemas["FlagVariant"];
export type FlagRule = Schemas["FlagRule"];
export type FlagDecisions = Schemas["FlagDecisions"];
export type ABTest = Schemas["ABTest"];
export type ABTestGoal = Schemas["ABTestGoal"];
export type ABTestResults = Schemas["ABTestResults"];
export type ABTestArm = Schemas["ABTestArm"];
export type ABTestComparison = Schemas["ABTestComparison"];
export type ABTestPlan = Schemas["ABTestPlan"];
export type ABTestBreakdown = Schemas["ABTestBreakdown"];
export type ABTestAIRun = Schemas["ABTestAIRun"];
export type EmailBroadcastRecipient = Schemas["EmailBroadcastRecipient"];
export type EmailTopicRef = Schemas["EmailTopicRef"];
export type EmailPreview = Schemas["EmailPreview"];
export type Email = Schemas["Email"];
export type EmailEvent = Schemas["EmailEvent"];
export type EmailStatus = Schemas["EmailStatus"];
export type SendEmailInput = Schemas["SendEmailInput"];
export type WebhookDelivery = Schemas["WebhookDelivery"];
export type WebhookEventType = Schemas["WebhookEventType"];
/** The body of a webhook delivery. */
export type WebhookEvent = Schemas["WebhookEvent"];

export type ClientOptions = {
  /** Base URL of the instance, e.g. "https://outflare.example.com". Empty for same-origin (the web app). */
  baseUrl?: string;
  /** API token (oft_...). Omit in the browser to use the session cookie. */
  token?: string;
  fetch?: typeof fetch;
  /**
   * Act inside an agent mission: every call carries the Outflare-Mission
   * header, so it is logged against the mission and writes follow its mode
   * (they may be answered 202 with a MissionHeld body instead of running).
   */
  mission?: string;
  /**
   * The client will act inside missions without a fixed one (it sets the
   * Outflare-Mission header itself, as the MCP server does), so it is typed
   * as a {@link MissionClient}.
   */
  missions?: boolean;
  /**
   * Names the client in the Outflare-Client header, so the server's product
   * events say where calls come from: "web", "mcp/0.1.0", or your app's
   * name. Default "sdk-ts/<version>".
   */
  clientName?: string;
  /**
   * How many times to retry a call that failed on the network or was
   * answered 429, 502, 503 or 504, waiting as long as Retry-After asks (up
   * to a minute), else backing off from half a second. Default 3; 0 turns
   * retries off. Every write carries an Idempotency-Key, the same one on
   * each retry, so a retried write never runs twice.
   */
  maxRetries?: number;
};

/** This SDK's version, sent in the Outflare-Client header. */
export const SDK_VERSION = "0.1.0";

type Held = components["schemas"]["MissionHeld"];
type WithoutHeldResponse<R> = R extends { content: { "application/json": infer C } }
  ? [Exclude<C, Held>] extends [never]
    ? never
    : Omit<R, "content"> & { content: { "application/json": Exclude<C, Held> } }
  : R;
type WithoutHeldResponses<Rs> = {
  [K in keyof Rs as K extends 202 ? ([WithoutHeldResponse<Rs[K]>] extends [never] ? never : K) : K]: K extends 202 ? WithoutHeldResponse<Rs[K]> : Rs[K];
};
type WithoutHeldOperation<O> = O extends { responses: infer Rs } ? Omit<O, "responses"> & { responses: WithoutHeldResponses<Rs> } : O;

/**
 * The API as a client outside any mission sees it: writes are never answered
 * with a MissionHeld 202, so those answers are left out of the types.
 */
export type DirectPaths = { [P in keyof paths]: { [M in keyof paths[P]]: WithoutHeldOperation<paths[P][M]> } };

/** A client outside missions (the default). */
export type OutflareClient = Client<DirectPaths>;
/** A client that may act inside a mission: any workspace write can answer 202 with {@link MissionHeld}. */
export type MissionClient = Client<paths>;

/**
 * Whether a write was held by its mission (a dry run, or waiting for a
 * person) instead of running.
 */
export function isHeld(data: unknown): data is MissionHeld {
  return typeof data === "object" && data !== null && "mission_action" in data && "approval_required" in data;
}

export function createClient(opts: ClientOptions & ({ mission: string } | { missions: true })): MissionClient;
export function createClient(opts?: ClientOptions): OutflareClient;
export function createClient(opts: ClientOptions & { missions?: boolean } = {}): OutflareClient | MissionClient {
  const headers: Record<string, string> = { "Outflare-Client": opts.clientName ?? `sdk-ts/${SDK_VERSION}` };
  if (opts.token) headers.Authorization = `Bearer ${opts.token}`;
  if (opts.mission) headers["Outflare-Mission"] = opts.mission;
  return createFetchClient<DirectPaths>({
    baseUrl: (opts.baseUrl ?? "").replace(/\/+$/, ""),
    headers,
    fetch: retrying(opts.fetch ?? ((req: Request) => globalThis.fetch(req)), opts.maxRetries ?? 3),
    credentials: opts.token ? "omit" : "same-origin",
  });
}

const RETRY_STATUSES = new Set([429, 502, 503, 504]);

/** Wraps fetch with idempotency keys on writes and retries with backoff. */
function retrying(base: (req: Request) => Promise<Response>, maxRetries: number): (req: Request) => Promise<Response> {
  return async (req) => {
    if (req.method !== "GET" && req.method !== "HEAD" && !req.headers.has("Idempotency-Key")) {
      req = new Request(req, { headers: new Headers(req.headers) });
      req.headers.set("Idempotency-Key", crypto.randomUUID());
    }
    for (let attempt = 0; ; attempt++) {
      const last = attempt >= maxRetries;
      let res: Response;
      try {
        res = await base(last ? req : req.clone());
      } catch (err) {
        if (last || (err as Error)?.name === "AbortError") throw err;
        await sleep(backoff(attempt + 1, null));
        continue;
      }
      if (last || !RETRY_STATUSES.has(res.status)) return res;
      await res.body?.cancel();
      await sleep(backoff(attempt + 1, res.headers.get("Retry-After")));
    }
  };
}

/** Milliseconds to wait: what Retry-After asks (up to a minute), else 0.5s doubling with jitter. */
function backoff(attempt: number, retryAfter: string | null): number {
  if (retryAfter) {
    const secs = Number(retryAfter);
    const ms = Number.isFinite(secs) ? secs * 1000 : Date.parse(retryAfter) - Date.now();
    if (Number.isFinite(ms)) return Math.min(Math.max(ms, 0), 60_000);
  }
  return 500 * 2 ** (attempt - 1) * (0.75 + Math.random() / 2);
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

type Page<T> = { data?: T[]; next_cursor?: string | null };

/**
 * Every item of a paged list, fetching page after page by next_cursor.
 * Pass a function that makes the call for a given cursor:
 *
 *   for await (const lead of paginate((cursor) => gtm.GET("/v1/workspaces/{ws}/leads", { params: { path: { ws }, query: { q: "acme", cursor } } }))) {
 *     console.log(lead.email);
 *   }
 *
 * Throws an {@link OutflareError} if a page fails.
 */
export async function* paginate<T>(page: (cursor: string | undefined) => Promise<Result<Page<T>>>): AsyncGenerator<T> {
  let cursor: string | undefined;
  do {
    const body = ok(await page(cursor));
    yield* body.data ?? [];
    cursor = body.next_cursor ?? undefined;
  } while (cursor);
}

/** An error answer from the API, thrown by {@link ok}. */
export class OutflareError extends Error {
  /** HTTP status, e.g. 409. */
  readonly status: number;
  /** Machine-readable code, e.g. "conflict" or "invalid_request". */
  readonly code: string;
  /** The request id, to find the request in the server's logs. */
  readonly requestId?: string;
  /** The request field a 400 is about, when there is one, e.g. "url". */
  readonly param?: string;

  constructor(status: number, code: string, message: string, requestId?: string, param?: string) {
    super(message);
    this.name = "OutflareError";
    this.status = status;
    this.code = code;
    this.requestId = requestId;
    this.param = param;
  }
}

type Result<T> = { data?: T; error?: unknown; response: Response };

/**
 * Returns a call's data, or throws an {@link OutflareError}. For scripts and
 * agents that would rather not check `error` after every call:
 *
 *   const { data: mailboxes } = ok(await gtm.GET("/v1/workspaces/{ws}/mailboxes", { params: { path: { ws } } }));
 */
export function ok<T>(result: Result<T>): T {
  const { data, error, response } = result;
  if (error === undefined && response.ok) return data as T;
  const e = (error as ApiError | undefined)?.error;
  throw new OutflareError(response.status, e?.code ?? "http_error", e?.message ?? response.statusText, e?.request_id ?? response.headers.get("X-Request-Id") ?? undefined, e?.param);
}

/** A webhook delivery whose signature or timestamp did not check out. */
export class WebhookVerificationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "WebhookVerificationError";
  }
}

type HeaderSource = Headers | Record<string, string | string[] | undefined>;

function header(headers: HeaderSource, name: string): string | undefined {
  if (typeof (headers as Headers).get === "function") return (headers as Headers).get(name) ?? undefined;
  const rec = headers as Record<string, string | string[] | undefined>;
  const key = Object.keys(rec).find((k) => k.toLowerCase() === name);
  const v = key === undefined ? undefined : rec[key];
  return Array.isArray(v) ? v[0] : v;
}

function fromBase64(s: string): Uint8Array<ArrayBuffer> {
  const bin = atob(s);
  return Uint8Array.from(bin, (c) => c.charCodeAt(0));
}

/**
 * Checks a webhook delivery from Outflare and returns its event. Pass the
 * raw request body (before any JSON parsing), the request headers and the
 * endpoint's signing secret (whsec_...). Throws a
 * {@link WebhookVerificationError} if the signature is wrong or the
 * delivery is more than five minutes old or early. Works anywhere with Web
 * Crypto: Node 18+, Deno, Bun, Cloudflare Workers, browsers.
 *
 *   const event = await verifyWebhook(await req.text(), req.headers, process.env.OUTFLARE_WEBHOOK_SECRET!);
 *   if (event.type === "lead.replied") { ... }
 *
 * Deliveries are signed the Standard Webhooks way, so any of its libraries
 * also verifies them. Use event.id to ignore a delivery you already handled.
 */
export async function verifyWebhook(
  payload: string,
  headers: HeaderSource,
  secret: string,
  opts: { toleranceSeconds?: number; now?: Date } = {},
): Promise<WebhookEvent> {
  const id = header(headers, "webhook-id");
  const ts = header(headers, "webhook-timestamp");
  const sigs = header(headers, "webhook-signature");
  if (!id || !ts || !sigs) throw new WebhookVerificationError("missing webhook-id, webhook-timestamp or webhook-signature header");
  const sent = Number(ts);
  const now = Math.floor((opts.now ?? new Date()).getTime() / 1000);
  if (!Number.isInteger(sent) || Math.abs(now - sent) > (opts.toleranceSeconds ?? 300)) {
    throw new WebhookVerificationError("webhook-timestamp is too far from now");
  }
  const key = await crypto.subtle.importKey("raw", fromBase64(secret.replace(/^whsec_/, "")), { name: "HMAC", hash: "SHA-256" }, false, ["verify"]);
  const signed = new TextEncoder().encode(`${id}.${ts}.${payload}`);
  for (const part of sigs.split(" ")) {
    const [version, sig] = part.split(",", 2);
    if (version !== "v1" || !sig) continue;
    let mac: Uint8Array<ArrayBuffer>;
    try {
      mac = fromBase64(sig);
    } catch {
      continue;
    }
    // subtle.verify compares in constant time.
    if (await crypto.subtle.verify("HMAC", key, mac, signed)) return JSON.parse(payload) as WebhookEvent;
  }
  throw new WebhookVerificationError("no signature matches the secret");
}

/**
 * The tag that hands your server's flag decisions to the page, so the
 * Outflare site script shows the visitor's version at once, with no request
 * and no flash of the original. Decide with `POST .../flags/evaluate`
 * (`gtm.POST("/v1/workspaces/{ws}/flags/evaluate", ...)`) for the visitor's
 * id: the `of_aid` cookie when the browser has one, else an id you made
 * (8 to 64 letters, digits and dashes), which the script then uses for
 * this page. Put the tag anywhere in the page, before site.js runs. The
 * JSON is escaped so content in payloads can't close the tag.
 */
export function flagsBootstrapTag(decisions: Pick<FlagDecisions, "flags"> & { payloads?: FlagDecisions["payloads"] }, anonymousId: string): string {
  const json = JSON.stringify({ anonymousId, flags: decisions.flags, payloads: decisions.payloads ?? {} }).replace(
    /[<>&\u2028\u2029]/g,
    (c) => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"),
  );
  return `<script type="application/json" id="outflare-flags">${json}</script>`;
}
