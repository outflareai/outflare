// Splits a large catalog CSV into batches the import endpoint takes one at
// a time. Every batch repeats the header row; quoted cells may hold commas
// and line breaks, so records are found with a small quote-aware scan
// rather than by splitting lines. Rows upsert by domain on the server, so
// the batches add up to the same catalog as one request would.

/** About 1 MB of CSV per request: well under the server's 32 MB limit and quick to write. */
export const BATCH_BYTES = 1 << 20;
/** No more than this many rows per request, however short the rows are. */
export const BATCH_ROWS = 1000;

export type CSVBatch = {
  /** The batch, starting with the header row. */
  csv: string;
  /** Data rows in the batch. */
  rows: number;
  /** Data rows in the batches before this one, to turn a row number in this batch back into the file's. */
  offset: number;
};

function utf8Length(s: string): number {
  let n = 0;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x80) n += 1;
    else if (c < 0x800) n += 2;
    else if (c >= 0xd800 && c <= 0xdbff) {
      n += 4;
      i++;
    } else n += 3;
  }
  return n;
}

/** The CSV's records as raw text (quotes kept), without their line endings. Blank lines are dropped. */
export function csvRecords(text: string): string[] {
  const out: string[] = [];
  let start = 0;
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c === '"') quoted = !quoted;
    else if (!quoted && (c === "\n" || c === "\r")) {
      const rec = text.slice(start, i);
      if (rec.trim() !== "") out.push(rec);
      if (c === "\r" && text[i + 1] === "\n") i++;
      start = i + 1;
    }
  }
  const last = text.slice(start);
  if (last.trim() !== "") out.push(last);
  return out;
}

/** Splits a CSV into batches of at most maxBytes (unless one row is larger) and maxRows rows, each with the header. */
export function csvBatches(text: string, maxBytes = BATCH_BYTES, maxRows = BATCH_ROWS): CSVBatch[] {
  const records = csvRecords(text.replace(/^\ufeff/, ""));
  if (records.length === 0) return [];
  const header = records[0];
  const headerBytes = utf8Length(header) + 1;
  const batches: CSVBatch[] = [];
  let cur: string[] = [];
  let bytes = headerBytes;
  let offset = 0;
  const flush = () => {
    if (!cur.length) return;
    batches.push({ csv: [header, ...cur].join("\n") + "\n", rows: cur.length, offset });
    offset += cur.length;
    cur = [];
    bytes = headerBytes;
  };
  for (const rec of records.slice(1)) {
    const n = utf8Length(rec) + 1;
    if (cur.length && (bytes + n > maxBytes || cur.length >= maxRows)) flush();
    cur.push(rec);
    bytes += n;
  }
  flush();
  if (!batches.length) batches.push({ csv: header + "\n", rows: 0, offset: 0 });
  return batches;
}
