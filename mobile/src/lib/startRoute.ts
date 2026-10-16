import type { components } from "@talanta/shared";

import { isGateRole } from "../features/gate/roles";

type Role = components["schemas"]["Role"];

export interface StartInput {
  status: "loading" | "signedOut" | "signedIn";
  role: Role | null | undefined;
  /** null while it is still being read from the phone. */
  welcomeSeen: boolean | null;
}

/**
 * Where the app opens. null means "not known yet, keep the splash". Gate staff go straight to
 * the scanner: the welcome slides are about buying tickets, which they never do on this phone.
 * Anyone signed in has used the app before, so they skip the slides too.
 */
export function startRoute({ status, role, welcomeSeen }: StartInput): "/gate" | "/welcome" | "/events" | null {
  if (status === "loading") return null;
  if (status === "signedIn") return isGateRole(role) ? "/gate" : "/events";
  if (welcomeSeen === null) return null;
  return welcomeSeen ? "/events" : "/welcome";
}
