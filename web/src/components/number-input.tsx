// NumberInput is a whole-number field that lets you type. A plain
// controlled input that clamps on every keystroke snaps to its minimum the
// moment it is cleared, so typing 45 over a 1 gives 145. This one keeps
// what you type, hands on a value only while it is in range, and settles
// on blur.

import { useEffect, useState } from "react";
import { field } from "../ui";

type Props = { value: number; onChange: (n: number) => void; min: number; max: number; label: string; disabled?: boolean; className?: string };

export function NumberInput({ value, onChange, min, max, label, disabled, className = "" }: Props) {
  const [text, setText] = useState(String(value));
  const [editing, setEditing] = useState(false);
  useEffect(() => {
    if (!editing) setText(String(value));
  }, [value, editing]);
  const n = Number(text);
  const ok = text.trim() !== "" && Number.isInteger(n) && n >= min && n <= max;
  return (
    <input
      aria-label={label}
      aria-invalid={editing && !ok}
      title={`${min.toLocaleString()} to ${max.toLocaleString()}`}
      type="number"
      inputMode="numeric"
      min={min}
      max={max}
      className={`${field} ${className}`}
      value={text}
      disabled={disabled}
      onFocus={() => setEditing(true)}
      onChange={(e) => {
        setText(e.target.value);
        const v = Number(e.target.value);
        if (e.target.value.trim() !== "" && Number.isInteger(v) && v >= min && v <= max) onChange(v);
      }}
      onBlur={() => {
        setEditing(false);
        // Out of range or empty: the nearest allowed value, or what it was.
        const settled = text.trim() === "" || !Number.isFinite(n) ? value : Math.max(min, Math.min(max, Math.round(n)));
        setText(String(settled));
        if (settled !== value) onChange(settled);
      }}
    />
  );
}
