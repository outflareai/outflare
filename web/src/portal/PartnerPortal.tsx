import {
  invitationToken,
  ProposalInvitation,
  PartnerProposals,
} from "./PartnerProposals";
import { PortalCoupons } from "./PortalCoupons";
import { BrandLinks, PortalHelp, SupportLink } from "./PortalHelp";
import { TermsGate } from "./TermsGate";
import {
  useCallback,
  useEffect,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import {
  ArrowDownLeft,
  ArrowRight,
  ArrowUpRight,
  BadgeCheck,
  BookOpen,
  Check,
  CheckCircle2,
  ChevronRight,
  CircleHelp,
  Clock3,
  Copy as CopyGlyph,
  ExternalLink,
  FileText,
  Gift,
  Globe2,
  LayoutDashboard,
  Link2,
  ListChecks,
  Loader2,
  LogOut,
  Menu,
  Moon,
  MoreHorizontal,
  Plus,
  Search,
  Settings2,
  ShieldCheck,
  Sparkles,
  Sun,
  Users,
  Video,
  Wallet,
} from "lucide-react";
import { toast } from "sonner";
import { OutflareMark } from "../brand";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from "@/components/ui/dialog";
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet";
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Skeleton } from "@/components/ui/skeleton";
import { Toaster } from "@/components/ui/sonner";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
} from "@/components/ui/dropdown-menu";
import {
  Table,
  TableHeader,
  TableHead,
  TableBody,
  TableRow,
  TableCell,
} from "@/components/ui/table";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import {
  request,
  signOut,
  hasEmbeddedSession,
  isEmbedded,
  invitedWebsite,
  invitationKey,
  money,
  reward,
  matches,
  needsAsset,
  names,
  PortalError,
  type Program,
  type Me,
  type Tasks,
  type Task,
  type Audience,
  type Submission,
} from "./client";

type Page =
  | "proposals"
  | "tasks"
  | "mine"
  | "profiles"
  | "dashboard"
  | "links"
  | "assets"
  | "referrals"
  | "payouts"
  | "settings"
  | "help";
