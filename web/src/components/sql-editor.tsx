import { useEffect, useRef } from "react";
import { basicSetup } from "codemirror";
import { EditorView, keymap, placeholder } from "@codemirror/view";
import { Compartment, EditorState, Prec } from "@codemirror/state";
import { PostgreSQL, sql } from "@codemirror/lang-sql";

// The SQL editor of Query Studio: CodeMirror 6 with Postgres highlighting
// and autocomplete from the tables and columns the brand's query can read.
// Cmd/Ctrl+Enter runs the query.

export type SqlSchema = Record<string, string[]>;

export function SqlEditor({ value, onChange, onRun, schema, editor, readOnly = false, label }: {
  value: string;
  onChange: (v: string) => void;
  onRun: () => void;
  schema: SqlSchema;
  // Set to the editor once it exists, so the page can insert a name at
  // the cursor.
  editor: { current: EditorView | null };
  readOnly?: boolean;
  label: string;
}) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const lang = useRef(new Compartment());
  const ro = useRef(new Compartment());
  // The latest callbacks, read by the editor's listeners.
  const cb = useRef({ onChange, onRun });
  cb.current = { onChange, onRun };

  useEffect(() => {
    if (!host.current) return;
    const v = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: value,
        extensions: [
          Prec.highest(keymap.of([{ key: "Mod-Enter", preventDefault: true, run: () => (cb.current.onRun(), true) }])),
          basicSetup,
          lang.current.of(sql({ dialect: PostgreSQL, schema, upperCaseKeywords: false })),
          ro.current.of(EditorState.readOnly.of(readOnly)),
          placeholder("select count(*) from people"),
          EditorView.contentAttributes.of({ "aria-label": label }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) cb.current.onChange(u.state.doc.toString());
          }),
          EditorView.theme({
            "&": { fontSize: "13px", backgroundColor: "transparent", minHeight: "180px", maxHeight: "50vh" },
            ".cm-scroller": { fontFamily: "var(--font-mono)", overflow: "auto" },
            ".cm-content": { minHeight: "180px" },
            ".cm-gutters": { backgroundColor: "transparent", borderRight: "1px solid var(--border)", color: "var(--muted-foreground)" },
            ".cm-activeLine, .cm-activeLineGutter": { backgroundColor: "color-mix(in oklab, var(--muted) 60%, transparent)" },
            "&.cm-focused": { outline: "none" },
            ".cm-tooltip": { backgroundColor: "var(--popover)", color: "var(--popover-foreground)", border: "1px solid var(--border)" },
          }),
        ],
      }),
    });
    view.current = v;
    editor.current = v;
    return () => {
      v.destroy();
      view.current = null;
      editor.current = null;
    };
    // The editor is made once; the effects below keep it current.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    view.current?.dispatch({ effects: lang.current.reconfigure(sql({ dialect: PostgreSQL, schema, upperCaseKeywords: false })) });
  }, [schema]);

  useEffect(() => {
    view.current?.dispatch({ effects: ro.current.reconfigure(EditorState.readOnly.of(readOnly)) });
  }, [readOnly]);

  // A value set from outside (a saved query or version opened).
  useEffect(() => {
    const v = view.current;
    if (v && v.state.doc.toString() !== value) v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: value } });
  }, [value]);

  return <div ref={host} className="overflow-hidden rounded-md border bg-card shadow-xs focus-within:border-ring" />;
}

// insertAt puts text at the editor's cursor, with a space before it when
// it would run into the word there.
export function insertAt(v: EditorView | null, text: string) {
  if (!v) return;
  const { from, to } = v.state.selection.main;
  const before = from > 0 ? v.state.sliceDoc(from - 1, from) : "";
  const insert = (before && !/[\s(,.]/.test(before) ? " " : "") + text;
  v.dispatch({ changes: { from, to, insert }, selection: { anchor: from + insert.length } });
  v.focus();
}
