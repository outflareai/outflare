// The app's UI kit. Components are shadcn/ui (src/components/ui, on Radix
// and Tailwind); this file wraps them in the small API the pages use.

import { Component, useEffect, useRef, useState, useSyncExternalStore, type ButtonHTMLAttributes, type InputHTMLAttributes, type ReactNode } from "react";
import { Check, ChevronDown, Copy } from "lucide-react";
import { toast as sonner } from "sonner";
import { cn } from "@/lib/utils";
import { track, trackCrash } from "@/lib/analytics";
import { Button as UIButton } from "@/components/ui/button";
import { AlertDialog, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Kbd as UIKbd } from "@/components/ui/kbd";
import { Skeleton } from "@/components/ui/skeleton";
import { Toaster } from "@/components/ui/sonner";
import { OutflareMark, OutflareWordmark } from "@/brand";

type Variant = "primary" | "ghost" | "danger" | "danger-solid";
const variants = { primary: "default", ghost: "outline", danger: "ghost", "danger-solid": "destructive" } as const;

export function Button({ variant = "primary", className = "", ...props }: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant }) {
  return (
    <UIButton
      variant={variants[variant]}
      size="sm"
      className={cn("text-[13px]", variant === "danger" && "text-destructive hover:bg-destructive/10 hover:text-destructive dark:hover:bg-destructive/15", className)}
      {...props}
    />
  );
}

export function Input({ label, hint, ...props }: InputHTMLAttributes<HTMLInputElement> & { label: string; hint?: string }) {
  return (
    <label className="block text-sm">
      <span className="mb-1.5 block text-[13px] font-medium">{label}</span>
      <input className={`${field} w-full`} {...props} />
      {hint && <span className="mt-1 block text-xs text-muted-foreground">{hint}</span>}
    </label>
  );
}

export const field = "h-8 rounded-md border border-input bg-transparent px-2.5 text-[13px] shadow-xs transition-[color,box-shadow] outline-none placeholder:text-muted-foreground hover:border-ring/60 focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/40 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive dark:bg-input/30 [textarea&]:h-auto [textarea&]:py-1.5 [select&]:pr-7";

export function ErrorText({ children }: { children: ReactNode }) {
  return children ? <p role="alert" className="text-sm text-destructive">{children}</p> : null;
}

export function Card({ title, children, action, description }: { title: ReactNode; children: ReactNode; action?: ReactNode; description?: ReactNode }) {
  return (
    <section data-slot="card" className="rounded-lg border bg-card text-card-foreground shadow-xs">
      <header className="flex min-h-11 flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
        <div>
          <h2 className="text-[13px] font-semibold">{title}</h2>
          {description && <p className="text-[12px] text-muted-foreground">{description}</p>}
        </div>
        {action}
      </header>
      <div className="p-4">{children}</div>
    </section>
  );
}

// The title row every page starts with: what this page is, in one line, and
// its main action.
export function PageHeader({ title, description, action, back }: { title: ReactNode; description?: ReactNode; action?: ReactNode; back?: { label: string; onClick: () => void } }) {
  return (
    <div className="mb-6 flex flex-wrap items-end justify-between gap-3 border-b pb-5">
      <div className="min-w-0">
        {back && (
          <button onClick={back.onClick} className="mb-1.5 font-mono text-[12px] text-muted-foreground hover:text-foreground">
            ← {back.label}
          </button>
        )}
        <h1 className="flex flex-wrap items-center gap-2 text-xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1 max-w-2xl text-[13px] text-muted-foreground">{description}</p>}
      </div>
      {action && <div className="flex flex-wrap gap-2">{action}</div>}
    </div>
  );
}

export function CenteredForm({ title, subtitle, children }: { title: string; subtitle?: string; children: ReactNode }) {
  return (
    <main className="flex min-h-screen items-center justify-center bg-[radial-gradient(ellipse_at_top,var(--color-neutral-100),transparent_60%)] px-4 py-16 dark:bg-[radial-gradient(ellipse_at_top,var(--color-neutral-900),transparent_60%)]">
      <div className="w-full max-w-sm">
        <Logo className="mb-8" />
        <div className="rounded-xl border bg-card p-6 shadow-sm">
          <h1 className="text-lg font-semibold tracking-tight">{title}</h1>
          {subtitle && <p className="mt-1 text-[13px] text-muted-foreground">{subtitle}</p>}
          <div className="mt-6 space-y-4">{children}</div>
        </div>
      </div>
    </main>
  );
}