const navigation: { page: Page; label: string; icon: typeof Globe2 }[] = [
  { page: "proposals", label: "Proposals", icon: BookOpen },
  { page: "tasks", label: "Earn", icon: Sparkles },
  { page: "mine", label: "My work", icon: ListChecks },
  { page: "profiles", label: "My channels", icon: Globe2 },
  { page: "dashboard", label: "Overview", icon: LayoutDashboard },
  { page: "payouts", label: "Earnings", icon: Wallet },
  { page: "links", label: "Promotion links", icon: Link2 },
  { page: "assets", label: "Brand assets", icon: BookOpen },
  { page: "referrals", label: "Referrals", icon: Users },
  { page: "settings", label: "Settings", icon: Settings2 },
  { page: "help", label: "Help & support", icon: CircleHelp },
];
const supported = [
  "website",
  "youtube",
  "x",
  "linkedin",
  "instagram",
  "tiktok",
  "newsletter",
  "reddit",
  "facebook",
  "discord",
  "podcast",
  "other",
];
function errorText(error: unknown) {
  track("ui.error_shown", {
    outcome: "failure",
    error_code: error instanceof PortalError ? error.code : "network_error",
  });
  return error instanceof Error
    ? error.message
    : "Something went wrong. Please try again.";
}
function Copy({
  value,
  label = "Copy",
  compact = false,
}: {
  value: string;
  label?: string;
  compact?: boolean;
}) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      variant="outline"
      size={compact ? "icon-sm" : "sm"}
      aria-label={label}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          track("ui.copied", { kind: "partner_portal" });
          setTimeout(() => setCopied(false), 1800);
        } catch {
          track("ui.error_shown", {
            outcome: "failure",
            error_code: "clipboard_unavailable",
          });
          toast.error("Copy is unavailable. Select and copy the text instead.");
        }
      }}
    >
      {copied ? <Check className="size-3.5" /> : <CopyIcon />}
      {!compact && (copied ? "Copied" : label)}
    </Button>
  );
}
function CopyIcon() {
  return <CopyGlyph className="size-3.5" />;
}
function ChannelIcon({
  kind,
  className,
}: {
  kind: string;
  className?: string;
}) {
  const Icon =
    kind === "youtube" || kind === "video"
      ? Video
      : kind === "newsletter"
        ? FileText
        : kind === "community" ||
            kind === "discord" ||
            kind === "reddit" ||
            kind === "facebook"
          ? Users
          : kind === "blog"
            ? BookOpen
            : kind === "other"
              ? Gift
              : Globe2;
  return (
    <div
      className={cn(
        "flex size-10 shrink-0 items-center justify-center rounded-lg border",
        kind === "video" || kind === "youtube"
          ? "border-rose-500/15 bg-rose-500/5 text-rose-600 dark:text-rose-400"
          : kind === "newsletter"
            ? "border-amber-500/15 bg-amber-500/5 text-amber-600 dark:text-amber-400"
            : kind === "blog" || kind === "website"
              ? "border-violet-500/15 bg-violet-500/5 text-violet-600 dark:text-violet-400"
              : "border-emerald-500/15 bg-emerald-500/5 text-emerald-600 dark:text-emerald-400",
        className,
      )}
    >
      <Icon className="size-[18px]" strokeWidth={1.6} />
    </div>
  );
}
function Status({ value }: { value: string }) {
  return (
    <Badge
      variant="outline"
      className={cn(
        "gap-1.5 rounded-md font-normal",
        value === "approved"
          ? "border-emerald-500/20 bg-emerald-500/5 text-emerald-700 dark:text-emerald-400"
          : value === "offered"
            ? "border-violet-500/20 bg-violet-500/5 text-violet-700 dark:text-violet-400"
            : "text-muted-foreground",
      )}
    >
      <span className="size-1.5 rounded-full bg-current" />
      {names[value] || value}
    </Badge>
  );
}
function Empty({
  title,
  children,
  action,
}: {
  title: string;
  children: ReactNode;
  action?: ReactNode;
}) {
  return (
    <Card className="items-center gap-3 border-dashed px-6 py-14 text-center shadow-none">
      <div className="mb-1 rounded-full bg-muted p-3">
        <Sparkles className="size-5 text-muted-foreground" />
      </div>
      <h3 className="text-base font-medium">{title}</h3>
      <p className="max-w-sm text-sm leading-6 text-muted-foreground">
        {children}
      </p>
      {action}
    </Card>
  );
}
function Field({
  label,
  children,
  hint,
}: {
  label: string;
  children: ReactNode;
  hint?: string;
}) {
  return (
    <label className="block space-y-2">
      <span className="text-xs font-medium">{label}</span>
      {children}
      {hint && (
        <span className="block text-xs leading-5 text-muted-foreground">
          {hint}
        </span>
      )}
    </label>
  );
}
function Pick({
  value,
  onChange,
  options,
  label,
}: {
  value: string;
  onChange: (v: string) => void;
  options: { value: string; label: string }[];
  label: string;
}) {
  return (
    <Select
      value={value || "none"}
      onValueChange={(v) => onChange(v === "none" ? "" : v)}
    >
      <SelectTrigger className="w-full" aria-label={label}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((o) => (
          <SelectItem key={o.value || "none"} value={o.value || "none"}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

export function PartnerPortal() {
  const [program, setProgram] = useState<Program | null>(null),
    [me, setMe] = useState<Me | null>(null),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const [page, setPage] = useState<Page>(
      (new URLSearchParams(location.search).has("proposal")
        ? "proposals"
        : location.hash.slice(1) || "tasks") as Page,
    ),
    [mobile, setMobile] = useState(false),
    [tasks, setTasks] = useState<Tasks>({ data: [], submissions: [] }),
    [audiences, setAudiences] = useState<Audience[]>([]);
  const [selected, setSelected] = useState<Task | null>(null),
    [adding, setAdding] = useState(false),
    [verifying, setVerifying] = useState<Audience | null>(null),
    [dark, setDark] = useState(
      document.documentElement.dataset.theme === "dark",
    );
  const [query, setQuery] = useState(""),
    [channel, setChannel] = useState("all"),
    [view, setView] = useState("recommended");
  const loadWork = useCallback(async () => {
    const [t, a] = await Promise.all([
      request<Tasks>("tasks"),
      request<{ data: Audience[] }>("audiences"),
    ]);
    setTasks(t);
    setAudiences(a.data);
    const pending = invitedWebsite();
    if (
      pending &&
      a.data.some((profile) => profile.host === pending && profile.verified_at)
    ) {
      try {
        sessionStorage.removeItem(invitationKey);
      } catch {}
    }
    return a.data;
  }, []);
  const reload = useCallback(async () => {
    const m = await request<Me>("me");
    setMe(m);
    setProgram(m.program);
    if (m.program.pages.includes("tasks")) await loadWork();
  }, [loadWork]);
  useEffect(() => {
    let live = true;
    (async () => {
      try {
        const match = /^#signin=(ofpl_[A-Za-z0-9_-]+)$/.exec(location.hash);
        if (match) {
          await request("session", "POST", { token: match[1] });
          history.replaceState(
            null,
            "",
            location.pathname +
              location.search +
              (new URLSearchParams(location.search).has("proposal")
                ? "#proposals"
                : invitedWebsite()
                  ? "#profiles"
                  : "#tasks"),
          );
          setPage(
            new URLSearchParams(location.search).has("proposal")
              ? "proposals"
              : invitedWebsite()
                ? "profiles"
                : "tasks",
          );
        }
        if (!hasEmbeddedSession()) {
          const p = await request<Program>("program");
          if (live) setProgram(p);
        }
        try {
          const m = await request<Me>("me");
          if (live) {
            setMe(m);
            setProgram(m.program);
            if (
              !m.program.pages.includes("tasks") &&
              !m.program.pages.includes(page)
            )
              setPage("dashboard");
            if (m.program.pages.includes("tasks")) await loadWork();
          }
        } catch (e) {
          if (!(e instanceof PortalError && e.status === 401)) throw e;
        }
      } catch (e) {
        if (live) setError(errorText(e));
      } finally {
        if (live) setLoading(false);
      }
    })();
    return () => {
      live = false;
    };
  }, [loadWork]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const change = () => setPage((location.hash.slice(1) || "tasks") as Page);
    window.addEventListener("hashchange", change);
    return () => window.removeEventListener("hashchange", change);
  }, []);
  useEffect(() => {
    if (!program) return;
    document.title = program.title;
    let style = document.getElementById("portal-brand-style");
    if (!style) {
      style = document.createElement("style");
      style.id = "portal-brand-style";
      document.head.appendChild(style);
    }
    style.textContent = program.custom_css || "";
    document.documentElement.style.setProperty(
      "--sidebar-primary",
      dark
        ? `color-mix(in oklab, ${program.color || "#111827"} 65%, white)`
        : program.color,
    );
    if (program.favicon_url) {
      let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
      if (!link) {
        link = document.createElement("link");
        link.rel = "icon";
        document.head.appendChild(link);
      }
      link.href = program.favicon_url;
    }
  }, [program, dark]);
  useEffect(() => {
    if (
      program &&
      me &&
      page !== "help" &&
      !program.pages.includes(page === "mine" ? "tasks" : page)
    ) {
      setPage(program.pages.includes("tasks") ? "tasks" : "dashboard");
    }
  }, [program, me, page]);
  const go = (next: Page, refresh = true) => {
    setPage(next);
    location.hash = next;
    setMobile(false);
    track("ui.partner_portal_navigated", { page: next });
    if (refresh && ["payouts", "dashboard", "mine"].includes(next))
      void reload().catch((e) => setError(errorText(e)));
  };
  const openTask = (task: Task) => {
    setSelected(task);
    track("ui.partner_opportunity_opened", { kind: task.config.category });
  };
  const openProfiles = () => {
    setAdding(true);
    track("ui.partner_profile_composer_opened");
  };
  const theme = () => {
    const next = !dark;
    setDark(next);
    document.documentElement.dataset.theme = next ? "dark" : "light";
    try {
      localStorage.setItem("theme", next ? "dark" : "light");
    } catch {
      /* Optional preference. */
    }
  };
  if (loading)
    return (
      <div className="min-h-screen bg-sidebar">
        <div className="mx-auto max-w-6xl space-y-8 p-8">
          <Skeleton className="h-10 w-48" />
          <Skeleton className="h-24 w-full" />
          <div className="grid gap-5 md:grid-cols-2">
            {[1, 2, 3, 4].map((n) => (
              <Skeleton key={n} className="h-64" />
            ))}
          </div>
        </div>
      </div>
    );
  if (!program)
    return (
      <div className="grid min-h-screen place-items-center p-6">
        <Empty
          title="This portal couldn't be opened"
          action={<Button onClick={() => location.reload()}>Try again</Button>}
        >
          {error || "Check your invitation link and try again."}
        </Empty>
      </div>
    );
  if (!me)
    return (
      <>
        {invitationToken ? (
          <ProposalInvitation program={program} />
        ) : (
          <Auth program={program} error={error} />
        )}
        <Toaster />
      </>
    );
  const currency = me.currency || program.currency;
  const earningPage = program.pages.includes("tasks") ? "tasks" : "dashboard";
  const nav = navigation.filter(
    (n) =>
      [earningPage, "mine", "profiles", "payouts", "help"].includes(n.page) &&
      (n.page === "help" ||
        program.pages.includes(n.page === "mine" ? "proposals" : n.page)),
  );
  const selectedNav =
    page === "proposals"
      ? "mine"
      : ["links", "assets", "referrals", "dashboard"].includes(page)
        ? earningPage
        : page;
  const active = navigation.find((n) => n.page === page) || nav[0];
  const firstName = (me.name || "there").split(" ")[0];
  const outstanding = tasks.submissions.filter((s) =>
    ["applied", "accepted", "offered", "submitted"].includes(s.status),
  );
  const verified = audiences.filter((a) => a.verified_at).length;
  const sidebar = (
    <div className="flex h-full flex-col">
      <div className="flex h-[76px] items-center gap-3 border-b px-5">
        {program.logo_url ? (
          <img
            src={program.logo_url}
            alt=""
            className="size-8 rounded-md object-contain"
          />
        ) : (
          <div className="flex size-8 items-center justify-center rounded-lg bg-foreground text-background">
            <OutflareMark className="size-5" />
          </div>
        )}
        <div className="min-w-0">
          <p className="truncate text-sm font-semibold">
            {program.title.replace(/ partners$/i, "")}
          </p>
          <p className="text-[11px] text-muted-foreground">Partner workspace</p>
        </div>
      </div>
      <div className="px-5 pb-3 pt-7 text-[10px] font-medium uppercase tracking-[.12em] text-muted-foreground">
        Your workspace
      </div>
      <nav className="space-y-1 px-3">
        {nav.map((n) => (
          <button
            key={n.page}
            onClick={() => go(n.page === "mine" ? "proposals" : n.page)}
            className={cn(
              "flex h-10 w-full items-center gap-3 rounded-md px-3 text-left text-[13px] transition-colors hover:bg-sidebar-accent",
              selectedNav === n.page
                ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
                : "text-muted-foreground",
            )}
          >
            <n.icon
              className={cn(
                "size-[17px]",
                selectedNav === n.page && "text-sidebar-primary",
              )}
              strokeWidth={1.7}
            />
            <span>{n.label}</span>
            {n.page === "mine" && outstanding.length > 0 && (
              <span className="ml-auto rounded bg-background px-1.5 text-[10px] text-muted-foreground">
                {outstanding.length}
              </span>
            )}
          </button>
        ))}
      </nav>
      <div className="mt-auto p-4">
        <BrandLinks
          links={program.header_links}
          className="mb-4 flex flex-wrap gap-3"
        />
        {program.home_url && (
          <a
            className="mb-4 flex items-center gap-2 text-xs text-muted-foreground hover:text-foreground"
            href={program.home_url}
            target="_blank"
            rel="noopener noreferrer"
          >
            Visit brand website
            <ArrowUpRight className="size-3" />
          </a>
        )}
        {program.pages.includes("profiles") && (
          <div className="mb-4 rounded-lg border bg-background p-3.5">
            <div className="mb-2 flex items-center gap-2">
              <ShieldCheck className="size-4 text-muted-foreground" />
              <span className="text-xs font-medium">Your partner profile</span>
            </div>
            <p className="text-[11px] leading-5 text-muted-foreground">
              {audiences.length
                ? `${audiences.length} channels added · ${verified} verified website${verified === 1 ? "" : "s"}`
                : "Add your channels to unlock relevant opportunities."}
            </p>
            <button
              className="mt-3 inline-flex items-center gap-1.5 text-xs font-medium"
              onClick={() => go("profiles")}
            >
              Manage profiles
              <ArrowUpRight className="size-3" />
            </button>
          </div>
        )}
        <div className="flex items-center gap-2.5 border-t pt-4">
          <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-medium">
            {(me.name || me.email)
              .split(" ")
              .map((w) => w[0])
              .slice(0, 2)
              .join("")
              .toUpperCase()}
          </div>
          <div className="min-w-0 flex-1">
            <p className="truncate text-xs font-medium">
              {me.name || "Partner"}
            </p>
            <p className="truncate text-[10px] text-muted-foreground">
              {me.email}
            </p>
          </div>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Settings"
            onClick={() => go("settings")}
          >
            <Settings2 className="size-4" />
          </Button>
        </div>
      </div>
    </div>
  );
  const submissions = new Map(tasks.submissions.map((s) => [s.task_id, s]));
  const visible = tasks.data
    .filter((t) => {
      const own = submissions.get(t.id);
      if (page === "mine" && !own) return false;
      if (page !== "mine" && own && own.status !== "offered") return false;
      if (
        page !== "mine" &&
        view === "recommended" &&
        needsAsset(t.config) &&
        !audiences.some((a) => matches(t.config, a)) &&
        !own
      )
        return false;
      if (channel !== "all" && t.config.asset_kind !== channel) return false;
      return (t.config.title + " " + t.config.description)
        .toLowerCase()
        .includes(query.toLowerCase());
    })
    .sort(
      (a, b) =>
        Number(!!b.config.target_host) - Number(!!a.config.target_host) ||
        Number(needsAsset(b.config)) - Number(needsAsset(a.config)),
    );
  return (
    <div className="min-h-screen bg-sidebar/40 text-foreground">
      <Toaster />
      <aside className="fixed inset-y-0 left-0 z-30 hidden w-56 border-r bg-sidebar lg:block">
        {sidebar}
      </aside>
      <Sheet open={mobile} onOpenChange={setMobile}>
        <SheetContent side="left" className="w-64 gap-0 p-0">
          <SheetHeader className="sr-only">
            <SheetTitle>Partner navigation</SheetTitle>
            <SheetDescription>
              Navigate your partner workspace.
            </SheetDescription>
          </SheetHeader>
          {sidebar}
        </SheetContent>
      </Sheet>
      <div className="lg:pl-56">
        <header className="sticky top-0 z-20 flex h-[76px] items-center justify-between border-b bg-background/95 px-5 backdrop-blur-sm sm:px-8">
          <div className="flex items-center gap-3">
            <Button
              className="lg:hidden"
              variant="ghost"
              size="icon-sm"
              aria-label="Open navigation"
              onClick={() => setMobile(true)}
            >
              <Menu className="size-5" />
            </Button>
            <span className="hidden text-xs text-muted-foreground sm:block">
              Partner workspace
            </span>
            <ChevronRight className="hidden size-3 text-muted-foreground sm:block" />
            <span className="text-xs font-medium">{active?.label}</span>
          </div>
          <div className="flex items-center gap-3">
            <BrandLinks
              links={program.header_links}
              className="hidden items-center gap-5 xl:flex"
            />
            <span className="hidden items-center gap-1.5 rounded-full border px-2.5 py-1 text-[10px] text-muted-foreground sm:flex">
              <span className="size-1.5 rounded-full bg-emerald-500" />
              {me.status === "active" ? "Active partner" : me.status}
            </span>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={dark ? "Use light theme" : "Use dark theme"}
              onClick={theme}
            >
              {dark ? <Sun className="size-4" /> : <Moon className="size-4" />}
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label="Account menu"
                >
                  <MoreHorizontal className="size-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onClick={() => go("settings")}>
                  <Settings2 />
                  Account settings
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  disabled={me.read_only}
                  onClick={() => {
                    void signOut()
                      .then(() => setMe(null))
                      .catch((e) => toast.error(errorText(e)));
                  }}
                >
                  <LogOut />
                  Sign out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>
        <main className="mx-auto max-w-[1320px] px-5 py-8 sm:px-8 lg:py-10">
          {!me.requires_terms &&
            ["tasks", "dashboard", "links", "assets", "referrals"].includes(
              page,
            ) && (
              <nav
                aria-label="Promotion tools"
                className="mb-7 flex flex-wrap gap-2 border-b pb-4"
              >
                {navigation
                  .filter(
                    (n) =>
                      [
                        earningPage,
                        "links",
                        "assets",
                        "referrals",
                        "dashboard",
                      ].includes(n.page) && program.pages.includes(n.page),
                  )
                  .map((n) => (
                    <Button
                      key={n.page}
                      variant={page === n.page ? "secondary" : "ghost"}
                      size="sm"
                      onClick={() => go(n.page)}
                    >
                      <n.icon className="size-3.5" />
                      {n.page === earningPage ? "For you" : n.label}
                    </Button>
                  ))}
              </nav>
            )}
          {me.requires_terms && page !== "help" ? (
            <TermsGate
              program={program}
              readOnly={me.read_only}
              onAccepted={reload}
            />
          ) : (
            <>
              {error && (
                <div
                  role="alert"
                  className="mb-6 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-destructive/20 bg-background p-4"
                >
                  <p className="text-xs text-destructive">{error}</p>
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() =>
                      void reload()
                        .then(() => setError(""))
                        .catch((e) => setError(errorText(e)))
                    }
                  >
                    Try again
                  </Button>
                </div>
              )}
              {me.read_only && (
                <Notice>
                  You're previewing this partner's workspace. Actions are
                  read-only.
                </Notice>
              )}
              {me.status !== "active" && (
                <Notice>
                  Your partnership is {me.status}. Opportunities become
                  available once your membership is active.
                </Notice>
              )}
              {["mine", "proposals"].includes(page) && (
                <div className="mb-6 flex gap-2">
                  <Button
                    variant={page === "proposals" ? "secondary" : "ghost"}
                    onClick={() => go("proposals")}
                  >
                    Personal proposals
                  </Button>
                  {program.pages.includes("tasks") && (
                    <Button
                      variant={page === "mine" ? "secondary" : "ghost"}
                      onClick={() => go("mine")}
                    >
                      Task assignments
                    </Button>
                  )}
                </div>
              )}
              {page === "proposals" && (
                <PartnerProposals me={me} program={program} />
              )}
              {(page === "tasks" || page === "mine") && (
                <>
                  <div className="mb-8 flex flex-wrap items-end justify-between gap-4">
                    <div>
                      <p className="mb-2 text-xs text-muted-foreground">
                        {page === "mine"
                          ? "Make it happen"
                          : "Your next opportunity"}
                      </p>
                      <h1 className="text-[28px] font-semibold leading-tight tracking-[-.035em] sm:text-[32px]">
                        {page === "mine"
                          ? "Good work starts here."
                          : `Let's grow together, ${firstName}.`}
                      </h1>
                      <p className="mt-3 max-w-xl text-sm leading-6 text-muted-foreground">
                        {page === "mine"
                          ? "Keep track of your offers, work in progress, and submitted tasks."
                          : "Put your audience to work. Find opportunities that fit your channels, create something great, and get rewarded."}
                      </p>
                    </div>
                    {program.pages.includes("links") && (
                      <Button variant="outline" onClick={() => go("links")}>
                        <Link2 className="size-4" />
                        Your referral link
                        <ArrowUpRight className="size-3.5" />
                      </Button>
                    )}
                  </div>
                  <div className="mb-9 grid grid-cols-2 gap-3 xl:grid-cols-4">
                    <Metric
                      label="Available balance"
                      value={money(me.stats.balance_cents, currency)}
                      detail="Approved rewards"
                      icon={Wallet}
                    />
                    <Metric
                      label="In progress"
                      value={String(outstanding.length)}
                      detail="Your active opportunities"
                      icon={ListChecks}
                    />
                    <Metric
                      label="Total paid"
                      value={money(me.stats.paid_cents, currency)}
                      detail="Paid to your account"
                      icon={ArrowDownLeft}
                    />
                    <Metric
                      label="Your channels"
                      value={String(audiences.length)}
                      detail={`${verified} verified website${verified === 1 ? "" : "s"}`}
                      icon={Globe2}
                    />
                  </div>
                  <div className="mb-5 flex flex-wrap items-center justify-between gap-3">
                    <Tabs
                      value={page === "mine" ? "mine" : view}
                      onValueChange={(v) => {
                        if (v === "mine") go("mine");
                        else {
                          setView(v);
                          if (page === "mine") go("tasks");
                        }
                      }}
                    >
                      <TabsList>
                        <TabsTrigger value="recommended">For you</TabsTrigger>
                        <TabsTrigger value="all">Explore all</TabsTrigger>
                        <TabsTrigger value="mine">
                          My tasks
                          {outstanding.length > 0 && (
                            <span className="ml-1 rounded bg-background px-1 text-[10px]">
                              {outstanding.length}
                            </span>
                          )}
                        </TabsTrigger>
                      </TabsList>
                    </Tabs>
                    <div className="flex w-full gap-2 sm:w-auto">
                      <div className="relative flex-1 sm:w-52">
                        <Search className="absolute left-3 top-2.5 size-3.5 text-muted-foreground" />
                        <Input
                          className="h-9 pl-9 text-xs"
                          placeholder="Search opportunities"
                          aria-label="Search opportunities"
                          value={query}
                          onChange={(e) => setQuery(e.target.value)}
                        />
                      </div>
                      <Select value={channel} onValueChange={setChannel}>
                        <SelectTrigger
                          className="h-9 w-[142px] text-xs"
                          aria-label="Filter channels"
                        >
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="all">All channels</SelectItem>
                          {supported.map((k) => (
                            <SelectItem key={k} value={k}>
                              {names[k]}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                  </div>
                  {visible.length === 0 ? (
                    <Empty
                      title={
                        page === "mine"
                          ? "Your next chapter starts here"
                          : "Nothing here just yet"
                      }
                      action={
                        <Button
                          variant="outline"
                          onClick={() =>
                            page === "mine" ? go("tasks") : openProfiles()
                          }
                        >
                          {page === "mine"
                            ? "Explore opportunities"
                            : "Add a channel"}
                          <ArrowRight />
                        </Button>
                      }
                    >
                      {page === "mine"
                        ? "Accept an opportunity to start building something with us."
                        : "Add your websites and social channels to discover opportunities that suit your audience."}
                    </Empty>
                  ) : (
                    <div className="grid gap-4 md:grid-cols-2">
                      {visible.map((t) => (
                        <Opportunity
                          key={t.id}
                          task={t}
                          submission={submissions.get(t.id)}
                          audiences={audiences}
                          currency={currency}
                          onOpen={() => openTask(t)}
                        />
                      ))}
                    </div>
                  )}
                  <div className="mt-7 flex items-center justify-center gap-2 text-[11px] text-muted-foreground">
                    <ShieldCheck className="size-3.5" />
                    Your opportunities are private to{" "}
                    {program.title.replace(/ partners$/i, "")}.
                  </div>
                </>
              )}
              {page === "profiles" && (
                <Profiles
                  audiences={audiences}
                  onAdd={openProfiles}
                  onVerify={setVerifying}
                  reload={loadWork}
                  canWrite={!me.read_only && me.status === "active"}
                />
              )}
              {page === "dashboard" && (
                <Overview
                  me={me}
                  currency={currency}
                  onTasks={() => go("tasks")}
                />
              )}
              {page === "help" && <PortalHelp program={program} me={me} />}
              {![
                "tasks",
                "mine",
                "profiles",
                "dashboard",
                "help",
                "proposals",
              ].includes(page) && (
                <Resources
                  key={page}
                  page={page}
                  me={me}
                  program={program}
                  reload={reload}
                />
              )}
            </>
          )}
        </main>
        <footer className="mx-auto flex max-w-[1320px] flex-wrap items-center justify-between gap-5 border-t px-5 py-6 text-[11px] text-muted-foreground sm:px-8">
          <div className="space-y-3">
            <span>{program.title}</span>
            <BrandLinks
              links={program.footer_links}
              className="flex flex-wrap gap-4"
            />
          </div>
          <div className="flex flex-wrap items-center gap-4">
            {program.terms_url && (
              <a
                href={program.terms_url}
                target="_blank"
                rel="noopener noreferrer"
              >
                Program terms
              </a>
            )}
            <button onClick={() => go("help")}>Help & support</button>
            {program.powered_by && (
              <a
                href="https://outflare.ai"
                target="_blank"
                rel="noopener noreferrer"
                className="flex items-center gap-1.5"
              >
                Powered by
                <OutflareMark className="size-3.5" />
                Outflare
              </a>
            )}
          </div>
        </footer>
      </div>
      <OpportunityDetail
        task={selected}
        submission={selected ? submissions.get(selected.id) : undefined}
        audiences={audiences}
        me={me}
        onClose={() => setSelected(null)}
        onSaved={async () => {
          await reload();
          setSelected(null);
          go("mine", false);
        }}
      />
      <AddProfile
        open={adding}
        onClose={() => setAdding(false)}
        onSaved={async () => {
          await loadWork();
          setAdding(false);
        }}
      />
      <VerifyProfile
        audience={verifying}
        onClose={() => setVerifying(null)}
        onRefresh={async () => {
          const refreshed = await loadWork();
          const verified = refreshed.find(
            (a) => a.id === verifying?.id,
          )?.verified_at;
          setVerifying(null);
          if (verified) {
            go("tasks");
            toast.success(
              "Website verified. Your private proposals are unlocked.",
            );
          }
        }}
      />
    </div>
  );
}
function Notice({ children }: { children: ReactNode }) {
  return (
    <div
      role="status"
      className="mb-6 rounded-lg border border-amber-500/20 bg-amber-500/5 px-4 py-3 text-xs text-amber-700 dark:text-amber-400"
    >
      {children}
    </div>
  );
}
function Metric({
  label,
  value,
  detail,
  icon: Icon,
}: {
  label: string;
  value: string;
  detail: string;
  icon: typeof Wallet;
}) {
  return (
    <Card className="gap-0 rounded-lg px-5 py-4 shadow-none">
      <div className="flex items-center justify-between">
        <span className="text-xs text-muted-foreground">{label}</span>
        <Icon className="size-3.5 text-muted-foreground" strokeWidth={1.6} />
      </div>
      <p className="my-3 text-[26px] font-semibold leading-none tracking-tight tabular-nums">
        {value}
      </p>
      <span className="text-[10px] text-muted-foreground">{detail}</span>
    </Card>
  );
}
function Opportunity({
  task,
  submission,
  audiences,
  currency,
  onOpen,
}: {
  task: Task;
  submission?: Submission;
  audiences: Audience[];
  currency: string;
  onOpen: () => void;
}) {
  const c = task.config,
    matched = needsAsset(c) ? audiences.find((a) => matches(c, a)) : undefined;
  return (
    <Card
      className={cn(
        "group gap-0 overflow-hidden rounded-xl py-0 shadow-none transition-colors hover:border-foreground/25",
        c.target_host && "border-emerald-500/25",
      )}
    >
      {c.target_host && (
        <div className="flex items-center gap-2 border-b border-emerald-500/15 bg-emerald-500/5 px-5 py-2.5 text-[10px] font-medium text-emerald-700 dark:text-emerald-400">
          <Sparkles className="size-3.5" />
          Selected for your website
          <span className="ml-auto flex items-center gap-1">
            <ShieldCheck className="size-3" />
            Ownership verified
          </span>
        </div>
      )}
      <div className="flex flex-1 flex-col p-5">
        <div className="mb-5 flex items-start justify-between gap-4">
          <div className="flex items-center gap-3">
            <ChannelIcon kind={c.asset_kind || c.category} />
            <div>
              <p className="text-xs font-medium">
                {names[c.category] || "Opportunity"}
              </p>
              <p className="mt-0.5 text-[10px] text-muted-foreground">
                {c.claim_mode === "application"
                  ? "By application"
                  : "Ready to start"}
              </p>
            </div>
          </div>
          <div className="text-right">
            <p className="text-[17px] font-semibold tracking-tight">
              {submission &&
              c.reward_kind === "cash" &&
              submission.status !== "applied"
                ? money(submission.amount_cents, currency)
                : reward(c, currency)}
            </p>
            <p className="mt-0.5 text-[10px] text-muted-foreground">
              {c.reward_kind === "free"
                ? "No reward attached"
                : c.reward_kind === "action"
                  ? "Product reward"
                  : c.pricing === "custom"
                    ? "Priced for your audience"
                    : "Per approved placement"}
            </p>
          </div>
        </div>
        <h2 className="mb-2 text-[17px] font-semibold leading-6 tracking-[-.015em]">
          {c.title}
        </h2>
        <p className="mb-5 line-clamp-2 min-h-10 text-xs leading-5 text-muted-foreground">
          {c.description}
        </p>
        <div className="mb-5 flex flex-wrap items-center gap-2">
          {submission ? (
            <Status value={submission.status} />
          ) : (
            <>
              <Badge
                variant="secondary"
                className="rounded-md text-[10px] font-normal"
              >
                {c.asset_kind ? names[c.asset_kind] : "Open to everyone"}
              </Badge>
              <Badge
                variant="outline"
                className="rounded-md text-[10px] font-normal text-muted-foreground"
              >
                {c.verification === "human"
                  ? "Reviewed by the team"
                  : c.verification === "auto_hold"
                    ? `${c.hold_days}-day review hold`
                    : "Automatic link check"}
              </Badge>
            </>
          )}
        </div>
        <div className="mt-auto flex flex-wrap items-center justify-between gap-3 border-t pt-4">
          <span className="flex min-w-0 items-center gap-1.5 text-[10px] text-muted-foreground">
            {matched ? (
              <>
                <CheckCircle2 className="size-3.5 text-emerald-600" />
                <span className="max-w-[190px] truncate">{matched.host}</span>
              </>
            ) : (
              <>
                <Users className="size-3.5" />
                {needsAsset(c)
                  ? "Add a matching channel"
                  : "No audience required"}
              </>
            )}
          </span>
          <Button
            size="sm"
            variant={
              c.target_host || submission?.status === "offered"
                ? "default"
                : "outline"
            }
            className="h-8 text-xs"
            onClick={onOpen}
          >
            {submission?.status === "accepted"
              ? "Continue task"
              : submission?.status === "offered"
                ? "View offer"
                : submission
                  ? "View details"
                  : "View opportunity"}
            <ArrowUpRight className="size-3.5" />
          </Button>
        </div>
      </div>
    </Card>
  );
}
function OpportunityDetail({
  task,
  submission,
  audiences,
  me,
  onClose,
  onSaved,
}: {
  task: Task | null;
  submission?: Submission;
  audiences: Audience[];
  me: Me;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [asset, setAsset] = useState(""),
    [note, setNote] = useState(""),
    [evidence, setEvidence] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    setAsset(
      submission?.audience_id ||
        audiences.find((a) => task && matches(task.config, a))?.id ||
        "",
    );
    setNote("");
    setEvidence("");
    setError("");
  }, [task?.id, submission?.id]); // eslint-disable-line react-hooks/exhaustive-deps
  const c = task?.config;
  async function act(action: string) {
    if (!task) return;
    setBusy(true);
    setError("");
    try {
      await request(`tasks/${task.id}`, "POST", {
        action,
        audience_id: submission?.audience_id || asset,
        note,
        ...(action === "submit" ? { evidence_url: evidence } : {}),
      });
      toast.success(
        action === "submit"
          ? "Your work is ready for review"
          : action === "accept"
            ? "Offer accepted. You're ready to start."
            : c?.claim_mode === "application"
              ? "Application sent to the team"
              : "Task accepted. Let's make something great.",
      );
      await onSaved();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  const candidates = c ? audiences.filter((a) => matches(c, a)) : [];
  const canWrite = !me.read_only && me.status === "active";
  return (
    <Sheet
      open={!!task}
      onOpenChange={(open) => {
        if (!open) onClose();
      }}
    >
      <SheetContent className="flex w-full flex-col gap-0 overflow-y-auto sm:max-w-[560px]">
        {task && c && (
          <>
            <SheetHeader className="border-b p-6">
              <div className="mb-4 flex items-center gap-3">
                <ChannelIcon kind={c.asset_kind || c.category} />
                <Badge variant="outline" className="text-[10px] font-normal">
                  {names[c.category] || "Opportunity"}
                </Badge>
                {submission && <Status value={submission.status} />}
              </div>
              <SheetTitle className="pr-4 text-2xl leading-8 tracking-tight">
                {c.title}
              </SheetTitle>
              <SheetDescription className="mt-2 text-sm">
                {c.target_host
                  ? "An offer prepared for your verified website."
                  : "Everything you need to get started."}
              </SheetDescription>
            </SheetHeader>
            <div className="space-y-7 p-6">
              <div className="flex items-center justify-between rounded-lg border bg-muted/30 p-4">
                <div>
                  <p className="text-xs text-muted-foreground">
                    {submission && submission.status !== "applied"
                      ? "Your agreed reward"
                      : "Your reward"}
                  </p>
                  <p className="mt-1 text-2xl font-semibold tracking-tight">
                    {submission &&
                    c.reward_kind === "cash" &&
                    submission.status !== "applied"
                      ? money(submission.amount_cents, me.currency)
                      : reward(c, me.currency)}
                  </p>
                </div>
                <div className="rounded-full bg-background p-3">
                  <Gift className="size-5 text-muted-foreground" />
                </div>
              </div>
              <section>
                <h3 className="mb-3 text-sm font-semibold">The brief</h3>
                <p className="whitespace-pre-wrap text-sm leading-7 text-muted-foreground">
                  {c.description}
                </p>
              </section>
              <div className="grid grid-cols-2 gap-4 border-y py-4 text-xs">
                <div>
                  <p className="mb-1.5 text-muted-foreground">Review</p>
                  <p className="font-medium">
                    {c.verification === "human"
                      ? "Reviewed by the team"
                      : c.verification === "auto_hold"
                        ? `Link check + ${c.hold_days}-day hold`
                        : "Automatic link check"}
                  </p>
                </div>
                <div>
                  <p className="mb-1.5 text-muted-foreground">
                    Getting started
                  </p>
                  <p className="font-medium">
                    {c.claim_mode === "application"
                      ? "Apply for an offer"
                      : "Accept and get started"}
                  </p>
                </div>
              </div>
              {submission?.note && (
                <div className="rounded-lg border p-4">
                  <p className="mb-2 text-xs font-medium">Latest note</p>
                  <p className="text-xs leading-6 text-muted-foreground">
                    {submission.note}
                  </p>
                </div>
              )}
              {submission?.hold_until && (
                <Notice>
                  Your work is on a review hold until{" "}
                  {new Date(submission.hold_until).toLocaleDateString()}.
                </Notice>
              )}
              {submission?.evidence_url && (
                <a
                  className="flex items-center gap-2 text-xs underline underline-offset-4"
                  href={submission.evidence_url}
                  target="_blank"
                  rel="noreferrer"
                >
                  View submitted work
                  <ExternalLink className="size-3" />
                </a>
              )}
              {!submission && canWrite && task.status === "active" && (
                <>
                  {needsAsset(c) && (
                    <Field label="Publish with">
                      <Pick
                        label="Choose a profile"
                        value={asset}
                        onChange={setAsset}
                        options={[
                          { value: "", label: "Choose a channel" },
                          ...candidates.map((a) => ({
                            value: a.id,
                            label: a.url,
                          })),
                        ]}
                      />
                    </Field>
                  )}
                  {needsAsset(c) && !candidates.length && (
                    <p className="rounded-lg bg-muted p-3 text-xs leading-6 text-muted-foreground">
                      Add a matching profile under Profiles & reach to
                      participate in this opportunity.
                    </p>
                  )}
                  <Field
                    label="Anything you'd like the team to know?"
                    hint="Optional. Tell us how you'd approach this opportunity."
                  >
                    <Textarea
                      value={note}
                      onChange={(e) => setNote(e.target.value)}
                      maxLength={2000}
                      placeholder="A little context goes a long way…"
                      rows={3}
                    />
                  </Field>
                </>
              )}
              {submission?.status === "accepted" && canWrite && (
                <>
                  <Field
                    label={
                      c.verification === "human"
                        ? "Link to your work (optional)"
                        : "Link to your published work"
                    }
                  >
                    <Input
                      type="url"
                      value={evidence}
                      onChange={(e) => setEvidence(e.target.value)}
                      placeholder="https://example.com/your-work"
                      aria-label="Published work URL"
                    />
                  </Field>
                  <Field
                    label="Completion note"
                    hint={
                      c.verification === "human"
                        ? "Add a link or describe how you completed the task."
                        : "Optional context for the team."
                    }
                  >
                    <Textarea
                      value={note}
                      onChange={(e) => setNote(e.target.value)}
                      maxLength={2000}
                      rows={4}
                      placeholder="Here's what I created…"
                    />
                  </Field>
                </>
              )}
              {error && (
                <p role="alert" className="text-xs leading-6 text-destructive">
                  {error}
                </p>
              )}
            </div>
            <div className="mt-auto border-t bg-background p-6">
              <div className="mb-4 flex items-start gap-2 text-[11px] leading-5 text-muted-foreground">
                <ShieldCheck className="mt-0.5 size-3.5 shrink-0" />
                <span>
                  {c.reward_kind === "free"
                    ? "This opportunity is voluntary and has no reward attached."
                    : "Your reward is recorded after approval. Referral commissions continue under your existing program rules."}
                </span>
              </div>
              {canWrite &&
              (!submission ||
                ["accepted", "offered"].includes(submission.status)) ? (
                <Button
                  className="w-full"
                  disabled={busy || (!submission && needsAsset(c) && !asset)}
                  onClick={() =>
                    void act(
                      submission?.status === "accepted"
                        ? "submit"
                        : submission?.status === "offered"
                          ? "accept"
                          : "apply",
                    )
                  }
                >
                  {busy ? <Loader2 className="size-4 animate-spin" /> : null}
                  {submission?.status === "accepted"
                    ? "Submit for review"
                    : submission?.status === "offered"
                      ? "Accept this offer"
                      : c.claim_mode === "application"
                        ? "Apply for this opportunity"
                        : "Accept this opportunity"}
                  <ArrowRight className="size-4" />
                </Button>
              ) : (
                <div className="flex items-center justify-center gap-2 rounded-lg bg-muted py-3 text-xs font-medium">
                  <CheckCircle2 className="size-4" />
                  {submission?.status === "applied"
                    ? "Your application is with the team"
                    : submission?.status === "submitted"
                      ? "Your work is being reviewed"
                      : submission?.status === "approved"
                        ? "Completed. Your reward is in Earnings."
                        : me.read_only
                          ? "Read-only preview"
                          : "No action needed"}
                </div>
              )}
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}

function Profiles({
  audiences,
  onAdd,
  onVerify,
  reload,
  canWrite,
}: {
  audiences: Audience[];
  onAdd: () => void;
  onVerify: (a: Audience) => void;
  reload: () => Promise<unknown>;
  canWrite: boolean;
}) {
  const [busy, setBusy] = useState("");
  async function action(a: Audience, action: string) {
    setBusy(a.id);
    try {
      await request(`audiences/${a.id}`, "POST", { action });
      toast.success(
        action === "remove"
          ? "Profile removed"
          : "Reach refresh queued. Check back shortly.",
      );
      await reload();
    } catch (e) {
      toast.error(errorText(e));
    } finally {
      setBusy("");
    }
  }
  return (
    <>
      <div className="mb-8 flex flex-wrap items-end justify-between gap-4">
        <div>
          <p className="mb-2 text-xs text-muted-foreground">
            Your audience, all in one place
          </p>
          <h1 className="text-[30px] font-semibold tracking-tight">
            Profiles & reach
          </h1>
          <p className="mt-3 max-w-xl text-sm leading-6 text-muted-foreground">
            Add your channels once. We'll help you find opportunities that fit
            the audience you've built.
          </p>
        </div>
        <div className="flex gap-2">
          <Button
            variant="outline"
            onClick={() =>
              void reload().catch((e) => toast.error(errorText(e)))
            }
          >
            Refresh status
          </Button>
          <Button onClick={onAdd} disabled={!canWrite}>
            <Plus className="size-4" />
            Add a channel
          </Button>
        </div>
      </div>
      <div className="mb-7 flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border bg-background px-5 py-4 text-xs">
        <span className="flex items-center gap-2">
          <Globe2 className="size-4 text-muted-foreground" />
          <b className="font-semibold">{audiences.length}</b>channels connected
        </span>
        <span className="flex items-center gap-2">
          <ShieldCheck className="size-4 text-emerald-600" />
          <b className="font-semibold">
            {audiences.filter((a) => a.verified_at).length}
          </b>
          verified websites
        </span>
        <span className="ml-auto text-[11px] text-muted-foreground">
          Only visible to you and your brand's team
        </span>
      </div>
      {!audiences.length ? (
        <Empty
          title="Your audience belongs here"
          action={
            <Button onClick={onAdd} disabled={!canWrite}>
              <Plus />
              Add your first channel
            </Button>
          }
        >
          Connect a website, social profile, newsletter, or community to unlock
          relevant opportunities.
        </Empty>
      ) : (
        <div className="grid gap-4 md:grid-cols-2">
          {audiences.map((a) => (
            <Card key={a.id} className="gap-0 p-5 shadow-none">
              <div className="flex items-start justify-between">
                <div className="flex items-center gap-3">
                  <ChannelIcon kind={a.kind} />
                  <div>
                    <p className="text-sm font-medium">
                      {names[a.kind] || a.kind}
                    </p>
                    <a
                      className="mt-0.5 flex max-w-56 items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground"
                      href={a.url}
                      target="_blank"
                      rel="noreferrer"
                    >
                      <span className="truncate">
                        {a.url.replace(/^https:\/\//, "")}
                      </span>
                      <ArrowUpRight className="size-3 shrink-0" />
                    </a>
                  </div>
                </div>
                <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Actions for ${a.host}`}
                      disabled={!canWrite || busy === a.id}
                    >
                      <MoreHorizontal className="size-4" />
                    </Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem onClick={() => void action(a, "refresh")}>
                      Refresh reach
                    </DropdownMenuItem>
                    {a.kind === "website" && (
                      <DropdownMenuItem onClick={() => onVerify(a)}>
                        Verify ownership
                      </DropdownMenuItem>
                    )}
                    <DropdownMenuSeparator />
                    <DropdownMenuItem
                      variant="destructive"
                      onClick={() => void action(a, "remove")}
                    >
                      Remove channel
                    </DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>
              <div className="my-6 flex items-end justify-between">
                <div>
                  <p className="text-2xl font-semibold tracking-tight tabular-nums">
                    {new Intl.NumberFormat(undefined, {
                      notation: "compact",
                      maximumFractionDigits: 1,
                    }).format(a.reach)}
                  </p>
                  <p className="mt-1 text-[11px] text-muted-foreground">
                    {a.kind === "website"
                      ? "Reported audience"
                      : "Followers / subscribers"}
                  </p>
                </div>
                <Badge variant="secondary" className="font-normal text-[10px]">
                  {a.reach_source === "scraped"
                    ? "Public page data"
                    : "Self-reported"}
                </Badge>
              </div>
              {a.topics && (
                <p className="mb-5 line-clamp-1 text-xs text-muted-foreground">
                  {a.topics}
                </p>
              )}
              <div className="mt-auto flex flex-wrap items-center justify-between gap-2 border-t pt-4">
                <span
                  className={cn(
                    "flex items-center gap-1.5 text-[11px]",
                    a.verified_at
                      ? "text-emerald-700 dark:text-emerald-400"
                      : "text-muted-foreground",
                  )}
                >
                  {a.verified_at ? (
                    <>
                      <BadgeCheck className="size-3.5" />
                      Website verified
                    </>
                  ) : (
                    <>
                      <ShieldCheck className="size-3.5" />
                      {a.kind === "website"
                        ? "Ownership not verified"
                        : "Ownership self-reported"}
                    </>
                  )}
                </span>
                {a.kind === "website" && !a.verified_at && (
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-8 text-xs"
                    disabled={!canWrite}
                    onClick={() => onVerify(a)}
                  >
                    Verify website
                    <ArrowRight className="size-3.5" />
                  </Button>
                )}
                {a.kind !== "website" && (
                  <span className="text-[10px] capitalize text-muted-foreground">
                    {a.role}
                  </span>
                )}
              </div>
              {a.last_error && (
                <p className="mt-3 rounded-md bg-muted p-3 text-[11px] leading-5 text-muted-foreground">
                  {a.last_error}
                </p>
              )}
            </Card>
          ))}
        </div>
      )}
      <div className="mt-7 flex items-start gap-3 rounded-lg border border-dashed p-5">
        <CircleHelp className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
        <div>
          <h3 className="text-xs font-medium">
            A little verification goes a long way
          </h3>
          <p className="mt-1 text-xs leading-6 text-muted-foreground">
            Verifying your website unlocks private proposals prepared for your
            domain. Public follower counts help describe your reach; they don't
            prove account ownership.
          </p>
        </div>
      </div>
    </>
  );
}
function AddProfile({
  open,
  onClose,
  onSaved,
}: {
  open: boolean;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const [url, setURL] = useState(""),
    [kind, setKind] = useState("website"),
    [role, setRole] = useState("owner"),
    [topics, setTopics] = useState(""),
    [reach, setReach] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    if (open) {
      setURL(invitedWebsite() ? "https://" + invitedWebsite() : "");
      setError("");
    }
  }, [open]);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await request("audiences", "POST", {
        url,
        kind,
        role,
        topics,
        reach: Number(reach || 0),
      });
      toast.success("Channel added. Your next opportunity is waiting.");
      await onSaved();
      setURL("");
      setTopics("");
      setReach("");
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        if (!v) onClose();
      }}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-[480px]">
        <DialogHeader>
          <DialogTitle className="text-xl tracking-tight">
            Add your channel
          </DialogTitle>
          <DialogDescription>
            Your website, audience, or community could be the perfect fit for
            the next opportunity.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="space-y-5 pt-3">
          <Field label="Where can people find you?">
            <Input
              type="url"
              required
              maxLength={2000}
              value={url}
              onChange={(e) => setURL(e.target.value)}
              placeholder="https://example.com"
            />
          </Field>
          <div className="grid grid-cols-2 gap-4">
            <Field label="Channel type">
              <Pick
                label="Channel type"
                value={kind}
                onChange={setKind}
                options={supported.map((k) => ({ value: k, label: names[k] }))}
              />
            </Field>
            <Field label="Your role">
              <Pick
                label="Your role"
                value={role}
                onChange={setRole}
                options={["owner", "admin", "moderator", "contributor"].map(
                  (k) => ({ value: k, label: k[0].toUpperCase() + k.slice(1) }),
                )}
              />
            </Field>
          </div>
          <Field label="What do you talk about?">
            <Input
              maxLength={500}
              value={topics}
              onChange={(e) => setTopics(e.target.value)}
              placeholder="e.g. SaaS, productivity, independent creators"
            />
          </Field>
          <Field
            label="Audience size (optional)"
            hint="An estimate is fine. Figures you enter are labeled self-reported."
          >
            <Input
              type="number"
              min="0"
              max="10000000000"
              value={reach}
              onChange={(e) => setReach(e.target.value)}
              placeholder="e.g. 12000"
            />
          </Field>
          {error && (
            <p role="alert" className="text-xs text-destructive">
              {error}
            </p>
          )}
          <Button className="w-full" disabled={busy}>
            {busy ? (
              <Loader2 className="size-4 animate-spin" />
            ) : (
              <Plus className="size-4" />
            )}
            Add channel
          </Button>
        </form>
      </DialogContent>
    </Dialog>
  );
}
function VerifyProfile({
  audience: a,
  onClose,
  onRefresh,
}: {
  audience: Audience | null;
  onClose: () => void;
  onRefresh: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false),
    [queued, setQueued] = useState(false),
    [error, setError] = useState("");
  useEffect(() => {
    setQueued(false);
    setError("");
  }, [a?.id]);
  return (
    <Dialog
      open={!!a}
      onOpenChange={(v) => {
        if (!v) onClose();
      }}
    >
      <DialogContent className="max-h-[90dvh] overflow-y-auto sm:max-w-[540px]">
        {a && (
          <>
            <DialogHeader>
              <div className="mb-3 flex size-11 items-center justify-center rounded-xl border bg-muted/40">
                <ShieldCheck className="size-5" />
              </div>
              <DialogTitle className="text-xl tracking-tight">
                Make it officially yours
              </DialogTitle>
              <DialogDescription>
                Verify {a.host} to unlock proposals prepared for your website.
              </DialogDescription>
            </DialogHeader>
            <div className="space-y-6 py-3">
              <div className="flex gap-3">
                <Step n={1} />
                <div className="pt-1">
                  <h3 className="text-sm font-medium">
                    Open your domain's DNS settings
                  </h3>
                  <p className="mt-1 text-xs leading-6 text-muted-foreground">
                    Use the provider where you manage this domain, then add a
                    TXT record.
                  </p>
                </div>
              </div>
              <div className="flex gap-3">
                <Step n={2} />
                <div className="min-w-0 flex-1 pt-1">
                  <h3 className="mb-3 text-sm font-medium">
                    Add your verification record
                  </h3>
                  <div className="space-y-4 rounded-lg border bg-muted/20 p-4">
                    {[
                      ["Type", "TXT"],
                      ["Name", a.dns_name],
                      ["Value", "outflare-partner=" + a.challenge],
                    ].map(([label, value]) => (
                      <div key={label}>
                        <div className="mb-1.5 flex items-center justify-between">
                          <span className="text-[10px] font-medium text-muted-foreground">
                            {label}
                          </span>
                          <Copy
                            value={value}
                            label={`Copy ${label.toLowerCase()}`}
                            compact
                          />
                        </div>
                        <p className="break-all font-mono text-[11px] leading-5">
                          {value}
                        </p>
                      </div>
                    ))}
                  </div>
                </div>
              </div>
              <div className="flex gap-3">
                <Step n={3} />
                <div className="pt-1">
                  <h3 className="text-sm font-medium">Come back and verify</h3>
                  <p className="mt-1 text-xs leading-6 text-muted-foreground">
                    DNS changes can take a little time to appear. Your
                    verification check runs in the background.
                  </p>
                </div>
              </div>
              {queued && (
                <div
                  role="status"
                  className="rounded-lg border border-emerald-500/20 bg-emerald-500/5 p-4 text-xs leading-6 text-emerald-700 dark:text-emerald-400"
                >
                  Verification queued. Give it a moment, then refresh your
                  profiles to check the result.
                </div>
              )}
              {error && (
                <p role="alert" className="text-xs text-destructive">
                  {error}
                </p>
              )}
            </div>
            <div className="flex gap-2 border-t pt-4">
              <Button
                variant="outline"
                className="flex-1"
                onClick={() => void onRefresh()}
              >
                Refresh profiles
              </Button>
              <Button
                className="flex-1"
                disabled={busy}
                onClick={async () => {
                  setBusy(true);
                  try {
                    await request(`audiences/${a.id}`, "POST", {
                      action: "verify",
                    });
                    setQueued(true);
                  } catch (e) {
                    setError(errorText(e));
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                {busy ? (
                  <Loader2 className="size-4 animate-spin" />
                ) : (
                  <ShieldCheck className="size-4" />
                )}
                Check verification
              </Button>
            </div>
          </>
        )}
      </DialogContent>
    </Dialog>
  );
}
function Step({ n }: { n: number }) {
  return (
    <span className="flex size-7 shrink-0 items-center justify-center rounded-full border text-[11px] font-medium">
      {n}
    </span>
  );
}
function Overview({
  me,
  currency,
  onTasks,
}: {
  me: Me;
  currency: string;
  onTasks: () => void;
}) {
  return (
    <>
      <div className="mb-8">
        <p className="mb-2 text-xs text-muted-foreground">
          Your impact, at a glance
        </p>
        <h1 className="text-[30px] font-semibold tracking-tight">
          Welcome back, {(me.name || "partner").split(" ")[0]}.
        </h1>
        <p className="mt-3 text-sm text-muted-foreground">
          A home for the work you create and the people you bring along.
        </p>
      </div>
      <div className="mb-6 grid grid-cols-2 gap-4 xl:grid-cols-4">
        <Metric
          label="Available balance"
          value={money(me.stats.balance_cents, currency)}
          detail="Approved rewards"
          icon={Wallet}
        />
        <Metric
          label="Referral clicks"
          value={me.stats.clicks.toLocaleString()}
          detail="Visits through your link"
          icon={ArrowUpRight}
        />
        <Metric
          label="Referred customers"
          value={me.stats.customers.toLocaleString()}
          detail={`${me.stats.referrals} total referrals`}
          icon={Users}
        />
        <Metric
          label="Total paid"
          value={money(me.stats.paid_cents, currency)}
          detail="Lifetime payouts"
          icon={CheckCircle2}
        />
      </div>
      <Card className="gap-4 p-6 shadow-none">
        <div className="flex items-center gap-3">
          <ChannelIcon kind="website" />
          <h2 className="text-base font-semibold">
            Good things start with a conversation.
          </h2>
        </div>
        <p className="text-sm text-muted-foreground">
          Share your link, introduce someone to{" "}
          {me.program.title.replace(/ partners$/i, "")}, or find your next
          opportunity.
        </p>
        <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border bg-muted/20 p-3">
          <span className="min-w-0 break-all font-mono text-xs">{me.link}</span>
          <Copy value={me.link} label="Copy link" />
        </div>
        {me.program.pages.includes("tasks") && (
          <Button className="self-start" onClick={onTasks}>
            Explore opportunities
            <ArrowRight />
          </Button>
        )}
      </Card>
    </>
  );
}

type Row = Record<string, unknown>;
function DataGrid({
  rows,
  columns,
  empty,
}: {
  rows: Row[];
  columns: { label: string; value: (r: Row) => ReactNode }[];
  empty: string;
}) {
  return rows.length ? (
    <div className="overflow-hidden rounded-lg border bg-background">
      <Table>
        <TableHeader>
          <TableRow>
            {columns.map((c) => (
              <TableHead
                key={c.label}
                className="h-11 px-4 text-[10px] font-medium"
              >
                {c.label}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r, i) => (
            <TableRow key={String(r.id || i)}>
              {columns.map((c) => (
                <TableCell key={c.label} className="px-4 py-3.5 text-xs">
                  {c.value(r)}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  ) : (
    <div className="rounded-lg border border-dashed px-6 py-12 text-center text-sm text-muted-foreground">
      {empty}
    </div>
  );
}
function Resources({
  page,
  me,
  program,
  reload,
}: {
  page: Page;
  me: Me;
  program: Program;
  reload: () => Promise<void>;
}) {
  const [rows, setRows] = useState<Row[]>([]),
    [extra, setExtra] = useState<Row[]>([]),
    [loading, setLoading] = useState(true),
    [error, setError] = useState("");
  const [destination, setDestination] = useState(""),
    [sub, setSub] = useState("");
  const [method, setMethod] = useState(me.payout_type || ""),
    [token, setToken] = useState(me.payout_token || ""),
    [details, setDetails] = useState(me.payout_method || ""),
    [postback, setPostback] = useState(me.postback_url || ""),
    [conversions, setConversions] = useState(me.notify.conversions !== false),
    [payoutNotice, setPayoutNotice] = useState(me.notify.payouts !== false),
    [busy, setBusy] = useState(false);
  useEffect(() => {
    setLoading(true);
    setError("");
    (async () => {
      try {
        if (page === "payouts") {
          const [a, b] = await Promise.all([
            request<{ data: Row[] }>("rewards"),
            request<{ data: Row[] }>("payouts"),
          ]);
          setRows(a.data);
          setExtra(b.data);
        } else if (page === "referrals" || page === "assets") {
          setRows((await request<{ data: Row[] }>(page)).data);
        } else if (page === "settings") {
          setExtra((await request<{ data: Row[] }>("postbacks")).data);
        }
      } catch (e) {
        setError(errorText(e));
      } finally {
        setLoading(false);
      }
    })();
  }, [page, program.kind, me]);
  const date = (v: unknown) =>
    v ? new Date(String(v)).toLocaleDateString() : "—";
  const text = (v: unknown) => (v == null ? "—" : String(v));
  let link = me.link;
  const params = new URLSearchParams();
  if (destination.trim()) params.set("to", destination.trim());
  if (sub.trim()) params.set("sub", sub.trim());
  if (params.size) link += "?" + params.toString();
  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await request("me", "PATCH", {
        payout_method: details,
        ...(method ? { payout_type: method } : {}),
        ...(method === "crypto" ? { payout_token: token.trim() || null } : {}),
        postback_url: postback,
        notify: { conversions, payouts: payoutNotice },
      });
      await reload();
      toast.success("Your preferences are saved");
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  const methodNames: Record<string, string> = {
    paypal: "PayPal",
    wise: "Wise",
    bank_transfer: "Bank transfer",
    crypto: "Crypto",
    other: "Other",
  };
  const title =
    navigation.find((n) => n.page === page)?.label || "Your workspace";
  return (
    <>
      <div className="mb-8">
        <p className="mb-2 text-xs text-muted-foreground">
          Your partner workspace
        </p>
        <h1 className="text-[30px] font-semibold tracking-tight">{title}</h1>
        <p className="mt-3 text-sm leading-6 text-muted-foreground">
          {page === "payouts"
            ? "Keep an eye on your rewards, pending reviews, and payouts."
            : page === "links"
              ? "A little link that opens up a lot of possibilities."
              : page === "assets"
                ? "Everything you need to tell your audience about us."
                : page === "settings"
                  ? "Make this partnership work your way."
                  : "The people you've introduced to the product, with their personal details kept private."}
        </p>
      </div>
      {error && (
        <p
          role="alert"
          className="mb-5 rounded-lg border border-destructive/20 p-4 text-xs text-destructive"
        >
          {error}
        </p>
      )}
      {page === "links" && (
        <div className="grid gap-5 xl:grid-cols-[1.5fr_1fr]">
          <Card className="gap-5 p-6 shadow-none">
            <h2 className="text-base font-semibold">Your promotion link</h2>
            <p className="text-xs leading-6 text-muted-foreground">
              Share this with your audience. Eligible referrals earn rewards
              under your program's rules.
            </p>
            <div className="flex items-center gap-3 rounded-lg border bg-muted/30 p-4">
              <Link2 className="size-4 shrink-0 text-muted-foreground" />
              <span className="min-w-0 flex-1 break-all font-mono text-xs">
                {link}
              </span>
              <Copy value={link} label="Copy link" />
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Destination page (optional)">
                {program.landing_pages?.length > 0 && (
                  <select
                    aria-label="Choose a landing page"
                    className="mb-2 h-10 w-full rounded-md border bg-background px-3 text-sm"
                    value={
                      program.landing_pages.some((p) => p.url === destination)
                        ? destination
                        : ""
                    }
                    onChange={(e) => setDestination(e.target.value)}
                  >
                    <option value="">Choose a brand landing page</option>
                    {program.landing_pages.map((p, i) => (
                      <option key={i} value={p.url}>
                        {p.label}
                      </option>
                    ))}
                  </select>
                )}
                <Input
                  placeholder="/pricing"
                  value={destination}
                  onChange={(e) => setDestination(e.target.value)}
                />
              </Field>
              <Field label="Campaign label (optional)">
                <Input
                  placeholder="newsletter-review"
                  value={sub}
                  onChange={(e) => setSub(e.target.value)}
                />
              </Field>
            </div>
            {me.campaign_link && (
              <div className="border-t pt-5">
                <p className="mb-3 text-xs font-medium">Direct link</p>
                <div className="flex items-center gap-2">
                  <p className="min-w-0 flex-1 break-all font-mono text-[11px] text-muted-foreground">
                    {me.campaign_link}
                  </p>
                  <Copy compact value={me.campaign_link} />
                </div>
              </div>
            )}
          </Card>
          <PortalCoupons me={me} program={program} reload={reload} />
        </div>
      )}
      {loading && !["links", "settings"].includes(page) ? (
        <div className="space-y-4">
          <Skeleton className="h-32" />
          <Skeleton className="h-56" />
        </div>
      ) : (
        <>
          {page === "payouts" && (
            <div className="space-y-8">
              <div className="grid gap-4 sm:grid-cols-3">
                <Metric
                  label="Available balance"
                  value={money(me.stats.balance_cents, me.currency)}
                  detail="Approved rewards"
                  icon={Wallet}
                />
                <Metric
                  label="Rewards on hold"
                  value={money(me.stats.pending_cents, me.currency)}
                  detail="Awaiting review or a hold"
                  icon={Clock3}
                />
                <Metric
                  label="Total paid"
                  value={money(me.stats.paid_cents, me.currency)}
                  detail="Lifetime payouts"
                  icon={CheckCircle2}
                />
              </div>
              <Card className="gap-4 p-5 shadow-none">
                <div className="flex flex-wrap items-center justify-between gap-4">
                  <div>
                    <h2 className="text-sm font-semibold">
                      Ready for your next payout?
                    </h2>
                    <p className="mt-2 max-w-xl text-xs leading-6 text-muted-foreground">
                      Request eligible cash from the brand. Minimum:{" "}
                      {money(me.min_payout_cents, me.currency)}. Payment terms
                      and reward holds apply. The brand sends the payment; this
                      portal records its status.
                    </p>
                  </div>
                  <Button
                    disabled={
                      busy ||
                      me.read_only ||
                      me.status !== "active" ||
                      me.stats.balance_cents <= 0 ||
                      me.stats.balance_cents < me.min_payout_cents ||
                      !me.payout_method
                    }
                    onClick={async () => {
                      setBusy(true);
                      setError("");
                      try {
                        await request("payouts", "POST", {});
                        await reload();
                        toast.success(
                          "Payout requested. The brand will process your payment.",
                        );
                      } catch (e) {
                        setError(errorText(e));
                      } finally {
                        setBusy(false);
                      }
                    }}
                  >
                    {busy ? "Requesting…" : "Request payout"}
                  </Button>
                </div>
                {!me.payout_method && (
                  <a className="text-xs underline" href="#settings">
                    Add payout details in Settings first
                  </a>
                )}
                {program.payout_terms && (
                  <p className="whitespace-pre-wrap border-t pt-4 text-xs leading-6 text-muted-foreground">
                    {program.payout_terms}
                  </p>
                )}
              </Card>
              <section>
                <h2 className="mb-4 text-sm font-semibold">Your rewards</h2>
                <DataGrid
                  rows={rows}
                  empty="Your approved work and referral rewards will appear here."
                  columns={[
                    {
                      label: "Reward",
                      value: (r) => (
                        <div>
                          <p className="font-medium">
                            {text(r.note || r.action || r.kind)}
                          </p>
                          <p className="mt-1 text-[10px] text-muted-foreground">
                            {date(r.created_at)}
                          </p>
                        </div>
                      ),
                    },
                    {
                      label: "Amount",
                      value: (r) =>
                        r.kind === "action"
                          ? "Product reward"
                          : money(
                              Number(r.amount_cents),
                              String(r.currency || me.currency),
                            ),
                    },
                    {
                      label: "Status",
                      value: (r) => <Status value={String(r.status)} />,
                    },
                    {
                      label: "Hold until",
                      value: (r) =>
                        r.status === "pending" ? date(r.hold_until) : "—",
                    },
                  ]}
                />
              </section>
              <section>
                <h2 className="mb-4 text-sm font-semibold">Payout history</h2>
                <DataGrid
                  rows={extra}
                  empty="Your payouts will appear here once the brand has paid them."
                  columns={[
                    {
                      label: "Amount",
                      value: (r) =>
                        money(
                          Number(r.amount_cents),
                          String(r.currency || me.currency),
                        ),
                    },
                    {
                      label: "Status",
                      value: (r) => <Status value={String(r.status)} />,
                    },
                    { label: "Reference", value: (r) => text(r.reference) },
                    { label: "Paid", value: (r) => date(r.paid_at) },
                  ]}
                />
              </section>
            </div>
          )}
          {page === "referrals" && (
            <DataGrid
              rows={rows}
              empty="Every introduction starts somewhere. Share your link to bring your first referral."
              columns={[
                { label: "Customer", value: (r) => text(r.label) },
                {
                  label: "Status",
                  value: (r) => <Status value={String(r.status)} />,
                },
                { label: "Source", value: (r) => text(r.sub_id || r.via) },
                {
                  label: "Sales",
                  value: (r) => money(Number(r.sales_cents), me.currency),
                },
                { label: "Joined", value: (r) => date(r.created_at) },
              ]}
            />
          )}
          {page === "assets" &&
            (rows.length ? (
              <div className="grid gap-5 md:grid-cols-2">
                {rows.map((r) => (
                  <Card
                    key={text(r.id)}
                    className="gap-4 overflow-hidden p-5 shadow-none"
                  >
                    {r.kind === "image" && r.url ? (
                      <img
                        src={String(r.url)}
                        alt={String(r.title)}
                        className="max-h-48 w-full rounded-lg object-contain"
                      />
                    ) : null}
                    <h2 className="text-base font-semibold">{text(r.title)}</h2>
                    {r.body ? (
                      <p className="whitespace-pre-wrap text-xs leading-6 text-muted-foreground">
                        {String(r.body)}
                      </p>
                    ) : null}
                    <div className="mt-auto flex gap-2">
                      {r.url ? (
                        <>
                          <Copy value={String(r.url)} label="Copy link" />
                          <Button variant="outline" size="sm" asChild>
                            <a
                              href={String(r.url)}
                              target="_blank"
                              rel="noreferrer"
                            >
                              Open asset
                              <ArrowUpRight className="size-3" />
                            </a>
                          </Button>
                        </>
                      ) : null}
                      {r.body ? (
                        <Copy value={String(r.body)} label="Copy text" />
                      ) : null}
                    </div>
                  </Card>
                ))}
              </div>
            ) : (
              <Empty title="Your creative toolkit is on its way">
                Banners, brand copy, and other assets shared by the team will
                appear here.
              </Empty>
            ))}
        </>
      )}
      {page === "settings" && (
        <div className="max-w-2xl space-y-6">
          <Card className="gap-5 p-6 shadow-none">
            <div>
              <h2 className="text-base font-semibold">Payout preferences</h2>
              <p className="mt-2 text-xs leading-6 text-muted-foreground">
                Choose how the brand should pay you. Your payout details are
                kept encrypted.
              </p>
            </div>
            <form className="space-y-5" onSubmit={save}>
              <Field label="Payout method">
                <Pick
                  value={method}
                  onChange={setMethod}
                  label="Payout method"
                  options={[
                    { value: "", label: "Choose a method" },
                    ...(me.payout_methods.length
                      ? me.payout_methods
                      : Object.keys(methodNames)
                    ).map((m) => ({ value: m, label: methodNames[m] || m })),
                  ]}
                />
              </Field>
              {method === "crypto" && (
                <Field label="Token / network">
                  {me.payout_tokens.length ? (
                    <Pick
                      value={token}
                      onChange={setToken}
                      label="Crypto token"
                      options={[
                        { value: "", label: "Choose a token" },
                        ...me.payout_tokens.map((t) => ({
                          value: t,
                          label: t,
                        })),
                      ]}
                    />
                  ) : (
                    <Input
                      value={token}
                      onChange={(e) => setToken(e.target.value)}
                      maxLength={40}
                      placeholder="USDC on Base"
                    />
                  )}
                </Field>
              )}
              <Field
                label="Payout details"
                hint="For example, your PayPal email or wallet address. Don't add bank account numbers here."
              >
                <Textarea
                  rows={3}
                  value={details}
                  onChange={(e) => setDetails(e.target.value)}
                  maxLength={500}
                />
              </Field>
              {
                <Field
                  label="Postback URL (optional)"
                  hint="Your own tracker, called when a conversion is credited or refunded."
                >
                  <Input
                    value={postback}
                    onChange={(e) => setPostback(e.target.value)}
                    maxLength={2000}
                    placeholder="https://tracker.example/postback"
                  />
                </Field>
              }
              <div className="space-y-3 border-t pt-5">
                <h3 className="text-xs font-medium">Notifications</h3>
                <label className="flex items-center gap-2 text-xs">
                  <input
                    type="checkbox"
                    checked={conversions}
                    onChange={(e) => setConversions(e.target.checked)}
                  />
                  Email me about new conversions
                </label>
                <label className="flex items-center gap-2 text-xs">
                  <input
                    type="checkbox"
                    checked={payoutNotice}
                    onChange={(e) => setPayoutNotice(e.target.checked)}
                  />
                  Email me about payouts
                </label>
              </div>
              <Button disabled={busy || me.read_only}>
                {busy && <Loader2 className="size-4 animate-spin" />}Save
                preferences
              </Button>
            </form>
          </Card>
          {
            <section>
              <h2 className="mb-4 text-sm font-semibold">Recent postbacks</h2>
              <DataGrid
                rows={extra}
                empty="No postbacks yet."
                columns={[
                  { label: "Sent", value: (r) => date(r.created_at) },
                  { label: "Status", value: (r) => text(r.status) },
                  { label: "Response", value: (r) => text(r.http_status) },
                  { label: "Note", value: (r) => text(r.error) },
                ]}
              />
            </section>
          }
        </div>
      )}
    </>
  );
}
function Auth({
  program,
  error: bootError,
}: {
  program: Program;
  error: string;
}) {
  const [applying, setApplying] = useState(location.hash === "#apply"),
    [email, setEmail] = useState(""),
    [name, setName] = useState(""),
    [answers, setAnswers] = useState<Record<string, string>>({}),
    [helpOpen, setHelpOpen] = useState(false),
    [busy, setBusy] = useState(false),
    [sent, setSent] = useState(false),
    [error, setError] = useState(bootError);
  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await request(
        applying ? "signup" : "sign-in",
        "POST",
        applying
          ? {
              email,
              name,
              answers,

              group: new URLSearchParams(location.search).get("group") || "",
            }
          : { email },
      );
      setSent(true);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="grid min-h-screen lg:grid-cols-2">
      <div className="relative hidden flex-col justify-between border-r bg-sidebar p-12 lg:flex">
        <div className="flex items-center gap-3">
          {program.logo_url ? (
            <img
              src={program.logo_url}
              alt=""
              className="size-9 object-contain"
            />
          ) : (
            <OutflareMark className="size-8" />
          )}
          <span className="text-base font-semibold">{program.title}</span>
        </div>
        <div className="max-w-lg">
          <Badge variant="outline" className="mb-7 gap-2 text-xs font-normal">
            <Sparkles className="size-3.5" />A partnership with possibilities
          </Badge>
          <h1 className="text-5xl font-semibold leading-[1.12] tracking-[-.04em]">
            Your audience.
            <br />
            Our next chapter.
          </h1>
          <p className="mt-6 max-w-sm text-base leading-8 text-muted-foreground">
            {program.welcome ||
              "Bring your ideas, your audience, and your unique perspective. Let's build something that rewards all of us."}
          </p>
          <div className="mt-10 flex gap-8 text-xs text-muted-foreground">
            <span className="flex items-center gap-2">
              <Globe2 className="size-4" />
              Your channels
            </span>
            <span className="flex items-center gap-2">
              <Sparkles className="size-4" />
              Relevant opportunities
            </span>
            <span className="flex items-center gap-2">
              <Wallet className="size-4" />
              Real rewards
            </span>
          </div>
        </div>
        <span className="text-[11px] text-muted-foreground">
          {program.title} · Partner workspace
        </span>
      </div>
      <div className="flex items-center justify-center p-6 sm:p-12">
        <div className="w-full max-w-sm">
          <div className="mb-10 flex items-center gap-3 lg:hidden">
            <OutflareMark className="size-7" />
            <span className="font-semibold">{program.title}</span>
          </div>
          {sent ? (
            <>
              <div className="mb-6 flex size-12 items-center justify-center rounded-xl border bg-muted">
                <CheckCircle2 className="size-5" />
              </div>
              <h2 className="text-2xl font-semibold tracking-tight">
                Check your inbox
              </h2>
              <p className="mt-4 text-sm leading-7 text-muted-foreground">
                {applying
                  ? "Thanks for joining. Check your email for a link to sign in."
                  : "If this email belongs to a partner, a sign-in link is on its way. It works once, for 30 minutes."}
              </p>
              <Button
                className="mt-6"
                variant="outline"
                onClick={() => setSent(false)}
              >
                Use another email
              </Button>
            </>
          ) : (
            <>
              <p className="mb-2 text-xs text-muted-foreground">
                {program.title}
              </p>
              <h2 className="text-[28px] font-semibold tracking-tight">
                {applying
                  ? "Let's get to know you."
                  : "Welcome to your workspace."}
              </h2>
              <p className="mb-8 mt-3 text-sm leading-6 text-muted-foreground">
                {applying
                  ? "Your next partnership starts here."
                  : isEmbedded
                    ? "Ask your app to open a fresh partner session, or sign in with your email."
                    : "Sign in with a secure link. No password to remember."}
              </p>
              <form onSubmit={submit} className="space-y-5">
                {applying && (
                  <Field label="Your name">
                    <Input
                      value={name}
                      onChange={(e) => setName(e.target.value)}
                      required
                      maxLength={200}
                      autoComplete="name"
                    />
                  </Field>
                )}
                <Field label="Email address">
                  <Input
                    type="email"
                    required
                    value={email}
                    onChange={(e) => setEmail(e.target.value)}
                    autoComplete="email"
                    placeholder="you@example.com"
                  />
                </Field>
                {applying &&
                  program.signup_fields.map((f) => (
                    <Field
                      key={f.key}
                      label={f.label + (f.required ? "" : " (optional)")}
                    >
                      {f.type === "select" ? (
                        <select
                          className="h-9 w-full rounded-md border bg-background px-3 text-sm"
                          required={f.required}
                          value={answers[f.key] || ""}
                          onChange={(e) =>
                            setAnswers((a) => ({
                              ...a,
                              [f.key]: e.target.value,
                            }))
                          }
                        >
                          <option value="">Choose an option</option>
                          {f.options.map((o) => (
                            <option key={o}>{o}</option>
                          ))}
                        </select>
                      ) : f.type === "textarea" ? (
                        <Textarea
                          required={f.required}
                          value={answers[f.key] || ""}
                          onChange={(e) =>
                            setAnswers((a) => ({
                              ...a,
                              [f.key]: e.target.value,
                            }))
                          }
                        />
                      ) : (
                        <Input
                          type={f.type === "url" ? "url" : "text"}
                          required={f.required}
                          value={answers[f.key] || ""}
                          onChange={(e) =>
                            setAnswers((a) => ({
                              ...a,
                              [f.key]: e.target.value,
                            }))
                          }
                        />
                      )}
                    </Field>
                  ))}
                {applying && program.terms_url && (
                  <p className="text-xs leading-6 text-muted-foreground">
                    After signing in, you'll review and agree to the{" "}
                    <a
                      href={program.terms_url}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="underline"
                    >
                      program terms
                    </a>{" "}
                    before promoting.
                  </p>
                )}

                {error && (
                  <p role="alert" className="text-xs text-destructive">
                    {error}
                  </p>
                )}
                <Button className="w-full" disabled={busy}>
                  {busy ? <Loader2 className="size-4 animate-spin" /> : null}
                  {applying ? "Join the program" : "Email me a sign-in link"}
                  <ArrowRight className="size-4" />
                </Button>
              </form>
              <div className="mt-6 space-y-5">
                <Button variant="ghost" onClick={() => setHelpOpen(true)}>
                  <CircleHelp className="size-4" />
                  Partner help
                </Button>
                <SupportLink program={program} />
                <Dialog open={helpOpen} onOpenChange={setHelpOpen}>
                  <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-5xl">
                    <DialogHeader>
                      <DialogTitle>Help & support</DialogTitle>
                      <DialogDescription>
                        Guides and program support from {program.title}.
                      </DialogDescription>
                    </DialogHeader>
                    <PortalHelp program={program} />
                  </DialogContent>
                </Dialog>
                <BrandLinks
                  links={[
                    ...(program.home_url
                      ? [{ label: "Visit website", url: program.home_url }]
                      : []),
                    ...(program.header_links || []),
                    ...(program.footer_links || []),
                  ]}
                  className="flex flex-wrap gap-4"
                />
              </div>
              {program.signup_open && (
                <p className="mt-6 text-center text-xs text-muted-foreground">
                  {applying ? "Already a partner?" : "New here?"}
                  <button
                    className="ml-1 font-medium text-foreground underline underline-offset-4"
                    onClick={() => {
                      setApplying(!applying);
                      setError("");
                    }}
                  >
                    {applying ? "Sign in" : "Join the program"}
                  </button>
                </p>
              )}
              <p className="mt-12 flex items-center gap-2 text-[11px] text-muted-foreground">
                <ShieldCheck className="size-3.5" />
                Your workspace is private to you and the brand.
              </p>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
