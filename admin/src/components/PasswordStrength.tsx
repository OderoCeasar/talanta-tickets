/** The API's only password rule (`Password` in the contract: 10 to 256 characters). */
export const PASSWORD_MIN = 10;
export const PASSWORD_MAX = 256;

const LEVELS = ["", "Weak", "Fair", "Good", "Strong"] as const;

/**
 * A rough guide, not a rule: the server only checks the length. Length counts for most,
 * because a long phrase beats a short string of symbols.
 */
export function passwordLevel(password: string): 0 | 1 | 2 | 3 | 4 {
  if (password.length === 0) return 0;
  if (password.length < PASSWORD_MIN) return 1;

  let points = 0;
  if (password.length >= 12) points += 1;
  if (password.length >= 16) points += 1;
  const kinds = [/[a-z]/, /[A-Z]/, /[0-9]/, /[^A-Za-z0-9]/].filter((kind) => kind.test(password)).length;
  if (kinds >= 2) points += 1;
  if (kinds >= 3) points += 1;
  // One character repeated, or very few different ones, is easy to guess at any length.
  if (new Set(password.toLowerCase()).size < 6) points = 0;

  if (points >= 3) return 4;
  if (points >= 2) return 3;
  return 2;
}

export function PasswordStrength({ password, id }: { password: string; id?: string }) {
  const level = passwordLevel(password);
  const longEnough = password.length >= PASSWORD_MIN;
  return (
    <div className="strength" data-level={level} id={id}>
      <div className="strength__bar" aria-hidden="true">
        <span />
        <span />
        <span />
        <span />
      </div>
      <ul className="checklist">
        <li data-met={longEnough}>
          <span className="checklist__mark" aria-hidden="true">
            {longEnough ? "✓" : ""}
          </span>
          At least {PASSWORD_MIN} characters
          <span className="visually-hidden">{longEnough ? " (done)" : " (not yet)"}</span>
          {!longEnough && password.length > 0 && <span>— {PASSWORD_MIN - password.length} more to go</span>}
        </li>
      </ul>
      <p className="field__hint" aria-live="polite">
        {level >= 2 ? (
          <>
            Strength: <strong>{LEVELS[level]}</strong>.{" "}
          </>
        ) : null}
        {level < 4 && "A few unrelated words are easy to remember and hard to guess."}
      </p>
    </div>
  );
}
