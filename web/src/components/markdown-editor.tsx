import { Fragment, useEffect, useRef, useState, type ReactNode } from "react";
import { EditorContent, useEditor, useEditorState, type Editor } from "@tiptap/react";
import StarterKit from "@tiptap/starter-kit";
import { Markdown as MarkdownExtension } from "@tiptap/markdown";
import { TableKit } from "@tiptap/extension-table";
import { TaskItem, TaskList } from "@tiptap/extension-list";
import { Placeholder } from "@tiptap/extensions";
import { cn } from "@/lib/utils";
import { track } from "@/lib/analytics";
import { ask } from "../ui";

// A writing box for knowledge that anyone can use without knowing Markdown:
// what you see is what you get, with a toolbar, "/" at the start of a line
// for headings, lists, tables and the rest, and the usual shortcuts (Ctrl
// or Cmd with B, I, K, Z). Pasting from Google Docs, Word or a web page
// keeps headings, lists, links and tables. It stores Markdown, which every
// AI in Outflare reads as it is.

type Command = { id: string; label: string; hint: string; keys: string[]; run: (e: Editor) => void };

const commands: Command[] = [
  { id: "h2", label: "Heading", hint: "A section title", keys: ["heading", "title", "h2"], run: (e) => e.chain().focus().toggleHeading({ level: 2 }).run() },
  { id: "h3", label: "Subheading", hint: "A smaller title", keys: ["subheading", "h3"], run: (e) => e.chain().focus().toggleHeading({ level: 3 }).run() },
  { id: "ul", label: "Bulleted list", hint: "A simple list", keys: ["bullet", "list", "ul"], run: (e) => e.chain().focus().toggleBulletList().run() },
  { id: "ol", label: "Numbered list", hint: "Steps in order", keys: ["numbered", "steps", "ol"], run: (e) => e.chain().focus().toggleOrderedList().run() },
  { id: "todo", label: "Checklist", hint: "Things to tick off", keys: ["check", "todo", "task"], run: (e) => e.chain().focus().toggleTaskList().run() },
  { id: "quote", label: "Quote", hint: "Words to say exactly", keys: ["quote", "say"], run: (e) => e.chain().focus().toggleBlockquote().run() },
  { id: "table", label: "Table", hint: "Rows and columns", keys: ["table", "grid"], run: (e) => e.chain().focus().insertTable({ rows: 3, cols: 2, withHeaderRow: true }).run() },
  { id: "code", label: "Code", hint: "Text kept exactly as typed", keys: ["code"], run: (e) => e.chain().focus().toggleCodeBlock().run() },
  { id: "hr", label: "Divider", hint: "A line between parts", keys: ["divider", "line", "hr"], run: (e) => e.chain().focus().setHorizontalRule().run() },
];

type Slash = { from: number; to: number; q: string; i: number; top: number; left: number };

