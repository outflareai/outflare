import { useState } from "react";
import type { Schemas } from "@outflare/sdk";
import { Search, Star } from "lucide-react";
import { api, errorMessage } from "@/api";
import { Button, ErrorText, field } from "@/ui";
import { track } from "@/lib/analytics";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { salesChecked } from "./SalesCollaboration";

type Template = Schemas["SalesTemplate"];
export function SalesTemplatePicker({
  brand,
  templates,
  disabled,
  apply,
  onLibrary,
}: {
  brand: string;
  templates: Template[];
  disabled: boolean;
  apply: (id: string) => Promise<boolean>;
  onLibrary: () => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function useTemplate(id: string) {
    if (await apply(id)) setOpen(false);
  }
  const favorites = templates.filter((t) => t.favorite);
  const matches = templates
    .filter((t) =>
      `${t.name} ${t.body}`.toLowerCase().includes(search.toLowerCase()),
    )
    .sort(
      (a, b) =>
        Number(b.favorite) - Number(a.favorite) || a.name.localeCompare(b.name),
    );
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-auto text-xs font-medium">Favorite templates</span>
        <Button
          variant="ghost"
          disabled={disabled}
          onClick={() => {
            setOpen(true);
            setError("");
            track("ui.sales_dialog_opened", { kind: "template_picker" });
          }}
        >
          <Search className="size-3.5" />
          Browse templates
        </Button>
      </div>
      <div className="flex flex-wrap gap-2">
        {favorites.map((t) => (
          <Button
            key={t.id}
            variant="ghost"
            disabled={disabled}
            onClick={() => void apply(t.id)}
          >
            <Star className="size-3.5 fill-current" />
            {t.name}
          </Button>
        ))}
        {!favorites.length && (
          <p className="text-xs text-muted-foreground">
            Star templates in Browse templates to apply them here in one click.
          </p>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        Apply inserts saved text and fills name/company fields. Use Personalize
        with AI to adapt it to this conversation.
      </p>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>Your templates</DialogTitle>
            <DialogDescription>
              Preview a template, apply its text, or star it for quick access.
              Favorites are saved for your account.
            </DialogDescription>
          </DialogHeader>
          <input
            autoFocus
            aria-label="Search templates"
            className={field}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search template names or text…"
          />
          <ErrorText>{error}</ErrorText>
          <div className="max-h-[55vh] space-y-3 overflow-y-auto">
            {matches.map((t) => (
              <div key={t.id} className="space-y-3 rounded-lg border p-4">
                <div className="flex items-center justify-between gap-2">
                  <h4 className="font-medium">{t.name}</h4>
                  <Button
                    variant="ghost"
                    aria-label={`${t.favorite ? "Unfavorite" : "Favorite"} ${t.name}`}
                    aria-pressed={t.favorite}
                    disabled={busy}
                    onClick={async () => {
                      setBusy(true);
                      setError("");
                      try {
                        salesChecked(
                          await api.PUT(
                            "/v1/brands/{brand}/sales/templates/{id}/favorite",
                            {
                              params: { path: { brand, id: t.id } },
                              body: { favorite: !t.favorite },
                            },
                          ),
                        );
                        await onLibrary();
                      } catch (e) {
                        setError(errorMessage(e));
                      } finally {
                        setBusy(false);
                      }
                    }}
                  >
                    <Star
                      className={`size-4 ${t.favorite ? "fill-current" : ""}`}
                    />
                  </Button>
                </div>
                <p className="max-h-40 overflow-y-auto whitespace-pre-wrap break-words text-sm text-muted-foreground">
                  {t.body}
                </p>
                <Button
                  disabled={disabled || busy}
                  onClick={() => void useTemplate(t.id)}
                >
                  Apply text
                </Button>
              </div>
            ))}
            {!matches.length && (
              <p className="py-6 text-sm text-muted-foreground">
                {templates.length
                  ? "No matching templates."
                  : "No templates yet. Write a reply and choose Save as template, or create one in My templates."}
              </p>
            )}
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
