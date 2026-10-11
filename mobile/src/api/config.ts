import { API_PREFIX } from "@talanta/shared";

// The API origin, with no trailing slash and no `/v1`. Baked in at build time: EAS builds set
// it in eas.json; in development it comes from mobile/.env.local (see mobile/README.md).
// An Android emulator reaches the host machine at 10.0.2.2, never at "localhost".
const origin = (process.env.EXPO_PUBLIC_API_URL ?? "http://10.0.2.2:8080").replace(/\/+$/, "");

export const API_ORIGIN = origin;
/** What the shared client is given: origin plus the `/v1` prefix every route sits under. */
export const API_BASE_URL = `${origin}${API_PREFIX}`;
