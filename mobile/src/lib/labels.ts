// Names shown for places in the stadium. No React Native imports.

import type { components } from "@talanta/shared";

type Tier = components["schemas"]["Tier"];

const TIER_WORD: Record<Tier, string> = { LOWER: "Lower", UPPER: "Upper" };

/**
 * A stand's name with its tier: `North Stand · Upper`. The two tiers of one side are two
 * stands with the SAME name in the API ("North Stand" twice), so the name alone does not
 * tell a fan which one they are choosing. A name that already says its tier is
 * left as it is.
 */
export function standTitle(name: string, tier: Tier): string {
  const word = TIER_WORD[tier] ?? "";
  if (!word || name.toLowerCase().includes(word.toLowerCase())) return name;
  return `${name} · ${word}`;
}
