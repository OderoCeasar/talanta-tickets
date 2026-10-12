/** Why a new event has no venue to belong to yet, in words for the form. Null when it has one. */
export interface VenueProblem {
  /** Shown where the venue's name would be. */
  label: string;
  /** Shown under it once the organiser tries to create the event. */
  message: string;
  /** True when waiting or trying again cannot help: the server holds no venue at all. */
  missing: boolean;
}

export function venueProblem(query: { isError: boolean; isSuccess: boolean; count: number }): VenueProblem | null {
  if (query.isError) {
    return { label: "Could not load the venue", message: "The venue did not load. Try again, then create the event.", missing: false };
  }
  if (!query.isSuccess) {
    return { label: "Loading…", message: "The venue is still loading. Try again in a moment.", missing: false };
  }
  if (query.count > 0) return null;
  // An empty list is an answer, not a wait: saying "Loading…" here left the form stuck for good.
  return { label: "No venue has been set up", message: "An event needs a venue, and this site has none yet.", missing: true };
}
