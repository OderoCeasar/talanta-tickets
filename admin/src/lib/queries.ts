import { unwrap } from "@talanta/shared";
import { useInfiniteQuery, useIsMutating, useQuery } from "@tanstack/react-query";
import { api, type Schemas } from "./api";

export type AdminEvent = Schemas["Event"];
export type EventStatus = Schemas["EventStatus"];
export type Category = Schemas["Category"];
export type Assignments = Schemas["Assignments"];
export type VenueDetail = Schemas["VenueDetail"];
export type Stand = VenueDetail["stands"][number];
export type Block = Schemas["Block"];
export type BlockLayout = Schemas["BlockLayout"];
export type Template = Schemas["Template"];
export type Readiness = Schemas["Readiness"];
export type ReadinessProblem = Schemas["ReadinessProblem"];
export type SalesSummary = Schemas["SalesSummary"];
export type SalesBucket = Schemas["SalesBucket"];

/**
 * openapi-fetch 0.17 widens tuples in response types: an `[x, y]` point or a seat tuple
 * comes back typed as an array of the union of its members. The wire data is unchanged, so
 * this is the one place where an answer is given back its generated schema type.
 */
function asSchema<K extends keyof Schemas>(_schema: K, value: unknown): Schemas[K] {
  return value as Schemas[K];
}

export const keys = {
  venues: ["venues"] as const,
  venue: (venueId: number) => ["venues", venueId] as const,
  templates: (venueId: number) => ["venues", venueId, "templates"] as const,
  blockLayout: (blockId: number) => ["blocks", blockId, "layout"] as const,
  events: ["events"] as const,
  eventList: (status: EventStatus | "ALL") => ["events", "list", status] as const,
  event: (eventId: number) => ["events", eventId] as const,
  categories: (eventId: number) => ["events", eventId, "categories"] as const,
  assignments: (eventId: number) => ["events", eventId, "assignments"] as const,
  readiness: (eventId: number) => ["events", eventId, "readiness"] as const,
  sales: (eventId: number) => ["events", eventId, "sales"] as const,
  refundBookings: (eventId: number) => ["events", eventId, "refund-bookings"] as const,
  alerts: ["alerts"] as const,
  alertList: (acknowledged: boolean, eventId: number | null) => ["alerts", "list", acknowledged, eventId] as const,
  openAlertCount: (eventId: number | null) => ["alerts", "open-count", eventId] as const,
  refunds: ["refunds"] as const,
  refundList: (status: Schemas["RefundStatus"], eventId: number | null) => ["refunds", "list", status, eventId] as const,
  pendingRefundCount: ["refunds", "pending-count"] as const,
  publish: (eventId: number) => ["publish", eventId] as const,
  cancel: (eventId: number) => ["cancel", eventId] as const,
};

export function fetchVenue(venueId: number): Promise<VenueDetail> {
  return unwrap(api.GET("/admin/venues/{venueId}", { params: { path: { venueId } } })).then((data) =>
    asSchema("VenueDetail", data),
  );
}

export function fetchBlockLayout(blockId: number): Promise<BlockLayout> {
  return unwrap(api.GET("/admin/blocks/{blockId}/seats", { params: { path: { blockId } } })).then((data) =>
    asSchema("BlockLayout", data),
  );
}

export function generateSeats(blockId: number, body: Schemas["GenerateSeats"]): Promise<BlockLayout> {
  return unwrap(api.POST("/admin/blocks/{blockId}/generate-seats", { params: { path: { blockId } }, body })).then(
    (data) => asSchema("BlockLayout", data),
  );
}

export function createBlock(venueId: number, body: Schemas["CreateBlock"]): Promise<Block> {
  return unwrap(api.POST("/admin/venues/{venueId}/blocks", { params: { path: { venueId } }, body })).then((data) =>
    asSchema("Block", data),
  );
}

export function updateBlock(blockId: number, body: Schemas["UpdateBlock"]): Promise<Block> {
  return unwrap(api.PATCH("/admin/blocks/{blockId}", { params: { path: { blockId } }, body })).then((data) =>
    asSchema("Block", data),
  );
}

export function useVenues() {
  return useQuery({ queryKey: keys.venues, queryFn: () => unwrap(api.GET("/admin/venues")), staleTime: 60_000 });
}

export function useVenue(venueId: number | undefined) {
  return useQuery({
    queryKey: keys.venue(venueId ?? 0),
    queryFn: () => fetchVenue(venueId!),
    enabled: venueId !== undefined,
    staleTime: 60_000,
  });
}

export function useTemplates(venueId: number | undefined) {
  return useQuery({
    queryKey: keys.templates(venueId ?? 0),
    queryFn: () => unwrap(api.GET("/admin/venues/{venueId}/templates", { params: { path: { venueId: venueId! } } })),
    enabled: venueId !== undefined,
  });
}

export function useBlockLayout(blockId: number | null) {
  return useQuery({
    queryKey: keys.blockLayout(blockId ?? 0),
    queryFn: () => fetchBlockLayout(blockId!),
    enabled: blockId !== null,
  });
}

