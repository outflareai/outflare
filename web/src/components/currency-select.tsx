// CurrencySelect picks an ISO 4217 code from a list, so a typo ("dollars")
// can't be saved as a currency. The common ones come first.

import { field } from "../ui";

const common = ["USD", "EUR", "GBP", "CAD", "AUD", "JPY", "CHF", "SEK", "NOK", "DKK", "PLN", "BRL", "INR", "MXN", "SGD"];
const all: string[] = (() => {
  try {
    return (Intl as unknown as { supportedValuesOf: (k: string) => string[] }).supportedValuesOf("currency");
  } catch {
    return common;
  }
})();
const rest = all.filter((c) => !common.includes(c));

export const isCurrency = (code: string) => common.includes(code) || all.includes(code);

type Props = {
  value: string;
  onChange: (code: string) => void;
  // empty names the "" choice ("The source's currency"); leave it out when a
  // currency is required.
  empty?: string;
  label?: string;
  disabled?: boolean;
  className?: string;
};

export function CurrencySelect({ value, onChange, empty, label = "Currency", disabled, className = "" }: Props) {
  return (
    <select aria-label={label} className={`${field} ${className}`} value={value} disabled={disabled} onChange={(e) => onChange(e.target.value)}>
      {empty !== undefined && <option value="">{empty}</option>}
      {/* A code saved before this list existed still shows, so opening a form never changes it. */}
      {value && !isCurrency(value) && <option value={value}>{value} (not a currency)</option>}
      {common.map((c) => (
        <option key={c} value={c}>{c}</option>
      ))}
      <option disabled>──────</option>
      {rest.map((c) => (
        <option key={c} value={c}>{c}</option>
      ))}
    </select>
  );
}
