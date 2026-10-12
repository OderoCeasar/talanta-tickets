import type { components } from "@talanta/shared";

type Role = components["schemas"]["Role"];

/** The roles the gate endpoints accept. They land in gate mode instead of the fan tabs. */
export function isGateRole(role: Role | null | undefined): boolean {
  return role === "GATE_STAFF" || role === "ADMIN";
}
