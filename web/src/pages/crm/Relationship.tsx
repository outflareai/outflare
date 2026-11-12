// The relationship check on a card: before anyone reaches out, what the
// brand already has with this company. Already a customer, in another
// pipeline, an open deal, a teammate who has emailed them (a warm intro),
// or a conversation that went quiet.
import { useEffect, useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { api, errorMessage } from "@/api";
import { ago } from "@/format";
import { pickedBrandId } from "@/lib/brand";

type Check = Schemas["RecordRelationshipCheck"];

export function RelationshipCheck({
  id,
  brand,
  myId,
  typeName,
  onOpen,
}: {
  id: string;
  brand?: string | null;
  myId?: string;
  typeName: (key: string) => string;
  onOpen: (id: string) => void;
}) {
  const [check, setCheck] = useState<Check | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let live = true;
    void api
      .GET("/v1/brands/{brand}/records/{record}/relationship", {
        params: { path: { brand: brand ?? pickedBrandId(), record: id } },
      })
      .then(({ data, error }) => {
        if (!live) return;
        if (data) setCheck(data);
        else setError(errorMessage(error));
      })
      .catch((e) => live && setError(errorMessage(e)));
    return () => {
      live = false;
    };
  }, [id, brand]);
  if (error)
    return (
      <p className="text-xs text-muted-foreground">
        Relationship check: {error}
      </p>
    );
  if (!check) return null;
  const lines: { tone: "warn" | "info"; text: React.ReactNode }[] = [];
  if (check.customer.is) {
    const why = [
      check.customer.active_products > 0 &&
        `${check.customer.active_products} active product${check.customer.active_products === 1 ? "" : "s"}`,
      check.customer.won_deals > 0 &&
        `${check.customer.won_deals} won deal${check.customer.won_deals === 1 ? "" : "s"}`,
    ]
      .filter(Boolean)
      .join(", ");
    lines.push({ tone: "warn", text: <>Already a customer ({why}).</> });
  }
  if (check.deals.open > 0)
    lines.push({
      tone: "warn",
      text: (
        <>
          {check.deals.open === 1
            ? "An open deal"
            : `${check.deals.open} open deals`}{" "}
          with them.
        </>
      ),
    });
  if (check.pipelines.length > 0)
    lines.push({
      tone: "warn",
      text: (
        <>
          Also in{" "}
          {check.pipelines.map((p, i) => (
            <span key={p.id}>
              {i > 0 && ", "}
              <button
                type="button"
                className="underline underline-offset-2"
                onClick={() => onOpen(p.id)}
              >
                {typeName(p.type)}
              </button>
              {p.stage ? ` (${p.stage.replaceAll("_", " ")})` : ""}
            </span>
          ))}
          .
        </>
      ),
    });
  const others = check.team.filter((t) => t.user_id !== myId);
  if (others.length > 0)
    lines.push({
      tone: "info",
      text: (
        <>
          Warm intro:{" "}
          {others
            .map(
              (t) =>
                `${t.name || "A teammate"} (${t.messages} email${t.messages === 1 ? "" : "s"}, ${ago(t.last_at)})`,
            )
            .join(", ")}{" "}
          {others.length === 1 ? "has" : "have"} emailed them.
        </>
      ),
    });
  if (check.contact.stale && check.contact.last_at)
    lines.push({
      tone: "info",
      text: (
        <>
          The conversation went quiet: last contact {ago(check.contact.last_at)}
          . Worth picking back up.
        </>
      ),
    });
  else if (check.contact.last_at)
    lines.push({
      tone: "info",
      text: <>Last contact {ago(check.contact.last_at)}.</>,
    });
  if (lines.length === 0)
    return (
      <p className="text-xs text-muted-foreground">
        No history with this company yet: not a customer, in no other pipeline,
        and nobody here has been in touch.
      </p>
    );
  return (
    <section
      aria-label="Relationship check"
      className="space-y-1.5 rounded-lg border bg-muted/30 p-3 text-sm"
    >
      <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
        Before you reach out
      </h3>
      <ul className="space-y-1">
        {lines.map((l, i) => (
          <li
            key={i}
            className={
              l.tone === "warn" ? "text-amber-700 dark:text-amber-400" : ""
            }
          >
            {l.text}
          </li>
        ))}
      </ul>
    </section>
  );
}
