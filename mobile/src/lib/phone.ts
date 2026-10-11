// Kenyan phone numbers as people type them, turned into the E.164 form the API takes.

export type PhoneResult = { ok: true; e164: string } | { ok: false; message: string };

const E164 = /^\+[1-9][0-9]{7,14}$/;

/**
 * Accepts `0712 345 678`, `0110 123 456`, `712345678`, `254712345678`, `+254 712-345-678`.
 * A number already in international form for another country is passed through, since the
 * API accepts any E.164 number.
 */
export function normalisePhone(input: string): PhoneResult {
  const trimmed = input.trim();
  if (trimmed === "") return { ok: false, message: "Enter your phone number." };
  if (/[^0-9+\s().-]/.test(trimmed)) return { ok: false, message: "Use digits only, for example 0712 345 678." };

  const international = trimmed.startsWith("+");
  const digits = trimmed.replace(/[^0-9]/g, "");

  let national: string | null = null;
  if (international || digits.startsWith("254")) {
    if (digits.startsWith("254")) {
      // People often keep the trunk zero after the country code: +254 (0)712…
      const rest = digits.slice(3);
      national = rest.startsWith("0") ? rest.slice(1) : rest;
    } else {
      const e164 = `+${digits}`;
      return E164.test(e164) ? { ok: true, e164 } : { ok: false, message: "That number does not look complete." };
    }
  } else if (digits.startsWith("0")) {
    national = digits.slice(1);
  } else {
    national = digits;
  }

  if (national.length < 9) return { ok: false, message: "That number is too short. A Kenyan mobile number has 10 digits, for example 0712 345 678." };
  if (national.length > 9) return { ok: false, message: "That number is too long. A Kenyan mobile number has 10 digits, for example 0712 345 678." };
  // Kenyan mobile numbers start 07 or 01.
  if (!/^[17]/.test(national)) return { ok: false, message: "Kenyan mobile numbers start with 07 or 01." };
  return { ok: true, e164: `+254${national}` };
}

/** `+254712345678` → `+254 712 345 678`. Other numbers are returned unchanged. */
export function formatPhone(e164: string | null | undefined): string {
  if (!e164) return "";
  const m = /^\+254(\d{3})(\d{3})(\d{3})$/.exec(e164);
  return m ? `+254 ${m[1]} ${m[2]} ${m[3]}` : e164;
}

/** `+254712345678` → `0712 345 678`, the way a Kenyan number is written locally. */
export function formatPhoneLocal(e164: string | null | undefined): string {
  if (!e164) return "";
  const m = /^\+254(\d{3})(\d{3})(\d{3})$/.exec(e164);
  return m ? `0${m[1]} ${m[2]} ${m[3]}` : e164;
}
