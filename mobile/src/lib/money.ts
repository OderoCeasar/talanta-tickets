// Money is integer cents everywhere (CLAUDE.md). Nothing here divides into a float: the
// shilling and cent parts are split with integer arithmetic and joined as text.

function assertCents(cents: number): void {
  if (!Number.isSafeInteger(cents)) throw new RangeError(`Money must be a whole number of cents, got ${cents}`);
}

function groupThousands(digits: string): string {
  let out = "";
  for (let i = 0; i < digits.length; i++) {
    if (i > 0 && (digits.length - i) % 3 === 0) out += ",";
    out += digits[i];
  }
  return out;
}

/** `150000` → `KES 1,500`; `150050` → `KES 1,500.50`. Cents are shown only when there are any. */
export function formatKes(cents: number): string {
  assertCents(cents);
  const negative = cents < 0;
  const abs = negative ? -cents : cents;
  const rem = abs % 100;
  const shillings = (abs - rem) / 100;
  const text = groupThousands(String(shillings)) + (rem === 0 ? "" : `.${String(rem).padStart(2, "0")}`);
  return `${negative ? "-" : ""}KES ${text}`;
}

/** Adds amounts in cents. Refuses anything that is not a whole number. */
export function sumCents(amounts: readonly number[]): number {
  let total = 0;
  for (const a of amounts) {
    assertCents(a);
    total += a;
  }
  assertCents(total);
  return total;
}

/** `12562` → `12,562`, for seat counts. */
export function formatCount(n: number): string {
  return groupThousands(String(Math.max(0, Math.trunc(n))));
}