export function MarkdownEditor({
  value,
  onChange,
  placeholder,
  readOnly,
  label,
  minRows = 14,
  onSave,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  readOnly?: boolean;
  label: string;
  minRows?: number;
  onSave?: () => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  // The Markdown last sent out, so a value coming back in isn't re-parsed
  // (which would move the cursor) and a new one from outside (a restored
  // version) replaces the text.
  const emitted = useRef(value);
  const [slash, setSlash] = useState<Slash | null>(null);
  const slashRef = useRef<Slash | null>(null);
  slashRef.current = slash;
  const matches = (s: Slash | null) => (s ? commands.filter((c) => !s.q || c.label.toLowerCase().includes(s.q) || c.keys.some((k) => k.startsWith(s.q))) : []);
  const saveRef = useRef(onSave);
  saveRef.current = onSave;

  const editor = useEditor({
    extensions: [
      StarterKit.configure({ heading: { levels: [2, 3] }, link: { openOnClick: false, autolink: true, defaultProtocol: "https" } }),
      TableKit.configure({ table: { resizable: false } }),
      TaskList,
      TaskItem.configure({ nested: true }),
      Placeholder.configure({ placeholder: placeholder ?? "" }),
      MarkdownExtension,
    ],
    content: value,
    contentType: "markdown",
    editable: !readOnly,
    immediatelyRender: true,
    shouldRerenderOnTransaction: false,
    editorProps: {
      attributes: { class: "kb-doc outline-none", "aria-label": label, role: "textbox", "aria-multiline": "true" },
      handleKeyDown: (_view, e) => {
        const s = slashRef.current;
        const list = matches(s);
        if (s && list.length) {
          if (e.key === "ArrowDown" || e.key === "ArrowUp") {
            const d = e.key === "ArrowDown" ? 1 : -1;
            setSlash({ ...s, i: (s.i + d + list.length) % list.length });
            return true;
          }
          if (e.key === "Enter" || e.key === "Tab") {
            pick(list[Math.min(s.i, list.length - 1)]);
            return true;
          }
          if (e.key === "Escape") {
            setSlash(null);
            return true;
          }
        }
        if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s" && saveRef.current) {
          e.preventDefault();
          saveRef.current();
          return true;
        }
        if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
          e.preventDefault();
          void link("key");
          return true;
        }
        return false;
      },
      handlePaste: (_view, e) => {
        if (e.clipboardData?.types.includes("text/html")) track("ui.knowledge_pasted", { formatted: true });
        return false;
      },
    },
    onUpdate: ({ editor }) => {
      const md = editor.getMarkdown();
      emitted.current = md;
      onChange(md);
      findSlash(editor);
    },
    onSelectionUpdate: ({ editor }) => findSlash(editor),
    onBlur: () => setTimeout(() => setSlash(null), 150),
  });

  // A value set from outside (a restored version, a template) replaces the text.
  useEffect(() => {
    if (!editor || value === emitted.current) return;
    emitted.current = value;
    editor.commands.setContent(value, { contentType: "markdown", emitUpdate: false });
  }, [editor, value]);

  useEffect(() => {
    editor?.setEditable(!readOnly);
  }, [editor, readOnly]);

  // "/" typed at the start of an empty line, then letters, opens the menu.
  function findSlash(e: Editor) {
    const { $from, empty } = e.state.selection;
    if (!empty || $from.parent.type.name !== "paragraph") return setSlash(null);
    const typed = $from.parent.textBetween(0, $from.parentOffset);
    const m = typed.match(/^\/([a-z0-9]*)$/i);
    if (!m || !box.current) return setSlash(null);
    const at = e.view.coordsAtPos($from.pos);
    const outer = box.current.getBoundingClientRect();
    const q = m[1].toLowerCase();
    setSlash((prev) => ({ from: $from.start(), to: $from.pos, q, i: prev?.q === q ? prev.i : 0, top: at.bottom - outer.top + 4, left: Math.max(at.left - outer.left, 8) }));
  }

  function pick(c: Command) {
    const s = slashRef.current;
    if (!editor || !s) return;
    track("ui.knowledge_formatted", { command: c.id, how: "slash" });
    editor.chain().focus().deleteRange({ from: s.from, to: s.to }).run();
    c.run(editor);
    setSlash(null);
  }

  async function link(how: "toolbar" | "key") {
    if (!editor) return;
    track("ui.knowledge_formatted", { command: "link", how });
    const current = editor.getAttributes("link").href as string | undefined;
    const r = await ask({
      title: current ? "Change link" : "Add a link",
      input: { label: "Web address", value: current ?? "", placeholder: "https://northpeak.example/pricing", maxLength: 2000 },
      choices: [
        ...(current ? [{ label: "Remove link", value: "remove", variant: "ghost" as const }] : []),
        { label: "Cancel", value: "cancel", variant: "ghost" },
        { label: "Save", value: "ok", variant: "primary" },
      ],
    });
    if (r?.choice === "remove") return void editor.chain().focus().extendMarkRange("link").unsetLink().run();
    if (r?.choice !== "ok" || !r.value.trim()) return;
    let href = r.value.trim();
    if (!/^(https?:|mailto:)/i.test(href)) href = `https://${href}`;
    const chain = editor.chain().focus().extendMarkRange("link");
    if (editor.state.selection.empty && !current) chain.insertContent({ type: "text", text: r.value.trim(), marks: [{ type: "link", attrs: { href } }] }).run();
    else chain.setLink({ href }).run();
  }

  const active = useEditorState({
    editor,
    selector: ({ editor: e }) =>
      e
        ? {
            h2: e.isActive("heading", { level: 2 }),
            bold: e.isActive("bold"),
            italic: e.isActive("italic"),
            ul: e.isActive("bulletList"),
            ol: e.isActive("orderedList"),
            todo: e.isActive("taskList"),
            link: e.isActive("link"),
            quote: e.isActive("blockquote"),
            table: e.isActive("table"),
          }
        : null,
  });

  const tools: { id: keyof NonNullable<typeof active>; label: string; icon: ReactNode; run: () => void }[] = editor
    ? [
        { id: "h2", label: "Heading", icon: <span className="font-semibold">H</span>, run: () => editor.chain().focus().toggleHeading({ level: 2 }).run() },
        { id: "bold", label: "Bold (Ctrl+B)", icon: <span className="font-bold">B</span>, run: () => editor.chain().focus().toggleBold().run() },
        { id: "italic", label: "Italic (Ctrl+I)", icon: <span className="font-serif italic">I</span>, run: () => editor.chain().focus().toggleItalic().run() },
        { id: "ul", label: "Bulleted list", icon: <span>•≡</span>, run: () => editor.chain().focus().toggleBulletList().run() },
        { id: "ol", label: "Numbered list", icon: <span>1≡</span>, run: () => editor.chain().focus().toggleOrderedList().run() },
        { id: "todo", label: "Checklist", icon: <span>☑</span>, run: () => editor.chain().focus().toggleTaskList().run() },
        { id: "link", label: "Link (Ctrl+K)", icon: <span className="underline">a</span>, run: () => void link("toolbar") },
        { id: "quote", label: "Quote", icon: <span>“</span>, run: () => editor.chain().focus().toggleBlockquote().run() },
        { id: "table", label: "Table", icon: <span>▦</span>, run: () => editor.chain().focus().insertTable({ rows: 3, cols: 2, withHeaderRow: true }).run() },
      ]
    : [];

  const list = matches(slash);
  return (
    <div className="rounded-lg border" ref={box}>
      {!readOnly && (
        <div className="sticky top-0 z-10 flex flex-wrap items-center gap-0.5 rounded-t-lg border-b bg-background px-1.5 py-1">
          {tools.map((t) => (
            <button
              key={t.id}
              type="button"
              title={t.label}
              aria-label={t.label}
              aria-pressed={!!active?.[t.id]}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => {
                if (t.id !== "link") track("ui.knowledge_formatted", { command: t.id, how: "toolbar" });
                t.run();
              }}
              className={cn("inline-flex h-7 min-w-7 items-center justify-center rounded px-1.5 text-[13px] hover:bg-accent hover:text-foreground", active?.[t.id] ? "bg-accent text-foreground" : "text-muted-foreground")}
            >
              {t.icon}
            </button>
          ))}
          {active?.table && editor && (
            <span className="ml-1 flex items-center gap-0.5 border-l pl-1.5 text-[12px] text-muted-foreground">
              {[
                ["+ Row", () => editor.chain().focus().addRowAfter().run()],
                ["+ Column", () => editor.chain().focus().addColumnAfter().run()],
                ["− Row", () => editor.chain().focus().deleteRow().run()],
                ["− Column", () => editor.chain().focus().deleteColumn().run()],
              ].map(([l, f]) => (
                <button key={l as string} type="button" onMouseDown={(e) => e.preventDefault()} onClick={f as () => void} className="rounded px-1.5 py-0.5 hover:bg-accent hover:text-foreground">
                  {l as string}
                </button>
              ))}
            </span>
          )}
          <span className="ml-auto hidden pr-1 text-[11px] text-muted-foreground sm:inline">Type / for more</span>
        </div>
      )}
      <div className="relative cursor-text px-4 py-3" style={{ minHeight: `${minRows * 1.4}rem` }} onClick={() => editor?.commands.focus()}>
        <EditorContent editor={editor} />
        {slash && list.length > 0 && (
          <ul role="listbox" aria-label="Insert" className="absolute z-20 w-64 overflow-hidden rounded-md border bg-popover p-1 shadow-md" style={{ top: slash.top, left: slash.left }}>
            {list.map((c, i) => (
              <li
                key={c.id}
                role="option"
                aria-selected={i === slash.i}
                onMouseDown={(e) => {
                  e.preventDefault();
                  pick(c);
                }}
                className={cn("cursor-pointer rounded px-2 py-1.5 text-[13px]", i === slash.i && "bg-accent")}
              >
                <span className="font-medium">{c.label}</span> <span className="text-muted-foreground">{c.hint}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

// Markdown shows knowledge formatted. It understands what the editor
// writes (headings, lists, checklists, quotes, code, tables, dividers,
// bold, italics, code and links) and builds elements, never HTML, so
// nothing in the text can run.
export function Markdown({ text, className }: { text: string; className?: string }) {
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  const out: ReactNode[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
      continue;
    }
    const key = out.length;
    if (line.startsWith("```")) {
      const code: string[] = [];
      i++;
      while (i < lines.length && !lines[i].startsWith("```")) code.push(lines[i++]);
      i++;
      out.push(<pre key={key} className="my-3 overflow-x-auto rounded-md bg-muted px-3 py-2 font-mono text-[12px]">{code.join("\n")}</pre>);
      continue;
    }
    const h = line.match(/^(#{1,6})\s+(.*)$/);
    if (h) {
      const Tag = h[1].length <= 2 ? "h2" : "h3";
      out.push(<Tag key={key} className={Tag === "h2" ? "mt-5 mb-2 text-base font-semibold first:mt-0" : "mt-4 mb-1.5 text-sm font-semibold first:mt-0"}>{spans(h[2])}</Tag>);
      i++;
      continue;
    }
    if (/^(-{3,}|\*{3,})\s*$/.test(line)) {
      out.push(<hr key={key} className="my-4" />);
      i++;
      continue;
    }
    if (line.startsWith(">")) {
      const q: string[] = [];
      while (i < lines.length && lines[i].startsWith(">")) q.push(lines[i++].replace(/^>\s?/, ""));
      out.push(<blockquote key={key} className="my-3 border-l-2 pl-3 text-muted-foreground">{q.map((l, j) => <p key={j}>{spans(l)}</p>)}</blockquote>);
      continue;
    }
    if (line.startsWith("|") && lines[i + 1]?.match(/^\|\s*:?-{2,}/)) {
      const cells = (l: string) => l.replace(/^\||\|\s*$/g, "").split(/(?<!\\)\|/).map((c) => c.replace(/\\\|/g, "|").trim());
      const head = cells(line);
      i += 2;
      const body: string[][] = [];
      while (i < lines.length && lines[i].startsWith("|")) body.push(cells(lines[i++]));
      out.push(
        <div key={key} className="my-3 overflow-x-auto">
          <table className="w-full border-collapse text-[13px]">
            <thead><tr>{head.map((c, j) => <th key={j} className="border px-2 py-1 text-left font-medium">{spans(c)}</th>)}</tr></thead>
            <tbody>{body.map((r, j) => <tr key={j}>{head.map((_, k) => <td key={k} className="border px-2 py-1">{spans(r[k] ?? "")}</td>)}</tr>)}</tbody>
          </table>
        </div>,
      );
      continue;
    }
    const li = /^\s*([-*]|\d+\.)\s+/;
    if (li.test(line)) {
      const ordered = /^\s*\d+\./.test(line);
      const items: ReactNode[] = [];
      while (i < lines.length && li.test(lines[i])) {
        const raw = lines[i].replace(li, "");
        const todo = raw.match(/^\[([ x])\]\s+(.*)$/);
        const indent = Math.floor((lines[i].match(/^\s*/)?.[0].length ?? 0) / 2);
        items.push(
          <li key={i} style={indent ? { marginLeft: `${indent * 1.25}rem` } : undefined}>
            {todo ? <><input type="checkbox" checked={todo[1] === "x"} readOnly className="mr-1.5 align-middle" aria-label="Done" />{spans(todo[2])}</> : spans(raw)}
          </li>,
        );
        i++;
      }
      const List = ordered ? "ol" : "ul";
      out.push(<List key={key} className={cn("my-2 space-y-1 pl-5", ordered ? "list-decimal" : "list-disc")}>{items}</List>);
      continue;
    }
    const para: string[] = [];
    while (i < lines.length && lines[i].trim() && !/^(#{1,6}\s|>|```|\||\s*([-*]|\d+\.)\s|-{3,}\s*$)/.test(lines[i])) para.push(lines[i++]);
    if (!para.length) para.push(lines[i++]);
    out.push(<p key={key} className="my-2 leading-relaxed">{para.map((l, j) => <Fragment key={j}>{j > 0 && <br />}{spans(l)}</Fragment>)}</p>);
  }
  return <div className={cn("text-sm", className)}>{out}</div>;
}

// spans formats one line: `code`, **bold**, _italic_ or *italic*, and
// [links](https://…). Only http, https and mailto links become links.
function spans(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /(`[^`]+`)|(\*\*[^*]+\*\*)|(_[^_]+_|\*[^*]+\*)|(\[[^\]]+\]\([^)\s]+\))/g;
  let last = 0;
  for (const m of text.matchAll(re)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const s = m[0];
    const k = `${m.index}`;
    if (m[1]) out.push(<code key={k} className="rounded bg-muted px-1 font-mono text-[12px]">{s.slice(1, -1)}</code>);
    else if (m[2]) out.push(<strong key={k}>{spans(s.slice(2, -2))}</strong>);
    else if (m[3]) out.push(<em key={k}>{spans(s.slice(1, -1))}</em>);
    else {
      const [, label, href] = s.match(/^\[([^\]]+)\]\(([^)]+)\)$/) ?? [];
      out.push(/^(https?:|mailto:)/i.test(href ?? "") ? <a key={k} href={href} target="_blank" rel="noreferrer noopener" className="text-primary underline underline-offset-2">{label}</a> : label);
    }
    last = m.index + s.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}
