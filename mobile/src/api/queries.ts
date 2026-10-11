// TanStack Query hooks for everything the server owns. Request and response types all come
// from the generated schema through the shared client; nothing is typed by hand here.

import { ApiError, unwrap } from "@talanta/shared";
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api, type Schemas } from "./client";
import { readStoredTickets, writeStoredTickets, writeStoredUser } from "./storage";

export const keys = {
  me: ["me"] as const,
  events: ["events", "list"] as const,
  event: (id: number) => ["events", id, "detail"] as const,
  categories: (id: number) => ["events", id, "categories"] as const,
  map: (id: number) => ["events", id, "map"] as const,
  stand: (id: number, standId: number) => ["events", id, "stand", standId] as const,
  block: (id: number, blockId: number) => ["events", id, "block", blockId] as const,
  myHolds: ["holds", "mine"] as const,
  hold: (id: number) => ["holds", id] as const,
  booking: (reference: string) => ["bookings", reference] as const,
  payment: (id: number) => ["payments", id] as const,
  tickets: ["tickets", "live"] as const,
  ticketsOffline: ["tickets", "offline"] as const,
};

// ---------------------------------------------------------------------------- me

export function useMe(enabled: boolean) {
  return useQuery({
    queryKey: keys.me,
    enabled,
    queryFn: async () => {
      const user = await unwrap(api.GET("/me"));
      await writeStoredUser(user);
      return user;
    },
  });
}

export function useUpdateName() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (name: string) => unwrap(api.PATCH("/me", { body: { name } })),
    onSuccess: async (user) => {
      qc.setQueryData(keys.me, user);
      await writeStoredUser(user);
    },
  });
}

// ---------------------------------------------------------------------------- events

const PAGE_SIZE = 10;

export function useEvents() {
  return useInfiniteQuery({
    queryKey: keys.events,
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) => unwrap(api.GET("/events", { params: { query: { limit: PAGE_SIZE, cursor: pageParam } } })),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });
}

export function useEvent(eventId: number) {
  return useQuery({
    queryKey: keys.event(eventId),
    queryFn: () => unwrap(api.GET("/events/{eventId}", { params: { path: { eventId } } })),
  });
}

export function useCategories(eventId: number) {
  return useQuery({
    queryKey: keys.categories(eventId),
    queryFn: () => unwrap(api.GET("/events/{eventId}/categories", { params: { path: { eventId } } })),
  });
}

// ---------------------------------------------------------------------------- seat map

/**
 * Restores the generated schema type on a seat-map answer.
 *
 * openapi-fetch 0.17 passes every response through its `Readable<T>` helper, which maps any
 * array type to `Readable<E>[]` and so turns the contract's tuples (`[x, y]`, the 7-item
 * seat tuple) into loose arrays of a union. The JSON on the wire is the tuple; only the
 * static type is lost. The target type is always the generated `components["schemas"]`
 * one, never a hand-written shape.
 */
function asSchema<K extends keyof Schemas>(value: unknown): Schemas[K] {
  return value as Schemas[K];
}

export function useStadiumMap(eventId: number) {
  return useQuery({
    queryKey: keys.map(eventId),
    staleTime: 5_000,
    queryFn: async () => asSchema<"StadiumMap">(await unwrap(api.GET("/events/{eventId}/map", { params: { path: { eventId } } }))),
  });
}

export function useStandBlocks(eventId: number, standId: number) {
  return useQuery({
    queryKey: keys.stand(eventId, standId),
    staleTime: 5_000,
    queryFn: async () => asSchema<"StandBlocks">(await unwrap(api.GET("/events/{eventId}/stands/{standId}/blocks", { params: { path: { eventId, standId } } }))),
  });
}

/** One request to the level-3 endpoint; with `since` it may answer `delta` or `full`. */
export async function fetchBlockSeats(eventId: number, blockId: number, since?: string): Promise<Schemas["BlockSeats"]> {
  const answer = await unwrap(
    api.GET("/events/{eventId}/blocks/{blockId}/seats", {
      params: { path: { eventId, blockId }, query: since ? { since } : undefined },
    }),
  );
  return asSchema<"BlockSeats">(answer);
}

/**
 * The full block. Changes arrive through `useBlockPolling`, which patches this cache entry,
 * so the query itself never refetches on a timer.
 */
export function useBlockSeats(eventId: number, blockId: number) {
  return useQuery({
    queryKey: keys.block(eventId, blockId),
    staleTime: Infinity,
    gcTime: 60_000,
    queryFn: async (): Promise<Schemas["BlockSeatsFull"]> => {
      const answer = await fetchBlockSeats(eventId, blockId);
      // Without `since` the contract promises `full`; anything else is a server fault.
      if (answer.kind !== "full") throw new ApiError({ status: 200, code: "UNEXPECTED_RESPONSE", message: "The seat map came back incomplete." });
      return answer;
    },
  });
}

