import type { TokenStore } from "@talanta/shared";
import * as SecureStore from "expo-secure-store";

// Both tokens live in the device's secure storage (Android Keystore / iOS Keychain), never in
// AsyncStorage. A copy is kept in memory so each request does not wait on the keystore.
const ACCESS_KEY = "talanta.access_token";
const REFRESH_KEY = "talanta.refresh_token";

let accessToken: string | null = null;
let refreshToken: string | null = null;
let loaded: Promise<void> | null = null;

async function read(key: string): Promise<string | null> {
  try {
    return await SecureStore.getItemAsync(key);
  } catch {
    // A keystore that cannot be read (restored backup, wiped lock screen) means signed out.
    return null;
  }
}

/** Reads the stored tokens once. Every other function waits for it. */
export function loadTokens(): Promise<void> {
  if (!loaded) {
    loaded = Promise.all([read(ACCESS_KEY), read(REFRESH_KEY)]).then(([a, r]) => {
      // An access token with no refresh token beside it (a half-finished write or sign-out)
      // is a dead token: the app counts as signed out, and sending it would turn every
      // public read into a 401 that nothing could ever refresh.
      accessToken = r ? a : null;
      refreshToken = r;
      if (a && !r) void SecureStore.deleteItemAsync(ACCESS_KEY).catch(() => undefined);
    });
  }
  return loaded;
}

export async function hasRefreshToken(): Promise<boolean> {
  await loadTokens();
  return refreshToken !== null;
}

export const tokenStore: TokenStore = {
  async getAccessToken() {
    await loadTokens();
    return accessToken;
  },
  async getRefreshToken() {
    await loadTokens();
    return refreshToken;
  },
  async setTokens(tokens) {
    accessToken = tokens.accessToken;
    refreshToken = tokens.refreshToken;
    await Promise.all([
      SecureStore.setItemAsync(ACCESS_KEY, tokens.accessToken),
      SecureStore.setItemAsync(REFRESH_KEY, tokens.refreshToken),
    ]);
  },
  async clearTokens() {
    accessToken = null;
    refreshToken = null;
    await Promise.all([
      SecureStore.deleteItemAsync(ACCESS_KEY).catch(() => undefined),
      SecureStore.deleteItemAsync(REFRESH_KEY).catch(() => undefined),
    ]);
  },
};