export const tones = {
  green: "border-green-600/25 bg-green-500/10 text-green-700 dark:border-green-400/20 dark:text-green-400",
  grey: "border-border bg-muted text-muted-foreground",
  amber: "border-amber-600/30 bg-amber-500/10 text-amber-700 dark:border-amber-400/20 dark:text-amber-400",
  red: "border-red-600/25 bg-red-500/10 text-red-700 dark:border-red-400/25 dark:text-red-400",
  blue: "border-blue-600/25 bg-blue-500/10 text-blue-700 dark:border-blue-400/25 dark:text-blue-400",
};

export function Badge({ tone = "grey", children, title }: { tone?: keyof typeof tones; children: ReactNode; title?: string }) {
  return <span title={title} className={`inline-flex h-5 items-center gap-1 whitespace-nowrap rounded border px-1.5 font-mono text-[11px] font-medium ${tones[tone]}`}>{children}</span>;
}

/** A status dot and label, for states in dense lists ("● active"). */
export function Status({ tone = "grey", children }: { tone?: keyof typeof tones; children: ReactNode }) {
  const dot = { green: "bg-green-500", grey: "bg-muted-foreground/60", amber: "bg-amber-500", red: "bg-red-500", blue: "bg-blue-500" }[tone];
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      <span aria-hidden="true" className={`size-1.5 rounded-full ${dot}`} />
      {children}
    </span>
  );
}

export function Logo({ className = "", markOnly = false }: { className?: string; markOnly?: boolean }) {
  return (
    // The Outflare lockup's mark and name touch: the gap is in the artwork.
    <span className={`inline-flex items-center font-semibold tracking-tight ${className}`}>
      <OutflareMark className={`${markOnly ? "h-[18px]" : "h-6"} w-auto shrink-0`} />
      {markOnly ? (
        <span className="sr-only">Outflare</span>
      ) : (
        <span className="inline-flex shrink-0">
          <OutflareWordmark className="h-6 w-auto" />
          <span className="sr-only">Outflare</span>
        </span>
      )}
    </span>
  );
}

export function Kbd({ children }: { children: ReactNode }) {
  return <UIKbd className="h-5 rounded border bg-muted font-mono text-[10px]">{children}</UIKbd>;
}

