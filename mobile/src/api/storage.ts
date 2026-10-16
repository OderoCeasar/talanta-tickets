// Small things kept on the phone outside secure storage: the tickets' offline copy, the
// signed-in user (so the app opens with no network) and pointers used to resume a checkout.
// Nothing secret goes here; tokens are in api/tokens.ts.

import AsyncStorage from "@react-native-async-storage/async-storage";

import type { Schemas } from "./client";

const USER_KEY = "talanta.user";
const TICKETS_KEY = "talanta.tickets";
const CHECKOUT_KEY = "talanta.checkout";
// Belongs to the phone, not the person: kept on sign-out so the welcome slides show once.
const WELCOME_KEY = "talanta.welcomeSeen";

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
 * The checkout in progress. `idempotencyKey` is made once per hold and sent with every
 * `POST /bookings` for it: the first when the fan taps Pay, and every retry after that,
 * including after the app was killed between the request and its answer.
 * `bookingReference` is null until that first Pay.
 */
export interface StoredCheckout {
  holdId: number;
  eventId: number;
  /** The block the seats are in, to go back to when the hold ends (an ended hold lists no seats). */
  blockId?: number;
  idempotencyKey: string;
  bookingReference: string | null;
}
export const readStoredCheckout = () => readJson<StoredCheckout>(CHECKOUT_KEY);
export const writeStoredCheckout = (value: StoredCheckout) => writeJson(CHECKOUT_KEY, value);
export const clearStoredCheckout = () => AsyncStorage.removeItem(CHECKOUT_KEY).catch(() => undefined);

/** Whether this phone has been shown the welcome slides. A storage failure counts as seen: better to skip the slides than to show them on every launch. */
export async function readWelcomeSeen(): Promise<boolean> {
  try {
    return (await AsyncStorage.getItem(WELCOME_KEY)) !== null;
  } catch {
    return true;
  }
}
export const writeWelcomeSeen = () => writeJson(WELCOME_KEY, true);

/** Everything that belongs to the signed-in person. Called on sign-out. */
export async function clearUserStorage(): Promise<void> {
  await AsyncStorage.multiRemove([USER_KEY, TICKETS_KEY, CHECKOUT_KEY]).catch(() => undefined);
}
