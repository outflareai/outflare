// CSV files for the reports people download: one header row, then one row
// per record, every value quoted where it must be (RFC 4180), with a byte
// order mark so spreadsheet apps read accents and currency symbols right.

export type CsvValue = string | number | boolean | null | undefined;

// A value that starts like a formula (=, +, -, @) is prefixed with a quote,
// so a spreadsheet shows it as text instead of running it. Numbers stay
// numbers.
function cell(v: CsvValue): string {
  if (v === null || v === undefined) return "";
  if (typeof v === "number") return Number.isFinite(v) ? String(v) : "";
  let s = String(v);
  if (/^[=+\-@\t\r]/.test(s)) s = `'${s}`;
  return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

export function toCsv(headers: string[], rows: CsvValue[][]): string {
  return [headers, ...rows].map((r) => r.map(cell).join(",")).join("\r\n") + "\r\n";
}

// csvMoney writes an amount in micros as a decimal in its currency, with
// the currency's code: "12.34 USD". Empty when there is none.
export function csvMoney(micros: number | null | undefined, currency: string): string {
  if (micros === null || micros === undefined) return "";
  return `${(micros / 1e6).toFixed(2)} ${currency}`;
}

// A file name made safe: lowercase letters, digits, dots and dashes.
export function csvName(...parts: string[]): string {
  return `${parts.map((p) => p.toLowerCase().replace(/[^a-z0-9.]+/g, "-").replace(/^-+|-+$/g, "")).filter(Boolean).join("-")}.csv`;
}

export function downloadCsv(file: string, text: string) {
  const blob = new Blob(["﻿", text], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = file;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