/** A small button that copies text and says so. */
export function CopyButton({ text, label = "Copy", className = "" }: { text: string; label?: string; className?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      onClick={async () => {
        await navigator.clipboard?.writeText(text);
        track("ui.copied");
        setDone(true);
        setTimeout(() => setDone(false), 1500);
      }}
      className={cn("inline-flex size-6 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-accent hover:text-foreground", done && "text-brand", className)}
    >
      {done ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
    </button>
  );
}

/** An ID or other machine value in mono, with a copy button. */
export function Mono({ value, short = false }: { value: string; short?: boolean }) {
  return (
    <span className="inline-flex min-w-0 items-center gap-0.5 font-mono text-[12px] text-muted-foreground">
      <span className="truncate" title={value}>{short && value.length > 14 ? `${value.slice(0, 8)}…${value.slice(-4)}` : value}</span>
      <CopyButton text={value} label={`Copy ${value}`} />
    </span>
  );
}

/** A headline number with a mono label, for the tiles at the top of a page. */
export function Stat({ label, value, sub, tone }: { label: ReactNode; value: ReactNode; sub?: ReactNode; tone?: "red" | "amber" | "green" }) {
  const color = tone === "red" ? "text-red-600 dark:text-red-400" : tone === "amber" ? "text-amber-600 dark:text-amber-400" : tone === "green" ? "text-green-600 dark:text-green-400" : "";
  return (
    <div className="min-w-0 bg-card px-4 py-3">
      <div className="truncate font-mono text-[11px] tracking-wide text-muted-foreground uppercase">{label}</div>
      <div className={`mt-1 font-mono text-2xl font-medium tracking-tight tabular-nums ${color}`}>{value}</div>
      {sub && <div className="mt-0.5 text-[12px] text-muted-foreground">{sub}</div>}
    </div>
  );
}

/** A row of Stat tiles joined by hairlines, like a terminal status bar. */
export function Stats({ children, cols = 4 }: { children: ReactNode; cols?: 2 | 3 | 4 | 5 | 6 }) {
  const grid = { 2: "sm:grid-cols-2", 3: "sm:grid-cols-3", 4: "sm:grid-cols-4", 5: "sm:grid-cols-5", 6: "sm:grid-cols-3 lg:grid-cols-6" }[cols];
  return <div className={`grid grid-cols-2 gap-px overflow-hidden rounded-lg border bg-border shadow-xs ${grid}`}>{children}</div>;
}

export function Empty({ title, children, action }: { title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-border bg-[repeating-linear-gradient(135deg,transparent,transparent_6px,var(--color-neutral-100)_6px,var(--color-neutral-100)_7px)] px-4 py-10 text-center dark:border-neutral-800 dark:bg-[repeating-linear-gradient(135deg,transparent,transparent_6px,var(--color-neutral-900)_6px,var(--color-neutral-900)_7px)]">
      <p className="font-medium">{title}</p>
      {children && <div className="mx-auto mt-1 max-w-md text-[13px] text-muted-foreground">{children}</div>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

export function Loading() {
  return (
    <div aria-live="polite" className="space-y-3">
      <span className="sr-only">Loading…</span>
      <Skeleton className="h-6 w-48" />
      <Skeleton className="h-20 rounded-lg" />
      <Skeleton className="h-40 rounded-lg" />
    </div>
  );
}

// A file picker that looks like the rest of the app and says which file is
// chosen, with an optional link to an example file.
export function FilePicker({ file, onChange, accept, label = "Choose CSV file", example }: { file: File | null; onChange: (f: File | null) => void; accept: string; label?: string; example?: { name: string; content: string } }) {
  const ref = useRef<HTMLInputElement>(null);
  return (
    <div className="flex flex-wrap items-center gap-3 text-sm">
      <input ref={ref} type="file" accept={accept} className="sr-only" aria-label={label} onChange={(e) => onChange(e.target.files?.[0] ?? null)} />
      <Button type="button" variant="ghost" onClick={() => ref.current?.click()}>{file ? "Change file" : label}</Button>
      <span className={file ? "" : "text-muted-foreground"}>{file ? `${file.name} (${formatBytes(file.size)})` : "No file chosen"}</span>
      {example && (
        <a className="text-muted-foreground underline hover:text-foreground" download={example.name} href={`data:text/csv;charset=utf-8,${encodeURIComponent(example.content)}`}>
          Download an example
        </a>
      )}
    </div>
  );
}

function formatBytes(n: number) {
  return n < 1024 ? `${n} B` : n < 1024 * 1024 ? `${Math.round(n / 1024)} KB` : `${(n / 1024 / 1024).toFixed(1)} MB`;
}

// A small "More" menu for row actions that don't need to be on screen all the time.
export function Menu({ label = "More", aria, items }: { label?: string; aria?: string; items: ({ label: string; onClick: () => void; danger?: boolean } | false)[] }) {
  const shown = items.filter((i): i is Exclude<typeof i, false> => !!i);
  if (shown.length === 0) return null;
  const firstDanger = shown.findIndex((i) => i.danger);
  return (
    <DropdownMenu modal={false}>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="ghost" aria-label={aria}>{label}<ChevronDown className="size-3.5 opacity-60" /></Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-44">
        {shown.map((i, n) => (
          <div key={i.label}>
            {n === firstDanger && n > 0 && <DropdownMenuSeparator />}
            <DropdownMenuItem variant={i.danger ? "destructive" : "default"} className="text-[13px]" onSelect={() => i.onClick()}>
              {i.label}
            </DropdownMenuItem>
          </div>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

// Dialogs: ask() replaces window.confirm and window.prompt with an in-app
// dialog that names the consequences and has clear button labels.

type Choice = { label: string; value: string; variant?: "primary" | "ghost" | "danger-solid" };
type Ask = { title: string; body?: ReactNode; choices: Choice[]; input?: { label: string; type?: string; placeholder?: string; value?: string; maxLength?: number; optional?: boolean }; resolve: (r: { choice: string; value: string } | null) => void };

let current: Ask | null = null;
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

export function ask(opts: Omit<Ask, "resolve">): Promise<{ choice: string; value: string } | null> {
  return new Promise((resolve) => {
    current = { ...opts, resolve };
    emit();
  });
}

/** Asks a yes/no question; resolves true when the confirm button is pressed. */
export async function confirmAction(title: string, body: ReactNode, confirm: string, danger = true): Promise<boolean> {
  const r = await ask({ title, body, choices: [{ label: "Cancel", value: "cancel", variant: "ghost" }, { label: confirm, value: "ok", variant: danger ? "danger-solid" : "primary" }] });
  return r?.choice === "ok";
}

export function DialogHost() {
  const a = useSyncExternalStore((l) => (listeners.add(l), () => listeners.delete(l)), () => current);
  const [value, setValue] = useState("");

  useEffect(() => {
    setValue(a?.input?.value ?? "");
  }, [a]);

  const done = (choice: string | null) => {
    if (a) track("ui.dialog_answered", { choice: choice ?? "cancel", prompt: !!a.input });
    a?.resolve(choice === null || choice === "cancel" ? null : { choice, value });
    current = null;
    emit();
  };

  return (
    <AlertDialog open={!!a} onOpenChange={(o) => !o && done(null)}>
      {a && (
        <AlertDialogContent className="gap-0 p-0 sm:max-w-md" onEscapeKeyDown={() => done(null)}>
          <form className="space-y-4 p-5" onSubmit={(e) => { e.preventDefault(); done(a.choices[a.choices.length - 1].value); }}>
            <AlertDialogHeader className="text-left">
              <AlertDialogTitle className="text-[15px]">{a.title}</AlertDialogTitle>
              {a.body ? <AlertDialogDescription asChild><div className="space-y-2 text-[13px]">{a.body}</div></AlertDialogDescription> : <AlertDialogDescription className="sr-only">{a.title}</AlertDialogDescription>}
            </AlertDialogHeader>
            {a.input && <Input label={a.input.label} type={a.input.type} placeholder={a.input.placeholder} maxLength={a.input.maxLength} value={value} onChange={(e) => setValue(e.target.value)} required={!a.input.optional} autoFocus />}
            <AlertDialogFooter className="flex-row flex-wrap justify-end gap-2">
              {a.choices.map((c, i) => (
                <Button key={c.value} type={i === a.choices.length - 1 ? "submit" : "button"} variant={c.variant ?? "ghost"} onClick={i === a.choices.length - 1 ? undefined : () => done(c.value)} autoFocus={!a.input && i === 0}>
                  {c.label}
                </Button>
              ))}
            </AlertDialogFooter>
          </form>
        </AlertDialogContent>
      )}
    </AlertDialog>
  );
}

// Toasts confirm that something happened, without a blocking alert.
export function toast(text: string, tone: "ok" | "error" = "ok") {
  if (tone === "error") sonner.error(text, { duration: 8000 });
  else sonner.success(text, { duration: 4000 });
}

export function ToastHost() {
  return <Toaster position="bottom-right" toastOptions={{ classNames: { toast: "text-[13px]" } }} />;
}

// A page that throws while rendering shows a way out instead of a blank
// screen, and is recorded as ui.crashed.
export class ErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() {
    return { failed: true };
  }
  componentDidCatch(error: unknown) {
    trackCrash(error, "render");
  }
  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div className="flex min-h-svh flex-col items-center justify-center gap-3 bg-background p-6 text-center text-foreground">
        <h1 className="text-lg font-semibold">Something broke on this page</h1>
        <p className="max-w-sm text-[13px] text-muted-foreground">It has been reported. Reloading usually fixes it.</p>
        <button type="button" className="h-9 rounded-md bg-primary px-4 text-[13px] font-medium text-primary-foreground" onClick={() => location.reload()}>
          Reload
        </button>
      </div>
    );
  }
}
