import { useState, type ReactNode } from "react";
import { Button } from "../ui";
import { Icon } from "../icons";

// The pieces every "put this on your site or DNS" step shares: brand
// setup's install steps and Lifecycle's sending domains. Each way to do it
// sits in a Panel: one click through a connection, a prompt for an AI
// agent, a message for a developer, or the values to copy.

export const linkButton = "inline-flex h-8 items-center gap-1.5 rounded-md border px-3 text-[13px] hover:bg-accent";

export function Code({ children }: { children: ReactNode }) {
  return <code className="font-mono text-[12px] break-all">{children}</code>;
}

export function Panel({ children }: { children: ReactNode }) {
  return <div className="space-y-3 rounded-lg border bg-muted/30 p-4">{children}</div>;
}

export function CopyText({ text, label = "Copy", icon, onCopy }: { text: string; label?: string; icon?: boolean; onCopy?: () => void }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard?.writeText(text);
    onCopy?.();
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };
  if (icon) {
    return (
      <button type="button" aria-label={label} title={label} onClick={copy} className="inline-flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground">
        <Icon name={copied ? "check" : "copy"} className="size-3.5" />
      </button>
    );
  }
  return <Button onClick={copy}><Icon name={copied ? "check" : "copy"} className="size-3.5" /> {copied ? "Copied" : label}</Button>;
}

export function Snippet({ text, onCopy }: { text: string; onCopy?: () => void }) {
  return (
    <div className="relative">
      <pre className="overflow-x-auto rounded-md border bg-background px-3 py-2 pr-9 font-mono text-[11.5px]">{text}</pre>
      <div className="absolute top-1.5 right-1.5"><CopyText text={text} icon onCopy={onCopy} /></div>
    </div>
  );
}

// A prompt for the person's own AI agent: copy it, or open it in Claude or
// ChatGPT. used says how (copied, claude, chatgpt).
export function AIPromptPanel({ prompt, intro, used }: { prompt: string; intro: ReactNode; used: (how: string) => void }) {
  return (
    <Panel>
      <p className="text-[13px] text-muted-foreground">{intro}</p>
      <div className="flex flex-wrap gap-2">
        <CopyText text={prompt} label="Copy prompt" onCopy={() => used("copied")} />
        <a className={linkButton} href={`https://claude.ai/new?q=${encodeURIComponent(prompt)}`} target="_blank" rel="noreferrer" onClick={() => used("claude")}>Open in Claude <Icon name="external" className="size-3" /></a>
        <a className={linkButton} href={`https://chatgpt.com/?q=${encodeURIComponent(prompt)}`} target="_blank" rel="noreferrer" onClick={() => used("chatgpt")}>Open in ChatGPT <Icon name="external" className="size-3" /></a>
      </div>
      <details>
        <summary className="cursor-pointer text-[12px] text-muted-foreground hover:text-foreground">Show the prompt</summary>
        <pre className="mt-2 max-h-72 overflow-y-auto rounded-md border bg-background px-3 py-2 font-mono text-[11.5px] leading-relaxed whitespace-pre-wrap text-muted-foreground">{prompt}</pre>
      </details>
    </Panel>
  );
}

// A ready-to-send message for a developer, from the person's own email.
export function DeveloperPanel({ message, used }: { message: { subject: string; body: string }; used: (how: string) => void }) {
  return (
    <Panel>
      <p className="text-[13px] text-muted-foreground">A ready-to-send message with everything they need. It comes from your own email.</p>
      <pre className="max-h-64 overflow-y-auto rounded-md border bg-background px-3 py-2 font-mono text-[11.5px] leading-relaxed whitespace-pre-wrap">{`Subject: ${message.subject}\n\n${message.body}`}</pre>
      <div className="flex flex-wrap gap-2">
        <CopyText text={`${message.subject}\n\n${message.body}`} label="Copy message" onCopy={() => used("copied")} />
        <a className={linkButton} href={`mailto:?subject=${encodeURIComponent(message.subject)}&body=${encodeURIComponent(message.body)}`} onClick={() => used("email")}>
          <Icon name="inbox" className="size-3.5" /> Open in my email
        </a>
      </div>
    </Panel>
  );
}
