import { useEffect, useState } from "react";
import type { PartnerProgram } from "@outflare/sdk";
import { api } from "../api";
import { appPath } from "@/lib/paths";
import { track } from "@/lib/analytics";
import { Snippet } from "@/components/install-methods";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";

// Install shows the two things a site needs for a program, ready to paste
// in the way the site is built: the website script, which keeps the ref
// code a partner's link carries, and the calls the backend makes when
// someone signs up or pays.

type Way = "html" | "nextjs" | "gtm";
type SiteKey = { brand_id: string | null; snippet: string };
type Call = "curl" | "node" | "postback";

// scriptAttrs reads the src and data- attributes of the site key's
// snippet, to write it again for Next.js.
export function scriptAttrs(snippet: string): [string, string][] {
  const out: [string, string][] = [];
  const tag = snippet.match(/<script\b([^>]*)>/i)?.[1] ?? "";
  for (const m of tag.matchAll(/([a-z][\w-]*)(?:="([^"]*)")?/gi)) {
    if (m[1] === "async") continue;
    out.push([m[1], m[2] ?? ""]);
  }
  return out;
}

export function nextSnippet(snippet: string) {
  const attrs = scriptAttrs(snippet).map(([k, v]) => (v === "" && k.startsWith("data-") ? `${k}=""` : `${k}="${v}"`));
  return `// app/layout.tsx (or pages/_app.tsx)\nimport Script from "next/script";\n\n// inside <body>:\n<Script ${attrs.join(" ")} strategy="afterInteractive" />`;
}

export function gtmSnippet(snippet: string) {
  return `<!-- Google Tag Manager: Tags, New, Custom HTML, trigger All Pages -->\n${snippet}`;
}

export function PartnerInstall({ ws, program: p }: { ws: string; program: PartnerProgram }) {
  const [keys, setKeys] = useState<SiteKey[] | null>(null);
  const [way, setWay] = useState<Way>("html");
  const [call, setCall] = useState<Call>("curl");
  useEffect(() => {
    api.GET("/v1/workspaces/{ws}/site-keys", { params: { path: { ws } } }).then(({ data }) => setKeys(data?.data ?? []));
  }, [ws]);
  // The program's brand's site, else the first.
  const key = keys?.find((k) => p.brand_id && k.brand_id === p.brand_id) ?? keys?.[0];
  const base = `${location.origin}/v1/workspaces/${ws}/partner-conversions`;
  const calls: Record<Call, string> = {
    curl: `# When someone signs up (a token with the partners.conversions scope)\ncurl ${base} \\\n  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \\\n  -d '{"type": "signup", "program": "${p.slug}", "customer_id": "user_123", "email": "ana@example.com"}'\n\n# When they pay, and again on every renewal; a refund names the sale\ncurl ${base} \\\n  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H "Content-Type: application/json" \\\n  -d '{"type": "sale", "program": "${p.slug}", "customer_id": "user_123", "amount_cents": 4900, "currency": "${p.currency}", "external_id": "inv_1001"}'`,
    node: `// When someone signs up, and when they pay (amounts in cents)\nawait fetch("${base}", {\n  method: "POST",\n  headers: { Authorization: \`Bearer \${process.env.OUTFLARE_TOKEN}\`, "Content-Type": "application/json" },\n  body: JSON.stringify({ type: "sale", program: "${p.slug}", customer_id: user.id, amount_cents: 4900, currency: "${p.currency}", external_id: invoice.id }),\n});`,
    postback: p.postback_hint
      ? `Settings, Postback URL (it starts ${p.postback_hint}), with:\n?type=sale&user_id=user_123&amount=49.00&order_id=inv_1001`
      : "Make a postback URL above, then call it with ?type=sale&user_id=user_123&amount=49.00&order_id=inv_1001",
  };
  const pick = <T extends string>(step: "script" | "conversions", set: (v: T) => void) => (v: string) => {
    set(v as T);
    track("ui.partner_install_viewed", { step, way: v });
  };

  return (
    <div className="space-y-4 border-t px-4 pt-4 pb-6">
      <h3 className="text-[13px] font-medium">Install</h3>
      <div className="space-y-2">
        <p className="text-[12px] text-muted-foreground">1. The website script on every page, so a visit from a partner's link (?ref= or ?via=) is remembered until the visitor signs up.</p>
        {!keys ? null : !key ? (
          <p className="text-[12px] text-muted-foreground">
            Make a site key first: <a className="underline underline-offset-2" href={appPath(ws, "tracking")}>Website tracking</a>.
          </p>
        ) : (
          <>
            <Tabs value={way} onValueChange={pick<Way>("script", setWay)}>
              <TabsList>
                <TabsTrigger value="html" className="px-3 text-[12px]">HTML</TabsTrigger>
                <TabsTrigger value="nextjs" className="px-3 text-[12px]">Next.js</TabsTrigger>
                <TabsTrigger value="gtm" className="px-3 text-[12px]">Google Tag Manager</TabsTrigger>
              </TabsList>
            </Tabs>
            <Snippet text={way === "html" ? `<!-- In <head> -->\n${key.snippet}` : way === "nextjs" ? nextSnippet(key.snippet) : gtmSnippet(key.snippet)} />
          </>
        )}
      </div>
      <div className="space-y-2">
        <p className="text-[12px] text-muted-foreground">
          2. Your backend reports each sign-up and payment with the customer's id in your product. Outflare credits the partner whose link they came from; identify the visitor with outflare.anonymousId() when they sign up, so their visit joins them.
        </p>
        <Tabs value={call} onValueChange={pick<Call>("conversions", setCall)}>
          <TabsList>
            <TabsTrigger value="curl" className="px-3 text-[12px]">API</TabsTrigger>
            <TabsTrigger value="node" className="px-3 text-[12px]">Node.js</TabsTrigger>
            <TabsTrigger value="postback" className="px-3 text-[12px]">Postback URL</TabsTrigger>
          </TabsList>
        </Tabs>
        <Snippet text={calls[call]} />
      </div>
    </div>
  );
}
