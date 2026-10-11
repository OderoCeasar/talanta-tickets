// Small things kept on the phone outside secure storage: the tickets' offline copy, the
// signed-in user (so the app opens with no network) and pointers used to resume a checkout.
// Nothing secret goes here; tokens are in api/tokens.ts.

import AsyncStorage from "@react-native-async-storage/async-storage";

import type { Schemas } from "./client";

const USER_KEY = "talanta.user";
const TICKETS_KEY = "talanta.tickets";
const CHECKOUT_KEY = "talanta.checkout";

async function readJson<T>(key: string): Promise<T | null> {
  try {
    const raw = await AsyncStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

async function writeJson(key: string, value: unknown): Promise<void> {
  try {
    await AsyncStorage.setItem(key, JSON.stringify(value));
  } catch {
    // Storage full or unavailable: the app still works, it just cannot resume or show offline.
  }
}

export const readStoredUser = () => readJson<Schemas["User"]>(USER_KEY);
export const writeStoredUser = (user: Schemas["User"]) => writeJson(USER_KEY, user);

export interface StoredTickets {
  userId: number;
  savedAt: string;
  items: Schemas["Ticket"][];
}
export const readStoredTickets = () => readJson<StoredTickets>(TICKETS_KEY);
export const writeStoredTickets = (value: StoredTickets) => writeJson(TICKETS_KEY, value);

/**
 * The checkout in progress. `idempotencyKey` is made once per hold and sent again on every
 * retry of `POST /bookings`, including after the app was killed between the request and its
 * answer.
 */
export interface StoredCheckout {
  holdId: number;
  eventId: number;
  idempotencyKey: string;
  bookingReference: string | null;
}
export const readStoredCheckout = () => readJson<StoredCheckout>(CHECKOUT_KEY);
export const writeStoredCheckout = (value: StoredCheckout) => writeJson(CHECKOUT_KEY, value);
export const clearStoredCheckout = () => AsyncStorage.removeItem(CHECKOUT_KEY).catch(() => undefined);

/** Everything that belongs to the signed-in person. Called on sign-out. */
export async function clearUserStorage(): Promise<void> {
  await AsyncStorage.multiRemove([USER_KEY, TICKETS_KEY, CHECKOUT_KEY]).catch(() => undefined);
}
