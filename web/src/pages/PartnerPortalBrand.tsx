import { CouponTemplates } from "./PartnerCoupons";
import type { PartnerProgram } from "@outflare/sdk";
import { ArrowUp, BookOpen, LifeBuoy, Plus, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { track } from "@/lib/analytics";

type Settings = PartnerProgram["portal"];
type Link = { label: string; url: string };
type Article = {
  id: string;
  title: string;
  category:
    | "getting_started"
    | "promotion"
    | "tracking"
    | "payouts"
    | "integrations"
    | "rules";
  body: string;
};
export function PartnerPortalBrand({
  value,
  onChange,
  currency,
}: {
  currency: string;
  value: Settings;
  onChange: (value: Settings) => void;
}) {
  const set = (patch: Partial<Settings>) => onChange({ ...value, ...patch });
  const field = (
    label: string,
    key: "home_url" | "support_url" | "support_email" | "terms_revision",
    hint: string,
    type = "text",
  ) => (
    <label className="space-y-2 text-xs font-medium">
      {label}
      <Input
        type={type}
        value={value[key] || ""}
        onChange={(e) => set({ [key]: e.target.value })}
      />
      <span className="block text-[11px] font-normal leading-5 text-muted-foreground">
        {hint}
      </span>
    </label>
  );
  return (
    <Card className="min-w-0 gap-5 p-5 shadow-none">
      <div>
        <h3 className="text-base font-semibold">Make this your partner home</h3>
        <p className="mt-2 text-xs leading-6 text-muted-foreground">
          Give partners a familiar place to promote, learn and get help. Changes
          publish when you save the portal.
        </p>
      </div>
      <Tabs className="min-w-0" defaultValue="navigation">
        <TabsList className="grid group-data-[orientation=horizontal]/tabs:h-auto w-full min-w-0 grid-cols-2 gap-1 sm:grid-cols-3">
          <TabsTrigger className="h-9 text-xs" value="navigation">Navigation</TabsTrigger>
          <TabsTrigger className="h-9 text-xs" value="help">Help center</TabsTrigger>
          <TabsTrigger className="h-9 text-xs" value="terms">Terms & payments</TabsTrigger>
          <TabsTrigger className="h-9 text-xs" value="destinations">Landing pages</TabsTrigger>
          <TabsTrigger className="h-9 text-xs" value="coupons">Coupon offers</TabsTrigger>
          <TabsTrigger className="h-9 text-xs" value="access">Connect your app</TabsTrigger>
        </TabsList>
        <TabsContent value="navigation" className="space-y-6 pt-4">
          <div className="grid gap-4 sm:grid-cols-2">
            {field(
              "Brand homepage",
              "home_url",
              "The website link appears in your portal navigation.",
              "url",
            )}
            {field(
              "Support page",
              "support_url",
              "Where partners should ask your team for help.",
              "url",
            )}
            {field(
              "Support email",
              "support_email",
              "Used when no support page is set.",
              "email",
            )}
          </div>
          <Links
            label="Header links"
            value={value.header_links || []}
            onChange={(header_links) => set({ header_links })}
          />
          <Links
            label="Footer links"
            value={value.footer_links || []}
            onChange={(footer_links) => set({ footer_links })}
          />
        </TabsContent>
        <TabsContent value="help" className="space-y-4 pt-4">
          <p className="text-xs leading-6 text-muted-foreground">
            Getting-started, tracking and payout guides are included. Add your
            own answers below. Articles are plain text, searchable, and visible
            to signed-in partners.
          </p>
          {(value.help_articles || []).map((a, i) => (
            <Card key={a.id} className="gap-4 p-4 shadow-none">
              <div className="flex items-center gap-2">
                <BookOpen className="size-4 text-muted-foreground" />
                <span className="flex-1 text-xs font-medium">
                  Article {i + 1}
                </span>
                <Button
                  aria-label={`Remove article ${i + 1}`}
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  onClick={() =>
                    set({
                      help_articles: value.help_articles?.filter(
                        (_, j) => j !== i,
                      ),
                    })
                  }
                >
                  <Trash2 className="size-4" />
                </Button>
              </div>
              <Input
                aria-label={`Article ${i + 1} title`}
                placeholder="How and when we pay partners"
                value={a.title}
                maxLength={160}
                onChange={(e) =>
                  set({
                    help_articles: value.help_articles?.map((x, j) =>
                      i === j ? { ...x, title: e.target.value } : x,
                    ),
                  })
                }
              />
              <select
                aria-label={`Article ${i + 1} category`}
                className="h-9 rounded-md border bg-background px-3 text-xs"
                value={a.category}
                onChange={(e) =>
                  set({
                    help_articles: value.help_articles?.map((x, j) =>
                      i === j
                        ? {
                            ...x,
                            category: e.target.value as Article["category"],
                          }
                        : x,
                    ),
                  })
                }
              >
                {Object.entries({
                  getting_started: "Getting started",
                  promotion: "Promotion",
                  tracking: "Tracking",
                  payouts: "Payouts",
                  integrations: "Integrations",
                  rules: "Program rules",
                }).map(([v, l]) => (
                  <option key={v} value={v}>
                    {l}
                  </option>
                ))}
              </select>
              <Textarea
                aria-label={`Article ${i + 1} body`}
                placeholder="Write a clear answer for your partners…"
                rows={7}
                maxLength={20000}
                value={a.body}
                onChange={(e) =>
                  set({
                    help_articles: value.help_articles?.map((x, j) =>
                      i === j ? { ...x, body: e.target.value } : x,
                    ),
                  })
                }
              />
            </Card>
          ))}
          <Button
            type="button"
            variant="outline"
            disabled={(value.help_articles?.length || 0) >= 40}
            onClick={() => {
              set({
                help_articles: [
                  ...(value.help_articles || []),
                  {
                    id: `article_${crypto.randomUUID().replaceAll("-", "").slice(0, 16)}`,
                    title: "",
                    category: "getting_started",
                    body: "",
                  },
                ],
              });
              track("ui.partner_help_composer_opened");
            }}
          >
            <Plus className="size-4" />
            Add help article
          </Button>
        </TabsContent>
        <TabsContent value="terms" className="space-y-5 pt-4">
          <div className="flex gap-3 rounded-lg bg-muted/40 p-4">
            <LifeBuoy className="mt-1 size-4 shrink-0 text-muted-foreground" />
            <p className="text-xs leading-6 text-muted-foreground">
              Set the program terms address in program Settings. Partners must
              explicitly agree before using promotion tools or submitting work.
              Change the revision below when the document changes at the same
              address.
            </p>
          </div>
          {field(
            "Terms revision",
            "terms_revision",
            "For example: 2026-11. Changing this requires existing partners to review the current terms again.",
          )}
          <label className="block space-y-2 text-xs font-medium">
            Payment guidance
            <Textarea
              rows={6}
              maxLength={6000}
              value={value.payout_terms || ""}
              onChange={(e) => set({ payout_terms: e.target.value })}
              placeholder="Explain payment dates, supported methods, fees, invoice requirements and who to contact."
            />
            <span className="block text-[11px] font-normal text-muted-foreground">
              Displayed in partner help. Cash minimums and holds come from your
              program settings.
            </span>
          </label>
        </TabsContent>
        <TabsContent value="destinations" className="space-y-5 pt-4">
          <p className="text-xs leading-6 text-muted-foreground">
            Offer named destinations for a homepage, signup page or campaign.
            Use pages on your program landing website or its subdomains.
          </p>
          <Links
            label="Landing pages"
            value={value.landing_pages || []}
            onChange={(landing_pages) => set({ landing_pages })}
          />
          <div className="rounded-lg border p-4 text-xs leading-6 text-muted-foreground">
            <p className="font-medium text-foreground">
              A memorable link on your main website
            </p>
            <p>
              Add a temporary (302 or 307) redirect from a path such as{" "}
              <code>/maya</code> to the partner's tracking link. For a
              particular landing page, use the full link generated by the
              portal. DNS routes hostnames, so your website or CDN must handle a
              root-domain path. Reserve existing pages and forward campaign
              query parameters deliberately.
            </p>
            <p className="mt-2">
              Connected tracking domains already serve{" "}
              <code>/r/partner-code</code>. Configure your website alias to
              point there; no website changes are made automatically.
            </p>
          </div>
        </TabsContent>
        <TabsContent value="coupons" className="pt-4">
          <CouponTemplates
            value={value.coupon_templates || []}
            onChange={(coupon_templates) => set({ coupon_templates })}
            currency={currency}
          />
        </TabsContent>
        <TabsContent value="access" className="space-y-4 pt-4">
          <h4 className="text-sm font-semibold">
            One login for your app and partner portal
          </h4>
          <p className="text-xs leading-6 text-muted-foreground">
            From your authenticated backend, create a hosted portal session for
            the current customer's stable ID. Redirect that customer to the
            returned sign_in_url. They are provisioned automatically and review
            your terms before promoting. Existing email accounts use the same
            customer record.
          </p>
          <pre className="overflow-x-auto rounded-lg border bg-muted/30 p-4 text-[11px] leading-6">{`POST /v1/workspaces/{workspace}/partner-programs/{program}/portal-sessions
{ "customer_id": "your_authenticated_user_id" }`}</pre>
          <p className="text-xs leading-6 text-muted-foreground">
            Use a backend API token with partners.conversions. The returned URL
            is single-use and expires in 60 seconds. Never expose your API
            token, accept an arbitrary browser-supplied customer ID, cache the
            link, or share it with another user.
          </p>
          <p className="text-xs leading-6 text-muted-foreground">
            For an iframe inside your app, use widget-sessions and open the full
            portal with view=portal. Your API and MCP can also manage portal
            settings, coupon offers and assigned codes. A dedicated external
            identity-provider integration is separate from this backend handoff.
          </p>
        </TabsContent>
      </Tabs>
    </Card>
  );
}
function Links({
  label,
  value,
  onChange,
}: {
  label: string;
  value: Link[];
  onChange: (v: Link[]) => void;
}) {
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h4 className="text-xs font-medium">{label}</h4>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={value.length >= 12}
          onClick={() => onChange([...value, { label: "", url: "" }])}
        >
          <Plus className="size-3" />
          Add link
        </Button>
      </div>
      {value.length === 0 && (
        <p className="text-xs text-muted-foreground">No custom links yet.</p>
      )}
      {value.map((l, i) => (
        <div key={i} className="flex flex-wrap gap-2 rounded-lg border p-3">
          <Input
            aria-label={`${label} ${i + 1} label`}
            className="min-w-0 flex-1 basis-32"
            maxLength={80}
            placeholder="Label"
            value={l.label}
            onChange={(e) =>
              onChange(
                value.map((x, j) =>
                  i === j ? { ...x, label: e.target.value } : x,
                ),
              )
            }
          />
          <Input
            aria-label={`${label} ${i + 1} URL`}
            type="url"
            className="min-w-0 flex-[2] basis-48"
            placeholder="https://"
            value={l.url}
            onChange={(e) =>
              onChange(
                value.map((x, j) =>
                  i === j ? { ...x, url: e.target.value } : x,
                ),
              )
            }
          />
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={`Move ${label} ${i + 1} up`}
            disabled={i === 0}
            onClick={() => {
              const next = [...value];
              [next[i - 1], next[i]] = [next[i], next[i - 1]];
              onChange(next);
            }}
          >
            <ArrowUp className="size-3" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={`Remove ${label} ${i + 1}`}
            onClick={() => onChange(value.filter((_, j) => i !== j))}
          >
            <Trash2 className="size-4" />
          </Button>
        </div>
      ))}
    </div>
  );
}