// ---------------------------------------------------------------------------- holds

export function useMyHolds(enabled: boolean) {
  return useQuery({
    queryKey: keys.myHolds,
    enabled,
    staleTime: 0,
    queryFn: () => unwrap(api.GET("/me/holds")),
  });
}

export function useHold(holdId: number) {
  return useQuery({
    queryKey: keys.hold(holdId),
    staleTime: 0,
    queryFn: () => unwrap(api.GET("/holds/{holdId}", { params: { path: { holdId } } })),
  });
}

export function useCreateHold() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["CreateHold"]) => unwrap(api.POST("/holds", { body })),
    onSuccess: (hold) => {
      qc.setQueryData(keys.hold(hold.id), hold);
      void qc.invalidateQueries({ queryKey: keys.myHolds });
    },
  });
}

export function useBestAvailable() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: Schemas["CreateBestAvailableHold"]) => unwrap(api.POST("/holds/best-available", { body })),
    onSuccess: (hold) => {
      qc.setQueryData(keys.hold(hold.id), hold);
      void qc.invalidateQueries({ queryKey: keys.myHolds });
    },
  });
}

export function useReleaseHold() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (hold: { id: number; eventId: number }) => unwrap(api.DELETE("/holds/{holdId}", { params: { path: { holdId: hold.id } } })),
    onSuccess: (_data, hold) => {
      qc.removeQueries({ queryKey: keys.hold(hold.id) });
      void qc.invalidateQueries({ queryKey: keys.myHolds });
      // The seats are free again: every cached view of this event's map is now out of date.
      void qc.invalidateQueries({ queryKey: ["events", hold.eventId] });
    },
  });
}

// ---------------------------------------------------------------------------- bookings and payments

export function createBooking(holdId: number, idempotencyKey: string) {
  return unwrap(api.POST("/bookings", { params: { header: { "Idempotency-Key": idempotencyKey } }, body: { hold_id: holdId } }));
}

export function fetchBooking(reference: string) {
  return unwrap(api.GET("/bookings/{reference}", { params: { path: { reference } } }));
}

export function useBooking(reference: string | null) {
  return useQuery({
    queryKey: keys.booking(reference ?? ""),
    enabled: reference !== null,
    staleTime: 0,
    queryFn: () => fetchBooking(reference as string),
  });
}

export function startStkPush(bookingId: number, phone: string, mockOutcome?: Schemas["MockOutcome"]) {
  return unwrap(
    api.POST("/payments/mpesa/stk", {
      params: mockOutcome ? { header: { "X-Mock-Outcome": mockOutcome } } : {},
      body: { booking_id: bookingId, phone },
    }),
  );
}

export const isLivePayment = (status: Schemas["PaymentStatus"]) => status === "INITIATED" || status === "PENDING";

/** Polls a payment every two seconds until its status is final (the contract's suggestion). */
export function usePayment(paymentId: number | null) {
  return useQuery({
    queryKey: keys.payment(paymentId ?? 0),
    enabled: paymentId !== null,
    staleTime: 0,
    queryFn: () => unwrap(api.GET("/payments/{paymentId}", { params: { path: { paymentId: paymentId as number } } })),
    refetchInterval: (query) => (query.state.data && !isLivePayment(query.state.data.status) ? false : 2000),
    refetchIntervalInBackground: false,
  });
}

// ---------------------------------------------------------------------------- tickets

async function fetchAllTickets(): Promise<Schemas["Ticket"][]> {
  const all: Schemas["Ticket"][] = [];
  let cursor: string | undefined;
  // A fan holds a handful of tickets, so reading every page is one or two requests. The cap
  // only stops a server that never ends its pages.
  for (let page = 0; page < 20; page++) {
    const answer = await unwrap(api.GET("/me/tickets", { params: { query: { limit: 100, cursor } } }));
    all.push(...answer.items);
    if (!answer.next_cursor) break;
    cursor = answer.next_cursor;
  }
  return all;
}

/** The fan's tickets from the server. Every good answer is also saved on the phone. */
export function useTickets(userId: number | null) {
  const qc = useQueryClient();
  return useQuery({
    queryKey: keys.tickets,
    enabled: userId !== null,
    queryFn: async () => {
      const items = await fetchAllTickets();
      const stored = { userId: userId as number, savedAt: new Date().toISOString(), items };
      await writeStoredTickets(stored);
      qc.setQueryData(keys.ticketsOffline, stored);
      return items;
    },
  });
}

/** The copy saved on the phone, for when there is no network. Null when nothing was saved. */
export function useOfflineTickets(userId: number | null) {
  return useQuery({
    queryKey: keys.ticketsOffline,
    enabled: userId !== null,
    staleTime: Infinity,
    gcTime: Infinity,
    queryFn: async () => {
      const stored = await readStoredTickets();
      return stored && stored.userId === userId ? stored : null;
    },
  });
}