export function useAdminEvents(status: EventStatus | "ALL" = "ALL", pageSize = 20) {
  return useInfiniteQuery({
    queryKey: [...keys.eventList(status), pageSize],
    queryFn: ({ pageParam }) =>
      unwrap(
        api.GET("/admin/events", {
          params: { query: { limit: pageSize, cursor: pageParam, status: status === "ALL" ? undefined : status } },
        }),
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
}

export function useEvent(eventId: number) {
  return useQuery({
    queryKey: keys.event(eventId),
    queryFn: () => unwrap(api.GET("/admin/events/{eventId}", { params: { path: { eventId } } })),
  });
}

export function useCategories(eventId: number) {
  return useQuery({
    queryKey: keys.categories(eventId),
    queryFn: () => unwrap(api.GET("/admin/events/{eventId}/categories", { params: { path: { eventId } } })),
  });
}

export function useAssignments(eventId: number) {
  return useQuery({
    queryKey: keys.assignments(eventId),
    queryFn: () => unwrap(api.GET("/admin/events/{eventId}/assignments", { params: { path: { eventId } } })),
  });
}

export function useReadiness(eventId: number, enabled = true) {
  return useQuery({
    queryKey: keys.readiness(eventId),
    queryFn: () => unwrap(api.GET("/admin/events/{eventId}/readiness", { params: { path: { eventId } } })),
    enabled,
    staleTime: 0,
  });
}

/**
 * True while this browser is waiting for a publish to answer. With an event ID: that event.
 * Without: any event.
 *
 * The server holds the event (and the venue row) locked for the whole publish, and anything
 * else sent for the event meanwhile is answered 409 `EVENT_BUSY` after a few seconds. So
 * every editing action for the event, and block edits at the venue, are switched off until
 * the publish has answered, instead of letting the organiser run into that.
 */
export function usePublishInFlight(eventId?: number): boolean {
  return useIsMutating({ mutationKey: eventId === undefined ? ["publish"] : keys.publish(eventId) }) > 0;
}

/** True while this browser is waiting for a cancel of this event to answer. The event is locked as it is during a publish. */
export function useCancelInFlight(eventId: number): boolean {
  return useIsMutating({ mutationKey: keys.cancel(eventId) }) > 0;
}

export const STATUS_LABELS: Record<EventStatus, string> = {
  DRAFT: "Draft",
  PUBLISHED: "Published",
  ON_SALE: "On sale",
  SOLD_OUT: "Sold out",
  CANCELLED: "Cancelled",
  COMPLETED: "Completed",
};

/** What an organiser needs to know about a status in one line. */
export const STATUS_HINTS: Record<EventStatus, string> = {
  DRAFT: "Not visible to fans yet.",
  PUBLISHED: "Visible to fans. Ticket sales are not open right now.",
  ON_SALE: "Fans can buy tickets now.",
  SOLD_OUT: "Every seat is sold.",
  CANCELLED: "Cancelled. Tickets are void.",
  COMPLETED: "The match has been played.",
};

export function isPublished(status: EventStatus): boolean {
  return status === "PUBLISHED" || status === "ON_SALE" || status === "SOLD_OUT";
}

/** How often the lists staff act on (alerts, the refund queue) and their badges re-check the server. */
const ATTENTION_REFRESH_MS = 30_000;
/** The largest page the API gives. A badge counts one page and shows "100+" when there is more. */
const COUNT_PAGE = 100;

/** One page of alerts, newest first: the open ones (`acknowledged` false) or the acknowledged ones. */
export function useAlerts(acknowledged: boolean, eventId: number | null, enabled = true) {
  return useInfiniteQuery({
    queryKey: keys.alertList(acknowledged, eventId),
    queryFn: ({ pageParam }) =>
      unwrap(api.GET("/admin/alerts", { params: { query: { acknowledged, event_id: eventId ?? undefined, limit: 20, cursor: pageParam } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    enabled,
    staleTime: 0,
    refetchInterval: ATTENTION_REFRESH_MS,
    refetchOnWindowFocus: true,
  });
}

/**
 * How many alerts nobody has acknowledged: everywhere, or for one event. `more` is true when
 * the server has further pages, so the true number is above `count`.
 */
export function useOpenAlertCount(eventId: number | null = null, enabled = true) {
  return useQuery({
    queryKey: keys.openAlertCount(eventId),
    queryFn: () =>
      unwrap(api.GET("/admin/alerts", { params: { query: { acknowledged: false, event_id: eventId ?? undefined, limit: COUNT_PAGE } } })),
    select: (page) => ({ count: page.items.length, more: page.next_cursor !== null }),
    enabled,
    staleTime: 0,
    refetchInterval: ATTENTION_REFRESH_MS,
    refetchOnWindowFocus: true,
  });
}

/** One page of the refund queue, oldest first. */
export function useRefunds(status: Schemas["RefundStatus"], eventId: number | null) {
  return useInfiniteQuery({
    queryKey: keys.refundList(status, eventId),
    queryFn: ({ pageParam }) =>
      unwrap(api.GET("/admin/refunds", { params: { query: { status, event_id: eventId ?? undefined, limit: 20, cursor: pageParam } } })),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    staleTime: 0,
    refetchInterval: ATTENTION_REFRESH_MS,
    refetchOnWindowFocus: true,
  });
}

/** How many refunds are waiting to be paid back, across all events. */
export function usePendingRefundCount() {
  return useQuery({
    queryKey: keys.pendingRefundCount,
    queryFn: () => unwrap(api.GET("/admin/refunds", { params: { query: { status: "PENDING", limit: COUNT_PAGE } } })),
    select: (page) => ({ count: page.items.length, more: page.next_cursor !== null }),
    staleTime: 0,
    refetchInterval: ATTENTION_REFRESH_MS,
    refetchOnWindowFocus: true,
  });
}
