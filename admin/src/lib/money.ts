// Money is integer cents everywhere. The only place shillings appear is a price box and the
// text shown to people, and both are handled here with integer arithmetic on the digits.

export type ParsedMoney = { ok: true; cents: number } | { ok: false; message: string };

/** Largest price the box accepts: KES 99,999,999.99. Far above any ticket, far below 2^53. */
const MAX_SHILLING_DIGITS = 8;

const SHAPE = /^(\d{1,3}(?:,\d{3})+|\d+)(?:\.(\d{1,2}))?$/;

/**
 * Reads what an organiser typed into a price box ("1500", "1,500", "1500.50") and returns
 * cents. The string is never turned into a float: the shillings and cents digits are read
 * as two whole numbers. Anything that is not plainly a price is refused rather than guessed
 * at ("12.345", "1e3", "-5", "1,50").
 */
export function parseShillingsToCents(input: string): ParsedMoney {
  const text = input.trim();
  if (text === "") return { ok: false, message: "Enter a price in shillings, for example 1500." };
  const match = SHAPE.exec(text);
  if (!match) {
    return {
      ok: false,
      message: "Enter the price as a plain number of shillings, for example 1500 or 1,500.50.",
    };
  }
  const whole = match[1]!.replace(/,/g, "").replace(/^0+(?=\d)/, "");
  if (whole.length > MAX_SHILLING_DIGITS) return { ok: false, message: "That price is too large." };
  const fraction = (match[2] ?? "").padEnd(2, "0");
  return { ok: true, cents: Number.parseInt(whole, 10) * 100 + Number.parseInt(fraction, 10) };
}

function splitCents(cents: number): { negative: boolean; shillings: string; fraction: string } {
  const whole = Math.trunc(cents);
  const absolute = Math.abs(whole);
  const fraction = absolute % 100;
  const shillings = (absolute - fraction) / 100;
  return { negative: whole < 0, shillings: String(shillings), fraction: String(fraction).padStart(2, "0") };
}

/** "KES 1,500" or "KES 1,500.50". Whole shillings are shown without ".00". */
export function formatKes(cents: number): string {
  const { negative, shillings, fraction } = splitCents(cents);
  const grouped = shillings.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return `${negative ? "-" : ""}KES ${grouped}${fraction === "00" ? "" : `.${fraction}`}`;
}

/** What to put back into a price box for a stored price: "1500" or "1500.50". */
export function centsToShillingsInput(cents: number): string {
  const { shillings, fraction } = splitCents(cents);
  return fraction === "00" ? shillings : `${shillings}.${fraction}`;
}

/** "59,568" */
export function formatCount(value: number): string {
  return String(Math.trunc(value)).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

/**
 * A short amount for a chart axis: "KES 950", "KES 12K", "KES 1.2M". Rounded for display
 * only; never used for anything that is added up or sent to the server.
 */
export function formatKesCompact(cents: number): string {
  const shillings = Math.round(Math.abs(cents) / 100);
  const short = (value: number, unit: string) => `${String(Math.round(value * 10) / 10).replace(/\.0$/, "")}${unit}`;
  if (shillings >= 1_000_000) return `KES ${short(shillings / 1_000_000, "M")}`;
  if (shillings >= 1_000) return `KES ${short(shillings / 1_000, "K")}`;
  return `KES ${shillings}`;
}
